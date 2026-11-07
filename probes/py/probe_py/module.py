"""The probe module: the user surface (/api/probe/...), the system surface (/test.probe.v1.Probe/Echo, /Work and, since
3.2.22, /Note), Write with its event, the relay of the peer's events to the next probe and, since 3.2.44, the jobs
of the background work profile (jobs.py)."""
from __future__ import annotations

import asyncio
import contextlib
import os
import sys
import time
import uuid

import grpc
from fastapi.responses import JSONResponse
from google.protobuf import wrappers_pb2

from besdk import errs
from besdk.calls.client import options
from besdk.events.consumer import Received, Sub
from besdk.events.core import Event, Projection
from besdk.http import Route
from besdk.system import Method
from probe_py import (CALLERS, MAX_BYTES, MAX_LABEL, MAX_WORK_MS, NOTED_TYPE, RELAY_BYTES, WRITTEN_TYPE, Variant,
                      prefix)

ECHO, WORK, NOTE = "/test.probe.v1.Probe/Echo", "/test.probe.v1.Probe/Work", "/test.probe.v1.Probe/Note"


def invalid(msg: str) -> errs.Error:
    return errs.new("REQUEST_INVALID", msg)


# A worker spends the milliseconds each line asks for (wall clock as the measure) and answers a line. Standalone code
# under -I, so a worker imports neither this package nor the SDK.
BURNER = """
import sys, time
for line in sys.stdin:
    x, end = 1, time.perf_counter() + int(line) / 1000
    while time.perf_counter() < end:
        for _ in range(1000):
            x = (x * 6364136223846793005 + 1442695040888963407) & 0xFFFFFFFFFFFFFFFF
    sys.stdout.write("ok\\n")
    sys.stdout.flush()
"""


