"""The probe module: the user surface (/api/probe/...), the system surface (/test.probe.v1.Probe/Echo and /Work),
Write with its event, and the relay of the peer's events to the next probe."""
from __future__ import annotations

import asyncio
import time
import uuid

import grpc
from fastapi.responses import JSONResponse
from google.protobuf import wrappers_pb2

from besdk import errs, reqctx
from besdk.calls.client import options
from besdk.events.consumer import Received, Sub
from besdk.events.core import Event, Projection
from besdk.http import Route
from besdk.system import Method
from probe_py import CALLERS, MAX_BYTES, MAX_WORK_MS, RELAY_BYTES, WRITTEN_TYPE, Variant, prefix

ECHO, WORK = "/test.probe.v1.Probe/Echo", "/test.probe.v1.Probe/Work"
RELAY_POLL, RELAY_CONCURRENCY, RELAY_LEASE, RELAY_TIMEOUT = 0.5, 4, 30, 10.0


def invalid(msg: str) -> errs.Error:
    return errs.new("REQUEST_INVALID", msg)


def burn(ms: int) -> None:
    """Spends ms of CPU time on this thread (wall clock as the measure); the event loop waits, as any CPU work does."""
    x, end = 1, time.perf_counter() + ms / 1000
    while time.perf_counter() < end:
        for _ in range(1000):
            x = (x * 6364136223846793005 + 1442695040888963407) & 0xFFFFFFFFFFFFFFFF


def number(body: dict, key: str, hi: int) -> int:
    v = body.get(key, 0)
    if isinstance(v, bool) or not isinstance(v, int) or not 0 <= v <= hi:
        raise invalid(f"{key} must be 0..{hi}")
    return v


