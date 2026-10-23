"""L3, the trading chain: erp/sales coordinates erp/inventory and erp/credit (protocol 04), owns the billable ledger
erp/billing claims from (protocol 05), and follows mdm/org's departments by events (protocol 02); infra/print renders
the invoice. Each test makes its own documents (fresh idempotency keys) on top of the seed data."""
from __future__ import annotations

import uuid
from decimal import Decimal

from conftest import World, eventually


def position(bob, world: World, warehouse: str, product: str) -> tuple[Decimal, Decimal]:
    rows = bob.get("erp/inventory", f"/api/inventory/positions?warehouse_id={warehouse}&product_id={product}")["positions"]
    return sum(Decimal(r["on_hand"]) for r in rows), sum(Decimal(r["reserved"]) for r in rows)


def exposure(bob, world: World, customer: str) -> Decimal:
    rows = bob.get("erp/credit", f"/api/credit/limits?partner_id={customer}&legal_entity_id={world.le}&currency=CNY")["limits"]
    assert len(rows) == 1, rows
    return Decimal(rows[0]["order_exposure"])


def order(bob, world: World, customer: str, lines: list[dict], department: str | None = None) -> dict:
    body = {"legal_entity_id": world.le, "customer_id": customer, "currency": "CNY", "owner_id": bob.principal,
            "lines": lines}
    if department:
        body["department_id"] = department
    return bob.post("erp/sales", "/api/sales/orders", body)


def goods(world: World, qty: int, warehouse: str | None = None, product: dict | None = None) -> dict:
    p = product or world.goods
    return {"product_id": p["id"], "quantity": str(qty), "unit": p["base_unit"], "warehouse_id": warehouse or world.shanghai,
            "tax_rate": "0.13"}


def service(world: World, qty: int) -> dict:
    return {"product_id": world.service["id"], "quantity": str(qty), "unit": world.service["base_unit"], "tax_rate": "0.06"}


def test_sales_inventory_credit_billing_print_order_to_invoice(bob, world):
    """An order goes from confirmation to a printed invoice: the holds are taken, the delivery issues the stock, the
    invoice claims the billable amount, and the order completes once billing formalizes."""
    on_hand, reserved = position(bob, world, world.shanghai, world.goods["id"])
    exposed = exposure(bob, world, world.large)

    o = order(bob, world, world.large, [goods(world, 3), service(world, 1)])
    ans = bob.post("erp/sales", f"/api/sales/orders/{o['id']}/confirm")
    assert ans["outcome"] == "confirmed", ans
    gross = Decimal(ans["order"]["totals"]["gross"])
    assert position(bob, world, world.shanghai, world.goods["id"]) == (on_hand, reserved + 3), "inventory holds the goods"
    assert exposure(bob, world, world.large) == exposed + gross, "credit holds the order's gross"

    line = next(x for x in ans["order"]["lines"] if x["kind"] == "goods")
    d = bob.post("erp/sales", "/api/sales/deliveries", {"order_id": o["id"], "lines": [{"order_line_id": line["id"], "quantity": "3"}]})
    assert d["state"] == "posted", d
    assert position(bob, world, world.shanghai, world.goods["id"]) == (on_hand - 3, reserved), "the delivery issued the hold"

    inv = bob.post("erp/billing", "/api/billing/invoices", {"order_id": o["id"]})
    assert inv["state"] == "draft" and Decimal(inv["totals"]["gross"]) == gross, inv
    issued = bob.post("erp/billing", f"/api/billing/invoices/{inv['id']}/issue")
    assert issued["state"] == "issued" and issued["number"].startswith("INV-CN01-"), issued

    pdf = bob.call("POST", "erp/billing", f"/api/billing/invoices/{inv['id']}/print", idempotent=False)
    assert pdf.status_code == 200 and pdf.headers["content-type"].startswith("application/pdf"), pdf.text[:200]
    assert pdf.content.startswith(b"%PDF"), "infra/print rendered a PDF"

    done = eventually(lambda: (o2 := bob.get("erp/sales", f"/api/sales/orders/{o['id']}"))["state"] == "completed" and o2)
    assert all(Decimal(x["delivered_quantity"]) == Decimal(x["quantity"]) for x in done["lines"] if x["kind"] == "goods")
    eventually(lambda: exposure(bob, world, world.large) == exposed)  # the completed order holds no credit


