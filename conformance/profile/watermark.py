"""The watermark source profile (be-protocol chapter 09, P9.13-P9.18): the suite pulls a probe's watermark stream over
mTLS as a declared puller (and as a stranger), writes through the probe's user surface, and tampers with the probe's
outbox through the admin connection the way a restore without an advanced recovery generation would. The pull's
messages come from be-protocol's proto/ as a descriptor set (watermark.binpb, `make watermark-proto`), so no
generated code meets the Python SDK's own `be` package. Each judge returns the problems; each bad() turns a good
observation into a known violation.

Not here: the consumer side (CK-WM-04..10): it needs a probe that keeps a watermarked projection; the SDK's directed
tests run it (be-sdk-go projection, foundations item 1 T7)."""
from __future__ import annotations

import asyncio
import pathlib
import secrets
import time

import asyncpg
import grpc
from google.protobuf import descriptor_pb2, message_factory

from besdk import errs, reqctx
from besdk.calls.client import Member, options
from harness import Deployment, Running, Spec
from profile.checks import mutated
from profile.events import admin_url, write
from profile.model import Check

SUITE = "test/acceptance"  # the suite's service identity: the probe's watermark stream lists it as a puller
STRANGER = "test/stranger"
ECHO = "be.source.echo.v1"
PAGE = 500

_fds = descriptor_pb2.FileDescriptorSet.FromString((pathlib.Path(__file__).parent / "watermark.binpb").read_bytes())
_m = message_factory.GetMessages(list(_fds.file))
PullRequest, PullResponse = _m["be.source.v1.PullRequest"], _m["be.source.v1.PullResponse"]
NEED = {0: "", 1: "beyond_retention", 2: "rollback_detected"}


def stream_of(spec: Spec) -> str:
    return spec.stream + "_wm"


async def pull(d: Deployment, r: Running, caller: str, stream: str, positions: dict | None = None) -> dict:
    """One pull and its pages: {"code", "pages": [{"source", "partitions": [{"partition", "need", "echo", "events"}]}]}.
    positions: partition -> (impl, epoch, seq, row) with row {"echo_nonce"} or {"record_id", "version", "digest"}."""
    req = PullRequest(stream=stream, nonce=secrets.token_hex(16))
    for p, (impl, epoch, seq, row) in (positions or {}).items():
        pp = req.partitions.add(partition=p)
        pp.position.impl, pp.position.epoch, pp.position.seq = impl, epoch, seq
        if "echo_nonce" in row:
            pp.row.echo_nonce = row["echo_nonce"]
        else:
            pp.row.record.record_id, pp.row.record.version, pp.row.record.digest = (row["record_id"], row["version"],
                                                                                    row["digest"])
    m = Member()
    m.set_tls(d.world.tls(caller))
    out: dict = {"nonce": req.nonce, "code": "OK", "pages": []}
    try:
        ch = m.grpc(r.spec.id, f"127.0.0.1:{r.spec.grpc_port}")
        call = ch.unary_unary("/be.source.v1.WatermarkService/Pull", request_serializer=PullRequest.SerializeToString,
                              response_deserializer=PullResponse.FromString)
        while True:
            with reqctx.deadline_scope(time.monotonic() + 10):
                resp = await call(req, metadata=options(idempotent=True))
            out["pages"].append({
                "source": {"deployment": resp.source.deployment, "impl": resp.source.impl,
                           "system_id": resp.source.system_id, "epoch": resp.source.epoch, "primary": resp.source.primary},
                "partitions": [{"partition": pr.partition, "need": NEED.get(pr.need_snapshot, "?"),
                                "echo": pr.echo.seq if pr.HasField("echo") else None,
                                "events": [{"type": e.headers["ce-type"], "subject": e.headers["ce-subject"],
                                            "seq": int(e.headers["ce-beseq"]), "version": int(e.headers["ce-beversion"]),
                                            "digest": e.headers["ce-bedigest"]} for e in pr.events]}
                               for pr in resp.partitions]})
            if not resp.next_page_token:
                break
            req = PullRequest(page_token=resp.next_page_token)
    except errs.Error as e:  # what the SDK's client makes of a refused or failed call
        out["code"] = e.reason
    except grpc.aio.AioRpcError as e:
        out["code"] = e.code().name
    finally:
        await m.close()
    return out


def echoes(p: dict) -> dict[int, int]:
    """partition -> the echo's position, from a pull's last pages."""
    return {pr["partition"]: pr["echo"] for pg in p["pages"] for pr in pg["partitions"] if pr["echo"] is not None}


