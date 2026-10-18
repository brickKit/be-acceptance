"""The two probes alone and together (step 3, T3.8): every operation of the contract (probes/CONTRACT.md) on both
surfaces, calls in both directions over mTLS, events in both directions on JetStream and on the PostgreSQL queue
(a payload over 64 KiB included), and the relay of each event back as a call."""
import asyncio

import pytest

from harness import Deployment, address, free_port, fresh_bus

KIB = 1024


async def eventually(fn, want, within=30.0):
    got = None
    for _ in range(int(within / 0.1)):
        got = await fn()
        if got == want:
            return
        await asyncio.sleep(0.1)
    raise AssertionError(f"{got} != {want}")


async def operations_alone(p):
    for n in (KIB, 60 * KIB):
        r = await p.post("/api/probe/echo", {"payload": "x" * n})
        assert r.status_code == 200 and len(r.json()["payload"]) == n, r.text
    r = await p.post("/api/probe/work", {"ms": 20})
    assert r.json() == {"ms": 20}
    assert (await p.post("/api/probe/work", {"ms": 5000})).json()["reason"] == "REQUEST_INVALID"
    assert (await p.post("/api/probe/write", {"bytes": 10})).status_code == 201
    r = await p.post("/api/probe/call", {"op": "echo", "surface": "grpc", "bytes": 10})
    assert r.status_code == 409 and r.json()["reason"] == "NO_NEXT_PROBE", r.text
    assert r.json()["domain"] == p.spec.id, "the variant's own reason"


@pytest.mark.parametrize("kind", ["go", "go2", "go3", "go4", "py", "py2"])
async def test_each_probe_alone(kind, specs, world, tmp_path):
    spec = specs[kind]
    d = Deployment(world, await fresh_bus("jetstream", [spec.stream]), tmp_path)
    try:
        p = await d.start(spec, await d.env(spec, {}))
        await operations_alone(p)
        assert (await p.get("/api/probe/seen")).json() == {"count": 0, "big": 0, "relayed": 0}
    finally:
        await d.stop()


@pytest.mark.parametrize("bus", ["jetstream", "pgqueue"])
async def test_the_probes_call_each_other_and_exchange_events(bus, specs, world, tmp_path):
    go, py = specs["go"], specs["py"]
    d = Deployment(world, await fresh_bus(bus, [go.stream, py.stream]), tmp_path)
    try:
        gport, pport = free_port(), free_port()
        g = await d.start(go, await d.env(go, {"PROBE_NEXT": py.id, "PROBE_PEER_STREAM": py.stream,
                                               **address(py, pport)}), gport)
        p = await d.start(py, await d.env(py, {"PROBE_NEXT": go.id, "PROBE_PEER_STREAM": go.stream,
                                               **address(go, gport)}), pport)
        for caller in (g, p):
            for surface in ("grpc", "http"):
                for body in ({"op": "echo", "bytes": KIB}, {"op": "echo", "bytes": 60 * KIB}, {"op": "work", "ms": 10}):
                    r = await caller.post("/api/probe/call", {**body, "surface": surface})
                    assert r.status_code == 200, (caller.spec.id, surface, body, r.text, caller.logs()[-2000:])
                    assert r.json()["elapsed_us"] > 0, "the caller measures the hop itself"
        for producer in (g, p):
            for i in range(5):
                assert (await producer.post("/api/probe/write", {"bytes": 100, "relay": i % 2 == 0})).status_code == 201
            assert (await producer.post("/api/probe/write", {"bytes": 100 * KIB})).status_code == 201
        # each probe saw the other's six events, one over 64 KiB, and relayed three of them back as calls
        for consumer in (g, p):
            await eventually(lambda c=consumer: _seen(c),
                             {"count": 6, "big": 1, "relayed": 3})
    finally:
        await d.stop()


async def _seen(r):
    return (await r.get("/api/probe/seen")).json()
