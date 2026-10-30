"""Suite C (the deployment experiment's faults): what must hold in a cell's data once the work behind a fault has run,
whatever the fault was. The digest (digest.py) compares two cells; these compare a cell with itself: every confirmation
process ended, orders and the participants' branches agree (protocol 04), the stock and credit reservations add up to
their branches, invoices and the sales claims agree (protocol 05), every outbox row is published, no work item died.
The SQL only reads facts; the judging is in plain functions, so the tests can feed them rows.

The background load keeps running through the faults, so at any moment some orders are being confirmed, some events
are on their way out: what is judged is what has stood still for longer than `age` seconds (a confirmation takes
milliseconds, a recovery seconds), not what is merely in flight. The reservations and their sums are written in one
transaction by their owner, so those are judged at once."""
from __future__ import annotations

import asyncio
import json
import pathlib
from urllib.parse import urlsplit

import asyncpg

from cells.spec import Cell
from harness import need

OPEN = ("reserving", "committing", "rejecting", "cancelling")
# a participant branch's states that hold stock or credit
HOLDING = ("held", "committed")

# $2 below: how many seconds a row must have stood still to be judged
PROCESSES = "SELECT o.number, c.kind, c.state FROM erp_sales.confirmation c JOIN erp_sales.sales_order o ON o.id = c.order_id " \
            "WHERE c.state = ANY($1::text[]) AND c.updated_at < now() - make_interval(secs => $2)"
CONFIRMING = "SELECT number, state FROM erp_sales.sales_order WHERE state = 'confirming' " \
             "AND updated_at < now() - make_interval(secs => $1)"
# every branch of an order's current confirmation, with what the participant has under that ID (null: nothing)
ORDER_BRANCHES = """
SELECT o.number, o.state, cb.component, coalesce(ib.state, cr.state)
FROM erp_sales.sales_order o
JOIN erp_sales.confirmation c ON c.id = o.confirmation_id
JOIN erp_sales.confirmation_branch cb ON cb.confirmation_id = c.id
LEFT JOIN erp_inventory.branch ib ON cb.component = 'erp/inventory' AND ib.id = cb.branch_id
LEFT JOIN erp_credit.branch cr ON cb.component = 'erp/credit' AND cr.id = cb.branch_id
WHERE o.updated_at < now() - make_interval(secs => $1)
  AND coalesce(ib.updated_at, cr.updated_at, '-infinity') < now() - make_interval(secs => $1)"""
