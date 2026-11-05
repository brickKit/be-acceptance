"""The authorization profile of a component (be-protocol chapter 08, CK-AZ-01..05): the suite is the authorization
member (the test deployment issues the views and answers the verifications) and drives a probe's entries (probes
CONTRACT.md, since 3.2.38) with its guard on. Each judge returns the problems; each bad() turns a good observation into
a known violation.

Per dimension kind (value set, org tree, principal, amount) CK-AZ-05 sees: another's entries invisible on the list and
one by one, the list and the single object agreeing on rows and fields (an unreadable field null and named under
_masked), a list filter outside the grant narrowing without an error, a command whose target state is outside refused
with PERMISSION_DENIED and the entry unchanged, and refusals carrying none of the hidden entries' values."""
from __future__ import annotations

import asyncio
import time

import httpx

from harness import Deployment, Running, Spec
from profile.checks import mutated
from profile.identity import ALICE, prefix, sdk_of
from profile.model import Check

BOB = "0190f3a1-0000-7000-8000-0000000000b2"
CAROL = "0190f3a1-0000-7000-8000-0000000000c3"  # seeds the entries
W_HIGH, W_READ = 5.0, 30.0
FROM = {"from": "2026-01-01T00:00:00Z"}
SHOWN = ["amount", "currency", "dept_path", "id", "legal_entity", "number", "owner_id", "approved"]
ALL = {"kind": "all"}
DIMENSIONS = ("legal_entity", "dept", "owner", "amount")

# The entries the suite seeds: legal entity, department, owner, amount and currency, cost.
ENTRIES = {
    "e1": ("LE1", "org1/org2", ALICE, "100", "CNY", "7"),
    "e2": ("LE2", "org1/org3", BOB, "500", "CNY", "8"),
    "e3": ("LE1", "org1/org22", BOB, "50", "USD", "9"),
    "e4": ("LE2", "org1/org2/org5", BOB, "75", "CNY", "10"),
    "e5": ("LE1", None, ALICE, "20000", "CNY", "11"),
}

# Per dimension kind: Alice's atom, the entries it shows, a list filter outside and one inside the grant (column ->
# value), a move outside (refused) and one inside (allowed), both on e1.
CASES = {
    "value_set": {"atoms": {"legal_entity": {"kind": "in", "values": ["LE1"]}}, "visible": ["e1", "e3", "e5"],
                  "filter_out": {"legal_entity": "LE2"}, "filter_in": ({"legal_entity": "LE1"}, ["e1", "e3", "e5"]),
                  "move_out": {"legal_entity": "LE2"}, "move_in": {"legal_entity": "LE1"}},
    "org_tree": {"atoms": {"dept": {"kind": "tree", "items": [{"path": "org1/org2", "mode": "subtree"}]}},
                 "visible": ["e1", "e4"], "filter_out": {"dept_path": "org1/org3"},
                 "filter_in": ({"dept_path": "org1/org2/org5"}, ["e4"]),
                 "move_out": {"dept_path": "org1/org22"}, "move_in": {"dept_path": "org1/org2"}},
    "principal": {"atoms": {"owner": {"kind": "self"}}, "visible": ["e1", "e5"], "filter_out": {"owner_id": BOB},
                  "filter_in": ({"owner_id": ALICE}, ["e1", "e5"]), "move_out": {"owner_id": BOB},
                  "move_in": {"owner_id": ALICE}},
    "amount": {"atoms": {"amount": {"kind": "interval", "currency": "CNY", "hi": "100", "hi_closed": True}},
               "visible": ["e1", "e4"], "filter_out": {"amount": "500"}, "filter_in": ({"currency": "CNY"}, ["e1", "e4"]),
               "move_out": {"amount": "150"}, "move_in": {"amount": "100"}},
}


def entry_type(spec: Spec) -> str:
    return prefix(spec.id) + ".entry"


def seg(spec: Spec, op: str, atoms: dict | None = None, fields: list[str] | None = None) -> dict:
    a = {d: ALL for d in DIMENSIONS}
    a.update(atoms or {})
    return {"resource_type": entry_type(spec), "operation": f"{entry_type(spec)}.{op}", "atoms": a,
            "read_fields": sorted(fields if fields is not None else SHOWN), "valid": FROM}


