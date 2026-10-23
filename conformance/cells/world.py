"""The load driver's world for business chains (load -flow): from a running, seeded cell, the user surfaces and what an
order is made of in CN01 — customers without a credit limit (credit takes part but never refuses: the load measures
the chain, not the limits), goods not tracked by batch or serial, priced in CNY, in stock in Shanghai. Each good is
topped up to at least TOP_UP units first, so a run does not drain the seeded stock into refusals."""
from __future__ import annotations

import json
import pathlib

import httpx

from cells.spec import Cell

TOP_UP, CUSTOMERS, GOODS = 100_000, 16, 8


def world(directory: str | pathlib.Path, out: str | pathlib.Path) -> dict:
    c = Cell.load(directory)
    creds = json.loads((c.dir / ".secrets" / "load-user.json").read_text())
    with httpx.Client(timeout=30) as h:
        r = h.post(creds["token_url"], json={"username": creds["username"], "password": creds["password"]})
        r.raise_for_status()
        auth = {"Authorization": "Bearer " + r.json()["access_token"]}
        principal = r.json()["principal"]

        def get(component: str, path: str) -> dict:
            r = h.get(f"http://127.0.0.1:{c.host_port(component)}{path}", headers=auth)
            r.raise_for_status()
            return r.json()

        def pages(component: str, path: str, key: str) -> list[dict]:
            rows, cursor = [], ""
            while True:
                page = get(component, f"{path}{'&' if '?' in path else '?'}limit=200&cursor={cursor}")
                rows += page[key]
                if not (cursor := page.get("next_cursor") or ""):
                    return rows

        le = next(e["id"] for e in get("mdm/org", "/api/org/legal-entities?limit=200")["legal_entities"] if e["code"] == "CN01")
        shanghai = next(w["id"] for w in get("erp/inventory", f"/api/inventory/warehouses?limit=200&legal_entity_id={le}")["warehouses"]
                        if w["code"] == "SH-01")
        priced = {i["product_id"] for pl in get("erp/sales", f"/api/sales/price-lists?legal_entity_id={le}")["price_lists"]
                  if pl["currency"] == "CNY" for i in pages("erp/sales", f"/api/sales/price-lists/{pl['id']}/items?on=2026-10-01", "items")}
        stocked = {p["product_id"]: p for p in pages("erp/inventory", f"/api/inventory/positions?warehouse_id={shanghai}", "positions")}
        goods = [p for p in pages("mdm/product", "/api/product/products", "products")
                 if p["kind"] == "stock" and p["tracking"] == "none" and p["state"] == "active" and p["id"] in priced
                 and p["id"] in stocked][:GOODS]
        customers = [lim["partner_id"] for lim in pages("erp/credit", f"/api/credit/limits?legal_entity_id={le}&currency=CNY&state=active", "limits")
                     if lim.get("limit") is None][:CUSTOMERS]
        if not goods or not customers:
            raise RuntimeError(f"the cell has {len(goods)} goods and {len(customers)} customers for the load: seed it first")
        low = [p for p in goods if float(stocked[p["id"]]["available"]) < TOP_UP]
        if low:
            r = h.post(f"http://127.0.0.1:{c.host_port('erp/inventory')}/api/inventory/receipts", headers={
                **auth, "Idempotency-Key": "cells-world:" + ":".join(f"{p['code']}={stocked[p['id']]['on_hand']}" for p in low)},
                json={"warehouse_id": shanghai, "business_date": "2026-10-01", "note": "「本地测试」负载用的库存",
                      "lines": [{"product_id": p["id"], "quantity": str(TOP_UP)} for p in low]})
            if r.status_code != 200:
                raise RuntimeError(f"topping up the stock: {r.status_code} {r.text[:200]}")
    w = {"sales": f"http://127.0.0.1:{c.host_port('erp/sales')}", "billing": f"http://127.0.0.1:{c.host_port('erp/billing')}",
         "legal_entity_id": le, "currency": "CNY", "owner_id": principal, "customers": customers,
         "goods": [{"product_id": p["id"], "unit": p["base_unit"], "warehouse_id": shanghai} for p in goods]}
    pathlib.Path(out).write_text(json.dumps(w, indent=2) + "\n")
    return {"customers": len(customers), "goods": len(goods), "topped_up": len(low)}
