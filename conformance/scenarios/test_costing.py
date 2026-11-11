"""L3, the value of stock: erp/costing values erp/inventory's movements by their valuation sequence (protocol 05b):
a purchase receipt's layer takes erp/purchase's valuation basis, erp/finance's posted supplier invoice replaces it
(an adjustment), and each cost fact posts in erp/finance (CN01 values by moving average); the valuation follows
the stock; with FIFO (ES01, the seed's policy) a purchase return asks how much of the receipt's layer is still in
stock and more is refused (RT01). Each test makes its own documents (fresh idempotency keys) on top of the seed."""
from __future__ import annotations

from decimal import Decimal

import pytest

from conftest import World, eventually
from test_purchase import approved_order, receive


@pytest.fixture(autouse=True)
def _needs_costing(cell):
    if "erp/costing" not in cell.placed:
        pytest.skip("the cell has no erp/costing")


def layer(bob, movement: str, **want) -> dict:
    """The cost layer of a receipt movement once it has the wanted fields (costing values asynchronously)."""
    def found():
        r = bob.call("GET", "erp/costing", f"/api/costing/layers/{movement}")
        if r.status_code != 200:
            return None
        lay = r.json()
        return lay if all(lay[k] == v for k, v in want.items()) else None
    return eventually(found)


def facts(bob, movement: str) -> dict[str, dict]:
    return {f["rule_key"]: f for f in bob.get("erp/costing", f"/api/costing/facts?movement_id={movement}")["facts"]}


def scope(bob, le: str, product: str) -> dict | None:
    r = bob.call("GET", "erp/costing", f"/api/costing/scopes/{le}/{product}")
    return r.json() if r.status_code == 200 else None


def on_hand(bob, le: str, product: str) -> Decimal:
    """The legal entity's stock of a product over all its warehouses (a valuation scope is the legal entity's)."""
    whs = bob.get("erp/inventory", f"/api/inventory/warehouses?limit=200&legal_entity_id={le}")["warehouses"]
    return sum((Decimal(r["on_hand"]) for w in whs for r in bob.get(
        "erp/inventory", f"/api/inventory/positions?limit=200&warehouse_id={w['id']}&product_id={product}")["positions"]),
        Decimal(0))


def test_purchase_costing_finance_basis_then_invoice_replaces_it(bob, login, world):
    """LY01, LY02, AD01, CF01: the receipt's layer takes the order's price (the valuation basis) and posts inventory
    against goods received not invoiced; the posted supplier invoice at a higher price replaces it, the difference of
    the units in stock is an adjustment up; erp/finance posts both cost facts."""
    lead, finance_lead = login("test.buyer.lead"), login("test.finance.lead")
    orders = bob.get("erp/purchase", f"/api/purchase/orders?legal_entity_id={world.le}&state=completed&limit=50")["orders"]
    supplier = next(o["supplier_id"] for o in orders if o["currency"] == "CNY")
    o = approved_order(bob, lead, world, supplier, 5)
    r = receive(bob, o, "5")
    assert r.status_code == 200, r.text
    movement = r.json()["lines"][0]["movement_ids"][0]

    lay = layer(bob, movement, source="basis")
    assert Decimal(lay["unit_cost"]) == 10 and lay["quantity"] == "5", lay
    receipt = eventually(lambda: facts(bob, movement).get("inventory_receipt"))
    assert receipt["amounts"] == {"inventory": "50.00", "grni": "50.00"}, receipt

    line = o["lines"][0]["id"]
    si = bob.post("erp/finance", "/api/finance/supplier-invoices", {
        "legal_entity_id": world.le, "supplier_id": supplier, "supplier_number": f"L3-COST-{o['number']}",
        "invoice_date": o["business_date"], "due_date": o["business_date"], "currency": "CNY",
        "lines": [{"order_line_id": line, "quantity": "5", "unit_price": "12", "tax": "7.80"}]})
    path = f"/api/finance/supplier-invoices/{si['id']}"
    assert bob.post("erp/finance", f"{path}/match")["state"] == "mismatch", "the price is above the order's"
    finance_lead.post("erp/finance", f"{path}/approve-difference", {"reason": "L3 价格上调"})
    assert bob.post("erp/finance", f"{path}/post")["state"] == "posted"

    lay = layer(bob, movement, source="invoice")
    assert Decimal(lay["unit_cost"]) == 12, lay
    up = eventually(lambda: facts(bob, movement).get("cost_adjustment_up"))
    assert up["trigger"]["kind"] == "invoice", up
    assert Decimal(up["amounts"]["grni"]) == Decimal("10.00"), up

    def vouchers():  # erp/finance's voucher of an accounting fact has the fact as its source
        page = bob.get("erp/finance", f"/api/finance/vouchers?legal_entity_id={world.le}&limit=200")["vouchers"]
        mine = [v for v in page if v["source"]["id"] in (receipt["fact_id"], up["fact_id"]) and v["state"] == "posted"]
        return mine if len(mine) == 2 else None
    posted = eventually(vouchers)
    debit = sum(Decimal(x["amount"]) for v in posted for x in v["lines"] if x["account_code"] == "1405" and x["side"] == "debit")
    assert debit == Decimal("50") + Decimal(up["amounts"].get("inventory", "0")), posted


