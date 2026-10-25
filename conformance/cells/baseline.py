"""Step 8 (T8.2): the experiment plan's baseline data in a running, seeded cell, through the components' user surfaces
— two legal entities (CN01, ES01), 500 customers, 2,000 products in stock, 20,000 historical orders spread over the
year and over every state (draft, cancelled, confirmed, delivered, billed) — and the mixed load's world file.

Every write has a fixed identity: products by code, the rest by idempotency key (baseline:...). A rerun finds what is
there and makes only what is missing, so two runs end with the same rows; an interrupted run is picked up by the next.
The order plan (order_plan) is pure and the same every time, so every cell gets the same input."""
from __future__ import annotations

import asyncio
import json
import os
import pathlib
import random
import time
from collections import Counter
from dataclasses import dataclass

import httpx

from cells.spec import Cell

ORDERS, GOODS, CUSTOMERS = 20_000, 2_000, {"CN01": 400, "ES01": 100}
SHARE = {"CN01": 0.8, "ES01": 0.2}
STATES = {"billed": 0.40, "confirmed": 0.25, "delivered": 0.10, "draft": 0.15, "cancelled": 0.10}
CURRENCY = {"CN01": "CNY", "ES01": "EUR"}
WAREHOUSE = {"CN01": "SH-01", "ES01": "MAD-01"}
# the sales seed's department and owner pairs (erp/sales cmd/seed): the sales accounts' scopes see their own share
CN_SLOTS = [("SALES-EAST", "east"), ("SALES-EAST-KA", "east"), ("SALES-EAST-SMB", "east"), ("SALES-SOUTH", "south"),
            ("SALES-SOUTH-KA", "south"), ("SALES-WEST", "seed"), ("SALES-NORTH", "seed"), ("OPS", "seed"), ("", "seed")]
OWNERS = {"east": "test.sales.cn", "south": "test.sales.multi", "es": "test.sales.es", "seed": "test.bob"}
STOCK = 1_000_000  # per product and warehouse: the year's orders and the load never run it down
PREFIX = "「本地测试」"
CONCURRENCY = 32


@dataclass(frozen=True)
class PlannedOrder:
    n: int
    legal_entity: str
    customer: int  # index into the legal entity's baseline customers
    goods: tuple[int, ...]  # indices into the baseline products, one line each
    quantity: int  # per line
    department: str  # department code; "" for none (the owner's)
    owner: str  # key of OWNERS
    state: str  # key of STATES: how far the order is taken
    business_date: str


def order_plan(customers: dict[str, int] = CUSTOMERS, goods: int = GOODS) -> list[PlannedOrder]:
    """The 20,000 orders, fixed: legal entity by share, state by share (exact counts, shuffled with a fixed seed),
    CN01 orders over the sales seed's department and owner pairs, 1-3 distinct goods, dates over 2026-01-01..10-24."""
    rng = random.Random(20261025)
    states = [s for s, share in STATES.items() for _ in range(round(ORDERS * share))]
    rng.shuffle(states)
    les = [le for le, share in SHARE.items() for _ in range(round(ORDERS * share))]
    rng.shuffle(les)
    days = 297  # 2026-01-01 .. 2026-10-24
    out = []
    for n in range(ORDERS):
        le = les[n]
        dept, owner = CN_SLOTS[n % len(CN_SLOTS)] if le == "CN01" else ("", "es")
        lines = tuple(rng.sample(range(goods), 1 + n % 3))
        day = rng.randrange(days)
        month_days = [31, 28, 31, 30, 31, 30, 31, 31, 30, 31]
        m = 0
        while day >= month_days[m]:
            day -= month_days[m]
            m += 1
        out.append(PlannedOrder(n, le, rng.randrange(customers[le]), lines, 1 + rng.randrange(5), dept, owner,
                                states[n], f"2026-{m + 1:02d}-{day + 1:02d}"))
    return out


