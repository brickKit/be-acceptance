"""The background work profile (be-protocol chapter 07) against one component: the Go probe's jobs (probes/CONTRACT.md,
"后台工作项") enqueued over its user surface, the probe stopped, killed and outdated while it runs them, and what each
claim did read from the component's schema: the item's row and the probe's record of every attempt. Each judge
returns the problems; each bad() turns a good observation into a known violation.

Before every fault the observation records that it hit what it means to: the attempt was running (started, not
ended) and the probe held the item with a live lease; the judges fail when that precondition did not hold.

Not here: CK-WK-05 and the takeover half of CK-WK-03 (a second replica: one host cannot run a component twice, the
grpc port is part of its definition; T10 runs replicas), the requeue half of CK-WK-01 (an operator's action, no such
interface on a component), the re-materializing half of CK-WK-10 (the probe has no cron job)."""
from __future__ import annotations

import asyncio
import time
from typing import Any

import asyncpg
import httpx

from harness import Deployment, Running, Spec
from profile.checks import mutated
from profile.events import admin_url, eventually
from profile.model import Check

LEASE_S = 30.0  # P7.7, the SDK's lease
HOLD_MS = 120_000  # long enough for every fault below
SLACK_S = 3.0


async def post(r: Running, token: str, path: str, body: dict) -> tuple[int, dict]:
    async with httpx.AsyncClient(timeout=15) as c:
        resp = await c.post(r.url + path, json=body, headers={"Authorization": "Bearer " + token})
    try:
        return resp.status_code, resp.json()
    except ValueError:
        return resp.status_code, {}


class Schema:
    """Reads of the probe's schema, with times as epoch seconds on the database's clock."""

    def __init__(self, c: asyncpg.Connection, schema: str):
        self.c, self.s = c, schema

    async def rows(self, sql: str, *args) -> list[dict]:
        return [dict(x) for x in await self.c.fetch(sql.replace("§", self.s + "."), *args)]

    async def now(self) -> float:
        return await self.c.fetchval("SELECT extract(epoch FROM now())::float8")

    async def item(self, id: str) -> dict | None:
        got = await self.rows("""SELECT id, state, attempts, epoch, holder, operation_id, outcome_unknown, pause_reason,
            extract(epoch FROM lease_until)::float8 AS lease_until, lease_until > now() AS lease_live
            FROM §be_work_item WHERE id = $1""", id)
        return got[0] if got else None

    async def attempts(self, id: str) -> list[dict]:
        return await self.rows("""SELECT attempt, step, operation_id, instance, ended,
            extract(epoch FROM started_at)::float8 AS started_at, extract(epoch FROM ended_at)::float8 AS ended_at
            FROM §probe_job_attempts WHERE item = $1 ORDER BY step, attempt""", id)

    async def effects(self, id: str) -> list[dict]:
        return await self.rows("SELECT step, operation_id, instance FROM §probe_job_effects WHERE item = $1 ORDER BY step, at", id)

    async def calls(self, id: str) -> list[dict]:
        return await self.rows("SELECT key, idempotent, instance FROM §probe_job_calls WHERE item = $1 ORDER BY at", id)

    async def dead(self, id: str) -> list[dict]:
        return await self.rows("SELECT cause FROM §probe_job_dead WHERE item = $1", id)

    async def snapshot(self, id: str) -> dict:
        return {"item": await self.item(id), "attempts": await self.attempts(id), "effects": await self.effects(id),
                "calls": await self.calls(id), "dead": await self.dead(id), "at": await self.now()}

    async def state(self, id: str) -> str:
        i = await self.item(id)
        return i["state"] if i else ""


async def running(s: Schema, id: str, step: int = 0) -> bool:
    """The precondition of a fault: the attempt has started and not ended."""
    return any(a["step"] == step and a["ended"] is None for a in await s.attempts(id))


