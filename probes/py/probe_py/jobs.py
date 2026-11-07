"""The probe's jobs (3.2.44; the Go probe since 3.2.30; CONTRACT.md "后台工作项"): the relay of the peer's events, one
queue job per effect class, the minutely cron job with PROBE_CRON and the steps singleton with PROBE_STEPS_EVERY.
Every attempt leaves a row in probe_job_attempts, so the suite's background work profile sees from outside what each
claim did."""
from __future__ import annotations

import asyncio
import time
import uuid

from fastapi.responses import JSONResponse

from besdk import errs, reqctx
from probe_py import RELAY_BYTES
from besdk.work import (CRON, IDEMPOTENT_EXTERNAL, LOCAL, NON_IDEMPOTENT_EXTERNAL, QUEUE, SINGLETON, Job, Permanent)

JOB_ATTEMPTS = 3
MAX_JOB_HOLD_MS = 120_000
MAX_STEPS = 20
RELAY_TIMEOUT = 10.0
JOB_NAMES = {"local": "probe.job.local", "idempotent_external": "probe.job.idempotent",
             "non_idempotent_external": "probe.job.non_idempotent"}
EFFECTS = {"local": LOCAL, "idempotent_external": IDEMPOTENT_EXTERNAL, "non_idempotent_external": NON_IDEMPOTENT_EXTERNAL}


def invalid(msg: str) -> errs.Error:
    return errs.new("REQUEST_INVALID", msg)


