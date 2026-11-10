"""L3, procure to stock: erp/purchase receives through the running erp/inventory (its system surface, protocol 05b):
the stock grows by the receipt, the movement carries the receipt line's reference, the order's received quantity is
the movement's; a return issues from that movement; over the legal entity's over-receipt percent nothing is taken in;
the approver is never the submitter, and the buyer sees its department's orders only (security 07). Each test makes
its own documents (fresh idempotency keys) on top of the seed data (CN01: approval required, 5% over-receipt)."""
from __future__ import annotations

from decimal import Decimal

import pytest

from conftest import Refused, World


@pytest.fixture(autouse=True)
def _needs_purchase(cell):
    if "erp/purchase" not in cell.placed:
        pytest.skip("the cell has no erp/purchase")


@pytest.fixture(scope="module")
def supplier(bob, world) -> str:
    """A CNY supplier of CN01, as the purchase seed's orders use them."""
    orders = bob.get("erp/purchase", f"/api/purchase/orders?legal_entity_id={world.le}&state=completed&limit=50")["orders"]
    return next(o["supplier_id"] for o in orders if o["currency"] == "CNY")


def on_hand(bob, world: World) -> Decimal:
    rows = bob.get("erp/inventory", f"/api/inventory/positions?warehouse_id={world.shanghai}&product_id={world.goods['id']}")["positions"]
    return sum(Decimal(r["on_hand"]) for r in rows)


def approved_order(bob, lead, world: World, supplier: str, qty: int, department: str | None = None) -> dict:
    body = {"legal_entity_id": world.le, "supplier_id": supplier, "currency": "CNY", "owner_id": bob.principal,
            "lines": [{"product_id": world.goods["id"], "quantity": str(qty), "unit": world.goods["base_unit"],
                       "unit_price": "10", "warehouse_id": world.shanghai}]}
    if department:
        body["department_id"] = department
    o = bob.post("erp/purchase", "/api/purchase/orders", body)
    assert bob.post("erp/purchase", f"/api/purchase/orders/{o['id']}/submit")["state"] == "submitted"
    o = lead.post("erp/purchase", f"/api/purchase/orders/{o['id']}/approve")
    assert o["state"] == "approved", o
    return o


def receive(user, order: dict, qty: str):
    line = order["lines"][0]["id"]
    return user.call("POST", "erp/purchase", f"/api/purchase/orders/{order['id']}/receipts",
                     {"lines": [{"order_line_id": line, "quantity": qty}]})


def test_purchase_inventory_order_to_stock_and_back(bob, login, world, supplier):
    """PO07, PO09, PO12: the receipt takes the goods into stock through erp/inventory; the order's received quantity
    is the movement's and the order completes; a return from that movement issues them again."""
    lead = login("test.buyer.lead")
    before = on_hand(bob, world)
    o = approved_order(bob, lead, world, supplier, 6)
    r = receive(bob, o, "6")
    assert r.status_code == 200, r.text
    receipt = r.json()
    assert receipt["state"] == "posted"
    movement_id = receipt["lines"][0]["movement_ids"][0]
    mv = bob.get("erp/inventory", f"/api/inventory/movements/{movement_id}")
    assert mv["kind"] == "receipt" and Decimal(mv["quantity"]) == 6, mv
    assert mv["reference"]["kind"] == "purchase_receipt" and mv["reference"]["line_id"] == receipt["lines"][0]["id"], mv
    assert on_hand(bob, world) == before + 6, "the receipt took the goods into stock"
    o = bob.get("erp/purchase", f"/api/purchase/orders/{o['id']}")
    assert o["lines"][0]["received_base_quantity"] == "6" and o["state"] == "completed", o

    ret = bob.post("erp/purchase", f"/api/purchase/orders/{o['id']}/returns",
                   {"reason": "L3", "lines": [{"origin_movement_id": movement_id, "base_quantity": "2"}]})
    assert ret["lines"][0]["state"] == "posted", ret
    back = bob.get("erp/inventory", f"/api/inventory/movements/{ret['lines'][0]['movement_id']}")
    assert back["kind"] == "purchase_return" and back["origin_movement_id"] == movement_id, back
    assert on_hand(bob, world) == before + 4, "the return issued two again"
    o = bob.get("erp/purchase", f"/api/purchase/orders/{o['id']}")
    assert o["lines"][0]["received_base_quantity"] == "4" and o["state"] == "completed", o


def test_purchase_inventory_over_receipt_is_refused_whole(bob, login, world, supplier):
    """PO07: CN01 allows 5% over the ordered quantity; more is refused before erp/inventory is asked."""
    o = approved_order(bob, login("test.buyer.lead"), world, supplier, 10)
    before = on_hand(bob, world)
    r = receive(bob, o, "11")
    assert r.status_code == 409 and r.json()["reason"] == "OVER_RECEIPT", r.text
    assert on_hand(bob, world) == before, "nothing was taken in"
    assert receive(bob, o, "10.5").status_code == 200, "within 5% it is taken in"


def test_purchase_authz_approver_is_not_the_submitter_and_the_buyer_sees_its_department(bob, login, world, supplier):
    """PO04: the submitter does not approve; security 07: test.buyer.cn keeps the purchasing department's orders
    (PUR and below) and does not see another department's."""
    body = {"legal_entity_id": world.le, "supplier_id": supplier, "currency": "CNY",
            "lines": [{"product_id": world.goods["id"], "quantity": "1", "unit": world.goods["base_unit"],
                       "unit_price": "10", "warehouse_id": world.shanghai}]}
    o = bob.post("erp/purchase", "/api/purchase/orders", body)
    bob.post("erp/purchase", f"/api/purchase/orders/{o['id']}/submit")
    r = bob.call("POST", "erp/purchase", f"/api/purchase/orders/{o['id']}/approve")
    assert r.status_code == 409 and r.json()["reason"] == "APPROVER_IS_SUBMITTER", r.text

    buyer = login("test.buyer.cn")
    mine = bob.post("erp/purchase", "/api/purchase/orders", {**body, "department_id": world.departments["PUR-RAW"]})
    theirs = bob.post("erp/purchase", "/api/purchase/orders", {**body, "department_id": world.departments["RD"]})
    assert buyer.get("erp/purchase", f"/api/purchase/orders/{mine['id']}")["id"] == mine["id"]
    with pytest.raises(Refused, match="404"):
        buyer.get("erp/purchase", f"/api/purchase/orders/{theirs['id']}")