async def observe(d: Deployment, r: Running, spec: Spec, env: dict, token: str) -> tuple[dict, Running]:
    """Everything the work checks judge; the probe is stopped and killed on the way, so the running one comes back."""
    c = await asyncpg.connect(admin_url())
    s = Schema(c, spec.schema)
    obs: dict[str, Any] = {"lease_s": LEASE_S}

    async def enqueue(**body) -> str:
        code, got = await post(r, token, "/api/probe/job", body)
        assert code == 201, (code, got)
        return got["id"]

    async def settled(id: str, *states: str, timeout: float) -> str:
        return await eventually(lambda: _in(s, id, states), timeout=timeout)

    try:
        # CK-WK-01: a rolled-back business transaction leaves no item
        count = "SELECT (SELECT count(*) FROM §be_work_item WHERE job LIKE 'probe.job.%') AS items, (SELECT count(*) FROM §probe_job_requests) AS requests"
        before = (await s.rows(count))[0]
        code, _ = await post(r, token, "/api/probe/job", {"rollback": True})
        obs["rollback"] = {"code": code, "before": before, "after": (await s.rows(count))[0]}

        # CK-WK-08: retryable failures back off and end dead with the dead letter; a permanent one at once; dead stays
        f5 = await enqueue(fail="retryable", fail_times=5)
        f1 = await enqueue(fail="retryable", fail_times=1)
        fp = await enqueue(fail="permanent")
        for id, want in ((f5, "dead"), (f1, "done"), (fp, "dead")):
            await settled(id, want, timeout=20)
        await asyncio.sleep(SLACK_S)  # a dead item that came back would have by now (its next backoff is 4 s)
        obs["failures"] = {"exhausted": await s.snapshot(f5), "once": await s.snapshot(f1),
                           "permanent": await s.snapshot(fp), "max_attempts": 3}

        # CK-WK-04: stopped while it runs an item: the lease is given back and the restarted probe takes it at once
        st = await enqueue(hold_ms=HOLD_MS)
        await eventually(lambda: running(s, st), timeout=10)
        at_stop = await s.snapshot(st)
        code = await r.stop()
        d.running.remove(r)
        r = await d.start(spec, env)
        ready = await s.now()
        await settled(st, "done", timeout=LEASE_S)
        obs["stop"] = {"before": at_stop, "exit": code, "ready_at": ready, "after": await s.snapshot(st)}

        # CK-WK-02, 06, 07, 09: killed while it runs one item of each effect class and a singleton's segment
        k = {"local": await enqueue(hold_ms=HOLD_MS),
             "idempotent": await enqueue(effect="idempotent_external", hold_ms=HOLD_MS),
             "non_idempotent": await enqueue(effect="non_idempotent_external", hold_ms=HOLD_MS)}
        code, _ = await post(r, token, "/api/probe/steps", {"n": 5, "hold_step": 3})
        assert code == 202, code
        for id in k.values():
            await eventually(lambda: running(s, id), timeout=10)
        await eventually(lambda: running(s, "probe.steps", 3), timeout=10)
        at_kill = {name: await s.snapshot(id) for name, id in k.items()} | {"steps": await s.snapshot("probe.steps")}
        r.proc.kill()
        killed = await asyncio.to_thread(r.proc.wait, 10)
        d.running.remove(r)
        r = await d.start(spec, env)
        for id, want in ((k["local"], "done"), (k["idempotent"], "done"), (k["non_idempotent"], "paused")):
            await settled(id, want, timeout=LEASE_S + 20)
        await eventually(lambda: _stepped(s, 5), timeout=LEASE_S + 20)
        obs["kill"] = {"exit": killed, "before": at_kill,
                       "after": {name: await s.snapshot(id) for name, id in k.items()} | {"steps": await s.snapshot("probe.steps")}}

        # CK-WK-03: the recovery generation advances while it runs an item: the claim is outdated and its run cancelled
        e = await enqueue(hold_ms=HOLD_MS)
        await eventually(lambda: running(s, e), timeout=10)
        at_bump = await s.snapshot(e)
        await c.execute(f"UPDATE {spec.schema}.be_recovery_generation SET generation = generation + 1")
        bumped = await s.now()
        await settled(e, "done", timeout=LEASE_S + LEASE_S / 3 + 20)
        obs["epoch"] = {"before": at_bump, "bumped_at": bumped, "after": await s.snapshot(e)}

        # CK-WK-10: the cleanup run deletes only what is past its retention and not dead or paused
        obs["cleanup"] = await cleanup(c, s, spec.schema)
    finally:
        await c.close()
    return obs, r


async def _in(s: Schema, id: str, states: tuple[str, ...]) -> str:
    st = await s.state(id)
    return st if st in states else ""


async def _stepped(s: Schema, n: int) -> bool:
    return any(e["step"] == n for e in await s.effects("probe.steps"))