class Probe:
    def __init__(self, rt, variant: Variant):
        self.rt, self.v, self.ops = rt, variant, prefix(variant.id)
        # the probe's own reason: this deployment gives the probe no next probe to call
        self.no_next = errs.define(variant.id, "NO_NEXT_PROBE", errs.Code.FAILED_PRECONDITION, 409, errs.RETRY_NO,
                                   "No next probe is configured")
        self.next = rt.config.string("PROBE_NEXT")
        self._tasks: list[asyncio.Task] = []

    # ------------------------------------------------------------------ user surface

    def routes(self, r) -> None:
        guard = self.rt.config.bool("PROBE_GUARD")
        for method, path, op, h in (("POST", "echo", "echo", self.echo_http), ("POST", "work", "work", self.work_http),
                                    ("POST", "write", "write", self.write), ("POST", "call", "call", self.call),
                                    ("GET", "seen", "read", self.seen)):
            r.add(Route(method, "/api/probe/" + path, h, f"{self.ops}.{op}" if guard else "", public=not guard))

    async def echo_http(self, c):
        body = await c.json()
        if not isinstance(body, dict) or not isinstance(body.get("payload", ""), str):
            raise invalid("payload must be a string")
        return JSONResponse({"payload": body.get("payload", "")})

    async def work_http(self, c):
        ms = number(await c.json(), "ms", MAX_WORK_MS)
        burn(ms)
        return JSONResponse({"ms": ms})

    async def write(self, c):
        body = await c.json()
        n, relay = number(body, "bytes", MAX_BYTES), bool(body.get("relay", False))
        rid = str(uuid.uuid7())

        async def fn(tx):
            await tx.execute("INSERT INTO probe_writes (id, size) VALUES ($1, $2)", uuid.UUID(rid), n)
            data = {"id": rid, "relay": relay, **({"blob": "w" * n} if n else {})}
            await self.rt.events.emit(tx, self.v.stream, Event(WRITTEN_TYPE, rid, 1, Projection.FULL, data))

        await self.rt.db.tx(fn)
        return JSONResponse({"id": rid}, status_code=201)

    async def call(self, c):
        body = await c.json()
        op, surface = body.get("op"), body.get("surface")
        n, ms = number(body, "bytes", MAX_BYTES), number(body, "ms", MAX_WORK_MS)
        if not self.next:
            raise self.no_next.new("PROBE_NEXT is not set")
        start = time.perf_counter()
        if surface == "grpc" and op == "echo":
            await self.echo_next(n)
        elif surface == "grpc" and op == "work":
            await self._stub(WORK, wrappers_pb2.UInt32Value)(wrappers_pb2.UInt32Value(value=ms),
                                                             metadata=options(idempotent=True))
        elif surface == "http" and op in ("echo", "work"):
            h = self.rt.http_at(self.next, self.rt.config.string("PROBE_NEXT_URL"))
            if h is None:
                raise self.no_next.new("PROBE_NEXT_URL is not set")
            path, payload = ("/api/probe/echo", {"payload": "e" * n}) if op == "echo" else ("/api/probe/work", {"ms": ms})
            await h.do("POST", path, payload, idempotent=True, forward_user=self.rt.config.bool("PROBE_GUARD"))
        else:
            raise invalid("op must be echo or work, surface grpc or http")
        # the hop alone, as the caller sees it: the driver's own request to this probe is not in it
        return JSONResponse({"ok": True, "elapsed_us": int((time.perf_counter() - start) * 1_000_000)})

    def _stub(self, method: str, kind):
        channel = self.rt.grpc_at(self.next, self.rt.config.string("PROBE_NEXT_GRPC_URL"))
        if channel is None:
            raise self.no_next.new("PROBE_NEXT_GRPC_URL is not set")
        return channel.unary_unary(method, request_serializer=kind.SerializeToString, response_deserializer=kind.FromString)

    async def echo_next(self, n: int) -> None:
        out = await self._stub(ECHO, wrappers_pb2.BytesValue)(wrappers_pb2.BytesValue(value=bytes(n)),
                                                              metadata=options(idempotent=True))
        if len(out.value) != n:
            raise RuntimeError("the echo came back with another size")

    async def seen(self, c):
        row = await self.rt.db.tx(lambda tx: tx.fetchrow(
            "SELECT count(*) AS count, count(*) FILTER (WHERE size > 65536) AS big, "
            "count(*) FILTER (WHERE relayed_at IS NOT NULL) AS relayed FROM probe_seen"))
        return JSONResponse(dict(row))

    # ------------------------------------------------------------------ system surface

    def services(self, s) -> None:
        async def echo(request, context):
            return request

        async def work(request, context):
            if request.value > MAX_WORK_MS:
                raise invalid("ms must be 0..1000")
            burn(request.value)
            return request

        def handler(fn, kind):
            return grpc.unary_unary_rpc_method_handler(fn, request_deserializer=kind.FromString,
                                                       response_serializer=kind.SerializeToString)

        s.add("test.probe.v1.Probe", {"Echo": handler(echo, wrappers_pb2.BytesValue),
                                      "Work": handler(work, wrappers_pb2.UInt32Value)},
              {"Echo": Method(f"{self.ops}.echo", CALLERS), "Work": Method(f"{self.ops}.work", CALLERS)})

    # ------------------------------------------------------------------ events and the relay

    def subscriptions(self, s) -> None:
        if peer := self.rt.config.string("PROBE_PEER_STREAM"):
            s.add(Sub("peer", [peer], self.received, types=[WRITTEN_TYPE]))

    async def received(self, tx, e: Received) -> None:
        w = e.decode()
        await tx.execute("INSERT INTO probe_seen (event_id, source, size, relay) VALUES ($1, $2, $3, $4)",
                         uuid.UUID(e.id), e.source, len(w.get("blob", "")), bool(w.get("relay")))
        if w.get("relay"):
            # the call to the next probe leaves the transaction through the relay queue (no network call inside it)
            await tx.execute("INSERT INTO probe_relay (event_id) VALUES ($1)", uuid.UUID(e.id))

    async def start(self) -> None:
        self._tasks.append(asyncio.create_task(self._relay_loop()))

    async def stop(self) -> None:
        for t in self._tasks:
            t.cancel()
        await asyncio.gather(*self._tasks, return_exceptions=True)

    async def _relay_loop(self) -> None:
        while True:
            try:
                n = await self._relay_once()
            except asyncio.CancelledError:
                raise
            except Exception as e:  # noqa: BLE001 - the leases expire and the rows are claimed again
                self.rt.logger.warn("the relay failed; it is retried", error=str(e))
                n = 0
            if n == 0:
                await asyncio.sleep(RELAY_POLL)

    async def _relay_once(self) -> int:
        rows = await self.rt.db.background_tx(lambda tx: tx.fetch(
            "UPDATE probe_relay SET leased_to = now() + make_interval(secs => $2), attempts = attempts + 1 "
            "WHERE event_id IN (SELECT event_id FROM probe_relay WHERE leased_to IS NULL OR leased_to < now() "
            "ORDER BY event_id LIMIT $1 FOR UPDATE SKIP LOCKED) RETURNING event_id", RELAY_CONCURRENCY, RELAY_LEASE))
        await asyncio.gather(*(self._relay(r["event_id"]) for r in rows))
        return len(rows)

    async def _relay(self, event_id: uuid.UUID) -> None:
        if self.next:
            try:
                with reqctx.deadline_scope(time.monotonic() + RELAY_TIMEOUT):  # calls need a deadline (P4.4)
                    await self.echo_next(RELAY_BYTES)
            except (errs.Error, RuntimeError) as e:
                self.rt.logger.warn("a relay call failed; it is retried after its lease", error=str(e))
                return

        async def done(tx) -> None:
            await tx.execute("DELETE FROM probe_relay WHERE event_id = $1", event_id)
            await tx.execute("UPDATE probe_seen SET relayed_at = now() WHERE event_id = $1", event_id)

        await self.rt.db.background_tx(done)