def test_inventory_costing_valuation_follows_the_stock(bob, world):
    """VS01, VS02, IS01: a delivery's issue is valued in order; the valuation scope's quantity is the legal entity's
    stock of the product once the sequence has caught up."""
    from test_trading_chain import goods, order
    o = order(bob, world, world.large, [goods(world, 2)])
    ans = bob.post("erp/sales", f"/api/sales/orders/{o['id']}/confirm")
    assert ans["outcome"] == "confirmed", ans
    line = ans["order"]["lines"][0]
    bob.post("erp/sales", "/api/sales/deliveries", {"order_id": o["id"], "lines": [{"order_line_id": line["id"], "quantity": "2"}]})
    stock = on_hand(bob, world.le, world.goods["id"])
    s = eventually(lambda: (x := scope(bob, world.le, world.goods["id"])) and x["state"] == "running"
                   and Decimal(x["quantity"]) == stock and x)
    assert s["method"] == "moving_average", s


@pytest.fixture(scope="module")
def spain(bob) -> dict:
    """ES01 (FIFO in the seed): its warehouse, a EUR supplier, a customer under the 1,000,000 limit, and an untracked
    product in stock that is priced and purchasable there."""
    le = next(e["id"] for e in bob.get("mdm/org", "/api/org/legal-entities?limit=200")["legal_entities"] if e["code"] == "ES01")
    wh = next(w["id"] for w in bob.get("erp/inventory", f"/api/inventory/warehouses?limit=200&legal_entity_id={le}")["warehouses"]
              if w["code"] == "MAD-01")
    orders = bob.get("erp/purchase", f"/api/purchase/orders?legal_entity_id={le}&state=completed&limit=50")["orders"]
    supplier = next(o["supplier_id"] for o in orders if o["currency"] == "EUR")
    customer = next(lim["partner_id"] for lim in bob.get(
        "erp/credit", f"/api/credit/limits?limit=200&legal_entity_id={le}&currency=EUR&state=active")["limits"]
        if lim["limit"] == "1000000")
    priced = {i["product_id"] for pl in bob.get("erp/sales", f"/api/sales/price-lists?legal_entity_id={le}")["price_lists"]
              if pl["currency"] == "EUR"
              for i in bob.get("erp/sales", f"/api/sales/price-lists/{pl['id']}/items?on=2026-10-01&limit=200")["items"]}
    for p in bob.get("erp/inventory", f"/api/inventory/positions?limit=200&available_only=true&warehouse_id={wh}")["positions"]:
        product = bob.get("mdm/product", f"/api/product/products/{p['product_id']}")
        if p["product_id"] in priced and product["tracking"] == "none" and Decimal(p["available"]) == Decimal(p["on_hand"]) > 0:
            return {"le": le, "wh": wh, "supplier": supplier, "customer": customer, "product": product}
    raise AssertionError("no untracked, priced product in stock in MAD-01: seed the cell first")


def test_purchase_costing_fifo_return_limited_to_the_layer_in_stock(bob, spain):
    """IS01, RT01: by FIFO a delivery of all the stock and one more unit takes the oldest layers and one unit of the
    new receipt's; erp/purchase asks erp/costing before a return, so two of that receipt are refused
    (RETURN_EXCEEDS_COST_LAYER) and the one in stock goes back."""
    le, product = spain["le"], spain["product"]
    before = on_hand(bob, le, product["id"])
    o = bob.post("erp/purchase", "/api/purchase/orders", {
        "legal_entity_id": le, "supplier_id": spain["supplier"], "currency": "EUR", "owner_id": bob.principal,
        "lines": [{"product_id": product["id"], "quantity": "2", "unit": product["base_unit"], "unit_price": "10",
                   "warehouse_id": spain["wh"]}]})
    o = bob.post("erp/purchase", f"/api/purchase/orders/{o['id']}/submit")
    assert o["state"] == "approved", "ES01 asks no approval"
    r = receive(bob, o, "2")
    assert r.status_code == 200, r.text
    movement = r.json()["lines"][0]["movement_ids"][0]
    layer(bob, movement, source="basis")

    sale = bob.post("erp/sales", "/api/sales/orders", {
        "legal_entity_id": le, "customer_id": spain["customer"], "currency": "EUR", "owner_id": bob.principal,
        "lines": [{"product_id": product["id"], "quantity": str(before + 1), "unit": product["base_unit"],
                   "warehouse_id": spain["wh"], "tax_rate": "0.21"}]})
    ans = bob.post("erp/sales", f"/api/sales/orders/{sale['id']}/confirm")
    assert ans["outcome"] == "confirmed", ans
    line = ans["order"]["lines"][0]
    bob.post("erp/sales", "/api/sales/deliveries",
             {"order_id": sale["id"], "lines": [{"order_line_id": line["id"], "quantity": str(before + 1)}]})
    lay = layer(bob, movement, issued_quantity="1")
    assert lay["value_in_stock"] == "10", lay
    eventually(lambda: (x := scope(bob, le, product["id"])) and x["quantity"] == "1" and x["method"] == "fifo")

    r = bob.call("POST", "erp/purchase", f"/api/purchase/orders/{o['id']}/returns",
                 {"reason": "L3", "lines": [{"origin_movement_id": movement, "base_quantity": "2"}]})
    assert r.status_code == 409 and r.json()["reason"] == "RETURN_EXCEEDS_COST_LAYER", r.text
    ret = bob.post("erp/purchase", f"/api/purchase/orders/{o['id']}/returns",
                   {"reason": "L3", "lines": [{"origin_movement_id": movement, "base_quantity": "1"}]})
    assert ret["lines"][0]["state"] == "posted", ret
    back = eventually(lambda: facts(bob, ret["lines"][0]["movement_id"]).get("inventory_return"))
    assert back["amounts"] == {"grni": "10.00", "inventory": "10.00"}, back
    eventually(lambda: (x := scope(bob, le, product["id"])) and x["quantity"] == "0")