def test_sales_credit_refusal_releases_the_inventory_hold(bob, world):
    """Credit refuses an order over a 20,000 limit; the stock inventory held for it is released."""
    on_hand, reserved = position(bob, world, world.shanghai, world.goods["id"])
    o = order(bob, world, world.small, [goods(world, 2), service(world, 10000)])
    ans = bob.post("erp/sales", f"/api/sales/orders/{o['id']}/confirm")
    assert (ans["outcome"], ans["reason"], ans["order"]["state"]) == ("rejected", "over_limit", "draft"), ans
    eventually(lambda: position(bob, world, world.shanghai, world.goods["id"]) == (on_hand, reserved))


def test_sales_inventory_refusal_releases_the_credit_hold(bob, world):
    """Inventory refuses two tins from Beijing, which has one; the credit held for the order is released."""
    exposed = exposure(bob, world, world.large)
    o = order(bob, world, world.large, [goods(world, 2, world.beijing, world.tin)])
    ans = bob.post("erp/sales", f"/api/sales/orders/{o['id']}/confirm")
    assert (ans["outcome"], ans["reason"], ans["order"]["state"]) == ("rejected", "insufficient_stock", "draft"), ans
    eventually(lambda: exposure(bob, world, world.large) == exposed)


def test_sales_cancel_releases_inventory_and_credit(bob, world):
    """Cancelling a confirmed order releases both holds."""
    on_hand, reserved = position(bob, world, world.shanghai, world.goods["id"])
    exposed = exposure(bob, world, world.large)
    o = order(bob, world, world.large, [goods(world, 2)])
    assert bob.post("erp/sales", f"/api/sales/orders/{o['id']}/confirm")["outcome"] == "confirmed"
    assert bob.post("erp/sales", f"/api/sales/orders/{o['id']}/cancel")["state"] == "cancelled"
    eventually(lambda: position(bob, world, world.shanghai, world.goods["id"]) == (on_hand, reserved))
    eventually(lambda: exposure(bob, world, world.large) == exposed)


def test_billing_claim_and_void_against_sales_ledger(bob, world):
    """A draft invoice claims what is left to bill at sales, so a second draft finds nothing; voiding the first releases
    the claim and the order can be drafted again."""
    o = order(bob, world, world.large, [service(world, 2)])
    assert bob.post("erp/sales", f"/api/sales/orders/{o['id']}/confirm")["outcome"] == "confirmed"
    first = bob.post("erp/billing", "/api/billing/invoices", {"order_id": o["id"]})
    second = bob.call("POST", "erp/billing", "/api/billing/invoices", {"order_id": o["id"]})
    assert second.status_code in (409, 422), f"a second draft claims nothing: {second.status_code} {second.text[:200]}"
    assert bob.post("erp/billing", f"/api/billing/invoices/{first['id']}/void")["state"] == "void"
    again = bob.post("erp/billing", "/api/billing/invoices", {"order_id": o["id"]})
    assert again["totals"]["gross"] == first["totals"]["gross"]


def test_mdm_org_department_move_reaches_sales_scopes(bob, login, world):
    """A department moved from the east region to the south region in mdm/org: erp/sales rewrites its orders' paths
    from the event, so the east salesperson stops seeing the order and the one with both regions keeps it."""
    code = "L3-" + uuid.uuid4().hex[:8].upper()
    unit = bob.post("mdm/org", "/api/org/units", {"legal_entity_id": world.le, "parent_id": world.departments["SALES-EAST"],
                                                   "code": code, "name": "「本地测试」L3 " + code})
    east, both = login("test.sales.cn"), login("test.sales.multi")

    def sees(user, order_id) -> bool:
        r = user.call("GET", "erp/sales", f"/api/sales/orders/{order_id}")
        assert r.status_code in (200, 404), r.text[:200]
        return r.status_code == 200

    o = eventually(lambda: order(bob, world, world.large, [service(world, 1)], unit["id"]))  # the unit reaches sales first
    eventually(lambda: sees(east, o["id"]) and sees(both, o["id"]))
    bob.post("mdm/org", f"/api/org/units/{unit['id']}/move", {"parent_id": world.departments["SALES-SOUTH"],
                                                               "version": unit["version"]})
    eventually(lambda: not sees(east, o["id"]))
    assert sees(both, o["id"])