class Client:
    """One principal's calls to the probe's entries."""

    def __init__(self, d: Deployment, r: Running, principal: str):
        self.d, self.r, self.principal = d, r, principal

    async def call(self, method: str, path: str, body: dict | None = None, params: dict | None = None):
        async with httpx.AsyncClient(timeout=15) as c:
            resp = await c.request(method, self.r.url + "/api/probe/" + path, json=body, params=params,
                                   headers={"Authorization": "Bearer " + self.d.world.token(self.principal)})
        try:
            data = resp.json()
        except ValueError:
            data = None
        return resp.status_code, data, resp.text

    async def ids(self, params: dict | None = None) -> tuple[int, list[str]]:
        code, data, _ = await self.call("GET", "entries", params=params)
        return code, [i["id"] for i in (data or {}).get("items", [])] if code == 200 else []


async def until(f, timeout: float = 15.0, every: float = 0.25):
    end, v = time.monotonic() + timeout, None
    while time.monotonic() < end:
        v = await f()
        if v:
            return v
        await asyncio.sleep(every)
    return v


def grant(d: Deployment, principal: str, *segments: dict) -> None:
    d.world.revoke(principal)
    if segments:
        d.world.grant(principal, *segments)


async def seed(d: Deployment, r: Running, spec: Spec) -> dict[str, str]:
    """Carol creates the entries; their probe IDs by name."""
    grant(d, CAROL, seg(spec, "create", fields=SHOWN + ["cost"]), seg(spec, "read", fields=SHOWN + ["cost"]))
    carol, ids = Client(d, r, CAROL), {}
    for name, (le, dept, owner, amount, currency, cost) in ENTRIES.items():
        body = {"number": name, "legal_entity": le, "dept_path": dept, "owner_id": owner, "amount": amount,
                "currency": currency, "cost": cost, "note": "secret-" + name}
        answer = await until(lambda b=body: _created(carol, b))
        if not answer:
            raise AssertionError(f"the seeder could not create {name}")
        ids[name] = answer
    return ids


async def _created(c: Client, body: dict) -> str:
    code, data, _ = await c.call("POST", "entries", body)
    return data["id"] if code == 201 else ""


async def dimension(d: Deployment, r: Running, spec: Spec, ids: dict[str, str], case: dict) -> dict:
    """Alice reads and moves within one dimension's atom; what she sees, one by one and as a list."""
    names = {v: k for k, v in ids.items()}
    grant(d, ALICE, seg(spec, "read", case["atoms"]), seg(spec, "move", case["atoms"]), seg(spec, "create", case["atoms"]))
    alice, carol = Client(d, r, ALICE), Client(d, r, CAROL)
    want = sorted(case["visible"])
    await until(lambda: _listed(alice, names, want))
    code, data, _ = await alice.call("GET", "entries")
    rows = {names.get(i["id"], i["id"]): i for i in (data or {}).get("items", [])} if code == 200 else {}
    single, refusals = {}, []
    for name, eid in ids.items():
        s, body, text = await alice.call("GET", f"entries/{eid}")
        single[name] = {"status": s, "body": body if s == 200 else None, "reason": (body or {}).get("reason", "")}
        if s != 200:
            refusals.append(text)
    out_code, out_ids = await alice.ids(case["filter_out"])
    flt, in_want = case["filter_in"]
    in_code, in_ids = await alice.ids(flt)
    move_out = await alice.call("POST", f"entries/{ids['e1']}/move", case["move_out"])
    refusals.append(move_out[2])
    after = (await carol.call("GET", f"entries/{ids['e1']}"))[1] or {}
    move_in = await alice.call("POST", f"entries/{ids['e1']}/move", case["move_in"])
    # a create whose state is outside the grant (e1's values with the move's outside one): refused, nothing made
    le, dept, owner, amount, currency, _ = ENTRIES["e1"]
    body = {"number": "created", "legal_entity": le, "dept_path": dept, "owner_id": owner, "amount": amount,
            "currency": currency, **case["move_out"]}
    create_out = await alice.call("POST", "entries", body)
    refusals.append(create_out[2])
    all_after = await carol.ids()
    return {"want": want, "list_status": code, "rows": rows, "single": single,
            "filter_out": {"status": out_code, "ids": sorted(names.get(i, i) for i in out_ids)},
            "filter_in": {"status": in_code, "ids": sorted(names.get(i, i) for i in in_ids), "want": sorted(in_want)},
            "move_out": {"status": move_out[0], "reason": (move_out[1] or {}).get("reason", ""),
                         "e1_after": {k: after.get(k) for k in ("legal_entity", "dept_path", "owner_id", "amount")}},
            "move_in": move_in[0], "refusals": refusals,
            "create_out": {"status": create_out[0], "reason": (create_out[1] or {}).get("reason", ""),
                           "entries_after": len(all_after[1])}}


