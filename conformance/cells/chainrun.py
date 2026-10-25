"""Step 8 (T8.5): one business-chain cell's resources and performance (experiment plan, suite A item 2): an idle
window, then the representative mix (load -mix) with the sampler running over its measured part. Every component
container (alone or a shell) is sampled, with the base resources and the platform; the result goes to
<out>/idle.json and <out>/mix.json. A cell brought up without seed or baseline data (no world) is measured idle only."""
from __future__ import annotations

import asyncio
import json
import os
import pathlib

import asyncpg

from cells.runner import db_dsn, driver
from cells.spec import Cell, schema_of, stream_of
from harness import need
from measure.backlog import jetstream as backlog
from measure.series import Window

# the business components that publish events (cells/prepare.py PRODUCERS, and the master data)
STREAMED = ("mdm/org", "mdm/currency", "mdm/partner", "mdm/product", "erp/inventory", "erp/credit", "erp/sales", "erp/billing")


def containers(c: Cell) -> list[str]:
    """Every component container, and Casdoor behind the identity member: the whole deployment needs it, so it counts
    with the base resources (the sampler adds PostgreSQL, NATS and object storage itself)."""
    return sorted({c.container_of(p) for p in c.placed} | ({"be-casdoor"} if "infra/iam-casdoor" in c.placed else set()))


async def leftovers(c: Cell) -> dict:
    """Unpublished outbox rows of every business component, and messages its consumers have not acknowledged."""
    out = {}
    conn = await asyncpg.connect(db_dsn(c))
    try:
        for p in c.placed:
            s = schema_of(p)
            if await conn.fetchval("SELECT to_regclass($1) IS NOT NULL", f"{s}.be_outbox"):
                out[f"{p}:outbox"] = await conn.fetchval(f"SELECT count(*) FROM {s}.be_outbox WHERE published_at IS NULL")
    finally:
        await conn.close()
    streams = [stream_of(p) for p in STREAMED if p in c.placed]
    for stream, consumers in (await backlog(need("BE_ACC_NATS_URL"), streams)).items():
        for name, b in consumers.items():
            out[f"{stream}:{name}"] = b["pending"] + b["ack_pending"]
    return out


async def quiet(c: Cell, limit: float = 600) -> dict:
    """Waits until the earlier work has left nothing behind, then 3 s more (a busy start reads as the run's cost)."""
    loop = asyncio.get_running_loop()
    started = loop.time()
    while True:
        left = await leftovers(c)
        if not any(left.values()):
            await asyncio.sleep(3)
            return {"settled": True, "waited": round(loop.time() - started, 1)}
        if loop.time() - started > limit:
            return {"settled": False, "waited": limit, "left": {k: v for k, v in left.items() if v}}
        await asyncio.sleep(1)


async def completions(c: Cell, orders: list[str]) -> dict:
    """For each order billed in the run: how long after its invoice was issued the order completed (billing
    formalizes at the source, which completes the order: protocol 05). Orders not completed yet are counted."""
    if not orders:
        return {"orders": 0}
    conn = await asyncpg.connect(db_dsn(c))
    try:
        rows = await conn.fetch(
            "SELECT o.state, extract(epoch FROM o.updated_at - i.issued_at) * 1000 AS ms FROM erp_sales.sales_order o "
            "JOIN erp_billing.invoice i ON i.source_id = o.id AND i.state = 'issued' WHERE o.id = ANY($1::uuid[])", orders)
    finally:
        await conn.close()
    done = sorted(float(r["ms"]) for r in rows if r["state"] == "completed")

    def at(q: float) -> float:
        return round(done[max(0, min(len(done) - 1, int(q * len(done) + 0.5) - 1))], 1) if done else 0.0
    return {"orders": len(orders), "completed": len(done), "issue_to_completed_ms": {
        "P50": at(0.50), "P95": at(0.95), "P99": at(0.99), "Max": round(done[-1], 1) if done else 0.0}}


async def idle(c: Cell, out: pathlib.Path, seconds: int) -> dict:
    before = await quiet(c)
    w = Window(containers(c))
    await w.run(seconds, db_dsn(c))
    record = {"cell": c.name, "idle_seconds": seconds, "before": before, "resources": w.figures()}
    (out / "idle.json").write_text(json.dumps(record, indent=2))
    return record


async def mix(c: Cell, binary: pathlib.Path, world: str, out: pathlib.Path, rate: float, users: int, warmup: int,
              seconds: int) -> dict:
    before = await quiet(c)
    w, stop = Window(containers(c)), asyncio.Event()
    flags = ["-mix", world, "-rate", str(rate), "-users", str(users), "-warmup", f"{warmup}s", "-duration", f"{seconds}s"]
    env = {**os.environ, "BE_LOAD_PASSWORD": need("BE_ACC_IAM_TEST_PASSWORD")}
    proc = await asyncio.create_subprocess_exec(str(binary), *flags, "-out", str(out / "driver.json"), env=env)
    await asyncio.sleep(warmup)
    sampler = asyncio.create_task(w.run_until(stop, db_dsn(c)))
    if await proc.wait() != 0:
        stop.set()
        await sampler
        raise RuntimeError(f"{c.name}: the load driver failed (exit {proc.returncode})")
    stop.set()
    await sampler
    result = json.loads((out / "driver.json").read_text())
    billed = result["mix"].pop("billed_orders", None) or []
    after = await quiet(c)  # the formalizations behind the last bills have run
    record = {"cell": c.name, "flags": flags, "before": before, "containers": containers(c),
              "resources": w.figures(requests=result["sent"]), "driver": result,
              "backlog": await backlog(need("BE_ACC_NATS_URL"), [stream_of(p) for p in STREAMED if p in c.placed]),
              "after": after, "completion": await completions(c, billed)}
    (out / "mix.json").write_text(json.dumps(record, indent=2))
    (out / "driver.json").unlink()
    return record


async def run(directory: str, out_dir: str, world: str | None, rate: float, users: int, idle_seconds: int, warmup: int,
              seconds: int) -> None:
    c = Cell.load(directory)
    out = pathlib.Path(out_dir)
    out.mkdir(parents=True, exist_ok=True)
    if idle_seconds:
        i = await idle(c, out, idle_seconds)
        print(f"{c.name} idle {idle_seconds}s: platform {json.dumps(i['resources'].get('platform', {}))}", flush=True)
    if world is None:  # no seed or baseline data: nothing for the mix to work on
        return
    m = await mix(c, driver(), world, out, rate, users, warmup, seconds)
    d = m["driver"]
    print(f"{c.name} mix {rate}/s x {seconds}s: sent {d['sent']} ok {d['ok']} p95 {d['latency_ms']['P95']} ms; "
          f"issue to completed {m['completion']}", flush=True)