class Session:
    """One account's token, renewed before the identity member's five minutes run out."""

    def __init__(self, h: httpx.AsyncClient, token_url: str, user: str, password: str):
        self.h, self.url, self.user, self.password = h, token_url, user, password
        self.token, self.principal, self.at = "", "", 0.0

    async def auth(self) -> dict:
        if time.monotonic() - self.at > 120:
            r = await self.h.post(self.url, json={"username": self.user, "password": self.password})
            r.raise_for_status()
            self.token, self.principal, self.at = r.json()["access_token"], r.json()["principal"], time.monotonic()
        return {"Authorization": "Bearer " + self.token}


class Baseline:
    def __init__(self, c: Cell, h: httpx.AsyncClient, bob: Session):
        self.c, self.h, self.bob = c, h, bob
        self.failures: Counter = Counter()
        self.made: Counter = Counter()  # writes answered: made now, or found by their key from an earlier run
        self.gate = asyncio.Semaphore(CONCURRENCY)

    def url(self, component: str, path: str) -> str:
        return f"http://127.0.0.1:{self.c.host_port(component)}{path}"

    async def call(self, method: str, component: str, path: str, body=None, key: str = "", what: str = "") -> dict | None:
        headers = await self.bob.auth()
        if key:
            headers["Idempotency-Key"] = key
        for attempt in range(5):
            try:
                r = await self.h.request(method, self.url(component, path), json=body, headers=headers)
            except httpx.TransportError as e:
                if attempt == 4:
                    self.failures[f"{what}: {type(e).__name__}"] += 1
                    return None
                await asyncio.sleep(0.5 * (attempt + 1))
                continue
            if r.status_code == 503 and attempt < 4:  # dependency unavailable for a moment: the same key again
                await asyncio.sleep(0.5 * (attempt + 1))
                continue
            if r.status_code >= 300:
                self.failures[f"{what}: {r.status_code} {r.json().get('reason', '') if r.content else ''}"] += 1
                return None
            return r.json() if r.content else {}
        return None

    async def get(self, component: str, path: str) -> dict:
        r = await self.h.get(self.url(component, path), headers=await self.bob.auth())
        r.raise_for_status()
        return r.json()

    async def pages(self, component: str, path: str, key: str) -> list[dict]:
        rows, cursor = [], ""
        while True:
            page = await self.get(component, f"{path}{'&' if '?' in path else '?'}limit=200&cursor={cursor}")
            rows += page[key]
            if not (cursor := page.get("next_cursor") or ""):
                return rows

    async def bounded(self, coro):
        async with self.gate:
            return await coro

    # --- master data, prices, stock -----------------------------------------------------------------------------

    async def products(self, les: dict[str, str]) -> list[dict]:
        cats = {x["code"]: x["id"] for x in await self.pages("mdm/product", "/api/product/categories", "categories")}
        if "BASELINE" not in cats:
            out = await self.call("POST", "mdm/product", "/api/product/categories",
                                  {"code": "BASELINE", "name": PREFIX + "基线货物"}, what="category")
            cats["BASELINE"] = out["id"]
        have = {p["code"]: p for p in await self.pages("mdm/product", "/api/product/products", "products")}

        async def make(i: int) -> None:
            code = f"BL-{i:04d}"
            if code in have:
                return
            out = await self.call("POST", "mdm/product", "/api/product/products", {
                "code": code, "kind": "stock", "name": f"{PREFIX}基线货物 {i:04d}", "base_unit": "C62",
                "category_id": cats["BASELINE"], "tracking": "none"}, what="product")
            if out:
                have[code] = out
                self.made["products"] += 1
        await asyncio.gather(*(self.bounded(make(i)) for i in range(GOODS)))
        mine = [have[f"BL-{i:04d}"] for i in range(GOODS) if f"BL-{i:04d}" in have]

        async def open_in(p: dict, le: str) -> None:
            if any(a["legal_entity_id"] == les[le] for a in p.get("availability") or []):
                return
            if await self.call("PUT", "mdm/product", f"/api/product/products/{p['id']}/legal-entities/{les[le]}",
                               {"sellable": True, "purchasable": True}, what="product availability") is not None:
                self.made["availability"] += 1
        await asyncio.gather(*(self.bounded(open_in(p, le)) for p in mine for le in les))
        return mine

    async def prices(self, les: dict[str, str], goods: list[dict]) -> None:
        for le, ccy in CURRENCY.items():
            lists = [pl for pl in await self.pages("erp/sales", f"/api/sales/price-lists?legal_entity_id={les[le]}", "price_lists")
                     if pl["currency"] == ccy]
            pl = lists[0]["id"]
            priced = {i["product_id"] for i in await self.pages("erp/sales", f"/api/sales/price-lists/{pl}/items?on=2026-01-01", "items")}

            async def price(j: int, p: dict) -> None:
                if p["id"] in priced:
                    return
                amount = f"{20 + (j % 30) * 3}.{(j * 7) % 100:02d}" if ccy == "CNY" else f"{3 + (j % 30) // 2}.{(j * 3) % 100:02d}"
                if await self.call("POST", "erp/sales", f"/api/sales/price-lists/{pl}/items", {
                        "product_id": p["id"], "unit": "C62", "price": amount, "valid_from": "2026-01-01"},
                        what="price") is not None:
                    self.made["prices"] += 1
            await asyncio.gather(*(self.bounded(price(j, p)) for j, p in enumerate(goods)))

    async def stock(self, les: dict[str, str], goods: list[dict]) -> dict[str, str]:
        whs = {}
        for le, code in WAREHOUSE.items():
            whs[le] = next(w["id"] for w in await self.pages("erp/inventory", f"/api/inventory/warehouses?legal_entity_id={les[le]}", "warehouses")
                           if w["code"] == code)
            for k in range(0, len(goods), 500):
                if await self.call("POST", "erp/inventory", "/api/inventory/receipts", {
                        "warehouse_id": whs[le], "business_date": "2026-01-01", "note": PREFIX + "基线库存",
                        "lines": [{"product_id": p["id"], "quantity": str(STOCK)} for p in goods[k:k + 500]]},
                        key=f"baseline:receipt:{code}:{k}", what="receipt") is not None:
                    self.made["receipts"] += 1
        return whs

    async def customers(self, les: dict[str, str]) -> dict[str, list[str]]:
        out: dict[str, list[str]] = {le: [None] * n for le, n in CUSTOMERS.items()}

        async def make(le: str, i: int) -> None:
            country = le[:2]
            p = await self.call("POST", "mdm/partner", "/api/partner/partners", {
                "kind": "organization", "name": f"{PREFIX}基线客户 {le} {i:03d}", "country": country,
                "tax_id": f"BL{le}{i:06d}"}, key=f"baseline:partner:{le}:{i}", what="partner")
            if not p:
                return
            pid = p["partner"]["id"]
            if await self.call("PUT", "mdm/partner", f"/api/partner/partners/{pid}/roles/customer/{les[le]}",
                               {"default_currency": CURRENCY[le], "payment_terms": "NET30"}, what="customer role") is None:
                return
            if await self.call("POST", "erp/credit", "/api/credit/limits", {
                    "partner_id": pid, "legal_entity_id": les[le], "currency": CURRENCY[le], "limit": None},
                    key=f"baseline:limit:{le}:{i}", what="credit limit") is None:
                return
            out[le][i] = pid
        await asyncio.gather(*(self.bounded(make(le, i)) for le, n in CUSTOMERS.items() for i in range(n)))
        return out

    # --- orders ---------------------------------------------------------------------------------------------------

    async def order(self, o: PlannedOrder, les, depts, owners, customers, goods, whs) -> None:
        k = f"baseline:order:{o.n}"
        body = {"legal_entity_id": les[o.legal_entity], "customer_id": customers[o.legal_entity][o.customer],
                "currency": CURRENCY[o.legal_entity], "owner_id": owners[o.owner], "business_date": o.business_date,
                "lines": [{"product_id": goods[g]["id"], "quantity": str(o.quantity), "unit": "C62",
                           "warehouse_id": whs[o.legal_entity], "tax_rate": "0.13" if o.legal_entity == "CN01" else "0.21"}
                          for g in o.goods]}
        if o.department:
            body["department_id"] = depts[o.legal_entity][o.department]
        order = await self.call("POST", "erp/sales", "/api/sales/orders", body, key=f"{k}:order", what="order")
        if not order:
            return
        oid = order["id"]
        if o.state == "draft":
            self.made["orders"] += 1
            return
        if o.state == "cancelled":
            if await self.call("POST", "erp/sales", f"/api/sales/orders/{oid}/cancel", key=f"{k}:cancel", what="cancel") is not None:
                self.made["orders"] += 1
            return
        conf = await self.call("POST", "erp/sales", f"/api/sales/orders/{oid}/confirm", key=f"{k}:confirm", what="confirm")
        if not conf:
            return
        if conf.get("outcome") != "confirmed":
            self.failures[f"confirm: {conf.get('outcome')} {conf.get('reason')}"] += 1
            return
        if o.state == "confirmed":
            self.made["orders"] += 1
            return
        if not await self.call("POST", "erp/sales", "/api/sales/deliveries", {
                "order_id": oid, "lines": [{"order_line_id": ln["id"], "quantity": str(o.quantity)} for ln in order["lines"]]},
                key=f"{k}:deliver", what="deliver"):
            return
        if o.state == "delivered":
            self.made["orders"] += 1
            return
        inv = await self.call("POST", "erp/billing", "/api/billing/invoices", {"order_id": oid}, key=f"{k}:draft", what="draft")
        if inv and await self.call("POST", "erp/billing", f"/api/billing/invoices/{inv['id']}/issue", key=f"{k}:issue",
                                   what="issue") is not None:
            self.made["orders"] += 1