async def _listed(c: Client, names: dict, want: list[str]) -> bool:
    code, got = await c.ids()
    return code == 200 and sorted(names.get(i, i) for i in got) == want


async def overlap(d: Deployment, r: Running, spec: Spec, ids: dict[str, str]) -> dict:
    """Two segments over some of the same entries: LE1 with cost, the org1/org2 subtree without. Paged one row at a
    time every visible entry comes once, cost only where LE1's segment covers it; readable_only=cost narrows to those
    and lets cost sort. Then an export whose own segment reads fewer fields than the page."""
    names = {v: k for k, v in ids.items()}
    grant(d, ALICE, seg(spec, "read", {"legal_entity": {"kind": "in", "values": ["LE1"]}}, SHOWN + ["cost"]),
          seg(spec, "read", {"dept": {"kind": "tree", "items": [{"path": "org1/org2", "mode": "subtree"}]}}),
          seg(spec, "export", fields=["id", "number"]))
    alice = Client(d, r, ALICE)
    await until(lambda: _listed(alice, names, ["e1", "e3", "e4", "e5"]))
    pages, cursor = [], ""
    for _ in range(10):
        code, data, _ = await alice.call("GET", "entries", params={"limit": "1", "sort": "number",
                                                                   **({"cursor": cursor} if cursor else {})})
        if code != 200:
            pages.append({"_status": code})
            break
        pages += [{names.get(i["id"], i["id"]): i} for i in data["items"]]
        cursor = data["next"]
        if not cursor:
            break
    code, narrowed, _ = await alice.call("GET", "entries", params={"readable_only": "cost", "sort": "-cost"})
    e1 = ids["e1"]
    read = (await alice.call("GET", f"entries/{e1}"))[1]
    export = await alice.call("GET", f"entries/{e1}/export")
    return {"pages": pages, "narrowed": {"status": code, "ids": [names.get(i["id"]) for i in (narrowed or {}).get("items", [])],
                                         "readable_only": (narrowed or {}).get("readable_only")},
            "read": read, "export": {"status": export[0], "body": export[1]}}


# ------------------------------------------------------------------------------------------- view validity

async def answer(c: Client, method: str, path: str, body: dict | None = None) -> tuple[int, str]:
    code, data, _ = await c.call(method, path, body)
    return code, (data or {}).get("reason", "") if isinstance(data, dict) else ""


async def next_verification(d: Deployment, timeout: float = 10.0) -> float:
    """Waits for the probe's next verification to be answered; when it was (monotonic)."""
    before = d.world.verifications
    await until(lambda: _verified(d, before), timeout, 0.02)
    return d.world.verified_at


async def _verified(d: Deployment, before: int) -> bool:
    return d.world.verifications > before


