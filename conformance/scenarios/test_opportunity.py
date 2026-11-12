"""L3, from an opportunity to the order (A71): crm/opportunity asks for a quotation by an event, erp/sales makes a draft
that carries the opportunity, the quotation is completed, sent, accepted and its order confirmed; with the cell's
setting OPPORTUNITY_AUTO_WIN=order_confirmed the confirmed order wins the opportunity by its net total. CRM and ERP
never call each other. A salesperson sees and counts only his own opportunities. Each test makes its own documents on
top of the seed."""
from __future__ import annotations

from decimal import Decimal

import pytest

from conftest import World, eventually
from test_trading_chain import goods

OPP = "/api/opportunity/opportunities"


@pytest.fixture(autouse=True)
def _needs_opportunity(cell):
    if "crm/opportunity" not in cell.placed:
        pytest.skip("the cell has no crm/opportunity")


def test_opportunity_sales_quote_request_to_won_order(bob, world: World):
    """OP05, OP08, QR01-QR05: the request becomes one quotation draft in erp/sales (no lines, the requested products,
    the opportunity and the request on it); completed, sent and accepted it is an order carrying the opportunity; the
    order confirmed, the opportunity is won by the order's net total, by no user."""
    o = bob.post("crm/opportunity", OPP, {
        "legal_entity_id": world.le, "customer_id": world.large, "name": "L3 商机到订单", "amount": "5000",
        "lines": [{"product_id": world.goods["id"], "quantity": "2", "unit": world.goods["base_unit"]}]})
    assert o["stage"] == "qualification" and o["currency"] == "CNY", o
    req = bob.post("crm/opportunity", f"{OPP}/{o['id']}/quote-requests")
    assert req["state"] == "requested", req

    def draft():
        page = bob.get("erp/sales", f"/api/sales/quotations?legal_entity_id={world.le}&limit=200")["quotations"]
        return next((q for q in page if q["quote_request_id"] == req["id"]), None)
    q = eventually(draft)
    assert q["state"] == "draft" and q["opportunity_ref"] == o["id"] and q["lines"] == [], q
    assert q["requested_products"][0]["product_id"] == world.goods["id"], q
    assert q["owner_id"] == bob.principal, "the opportunity's owner"
    drafted = eventually(lambda: (r := bob.get("crm/opportunity", f"{OPP}/{o['id']}"))["quote_requests"][0]["state"]
                         == "drafted" and r)
    assert drafted["quote_requests"][0]["quotation_id"] == q["id"], drafted

    r = bob.call("POST", "erp/sales", f"/api/sales/quotations/{q['id']}/send")
    assert r.status_code == 400 and r.json()["reason"] == "REQUEST_INVALID", "a draft without lines is not sent"
    q = bob.ok(bob.call("PATCH", "erp/sales", f"/api/sales/quotations/{q['id']}", {"lines": [goods(world, 2)]}))
    bob.post("erp/sales", f"/api/sales/quotations/{q['id']}/send")
    q = bob.post("erp/sales", f"/api/sales/quotations/{q['id']}/accept")
    order = bob.get("erp/sales", f"/api/sales/orders/{q['order_id']}")
    assert order["opportunity_ref"] == o["id"], order
    ans = bob.post("erp/sales", f"/api/sales/orders/{order['id']}/confirm")
    assert ans["outcome"] == "confirmed", ans

    won = eventually(lambda: (r := bob.get("crm/opportunity", f"{OPP}/{o['id']}"))["stage"] == "won" and r)
    assert Decimal(won["won_amount"]) == Decimal(ans["order"]["totals"]["net"]), won
    last = won["stage_history"][-1]
    assert last["actor"] == "system" and last["trigger"] == {"kind": "sales_order", "document_id": order["id"]}, last
    kinds = {d["kind"]: d for d in won["sales_documents"]}
    assert kinds["quotation"]["state"] == "accepted" and kinds["sales_order"]["state"] == "confirmed", kinds


def test_opportunity_salesperson_sees_and_counts_his_own(login):
    """The owner dimension (security 07): test.sales.cn lists only the opportunities he owns, may not read another's
    (404), and his funnel counts only his."""
    sales = login("test.sales.cn")
    mine = sales.get("crm/opportunity", f"{OPP}?limit=200")["opportunities"]
    assert mine and all(x["owner_id"] == sales.principal for x in mine), "the seed gives him opportunities"
    funnel = sales.get("crm/opportunity", "/api/opportunity/funnel")["rows"]
    assert sum(r["count"] for r in funnel) == len(mine), funnel
    manager = login("test.unit.admin.cn")
    other = next(x for x in manager.get("crm/opportunity", f"{OPP}?limit=200")["opportunities"]
                 if x["owner_id"] != sales.principal)
    assert sales.call("GET", "crm/opportunity", f"{OPP}/{other['id']}").status_code == 404