async def build(directory: str | pathlib.Path, world_out: str | pathlib.Path) -> dict:
    c = Cell.load(directory)
    creds = json.loads((c.dir / ".secrets" / "load-user.json").read_text())
    password = os.environ.get("BE_ACC_IAM_TEST_PASSWORD") or creds["password"]
    started = time.monotonic()
    async with httpx.AsyncClient(timeout=60, limits=httpx.Limits(max_connections=CONCURRENCY * 2)) as h:
        bob = Session(h, creds["token_url"], creds["username"], creds["password"])
        b = Baseline(c, h, bob)
        sessions = {k: Session(h, creds["token_url"], acct, password) for k, acct in OWNERS.items() if acct != creds["username"]}
        sessions["seed"] = bob
        for s in sessions.values():
            await s.auth()
        owners = {k: s.principal for k, s in sessions.items()}
        les = {e["code"]: e["id"] for e in (await b.get("mdm/org", "/api/org/legal-entities?limit=200"))["legal_entities"]
               if e["code"] in CURRENCY}
        depts = {le: {u["code"]: u["id"] for u in await b.pages("mdm/org", f"/api/org/units?state=active&legal_entity_id={les[le]}", "units")}
                 for le in les}
        goods = await b.products(les)
        await b.prices(les, goods)
        whs = await b.stock(les, goods)
        customers = await b.customers(les)
        steps = {"master data": round(time.monotonic() - started)}
        if len(goods) < GOODS or any(None in v for v in customers.values()):
            raise RuntimeError(f"master data incomplete: {len(goods)} goods; failures {dict(b.failures)}")
        await asyncio.gather(*(b.bounded(b.order(o, les, depts, owners, customers, goods, whs)) for o in order_plan()))
        steps["orders"] = round(time.monotonic() - started) - steps["master data"]
    world = {
        "sales": b.url("erp/sales", ""), "billing": b.url("erp/billing", ""), "token_url": creds["token_url"],
        "users": [{"account": OWNERS[k], "legal_entity_id": les[le], "currency": CURRENCY[le], "owner_id": owners[k],
                   "department_id": depts[le].get(dept), "warehouse_id": whs[le], "customers": customers[le],
                   "goods": [g["id"] for g in goods], "unit": "C62", "tax_rate": "0.13" if le == "CN01" else "0.21"}
                  for k, le, dept in (("east", "CN01", "SALES-EAST"), ("south", "CN01", "SALES-SOUTH"), ("es", "ES01", ""),
                                      ("seed", "CN01", "SALES-WEST"))],
        "clerk": creds["username"]}
    pathlib.Path(world_out).write_text(json.dumps(world, indent=2) + "\n")
    return {"seconds": steps, "done": dict(b.made), "failures": dict(b.failures)}