async def validity(d: Deployment, r: Running, spec: Spec, ids: dict[str, str]) -> dict:
    """CK-AZ-02..04 with Alice reading and approving every entry (approve is high risk: W_high)."""
    w, alice, e1 = d.world, Client(d, r, ALICE), ids["e1"]
    grant(d, ALICE, seg(spec, "read"), seg(spec, "approve"))
    read, approve = ("GET", f"entries/{e1}"), ("POST", f"entries/{e1}/approve", {})
    obs: dict = {"baseline": await until(lambda: _is(alice, approve, 204))}

    # CK-AZ-02: answers with another nonce renew nothing: the high-risk validity runs out; the right nonce renews again
    w.wrong_nonce = True
    t0 = await next_verification(d)
    await asyncio.sleep(max(0.0, t0 + W_HIGH + 1.0 - time.monotonic()))
    obs["wrong_nonce"] = {"approve": await answer(alice, *approve), "verifications": w.verifications}
    w.wrong_nonce = False
    obs["wrong_nonce"]["renewed"] = await until(lambda: _is(alice, approve, 204))

    # CK-AZ-04: right after an answered verification the member stops; a write whose check passes before the
    # high-risk validity ends but whose transaction outlasts it rolls back
    t1 = await next_verification(d)
    w.authz_up = False
    await asyncio.sleep(max(0.0, t1 + W_HIGH - 1.7 - time.monotonic()))
    started = time.monotonic() - t1
    held = await answer(alice, "POST", f"entries/{ids['e4']}/approve", {"hold_ms": 2000})
    obs["held_write"] = {"answer": held, "started_after": round(started, 2)}

    # CK-AZ-03: with the member still down: past W_high high risk is refused, ordinary reads go on until W_read
    await asyncio.sleep(max(0.0, t1 + W_HIGH + 1.0 - time.monotonic()))
    obs["stopped_high"] = {"approve": await answer(alice, *approve), "read": await answer(alice, *read)}
    await asyncio.sleep(max(0.0, t1 + W_READ + 1.0 - time.monotonic()))
    obs["stopped_read"] = await answer(alice, *read)
    w.authz_up = True
    obs["restarted"] = await until(lambda: _is(alice, read, 200), 15)
    e4 = (await Client(d, r, CAROL).call("GET", f"entries/{ids['e4']}"))[1] or {}
    obs["held_write"]["approved_after"] = e4.get("approved")

    # CK-AZ-03: an answer of an older recovery generation is a rollback: the views are suspended until a good answer
    w.recovery = "0"
    await next_verification(d)
    obs["rollback"] = await answer(alice, *read)
    w.recovery = "1"
    obs["rollback_renewed"] = await until(lambda: _is(alice, read, 200), 15)
    return obs


async def _is(c: Client, call: tuple, want: int) -> bool:
    code, _ = await answer(c, *call)
    return code == want


async def undeclared(d: Deployment, spec: Spec, env: dict) -> dict:
    """CK-AZ-01: a guarded route naming an operation the probe does not declare stops the start."""
    before = len(d.running)
    try:
        r = await d.start(spec, {**env, "PROBE_GUARD": "true", "PROBE_UNDECLARED_ROUTE": "true"})
    except AssertionError as e:
        del d.running[before:]  # the refused start is over: its exit code is the observation, not a failed stop
        return {"started": False, "log": str(e)[-3000:]}
    started = {"started": True, "log": r.logs()[-3000:]}
    await r.stop()
    d.running.remove(r)
    return started


async def observe(d: Deployment, spec: Spec, env: dict) -> dict:
    obs: dict = {"sdk": sdk_of(spec)}
    obs["undeclared"] = await undeclared(d, spec, env)
    r = await d.start(spec, {**env, "PROBE_GUARD": "true"})
    ids = await seed(d, r, spec)
    obs["entries"] = sorted(ids)
    obs["dimensions"] = {kind: await dimension(d, r, spec, ids, case) for kind, case in CASES.items()}
    obs["overlap"] = await overlap(d, r, spec, ids)
    obs["validity"] = await validity(d, r, spec, ids)
    obs["log"] = r.logs()[-20000:]
    return obs


# ------------------------------------------------------------------------------------------------------ judges

def judge_undeclared(obs: dict) -> list[str]:
    u = obs["undeclared"]
    if u["started"]:
        return ["a route naming an undeclared operation started"]
    return [] if "undeclared" in u["log"] else ["the start failed, but not over the undeclared operation"]