class Burners:
    """Work's CPU runs in worker processes (merge safety): in a Python shell the members share one event loop, and a
    thread would still hold the GIL against it, so a member's work would hold up the others' requests and deadlines.
    A work takes an idle worker or starts one; a cancelled work kills its worker; at most one idle worker per CPU stays.
    Not a multiprocessing pool: the process does not own one per module (the forkserver is one per process, and fork
    copies the other members' connections, locks and threads into the worker)."""

    def __init__(self):
        self._idle: list[asyncio.subprocess.Process] = []
        self._all: set[asyncio.subprocess.Process] = set()

    async def burn(self, ms: int) -> None:
        w = self._idle.pop() if self._idle else await self._spawn()
        try:
            w.stdin.write(f"{ms}\n".encode())
            await w.stdin.drain()
            if await w.stdout.readline() != b"ok\n":
                raise RuntimeError("a burner ended")
        except BaseException:
            self._kill(w)
            raise
        if len(self._idle) < (os.process_cpu_count() or 1):
            self._idle.append(w)
        else:
            self._kill(w)

    async def _spawn(self) -> asyncio.subprocess.Process:
        w = await asyncio.create_subprocess_exec(sys.executable, "-I", "-c", BURNER, stdin=asyncio.subprocess.PIPE,
                                                 stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.DEVNULL,
                                                 env={})  # its own: the process environment is shared by a shell's members
        self._all.add(w)
        return w

    def _kill(self, w: asyncio.subprocess.Process) -> None:
        with contextlib.suppress(ProcessLookupError):
            w.kill()
        self._all.discard(w)

    async def close(self) -> None:
        workers, self._idle = list(self._all), []
        for w in workers:
            self._kill(w)
        await asyncio.gather(*(w.wait() for w in workers), return_exceptions=True)


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
        self._burners = Burners()
        from probe_py.jobs import Jobs

        self._jobs = Jobs(self)

    # ------------------------------------------------------------------ user surface

    def routes(self, r) -> None:
        guard = self.rt.config.bool("PROBE_GUARD")
        for method, path, op, h in (("POST", "echo", "echo", self.echo_http), ("POST", "work", "work", self.work_http),
                                    ("POST", "write", "write", self.write), ("POST", "call", "call", self.call),
                                    ("GET", "seen", "read", self.seen), ("POST", "note", "note", self.note_http),
                                    ("GET", "written/{id}", "read", self.written_row),
                                    ("POST", "job", "write", self._jobs.enqueue_job),
                                    ("POST", "steps", "write", self._jobs.set_steps)):
            r.add(Route(method, "/api/probe/" + path, h, f"{self.ops}.{op}" if guard else "", public=not guard))
        if guard:  # the entries are always protected (since 3.2.38)
            from probe_py.entries import Entries

            Entries(self.rt, self.ops).routes(
                lambda method, path, op, h: r.add(Route(method, "/api/probe/" + path, h, f"{self.ops}.{op}")))
            if self.rt.config.bool("PROBE_UNDECLARED_ROUTE"):  # an operation nobody declared: the start fails (CK-AZ-01)
                r.add(Route("GET", "/api/probe/undeclared", self.seen, f"{self.ops}.undeclared"))

    async def echo_http(self, c):
        body = await c.json()
        if not isinstance(body, dict) or not isinstance(body.get("payload", ""), str):
            raise invalid("payload must be a string")
        return JSONResponse({"payload": body.get("payload", "")})

    async def work_http(self, c):
        ms = number(await c.json(), "ms", MAX_WORK_MS)
        await self._burners.burn(ms)
        return JSONResponse({"ms": ms})

    async def write(self, c):
        body = await c.json()
        n, relay = number(body, "bytes", MAX_BYTES), bool(body.get("relay", False))
        label, event = body.get("label", ""), body.get("event", "")  # both since 3.2.22, optional
        if not isinstance(label, str) or len(label.encode()) > MAX_LABEL or event not in ("", "noted"):
            raise invalid("bytes must be 0..524288, label at most 64 bytes, event empty or noted")
        typ = NOTED_TYPE if event == "noted" else WRITTEN_TYPE
        rid = str(uuid.uuid7())

        async def fn(tx):
            await tx.execute("INSERT INTO probe_writes (id, bytes, label) VALUES ($1, $2, $3)", uuid.UUID(rid), n,
                             label or None)
            data = {"id": rid, "relay": relay, **({"blob": "w" * n} if n else {}), **({"label": label} if label else {})}
            await self.rt.events.emit(tx, self.v.stream, Event(typ, rid, 1, Projection.FULL, data))

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
        elif surface == "grpc" and op == "note":
            await self._stub(NOTE, wrappers_pb2.BytesValue)(wrappers_pb2.BytesValue(value=b"n"),
                                                            metadata=options(idempotent=True))
        elif surface == "http" and op in ("echo", "work", "note"):
            h = self.rt.http_at(self.next, self.rt.config.string("PROBE_NEXT_URL"))
            if h is None:
                raise self.no_next.new("PROBE_NEXT_URL is not set")
            path, payload = {"echo": ("/api/probe/echo", {"payload": "e" * n}), "work": ("/api/probe/work", {"ms": ms}),
                             "note": ("/api/probe/note", {"payload": "n"})}[op]
            await h.do("POST", path, payload, idempotent=True, forward_user=self.rt.config.bool("PROBE_GUARD"))
        else:
            raise invalid("op must be echo, work or note, surface grpc or http")
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
            "count(*) FILTER (WHERE relayed_at IS NOT NULL) AS relayed, count(*) FILTER (WHERE type = $1) AS noted "
            "FROM probe_seen", NOTED_TYPE))
        return JSONResponse(dict(row))

    async def note_http(self, c):
        """The payload with "noted:" before it (since 3.2.22)."""
        body = await c.json()
        if not isinstance(body, dict) or not isinstance(body.get("payload", ""), str):
            raise invalid("payload must be a string")
        return JSONResponse({"payload": "noted:" + body.get("payload", "")})

    async def written_row(self, c):
        """One written row read back (since 3.2.22): a row of an older probe on the same schema has no label."""
        try:
            rid = uuid.UUID(c.param("id"))
        except ValueError:
            raise errs.new("NOT_FOUND", "no such write") from None
        row = await self.rt.db.tx(lambda tx: tx.fetchrow("SELECT bytes, label FROM probe_writes WHERE id = $1", rid))
        if row is None:
            raise errs.new("NOT_FOUND", "no such write")
        return JSONResponse({"id": str(rid), "bytes": row["bytes"], "label": row["label"]})

    # ------------------------------------------------------------------ system surface

    def services(self, s) -> None:
        async def echo(request, context):
            return request

        async def work(request, context):
            if request.value > MAX_WORK_MS:
                raise invalid("ms must be 0..1000")
            await self._burners.burn(request.value)
            return request

        def handler(fn, kind):
            return grpc.unary_unary_rpc_method_handler(fn, request_deserializer=kind.FromString,
                                                       response_serializer=kind.SerializeToString)

        async def note(request, context):
            return wrappers_pb2.BytesValue(value=b"noted:" + request.value)

        s.add("test.probe.v1.Probe", {"Echo": handler(echo, wrappers_pb2.BytesValue),
                                      "Work": handler(work, wrappers_pb2.UInt32Value),
                                      "Note": handler(note, wrappers_pb2.BytesValue)},
              {"Echo": Method(f"{self.ops}.echo", CALLERS), "Work": Method(f"{self.ops}.work", CALLERS),
               "Note": Method(f"{self.ops}.note", CALLERS)})

    # ------------------------------------------------------------------ events and the relay

    def subscriptions(self, s) -> None:
        if peer := self.rt.config.string("PROBE_PEER_STREAM"):
            # both types since 3.2.22; an older probe takes test.probe.written.v1 only (P6.33: the transport filters the rest)
            s.add(Sub("peer", [peer], self.received, types=[WRITTEN_TYPE, NOTED_TYPE]))

    async def received(self, tx, e: Received) -> None:
        w = e.decode()
        await tx.execute("INSERT INTO probe_seen (event_id, source, size, relay, type) VALUES ($1, $2, $3, $4, $5)",
                         uuid.UUID(e.id), e.source, len(w.get("blob", "")), bool(w.get("relay")), e.type)
        if w.get("relay"):
            # the call to the next probe leaves the transaction as a work item (no network call inside it)
            await self.rt.work.enqueue(tx, "probe.relay", {"event": e.id})

    def jobs(self, j) -> None:
        self._jobs.declare(j)

    async def stop(self) -> None:
        await self._burners.close()