def events_of(p: dict, partition: int) -> list[dict]:
    return [e for pg in p["pages"] for pr in pg["partitions"] if pr["partition"] == partition for e in pr["events"]]


async def observe(d: Deployment, r: Running, spec: Spec, env: dict, token: str) -> dict:
    stream, schema = stream_of(spec), spec.schema
    obs: dict = {"impl": env["IMPLEMENTATION_INSTANCE"], "deployment": d.world.deployment}
    c = await asyncpg.connect(admin_url())
    try:
        # CK-WM-01: a component the stream does not list, then the suite as a declared puller
        obs["pullers"] = {"stranger": (await pull(d, r, STRANGER, stream))["code"]}
        first = await pull(d, r, SUITE, stream)
        obs["pullers"]["suite"] = first["code"]
        obs["pullers"]["source"] = first["pages"][0]["source"] if first["pages"] else None

        # CK-WM-02: a transaction held open across the pull, three committed changes, one rolled back
        held = asyncio.create_task(write(r, token, watermark=True, hold_ms=1500))
        await asyncio.sleep(0.3)
        committed = [(await write(r, token, watermark=True))[1].get("id") for _ in range(3)]
        rolled = await write(r, token, watermark=True, fail=True)
        echo_only = await pull(d, r, SUITE, stream)
        held_id = (await held)[1].get("id")
        nonce1, e1 = echo_only["nonce"], echoes(echo_only)
        impl, epoch = obs["impl"], echo_only["pages"][0]["source"]["epoch"]
        then = await pull(d, r, SUITE, stream, {p: (impl, epoch, s, {"echo_nonce": nonce1}) for p, s in e1.items()})
        # read after the second pull: it numbered everything up to its echo, the held row included
        at_seq = {x["record_id"]: (x["partition"], x["seq"]) for x in await c.fetch(
            f"SELECT record_id, partition, seq FROM {schema}.be_outbox WHERE stream = $1 AND seq IS NOT NULL", stream)}
        # pages: more rows than a page in one partition (a single-partition burst of the probe's own records)
        burst = await asyncio.gather(*(write(r, token, watermark=True) for _ in range(2 * PAGE + 60)))
        e2 = echoes(then)
        paged = await pull(d, r, SUITE, stream, {p: (impl, epoch, s, {"echo_nonce": then["nonce"]}) for p, s in e2.items()})
        obs["echo"] = {"committed": [at_seq.get(i) for i in committed], "rolled_code": rolled[0], "held": held_id,
                       "held_at": at_seq.get(held_id), "echo_only": echo_only, "first_echo": e1, "then": then,
                       "burst": len(burst), "paged": paged, "from": e2}

        # CK-WM-03 (SK17): the row at a position is checked
        part = max(e2, key=lambda n: len([e for e in events_of(paged, n) if e["type"] != ECHO]))
        a, b = [e for e in events_of(paged, part) if e["type"] != ECHO][:2]

        def at(row, seq=None, ep=epoch):
            ident = {"record_id": row["subject"], "version": row["version"], "digest": row["digest"]}
            return {part: (impl, ep, seq or row["seq"], ident)}

        check = {"partition": part, "kept": await pull(d, r, SUITE, stream, at(a))}
        await c.execute(f"UPDATE {schema}.be_outbox SET version = version + 1 WHERE stream = $1 AND partition = $2 AND seq = $3",
                        stream, part, a["seq"])
        check["replaced"] = await pull(d, r, SUITE, stream, at(a))
        check["past_the_counter"] = await pull(d, r, SUITE, stream, at(a, seq=10**6))
        await c.execute(f"DELETE FROM {schema}.be_outbox WHERE stream = $1 AND partition = $2 AND seq = $3",
                        stream, part, b["seq"])
        check["purged"] = await pull(d, r, SUITE, stream, at(b))
        check["other_epoch"] = await pull(d, r, SUITE, stream, at(a, ep="0.1"))
        obs["check"] = check
    finally:
        await c.close()
    return obs


# ---------------------------------------------------------------------------------------------- judges


def need_of(p: dict, partition: int) -> str:
    return next((pr["need"] for pg in p["pages"] for pr in pg["partitions"] if pr["partition"] == partition), "?")


