"""Step 8 (T8.4): a cell's result digest — what the business data adds up to once the work behind it has settled:
documents by state, quantities and amounts, stock by warehouse, credit branches, invoices, audit records. Two cells
that took the same input (the baseline, the scenarios) must have the same digest, item by item: the topology (alone,
in shells, mixed) changes how the components run, never what they decide."""
from __future__ import annotations

import asyncio
import json
import pathlib
from urllib.parse import urlsplit

import asyncpg

from cells.spec import Cell
from harness import need

# (item, SQL answering rows of (key, value)): counts by state, sums as text so decimals compare exactly
QUERIES = [
    ("erp_sales.quotation", "SELECT state, count(*)::text FROM erp_sales.quotation GROUP BY 1"),
    ("erp_sales.sales_order", "SELECT state, count(*)::text FROM erp_sales.sales_order GROUP BY 1"),
    ("erp_sales.order_line", "SELECT 'count', count(*)::text FROM erp_sales.order_line UNION ALL "
                             "SELECT 'quantity', coalesce(sum(quantity), 0)::text FROM erp_sales.order_line"),
    ("erp_sales.confirmation", "SELECT state, count(*)::text FROM erp_sales.confirmation GROUP BY 1"),
    ("erp_sales.confirmation_branch", "SELECT state, count(*)::text FROM erp_sales.confirmation_branch GROUP BY 1"),
    ("erp_sales.delivery", "SELECT state, count(*)::text FROM erp_sales.delivery GROUP BY 1"),
    ("erp_sales.billing_claim", "SELECT state, count(*)::text FROM erp_sales.billing_claim GROUP BY 1"),
    ("erp_sales.billing_claim_line", "SELECT 'amount', coalesce(sum(amount), 0)::text FROM erp_sales.billing_claim_line"),
    ("erp_inventory.stock_position", "SELECT w.code || '.' || k, v FROM erp_inventory.warehouse w JOIN LATERAL ("
                                     "SELECT 'on_hand' AS k, coalesce(sum(on_hand), 0)::text AS v FROM erp_inventory.stock_position p WHERE p.warehouse_id = w.id "
                                     "UNION ALL SELECT 'reserved', coalesce(sum(reserved), 0)::text FROM erp_inventory.stock_position p WHERE p.warehouse_id = w.id) x ON true"),
    ("erp_inventory.movement", "SELECT kind, count(*)::text || ' / ' || sum(quantity)::text FROM erp_inventory.movement GROUP BY 1"),
    ("erp_inventory.branch", "SELECT state, count(*)::text FROM erp_inventory.branch GROUP BY 1"),
    ("erp_credit.branch", "SELECT state, count(*)::text || ' / ' || coalesce(sum(amount), 0)::text FROM erp_credit.branch GROUP BY 1"),
    ("erp_credit.consumption", "SELECT 'amount', coalesce(sum(amount), 0)::text FROM erp_credit.consumption"),
    ("erp_billing.invoice", "SELECT state, count(*)::text FROM erp_billing.invoice GROUP BY 1"),
    ("erp_billing.invoice_line", "SELECT 'quantity', coalesce(sum(quantity), 0)::text FROM erp_billing.invoice_line"),
    ("infra_audit.audit_record", "SELECT 'count', count(*)::text FROM infra_audit.audit_record"),
]


async def read(c: Cell) -> dict:
    conn = await asyncpg.connect(urlsplit(need("BE_ACC_PG_ADMIN"))._replace(path="/" + c.database).geturl())
    try:
        return {item: {str(k): v for k, v in await conn.fetch(sql)} for item, sql in QUERIES}
    finally:
        await conn.close()


async def settled(directory: str | pathlib.Path, every: float = 10, limit: float = 600) -> dict:
    """The digest once two readings `every` seconds apart are the same (the work behind the last writes has run)."""
    c = Cell.load(directory)
    last, waited = await read(c), 0.0
    while waited < limit:
        await asyncio.sleep(every)
        waited += every
        now = await read(c)
        if now == last:
            return now
        last = now
    raise RuntimeError(f"{c.name}: the digest kept changing for {limit:.0f} s")


def compare(a: dict, b: dict) -> list[str]:
    """Every item that differs between two digests, sorted; empty when they are the same."""
    out = []
    for item in sorted(set(a) | set(b)):
        if item not in b:
            out.append(f"{item}: only in the first")
        elif item not in a:
            out.append(f"{item}: only in the second")
        else:
            for k in sorted(set(a[item]) | set(b[item])):
                if a[item].get(k) != b[item].get(k):
                    out.append(f"{item}.{k}: {a[item].get(k)} != {b[item].get(k)}")
    return out


def write(directory: str | pathlib.Path, out: str | pathlib.Path) -> dict:
    d = asyncio.run(settled(directory))
    pathlib.Path(out).write_text(json.dumps(d, indent=2, sort_keys=True, ensure_ascii=False) + "\n")
    return d