CLEANUP_ROWS = {  # id: (kind, state, finished days ago, run_at hours from now, expected to stay)
    "wk10-queue-old": ("queue", "done", 8, -200, False),
    "wk10-queue-young": ("queue", "done", 6, -150, True),
    "wk10-dead-old": ("queue", "dead", 40, -1000, True),
    "wk10-paused-old": ("queue", "paused", 40, -1000, True),
    "wk10-cron-old": ("cron", "done", 31, -750, False),
    "wk10-cron-ahead": ("cron", "done", 31, 1, True),  # its slot is after the job's materialization watermark
}


async def cleanup(c: asyncpg.Connection, s: Schema, schema: str) -> dict:
    await c.execute(f"INSERT INTO {schema}.be_cron_watermark (job, watermark_at) VALUES ('probe.wk10', now())")
    for id, (kind, state, days, hours, _) in CLEANUP_ROWS.items():
        await c.execute(f"""INSERT INTO {schema}.be_work_item (id, job, kind, state, max_attempts, effect_class,
            operation_id, pause_reason, slot, run_at, finished_at) VALUES ($1, $2, $3, $4, 3, 'local', $1, $5, $6,
            now() + make_interval(hours => $7), now() - make_interval(days => $8))""",
                        id, "probe.wk10" if kind == "cron" else "probe.job.local", kind, state,
                        "business_hold" if state == "paused" else None, id if kind == "cron" else None, hours, days)
    # the hourly cleanup is due now rather than in an hour
    await c.execute(f"UPDATE {schema}.be_work_item SET run_at = now() WHERE id = 'be.work.cleanup' AND state = 'pending'")
    started = await s.now()

    async def ran() -> bool:
        i = await s.rows("SELECT extract(epoch FROM run_at)::float8 AS run_at, state FROM §be_work_item WHERE id = 'be.work.cleanup'")
        return bool(i) and i[0]["state"] == "pending" and i[0]["run_at"] > started + 60

    did = await eventually(ran, timeout=20)
    left = {x["id"] for x in await s.rows("SELECT id FROM §be_work_item WHERE id LIKE 'wk10-%'")}
    return {"ran": did, "rows": {id: {"stays": v[4], "left": id in left} for id, v in CLEANUP_ROWS.items()}}


# ---------------------------------------------------------------------------------------------- judges


def _pre(snap: dict, what: str) -> list[str]:
    """A fault's precondition: the attempt was running and the probe held the item with a live lease."""
    i = snap["item"]
    if not i or i["state"] != "running" or not i["holder"] or not i["lease_live"]:
        return [f"{what}: the item was not held with a live lease when the fault came ({i})"]
    if not snap["attempts"] or snap["attempts"][-1]["ended"] is not None:
        return [f"{what}: no attempt was running when the fault came"]
    return []


def judge_rollback(obs: dict) -> list[str]:
    o, p = obs["rollback"], []
    if not 400 <= o["code"] < 500:
        p.append(f"the rolled-back enqueue answered {o['code']}")
    if o["after"] != o["before"]:
        p.append(f"a rolled-back enqueue left rows: {o['before']} -> {o['after']}")
    for name, snap in obs["kill"]["after"].items():
        ops = {a["operation_id"] for a in snap["attempts"] if a["step"] in (0, 3)}
        if name != "non_idempotent" and len(ops) != 1:
            p.append(f"{name}: the operation ID changed on takeover: {ops}")
    return p


def judge_takeover(obs: dict) -> list[str]:
    o = obs["kill"]
    b, a = o["before"]["local"], o["after"]["local"]
    p = _pre(b, "kill")
    if o["exit"] != -9:
        p.append(f"the probe was not killed: it exited {o['exit']}")
    if p:
        return p
    if a["item"]["state"] != "done":
        p.append(f"not taken over: {a['item']['state']}")
    if a["item"]["epoch"] != b["item"]["epoch"] + 1:
        p.append(f"the epoch went {b['item']['epoch']} -> {a['item']['epoch']}, not up by one")
    second = [x for x in a["attempts"] if x["attempt"] == 2]
    if not second or second[0]["instance"] == b["attempts"][0]["instance"]:
        p.append("no second attempt by another process")
    elif second[0]["started_at"] < b["item"]["lease_until"]:
        p.append("taken over before the dead holder's lease ran out")
    if {x["operation_id"] for x in a["attempts"]} != {b["item"]["operation_id"]}:
        p.append("the takeover ran under another operation ID")
    return p