# participant branches that hold something, with the states of the orders whose processes carry their IDs (none: an
# orphan the coordinator does not know)
HOLDING_BRANCHES = """
SELECT p.component, p.id::text, p.state, array_remove(array_agg(DISTINCT o.state), NULL)
FROM (SELECT 'erp/inventory' AS component, id, state, updated_at FROM erp_inventory.branch WHERE state = ANY($1::text[])
      UNION ALL SELECT 'erp/credit', id, state, updated_at FROM erp_credit.branch WHERE state = ANY($1::text[])) p
LEFT JOIN erp_sales.confirmation_branch cb ON cb.branch_id = p.id AND cb.component = p.component
LEFT JOIN erp_sales.confirmation c ON c.id = cb.confirmation_id
LEFT JOIN erp_sales.sales_order o ON o.id = c.order_id
WHERE p.updated_at < now() - make_interval(secs => $2)
GROUP BY 1, 2, 3
HAVING max(o.updated_at) IS NULL OR max(o.updated_at) < now() - make_interval(secs => $2)"""
STOCK = """
WITH r AS (SELECT bl.product_id, bl.warehouse_id, sum(bl.quantity - bl.consumed) AS q FROM erp_inventory.branch b
           JOIN erp_inventory.branch_line bl ON bl.branch_id = b.id WHERE b.state = ANY($1::text[]) GROUP BY 1, 2)
SELECT p.product_id::text || '@' || p.warehouse_id::text, p.reserved::text, coalesce(r.q, 0)::text
FROM erp_inventory.stock_position p LEFT JOIN r USING (product_id, warehouse_id)
WHERE p.reserved <> coalesce(r.q, 0)"""
CREDIT = """
WITH e AS (SELECT limit_id, sum(amount - consumed) AS a FROM erp_credit.branch WHERE state = ANY($1::text[]) GROUP BY 1)
SELECT l.id::text, l.order_exposure::text, coalesce(e.a, 0)::text
FROM erp_credit.credit_limit l LEFT JOIN e ON e.limit_id = l.id WHERE l.order_exposure <> coalesce(e.a, 0)"""
# an invoice and the sales claim of the same ID (the claim's ID is the invoice's)
BILLING = """
SELECT coalesce(i.number, i.id::text, c.id::text), i.state, i.formalization, c.state
FROM erp_billing.invoice i FULL JOIN erp_sales.billing_claim c ON c.id = i.id
WHERE greatest(i.updated_at, c.updated_at) < now() - make_interval(secs => $1)"""
BILLED = """
WITH b AS (SELECT il.source_line_id, sum(il.quantity) AS q FROM erp_billing.invoice_line il
           JOIN erp_billing.invoice i ON i.id = il.invoice_id WHERE i.state = 'issued' GROUP BY 1)
SELECT l.id::text, l.billed_qty::text, coalesce(b.q, 0)::text FROM erp_sales.order_line l
JOIN erp_sales.sales_order o ON o.id = l.order_id
LEFT JOIN b ON b.source_line_id = l.id WHERE l.billed_qty <> coalesce(b.q, 0)
  AND o.updated_at < now() - make_interval(secs => $1)"""
SCHEMAS = "SELECT table_schema FROM information_schema.tables WHERE table_name = $1 ORDER BY 1"


def judge_processes(processes: list, confirming: list) -> list[str]:
    """No confirmation process (confirm, cancel, release) is left open, and no order is left confirming."""
    return [f"{n}: {kind} process still {state}" for n, kind, state in processes] + \
           [f"{n}: order still {state}" for n, state in confirming]


def judge_orders(order_branches: list, holding: list) -> list[str]:
    """Protocol 04: a confirmed order's current confirmation has every branch occupied at its participant (committed,
    or consumed by a delivery); a completed order's are consumed or released; a draft or cancelled order's hold
    nothing. And the other way: a branch that holds stock or credit belongs to a confirmed order."""
    out = []
    for n, state, component, held in order_branches:
        if state == "confirmed" and held not in ("committed", "consumed"):
            out.append(f"{n}: confirmed but its {component} branch is {held or 'missing'}")
        elif state == "completed" and held not in ("consumed", "released"):
            out.append(f"{n}: completed but its {component} branch is {held or 'missing'}")
        elif state in ("draft", "cancelled") and held in HOLDING:
            out.append(f"{n}: {state} but its {component} branch is {held}")
    for component, bid, state, orders in holding:
        if state == "held":
            out.append(f"{component} {bid}: still held with no process open")
        elif "confirmed" not in orders:
            out.append(f"{component} {bid}: {state} for orders {sorted(orders) or 'unknown to sales'}")
    return out


def judge_sums(name: str, rows: list) -> list[str]:
    """Rows (key, recorded, from the branches) that differ."""
    return [f"{name} {k}: {recorded} recorded, {expected} by the branches" for k, recorded, expected in rows]


def judge_billing(rows: list, billed: list) -> list[str]:
    """Protocol 05: an issued invoice is formalized at the source and its claim is formalized; a formalized claim has
    its issued invoice; nothing waits on a formalization; a void invoice's claim is released; each order line's
    billed quantity is what the issued invoices bill of it. A draft's claim may be pending or confirmed (a draft lives
    for the claim period)."""
    out = []
    for key, inv, formal, claim in rows:
        if inv == "issued" and (formal != "done" or claim != "formalized"):
            out.append(f"invoice {key}: issued, formalization {formal}, claim {claim or 'missing'}")
        elif claim == "formalized" and inv != "issued":
            out.append(f"claim {key}: formalized but the invoice is {inv or 'missing'}")
        elif inv == "void" and claim not in ("released", "expired", None):
            out.append(f"invoice {key}: void but its claim is {claim}")
        elif formal == "pending":
            out.append(f"invoice {key}: formalization still pending")
    return out + [f"order line {k}: billed {rec}, issued invoices bill {exp}" for k, rec, exp in billed]