def judge_nonce(obs: dict) -> list[str]:
    v, out = obs["validity"], []
    if not v["baseline"]:
        out.append("precondition: the approval never went through")
    if tuple(v["wrong_nonce"]["approve"]) != (503, "AUTHORIZATION_VIEW_EXPIRED"):
        out.append(f"answers with another nonce renewed the view: approve {v['wrong_nonce']['approve']}")
    if not v["wrong_nonce"]["renewed"]:
        out.append("the right nonce again did not renew")
    return out


def judge_stopped(obs: dict) -> list[str]:
    v, out = obs["validity"], []
    if tuple(v["stopped_high"]["approve"]) != (503, "AUTHORIZATION_VIEW_EXPIRED"):
        out.append(f"past W_high the high-risk approval: {v['stopped_high']['approve']}")
    if v["stopped_high"]["read"][0] != 200:
        out.append(f"past W_high, within W_read, a read: {v['stopped_high']['read']}")
    if tuple(v["stopped_read"]) != (503, "AUTHORIZATION_VIEW_EXPIRED"):
        out.append(f"past W_read a read: {v['stopped_read']}")
    if not v["restarted"]:
        out.append("the member back, reads did not resume")
    if tuple(v["rollback"]) != (503, "AUTHORIZATION_VIEW_EXPIRED"):
        out.append(f"after an older recovery generation's answer a read: {v['rollback']}")
    if not v["rollback_renewed"]:
        out.append("a good answer after the rollback did not resume")
    return out


def judge_held_write(obs: dict) -> list[str]:
    h, out = obs["validity"]["held_write"], []
    if not 2.0 < h["started_after"] < W_HIGH:
        out.append(f"precondition: the write started {h['started_after']} s after the last renewal")
    if tuple(h["answer"]) != (503, "AUTHORIZATION_VIEW_EXPIRED"):
        out.append(f"a write outlasting its view: {h['answer']}, want 503 AUTHORIZATION_VIEW_EXPIRED")
    if h.get("approved_after") is not False:
        out.append(f"the write's effect stayed: approved {h.get('approved_after')}")
    return out


def judge_dimensions(obs: dict) -> list[str]:
    out = []
    for kind, o in obs["dimensions"].items():
        out += [f"{kind}: {p}" for p in _dimension_problems(o, obs["entries"])]
    return out


def _dimension_problems(o: dict, entries: list[str]) -> list[str]:
    out = []
    if o["list_status"] != 200 or sorted(o["rows"]) != o["want"]:
        out.append(f"the list shows {sorted(o['rows'])} ({o['list_status']}), want {o['want']}")
    for name in entries:
        s = o["single"][name]
        if name in o["want"]:
            if s["status"] != 200 or s["body"] != o["rows"].get(name):
                out.append(f"{name}: the single object ({s['status']}) differs from its list row")
        elif (s["status"], s["reason"]) != (404, "NOT_FOUND"):
            out.append(f"{name} is another's: {s['status']} {s['reason']}, want 404 NOT_FOUND")
    for name, row in o["rows"].items():
        if row.get("cost") is not None or row.get("_masked") != ["cost"] or "note" in row:
            out.append(f"{name}: the unreadable cost is not null under _masked, or the secret note shows")
    if (o["filter_out"]["status"], o["filter_out"]["ids"]) != (200, []):
        out.append(f"a filter outside the grant: {o['filter_out']}, want 200 and nothing")
    if (o["filter_in"]["status"], o["filter_in"]["ids"]) != (200, o["filter_in"]["want"]):
        out.append(f"a filter inside the grant: {o['filter_in']}")
    m = o["move_out"]
    if (m["status"], m["reason"]) != (403, "PERMISSION_DENIED"):
        out.append(f"a move outside the grant: {m['status']} {m['reason']}, want 403 PERMISSION_DENIED")
    want_e1 = dict(zip(("legal_entity", "dept_path", "owner_id", "amount"), ENTRIES["e1"][:4]))
    if m["e1_after"] != want_e1:
        out.append(f"the refused move changed the entry: {m['e1_after']}")
    if o["move_in"] != 204:
        out.append(f"precondition: a move inside the grant got {o['move_in']}")
    c = o["create_out"]
    if (c["status"], c["reason"]) != (403, "PERMISSION_DENIED"):
        out.append(f"a create outside the grant: {c['status']} {c['reason']}, want 403 PERMISSION_DENIED")
    if c["entries_after"] != len(entries):
        out.append(f"the refused create left {c['entries_after'] - len(entries)} new entries")
    # EN20: a refusal says why, never a hidden entry's owner or amount (as the JSON values they would be)
    hidden = {f'"{v}"' for name, e in ENTRIES.items() if name not in o["want"] for v in (e[2], e[3])}
    for text in o["refusals"]:
        if leaked := sorted(v for v in hidden if v in text):
            out.append(f"a refusal carries a hidden entry's value: {leaked}")
    return out