def judge_outdated(obs: dict) -> list[str]:
    o = obs["epoch"]
    p = _pre(o["before"], "epoch")
    if p:
        return p
    first = o["after"]["attempts"][0]
    if first["ended"] != "cancelled":
        p.append(f"the outdated claim's run was not cancelled: {first['ended']}")
    elif first["ended_at"] - o["bumped_at"] > obs["lease_s"] / 3 + SLACK_S:
        p.append(f"cancelled {first['ended_at'] - o['bumped_at']:.1f} s after the epoch changed, past a renewal")
    if len(o["after"]["effects"]) != 1 or o["after"]["item"]["state"] != "done":
        p.append(f"after the outdated claim: {len(o['after']['effects'])} effects, {o['after']['item']['state']}")
    return p


def judge_stop(obs: dict) -> list[str]:
    o = obs["stop"]
    p = _pre(o["before"], "stop")
    if o["exit"] != 0:
        p.append(f"the stopped probe exited {o['exit']}")
    if p:
        return p
    a = o["after"]
    first = a["attempts"][0]
    if first["ended"] != "cancelled":
        p.append(f"the stopped run was not cancelled: {first['ended']}")
    second = [x for x in a["attempts"] if x["attempt"] == 2]
    if not second:
        p.append("the restarted probe did not run it")
    elif second[0]["started_at"] - o["ready_at"] > SLACK_S + 1:
        p.append(f"taken {second[0]['started_at'] - o['ready_at']:.1f} s after the restart: the lease was not given back")
    if a["item"]["attempts"] != 1:
        p.append(f"the stopped attempt was not given back: {a['item']['attempts']} attempts counted")
    if len(a["effects"]) != 1:
        p.append(f"{len(a['effects'])} effects")
    return p


def judge_once(obs: dict) -> list[str]:
    o = obs["kill"]
    p = _pre(o["before"]["local"], "kill") + _pre(o["before"]["idempotent"], "kill")
    if p:
        return p
    for name in ("local", "idempotent"):
        if len(o["after"][name]["effects"]) != 1:
            p.append(f"{name}: {len(o['after'][name]['effects'])} effects after the takeover")
    calls = o["after"]["idempotent"]["calls"]
    if len(calls) != 1:
        p.append(f"the idempotent call reached its far side {len(calls)} times")
    if len({x["operation_id"] for x in o["after"]["idempotent"]["attempts"]}) != 1:
        p.append("the idempotent call's key changed on takeover")
    return p


def judge_unknown(obs: dict) -> list[str]:
    o = obs["kill"]
    b, a = o["before"]["non_idempotent"], o["after"]["non_idempotent"]
    p = _pre(b, "kill")
    if not p and (len(b["calls"]) != 1 or not b["item"]["outcome_unknown"]):
        p.append("the call was not made and marked before the kill")
    if p:
        return p
    if a["item"]["state"] != "paused" or a["item"]["pause_reason"] != "outcome_unknown":
        p.append(f"after the takeover: {a['item']['state']} ({a['item']['pause_reason']}), not paused outcome_unknown")
    if len(a["calls"]) != 1:
        p.append(f"the non-idempotent call was sent {len(a['calls'])} times")
    if len(a["attempts"]) != 1:
        p.append("the taker ran the handler again")
    return p


def judge_failures(obs: dict) -> list[str]:
    o, p = obs["failures"], []
    ex = o["exhausted"]
    if ex["item"]["state"] != "dead" or not ex["dead"] or len(ex["attempts"]) != o["max_attempts"]:
        p.append(f"exhausted retries: {ex['item']['state']}, {len(ex['attempts'])} attempts, dead letter {bool(ex['dead'])}")
    else:
        starts = [x["started_at"] for x in ex["attempts"]]
        gaps = [b - a for a, b in zip(starts, starts[1:])]
        if any(g < want for g, want in zip(gaps, (1.0, 2.0))):
            p.append(f"the retries did not back off 1 s then 2 s: {[round(g, 2) for g in gaps]}")
    once = o["once"]
    if once["item"]["state"] != "done" or len(once["attempts"]) != 2 or len(once["effects"]) != 1:
        p.append(f"one retryable failure: {once['item']['state']}, {len(once['attempts'])} attempts")
    pm = o["permanent"]
    if pm["item"]["state"] != "dead" or not pm["dead"] or len(pm["attempts"]) != 1:
        p.append(f"a permanent failure: {pm['item']['state']}, {len(pm['attempts'])} attempts")
    return p