def judge_pullers(obs: dict) -> list[str]:
    o, p = obs["pullers"], []
    if o["stranger"] == "OK":
        p.append("a component the stream does not list could pull it")
    if o["suite"] != "OK" or not o["source"]:
        return p + [f"the declared puller's pull failed: {o['suite']}"]
    s = o["source"]
    if not s["primary"] or s["impl"] != obs["impl"] or s["deployment"] != obs["deployment"] or not s["system_id"]:
        p.append(f"the declaration is not this source's: {s}")
    return p


def judge_echo(obs: dict) -> list[str]:
    o, p = obs["echo"], []
    if None in o["committed"] or o["held_at"] is None:
        return ["precondition: the writes were not numbered"]
    if not 400 <= o["rolled_code"] < 500:
        p.append(f"the rolled-back write answered {o['rolled_code']}")
    first = {int(k): v for k, v in o["first_echo"].items()}
    only = [e for pg in o["echo_only"]["pages"] for pr in pg["partitions"] for e in pr["events"]]
    if any(e["type"] != ECHO for e in only) or len(only) != len(first):
        p.append("an echo-only pull returned more than the echoes")
    if any(seq >= first[part] for part, seq in o["committed"]):
        p.append("a change committed before the pull was numbered after its echo")
    part, seq = o["held_at"]
    if seq <= first[part]:
        p.append("the long transaction's row was numbered before the echo while still open")
    if o["held"] not in [e["subject"] for e in events_of(o["then"], part)]:
        p.append("the long transaction's row did not come with the next pull")
    for n, start in first.items():  # after the echo, every position once, none missing (the rollback left no gap)
        seqs = [e["seq"] for e in events_of(o["then"], n)]
        if seqs != list(range(start + 1, start + 1 + len(seqs))):
            p.append(f"partition {n}: the positions after the echo are not contiguous")
    pages = o["paged"]["pages"]
    if len(pages) < 2:
        p.append(f"{o['burst']} rows came in {len(pages)} page(s)")
    for n in {pr["partition"] for pg in pages for pr in pg["partitions"]}:
        seen = [(i, pr) for i, pg in enumerate(pages) for pr in pg["partitions"] if pr["partition"] == n]
        if any(len(pr["events"]) > PAGE for _, pr in seen):
            p.append(f"partition {n}: a page over {PAGE} rows")
        if any(pr["echo"] is not None for _, pr in seen[:-1]) or seen[-1][1]["echo"] is None:
            p.append(f"partition {n}: the echo is not on its last page only")
    if any(pg["source"] != pages[0]["source"] for pg in pages):
        p.append("the pages declare different sources")
    return p


def judge_check(obs: dict) -> list[str]:
    o, n, p = obs["check"], obs["check"]["partition"], []
    want = {"kept": "", "replaced": "rollback_detected", "past_the_counter": "rollback_detected",
            "purged": "beyond_retention", "other_epoch": ""}
    for k, v in want.items():
        got = need_of(o[k], n)
        if got != v:
            p.append(f"{k}: {got or 'checked'} instead of {v or 'checked'}")
    other = events_of(o["other_epoch"], n)
    if len(other) != 1 or other[0]["type"] != ECHO:
        p.append("a position of another epoch is answered echo only")
    if not events_of(o["kept"], n):
        p.append("precondition: the kept position returned nothing")
    return p


def _no_check(o: dict) -> None:
    for k in ("replaced", "past_the_counter", "purged"):
        for pg in o["check"][k]["pages"]:
            for pr in pg["partitions"]:
                pr["need"] = ""


def _echo_early(o: dict) -> None:
    pages = o["echo"]["paged"]["pages"]
    for pr in pages[0]["partitions"]:
        if pr["events"]:
            pr["echo"] = pr["events"][-1]["seq"]


WATERMARK = [
    Check("CK-WM-01", "流的声明没有列出的组件调用补取被拒绝；补取答出来源的声明", ("P9.13", "P9.18"), ("pullers",),
          judge_pullers, lambda o: mutated(o, lambda x: x["pullers"].update(stranger="OK"))),
    Check("CK-WM-02", "回声在探测前提交的变更之后；长事务只推迟自己、回滚不留空洞；分页时回声只在最后一页", ("P9.14", "P9.16"),
          ("echo",), judge_echo, lambda o: mutated(o, _echo_early)),
    Check("CK-WM-03", "连续位置上的行被换掉、序号超出计数答 rollback_detected，已清理答 beyond_retention，别的纪元只要回声（SK17）",
          ("P9.15",), ("check",), judge_check, lambda o: mutated(o, _no_check)),
]