def judge_overlap(obs: dict) -> list[str]:
    o, out = obs["overlap"], []
    out += [f"a page answered {p['_status']}" for p in o["pages"] if "_status" in p]
    pages = [p for p in o["pages"] if "_status" not in p]
    seen = [name for page in pages for name in page]
    if seen != ["e1", "e3", "e4", "e5"]:
        out.append(f"paged one row at a time: {seen}, want each of e1 e3 e4 e5 once, in number order")
    for page in pages:
        for name, row in page.items():
            readable = name in ("e1", "e3", "e5")  # covered by the LE1 segment, which reads cost
            if readable != (row.get("cost") is not None) or readable == ("cost" in (row.get("_masked") or [])):
                out.append(f"{name}: cost {row.get('cost')!r} _masked {row.get('_masked')}, readable {readable}")
    n = o["narrowed"]
    if (n["status"], n["ids"], n["readable_only"]) != (200, ["e5", "e3", "e1"], ["cost"]):
        out.append(f"readable_only=cost sorted by cost descending: {n}")
    if (o["read"] or {}).get("cost") is None:
        out.append("precondition: the page read shows e1's cost")
    e = o["export"]
    if e["status"] != 200 or (e["body"] or {}).get("cost") is not None or "cost" not in ((e["body"] or {}).get("_masked") or []):
        out.append(f"the export outputs its own segment's fields, not the page's: {e}")
    return out


def _break_dimension(o: dict) -> None:
    d = o["dimensions"]["principal"]
    d["single"]["e2"] = {"status": 200, "body": {"id": "x"}, "reason": ""}


AUTHZ = [
    Check("CK-AZ-01", "引用未声明操作的路由使启动失败", ("P8.7",), ("undeclared",), judge_undeclared,
          lambda o: mutated(o, lambda x: x.update(undeclared={"started": True, "log": ""}))),
    Check("CK-AZ-02", "随机数不符的回答不续期", ("P8.9",), ("validity",), judge_nonce,
          lambda o: mutated(o, lambda x: x["validity"]["wrong_nonce"].update(approve=[204, ""]))),
    Check("CK-AZ-03", "授权成员停止后 W_high、W_read 过后分别被拒；恢复代次回退被识别", ("P8.10", "P8.11", "P8.13"),
          ("validity",), judge_stopped, lambda o: mutated(o, lambda x: x["validity"].update(rollback=[200, ""]))),
    Check("CK-AZ-04", "事务中视图过期，提交前的检查使事务回滚", ("P8.12",), ("validity",), judge_held_write,
          lambda o: mutated(o, lambda x: x["validity"]["held_write"].update(approved_after=True))),
    Check("CK-AZ-05", "每种维度：别人的看不见、越界取值被拒、列表与单对象一致、掩码", ("P8.14", "P8.15", "P8.16", "P8.17",
                                                                     "P8.18", "P8.31"),
          ("dimensions", "entries"), judge_dimensions, lambda o: mutated(o, _break_dimension)),
    Check("CK-AZ-05b", "重叠的段逐页翻：每条一次、字段按命中段；只看可读的行；导出用自己的字段", ("P8.15", "P8.17",
                                                                            "P9.4"),
          ("overlap",), judge_overlap, lambda o: mutated(o, lambda x: x["overlap"]["pages"].append(x["overlap"]["pages"][0]))),
]
