"""Runs a cell's measurements (experiment plan, suite A item 4 and item 3): an idle window, then for every edge of
the chain (caller -> callee) every probe operation at every load, with the resource sampler running for the measured
part of each. Writes completions (written -> received by the peer, -> relayed) are taken from the database. Every
result goes to the output directory as JSON, one file per measurement."""
from __future__ import annotations

import asyncio
import json
import pathlib
import subprocess
from urllib.parse import urlsplit

import asyncpg

from cells.spec import Cell, schema_of, stream_of
from harness import ROOT, need
from measure.backlog import jetstream as backlog
from measure.series import Window

KIB = 1024
OPS = {  # name -> driver flags (probes/CONTRACT.md)
    "echo-1k-grpc": ["-op", "call", "-surface", "grpc", "-inner", "echo", "-bytes", str(KIB)],
    "echo-1k-http": ["-op", "call", "-surface", "http", "-inner", "echo", "-bytes", str(KIB)],
    "echo-60k-grpc": ["-op", "call", "-surface", "grpc", "-inner", "echo", "-bytes", str(60 * KIB)],
    "echo-60k-http": ["-op", "call", "-surface", "http", "-inner", "echo", "-bytes", str(60 * KIB)],
    "work-grpc": ["-op", "call", "-surface", "grpc", "-inner", "work", "-ms", "10"],
    "work-http": ["-op", "call", "-surface", "http", "-inner", "work", "-ms", "10"],
    "write": ["-op", "write", "-bytes", "100"],
    "relay": ["-op", "write", "-bytes", "100", "-relay"],
    "big-event": ["-op", "write", "-bytes", str(100 * KIB)],
}
LOADS = {"50": ["-rate", "50"], "200": ["-rate", "200"], "ramp": ["-ramp"]}


def driver() -> pathlib.Path:
    out = ROOT / "conformance" / ".bin" / "load"
    subprocess.run(["go", "build", "-o", str(out), "."], cwd=ROOT / "load", check=True, timeout=600)
    return out


def db_dsn(c: Cell) -> str:
    return urlsplit(need("BE_ACC_PG_ADMIN"))._replace(path="/" + c.database).geturl()


async def completions(c: Cell, caller: str, callee: str, ids: list[str]) -> dict:
    """For each write: published, received by the callee, relayed (seconds after it was written)."""
    if not ids:
        return {}
    a, b = schema_of(caller), schema_of(callee)
    conn = await asyncpg.connect(db_dsn(c))
    try:
        rows = await conn.fetch(
            f"SELECT extract(epoch FROM o.published_at - w.written_at) AS published, "
            f"extract(epoch FROM s.received_at - w.written_at) AS received, "
            f"extract(epoch FROM s.relayed_at - w.written_at) AS relayed "
            f"FROM {a}.probe_writes w JOIN {a}.be_outbox o ON o.record_id = w.id::text "
            f"LEFT JOIN {b}.probe_seen s ON s.event_id = o.event_id WHERE w.id = ANY($1::uuid[])", ids)
    finally:
        await conn.close()

    def pct(xs: list[float]) -> dict:
        xs = sorted(x for x in xs if x is not None)
        if not xs:
            return {"n": 0}
        at = lambda q: xs[max(0, min(len(xs) - 1, int(q * len(xs) + 0.5) - 1))]  # noqa: E731
        return {"n": len(xs), "p50_ms": at(0.5) * 1000, "p95_ms": at(0.95) * 1000, "p99_ms": at(0.99) * 1000,
                "max_ms": xs[-1] * 1000}

    return {"writes": len(ids), "missing": len(ids) - sum(1 for r in rows if r["received"] is not None),
            **{k: pct([float(r[k]) if r[k] is not None else None for r in rows]) for k in ("published", "received", "relayed")}}


async def quiet(c: Cell, limit: float = 600) -> dict:
    """Waits until the previous measurement has left nothing behind: no unpublished outbox rows, no pending or
    unacknowledged messages, no relay waiting (Go: work items; Python: the probe's queue); then 3 s more. A
    measurement that starts on a busy system reads the leftovers as its own cost."""
    started = asyncio.get_running_loop().time()
    while True:
        left = await leftovers(c)
        if not any(left.values()):
            await asyncio.sleep(3)
            return {"settled": True, "waited": asyncio.get_running_loop().time() - started}
        if asyncio.get_running_loop().time() - started > limit:
            return {"settled": False, "waited": limit, "left": left}
        await asyncio.sleep(1)