def judge_segments(obs: dict) -> list[str]:
    o = obs["kill"]
    b, a = o["before"]["steps"], o["after"]["steps"]
    held = [x for x in b["attempts"] if x["step"] == 3 and x["ended"] is None]
    if not held or not b["item"] or b["item"]["state"] != "running":
        return ["kill: the singleton was not running its third segment when the fault came"]
    p, dead = [], held[0]["instance"]
    by_step = {}
    for e in a["effects"]:
        by_step.setdefault(e["step"], []).append(e)
    if sorted(by_step) != [1, 2, 3, 4, 5] or any(len(v) != 1 for v in by_step.values()):
        p.append(f"each step once: {[(k, len(v)) for k, v in sorted(by_step.items())]}")
        return p
    if any(by_step[s][0]["instance"] != dead for s in (1, 2)):
        p.append("the committed segments were redone by the taker")
    if by_step[3][0]["operation_id"] != held[0]["operation_id"]:
        p.append("the interrupted segment ran under another operation ID")
    if any(x["instance"] != dead and x["step"] < 3 for x in a["attempts"]):
        p.append("the taker started before the committed progress")
    return p


def judge_cleanup(obs: dict) -> list[str]:
    o = obs["cleanup"]
    if not o["ran"]:
        return ["the cleanup did not run"]
    return [f"{id}: {'deleted' if not v['left'] else 'kept'}, should be {'kept' if v['stays'] else 'deleted'}"
            for id, v in o["rows"].items() if v["left"] != v["stays"]]


def _redone(o: dict) -> None:
    effects = o["kill"]["after"]["local"]["effects"]
    effects.append(dict(effects[0]))


def _early(o: dict) -> None:
    a = o["kill"]["after"]["local"]["attempts"]
    a[-1]["started_at"] = o["kill"]["before"]["local"]["item"]["lease_until"] - 5


def _restart_from_zero(o: dict) -> None:
    effects = o["kill"]["after"]["steps"]["effects"]
    taker = next(e["instance"] for e in effects if e["step"] == 5)
    for e in effects:
        e["instance"] = taker


WORK = [
    Check("CK-WK-01", "业务事务回滚时没有工作项；接管后 operation_id 不变", ("P7.2", "P7.3"), ("rollback", "kill"),
          judge_rollback, lambda o: mutated(o, lambda x: x["rollback"]["after"].update(items=x["rollback"]["before"]["items"] + 1))),
    Check("CK-WK-02", "租约到期后被另一个进程以原身份接管，代次加一", ("P7.5",), ("kill",), judge_takeover,
          lambda o: mutated(o, _early)),
    Check("CK-WK-03", "推进部署恢复代次后旧认领失效，执行被取消", ("P7.6", "P7.7"), ("epoch",), judge_outdated,
          lambda o: mutated(o, lambda x: x["epoch"]["after"]["attempts"][0].update(ended="done"))),
    Check("CK-WK-04", "停止时释放租约，重启后不等租约到期就接手，尝试计数不增加", ("P7.8",), ("stop",), judge_stop,
          lambda o: mutated(o, lambda x: x["stop"]["after"]["item"].update(attempts=2))),
    Check("CK-WK-06", "local 作业在效果提交前崩溃，接管后效果只有一次；幂等调用沿用同一个键", ("P7.10", "P7.11"), ("kill",),
          judge_once, lambda o: mutated(o, _redone)),
    Check("CK-WK-07", "非幂等调用发出后崩溃，接管者转 paused，不重发", ("P7.12",), ("kill",), judge_unknown,
          lambda o: mutated(o, lambda x: x["kill"]["after"]["non_idempotent"]["calls"].append({"key": "again"}))),
    Check("CK-WK-08", "可重试错误按退避重试，用完进 dead 并调用死信处理；dead 不自动重试", ("P7.13", "P7.14"), ("failures",),
          judge_failures, lambda o: mutated(o, lambda x: x["failures"]["exhausted"].update(dead=[]))),
    Check("CK-WK-09", "singleton 换持有者后从已提交的进度继续，沿用这一段的 operation_id", ("P7.17",), ("kill",),
          judge_segments, lambda o: mutated(o, _restart_from_zero)),
    Check("CK-WK-10", "清理不删未到保留期的行，也不删 dead、paused", ("P7.19",), ("cleanup",), judge_cleanup,
          lambda o: mutated(o, lambda x: x["cleanup"]["rows"]["wk10-dead-old"].update(left=False))),
]