class Jobs:
    """What the probe module adds for its jobs; probe is the module (its runtime, next probe and relay call)."""

    def __init__(self, probe):
        self.p, self.rt = probe, probe.rt
        self.instance = str(uuid.uuid7())  # this process, as the job attempts record it

    def declare(self, j) -> None:
        j.add(Job("probe.relay", QUEUE, self.relay, IDEMPOTENT_EXTERNAL, timeout=RELAY_TIMEOUT))
        for effect, name in JOB_NAMES.items():
            j.add(Job(name, QUEUE, self.job, EFFECTS[effect], max_attempts=JOB_ATTEMPTS, dead_letter=self.dead_letter))
        if self.rt.config.bool("PROBE_CRON"):  # a slot a minute, its run recorded (CK-WK-05)
            async def minutely(run) -> None:
                await run.complete(lambda tx: tx.execute("INSERT INTO probe_cron (slot, instance) VALUES ($1, $2)",
                                                         run.slot, self.instance))
            j.add(Job("probe.minutely", CRON, minutely, LOCAL, cron="* * * * *", time_zone="UTC"))
        every = self.rt.config.duration("PROBE_STEPS_EVERY")
        if every and every.total_seconds() > 0:
            j.add(Job("probe.steps", SINGLETON, self.steps, LOCAL, interval=every))

    # ------------------------------------------------------------------ the relay

    async def relay(self, run) -> None:
        if self.p.next:
            with reqctx.deadline_scope(time.monotonic() + RELAY_TIMEOUT):  # calls need a deadline (P4.4)
                await self.p.echo_next(RELAY_BYTES)
        event = uuid.UUID(run.args["event"])
        await run.complete(lambda tx: tx.execute("UPDATE probe_seen SET relayed_at = now() WHERE event_id = $1", event))

    # ------------------------------------------------------------------ the profile's jobs

    async def enqueue_job(self, c):
        """A business row and a probe job in one transaction, `201 {"id": <item>}`."""
        body = await c.json()
        if not isinstance(body, dict):
            raise invalid("a JSON object")
        effect, hold, fail = body.get("effect") or "local", body.get("hold_ms", 0), body.get("fail", "")
        times, rollback = body.get("fail_times", 0), bool(body.get("rollback", False))
        if (effect not in JOB_NAMES or not isinstance(hold, int) or not 0 <= hold <= MAX_JOB_HOLD_MS
                or fail not in ("", "retryable", "permanent") or not isinstance(times, int) or times < 0):
            raise invalid("effect local, idempotent_external or non_idempotent_external; hold_ms 0..120000; fail empty, "
                          "retryable or permanent")
        args = {"effect": effect, "hold_ms": hold, "fail": fail, "fail_times": times, "rollback": rollback}
        out: dict[str, str] = {}

        async def fn(tx) -> None:
            await tx.execute("INSERT INTO probe_job_requests (id) VALUES ($1)", str(uuid.uuid7()))
            out["id"] = await self.rt.work.enqueue(tx, JOB_NAMES[effect], args)
            if rollback:
                raise invalid("rolled back as asked (rollback)")

        await self.rt.db.tx(fn)
        return JSONResponse({"id": out["id"]}, status_code=201)

    async def job(self, run) -> None:
        a = run.args
        attempt = await self._attempt(run, 0)
        ended = "failed"
        try:
            if a["fail"] == "permanent":
                raise Permanent("failed as asked (permanent)")
            if a["fail"] == "retryable" and attempt <= a["fail_times"]:
                raise RuntimeError("failed as asked (retryable)")
            await self._call_far_side(run, a["effect"])
            if attempt == 1 and a["hold_ms"]:
                try:
                    await asyncio.sleep(a["hold_ms"] / 1000)
                except asyncio.CancelledError:
                    ended = "cancelled"
                    raise
            await run.complete(self._effect(run, 0))
            ended = "done"
        finally:
            await self._ended(run.id, attempt, 0, ended)

    async def _call_far_side(self, run, effect: str) -> None:
        """The external job's call, recorded on its far side: the idempotent one under the run's sub-key (P7.11),
        once per key; the non-idempotent one after marking the call as possibly sent (P7.12), every time."""
        if effect == "local":
            return
        key, idempotent = run.sub_key("call"), True
        if effect == "non_idempotent_external":
            await run.before_call()
            key, idempotent = run.operation_id, False
        await self.rt.db.background_tx(lambda tx: tx.execute(
            """INSERT INTO probe_job_calls (key, item, idempotent, instance) VALUES ($1, $2, $3, $4)
            ON CONFLICT (key) WHERE idempotent DO NOTHING""", key, run.id, idempotent, self.instance))

    def _effect(self, run, step: int):
        async def fn(tx) -> None:
            await tx.execute("INSERT INTO probe_job_effects (item, step, operation_id, instance) VALUES ($1, $2, $3, $4)",
                             run.id, step, run.operation_id, self.instance)
        return fn

    async def dead_letter(self, tx, item, cause) -> None:
        await tx.execute("INSERT INTO probe_job_dead (item, cause) VALUES ($1, $2)", item.id, str(cause))

    async def _attempt(self, run, step: int) -> int:
        """Records an attempt as it starts and returns its number for the item and step."""
        return await self.rt.db.background_tx(lambda tx: tx.fetchval(
            """INSERT INTO probe_job_attempts (item, attempt, step, operation_id, instance)
            SELECT $1, count(*) + 1, $2, $3, $4 FROM probe_job_attempts WHERE item = $1 AND step = $2 RETURNING attempt""",
            run.id, step, run.operation_id, self.instance))

    async def _ended(self, item: str, attempt: int, step: int, how: str) -> None:
        """Records how an attempt ended, also while the run is being cancelled."""
        async def fn(tx) -> None:
            await tx.execute("UPDATE probe_job_attempts SET ended = $4, ended_at = now() WHERE item = $1 AND attempt = $2 "
                             "AND step = $3", item, attempt, step, how)
        try:
            await asyncio.shield(self.rt.db.background_tx(fn))
        except Exception:  # noqa: BLE001 - a record of how it ended, best effort
            pass

    # ------------------------------------------------------------------ the steps singleton

    async def set_steps(self, c):
        """The steps singleton's next task, `202`."""
        body = await c.json()
        n, hold = (body.get("n"), body.get("hold_step", 0)) if isinstance(body, dict) else (None, None)
        if not isinstance(n, int) or not isinstance(hold, int) or not 1 <= n <= MAX_STEPS or not 0 <= hold <= n:
            raise invalid("n 1..20, hold_step 0..n")
        await self.rt.db.tx(lambda tx: tx.execute(
            """INSERT INTO probe_steps (id, n, hold_step) VALUES (1, $1, $2)
            ON CONFLICT (id) DO UPDATE SET n = $1, hold_step = $2, held = false""", n, hold))
        return JSONResponse({"n": n, "hold_step": hold}, status_code=202)

    async def steps(self, run) -> None:
        """The task in segments, one step a segment, each committed with its progress (P7.17); the last step completes
        the run and takes the task away. The first process to reach hold_step waits there (up to 120 s)."""
        row = await self.rt.db.background_tx(lambda tx: tx.fetchrow(
            "SELECT n, hold_step, held FROM probe_steps WHERE id = 1"))
        if row is None:
            await run.complete()
            return
        n, hold_step, held = row["n"], row["hold_step"], row["held"]
        done = run.progress if isinstance(run.progress, int) else 0
        for s in range(done + 1, n + 1):
            attempt = await self._attempt(run, s)
            if s == hold_step and not held:
                await self.rt.db.background_tx(lambda tx: tx.execute("UPDATE probe_steps SET held = true WHERE id = 1"))
                held = True
                try:
                    await asyncio.sleep(MAX_JOB_HOLD_MS / 1000)
                except asyncio.CancelledError:
                    await self._ended(run.id, attempt, s, "cancelled")
                    raise
            if s == n:
                effect = self._effect(run, s)

                async def last(tx, effect=effect) -> None:
                    await effect(tx)
                    await tx.execute("DELETE FROM probe_steps WHERE id = 1")

                await run.complete(last)
            else:
                await run.checkpoint(s, self._effect(run, s))
            await self._ended(run.id, attempt, s, "done")