async def leftovers(c: Cell) -> dict:
    conn = await asyncpg.connect(db_dsn(c))
    out = {}
    try:
        for p in c.probes:
            s = schema_of(p)
            out[f"{p}:outbox"] = await conn.fetchval(f"SELECT count(*) FROM {s}.be_outbox WHERE published_at IS NULL")
            if await conn.fetchval("SELECT to_regclass($1) IS NOT NULL", f"{s}.be_work_item"):
                # only the relay's queue items: the SDK's own singleton jobs (audit transfer, cleanups) are always
                # scheduled and never "drain"
                out[f"{p}:work"] = await conn.fetchval(
                    f"SELECT count(*) FROM {s}.be_work_item WHERE job = 'probe.relay' AND state IN ('pending', 'running')")
            if await conn.fetchval("SELECT to_regclass($1) IS NOT NULL", f"{s}.probe_relay"):
                out[f"{p}:relay"] = await conn.fetchval(f"SELECT count(*) FROM {s}.probe_relay")
        if c.bus == "pgqueue":  # deliveries not settled, and messages past a subscription's read position
            out["queue:deliveries"] = await conn.fetchval("SELECT count(*) FROM be_bus.be_bus_delivery")
            out["queue:unread"] = await conn.fetchval(
                "SELECT count(*) FROM be_bus.be_bus_subscription s JOIN be_bus.be_bus_message m "
                "ON (m.xid, m.seq) > (s.pos_xid, s.pos_seq)")
    finally:
        await conn.close()
    if c.bus == "jetstream":
        for stream, consumers in (await backlog(need("BE_ACC_NATS_URL"), [stream_of(p) for p in c.probes])).items():
            for name, b in consumers.items():
                out[f"{stream}:{name}"] = b["pending"] + b["ack_pending"]
    return out


def window_of(c: Cell) -> Window:
    from cells.chainrun import window
    return window(c)


async def measure(c: Cell, binary: pathlib.Path, caller: str, callee: str, op: str, load: str, out: pathlib.Path,
                  warmup: int, seconds: int) -> dict:
    settled = await quiet(c)
    target = f"http://127.0.0.1:{c.host_port(caller)}"
    flags = [*OPS[op], *LOADS[load], "-warmup", f"{warmup}s", "-duration", f"{seconds}s", "-step-time", f"{seconds // 3}s"]
    # the whole cell, as on its row (containers, pods or processes); the tables read the caller's and callee's units
    units = sorted({c.unit_of(caller), c.unit_of(callee)})
    w, stop = window_of(c), asyncio.Event()
    creds = ["-credentials", str(c.dir / ".secrets" / "load-user.json")] if c.secured else []
    proc = await asyncio.create_subprocess_exec(str(binary), "-target", target, *flags, *creds, "-out", str(out / "driver.json"))
    await asyncio.sleep(warmup)
    sampler = asyncio.create_task(w.run_until(stop, db_dsn(c)))
    await proc.wait()
    stop.set()
    await sampler
    result = json.loads((out / "driver.json").read_text())
    sent = result.get("sent") or sum(s["sent"] for s in result.get("steps", []))
    record = {"cell": c.name, "caller": caller, "callee": callee, "op": op, "load": load, "flags": flags,
              "before": settled,
              "containers": {x: [p for p in (caller, callee) if c.unit_of(p) == x] for x in units},
              "resources": w.figures(requests=sent), "driver": result}
    if OPS[op][1] == "write" and "writes" in result:
        await asyncio.sleep(5)  # forwarding, the claim check, the relay
        record["completion"] = await completions(c, caller, callee, [x["id"] for x in result["writes"]])
        result.pop("writes", None)
    if c.bus == "jetstream":
        record["backlog"] = await backlog(need("BE_ACC_NATS_URL"), [stream_of(caller), stream_of(callee)])
    (out / "result.json").write_text(json.dumps(record, indent=2))
    (out / "driver.json").unlink()
    return record


async def idle(c: Cell, out: pathlib.Path, seconds: int) -> dict:
    await quiet(c)
    w = window_of(c)
    await w.run(seconds, db_dsn(c))
    record = {"cell": c.name, "row": c.row, "idle_seconds": seconds, "resources": w.figures(),
              "units": {p: c.unit_of(p) for p in c.probes}}
    (out / "idle.json").write_text(json.dumps(record, indent=2))
    return record


async def run(directory: str, out_dir: str, ops: list[str], loads: list[str], idle_seconds: int, warmup: int,
              seconds: int) -> None:
    c = Cell.load(directory)
    out = pathlib.Path(out_dir)
    out.mkdir(parents=True, exist_ok=True)
    binary = driver()
    await idle(c, out, idle_seconds)
    edges = [(p, c.next_of(p)) for p in c.chain if c.next_of(p)]
    for caller, callee in edges:
        for op in ops:
            for load in loads:
                d = out / f"{caller.split('/')[1]}--{callee.split('/')[1]}" / op / load
                d.mkdir(parents=True, exist_ok=True)
                r = await measure(c, binary, caller, callee, op, load, d, warmup, seconds)
                drv = r["driver"]
                head = drv.get("hop_ms") or drv.get("latency_ms") or {}
                print(f"{caller} -> {callee} {op} {load}: ok {drv.get('ok', '-')} "
                      f"p50 {head.get('P50', '-')} p99 {head.get('P99', '-')} "
                      f"sustained {drv.get('sustained_rate', '-')}", flush=True)