def judge_runtime(outbox: dict, work: dict) -> list[str]:
    """Every outbox row numbered and published; no work item dead or paused, no queued item still waiting (the
    singletons, scheduled and always pending, are not counted)."""
    out = [f"{s}: {n} outbox rows not published" for s, n in sorted(outbox.items()) if n]
    for s, states in sorted(work.items()):
        for state, n in sorted(states.items()):
            if n and state in ("dead", "paused", "queued"):
                out.append(f"{s}: {n} work items {state}")
    return out


async def read(c: Cell, age: float = 120) -> dict[str, list[str]]:
    conn = await asyncpg.connect(urlsplit(need("BE_ACC_PG_ADMIN"))._replace(path="/" + c.database).geturl())
    try:
        outbox, work = {}, {}
        for (s,) in await conn.fetch(SCHEMAS, "be_outbox"):
            outbox[s] = await conn.fetchval(f'SELECT count(*) FROM "{s}".be_outbox WHERE (seq IS NULL OR published_at IS NULL) '
                                            f'AND occurred_at < now() - make_interval(secs => $1)', age)
        for (s,) in await conn.fetch(SCHEMAS, "be_work_item"):
            work[s] = {st: n for st, n in await conn.fetch(
                f"""SELECT CASE WHEN state IN ('dead', 'paused') THEN state
                                WHEN kind <> 'singleton' AND state NOT IN ('done', 'resolved')
                                     AND run_at < now() - make_interval(secs => $1) THEN 'queued' ELSE state END,
                           count(*) FROM "{s}".be_work_item GROUP BY 1""", age)}
        return {
            "processes": judge_processes(await conn.fetch(PROCESSES, list(OPEN), age), await conn.fetch(CONFIRMING, age)),
            "orders": judge_orders(await conn.fetch(ORDER_BRANCHES, age), await conn.fetch(HOLDING_BRANCHES, list(HOLDING), age)),
            "stock": judge_sums("stock", await conn.fetch(STOCK, list(HOLDING))),
            "credit": judge_sums("credit limit", await conn.fetch(CREDIT, list(HOLDING))),
            "billing": judge_billing(await conn.fetch(BILLING, age), await conn.fetch(BILLED, age)),
            "runtime": judge_runtime(outbox, work),
        }
    finally:
        await conn.close()


async def check(directory: str | pathlib.Path, wait: float = 0, every: float = 10, age: float = 120) -> dict[str, list[str]]:
    """The cell's violations, waiting up to `wait` seconds for them to clear (the work behind a fault takes its
    time: a hold expires after five minutes, a work item backs off); each reading prints how many are left."""
    c, waited = Cell.load(directory), 0.0
    while True:
        v = await read(c, age)
        left = sum(len(x) for x in v.values())
        if not left or waited >= wait:
            return v
        print(f"{waited:.0f} s: {left} violations left ({', '.join(k for k, x in v.items() if x)})", flush=True)
        await asyncio.sleep(every)
        waited += every


def write(directory: str, out: str, wait: float) -> int:
    v = asyncio.run(check(directory, wait))
    pathlib.Path(out).write_text(json.dumps(v, indent=1) + "\n")
    bad = {k: len(x) for k, x in v.items() if x}
    print(f"invariants of {directory}: " + (f"VIOLATED {bad}" if bad else "all hold") + f" ({out})")
    for k, x in v.items():
        for line in x[:5]:
            print(f"  {k}: {line}")
    return 1 if bad else 0
