"""L3, order to voucher: erp/finance takes erp/billing's issued invoice (its event) into a receivable and a posted
voucher; a supplier invoice is matched against erp/purchase's received order (its system surface, D50) and posted
into a payable and a voucher; the approver is never the maker, and the accountant keeps CN01 (security 07). Each test
makes its own documents (fresh idempotency keys) on top of the seed data (the demo chart and the mappings)."""
from __future__ import annotations

from datetime import date
from decimal import Decimal

import pytest

from conftest import Refused, World, eventually
from test_purchase import approved_order, receive
from test_trading_chain import exposure, goods, order


@pytest.fixture(autouse=True)
def _needs_finance(cell):
    if "erp/finance" not in cell.placed:
        pytest.skip("the cell has no erp/finance")


@pytest.fixture(scope="module")
def supplier(bob, world) -> str:
    """A CNY supplier of CN01, as the purchase seed's orders use them."""
    orders = bob.get("erp/purchase", f"/api/purchase/orders?legal_entity_id={world.le}&state=completed&limit=50")["orders"]
    return next(o["supplier_id"] for o in orders if o["currency"] == "CNY")


def voucher_of(bob, world: World, source: str) -> dict:
    """The posted voucher of a source document (the newest page of CN01's vouchers)."""
    def found():
        page = bob.get("erp/finance", f"/api/finance/vouchers?legal_entity_id={world.le}&limit=200")["vouchers"]
        return next((v for v in page if v["source"]["id"] == source), None)
    return eventually(found)


def side(voucher: dict, account: str, which: str) -> Decimal:
    return sum((Decimal(x["amount"]) for x in voucher["lines"] if x["account_code"] == account and x["side"] == which),
               Decimal(0))


def test_billing_finance_issued_invoice_forms_the_receivable_and_its_voucher(bob, world):
    """AR01–AR03: the issued invoice's event forms a receivable of its gross for the customer and a posted voucher
    (receivable debit, revenue and output tax credit) with a continuous number of CN01's month."""
    exposed = exposure(bob, world, world.large)
    o = order(bob, world, world.large, [goods(world, 2)])
    ans = bob.post("erp/sales", f"/api/sales/orders/{o['id']}/confirm")
    assert ans["outcome"] == "confirmed", ans
    line = ans["order"]["lines"][0]
    bob.post("erp/sales", "/api/sales/deliveries", {"order_id": o["id"], "lines": [{"order_line_id": line["id"], "quantity": "2"}]})
    inv = bob.post("erp/billing", "/api/billing/invoices", {"order_id": o["id"]})
    issued = bob.post("erp/billing", f"/api/billing/invoices/{inv['id']}/issue")
    gross = Decimal(issued["totals"]["gross"])

    def receivable():
        items = bob.get("erp/finance", f"/api/finance/open-items?legal_entity_id={world.le}&kind=receivable"
                                       f"&partner_id={world.large}&limit=200")["open_items"]
        return next((x for x in items if x["source"]["id"] == inv["id"]), None)
    item = eventually(receivable)
    assert item["origin"] == "invoice" and Decimal(item["amount"]) == gross and item["state"] == "open", item
    v = voucher_of(bob, world, inv["id"])
    assert v["state"] == "posted" and v["number"].startswith("JV-CN01-"), v
    assert side(v, "1122", "debit") == gross, v
    assert side(v, "6001", "credit") + side(v, "2221.02", "credit") == gross, v
    # the order completes and gives its credit back on its own time: wait for it, so the next test of this customer
    # starts from a settled exposure
    eventually(lambda: bob.get("erp/sales", f"/api/sales/orders/{o['id']}")["state"] == "completed")
    eventually(lambda: exposure(bob, world, world.large) == exposed)


def test_purchase_finance_supplier_invoice_matched_against_the_receipt_and_posted(bob, login, world, supplier):
    """SI02–SI04: more than received is a mismatch its maker cannot approve; the received quantity matches and posts
    into a payable of the gross and a voucher (purchase and input tax debit, payable credit)."""
    o = approved_order(bob, login("test.buyer.lead"), world, supplier, 6)
    assert receive(bob, o, "6").status_code == 200
    line = o["lines"][0]["id"]
    body = {"legal_entity_id": world.le, "supplier_id": supplier, "supplier_number": f"L3-{o['number']}",
            "invoice_date": o["business_date"], "due_date": o["business_date"], "currency": "CNY",
            "lines": [{"order_line_id": line, "quantity": "7", "unit_price": "10", "tax": "9.10"}]}
    si = bob.post("erp/finance", "/api/finance/supplier-invoices", body)
    path = f"/api/finance/supplier-invoices/{si['id']}"
    si = bob.post("erp/finance", f"{path}/match")
    assert si["state"] == "mismatch", si
    r = bob.call("POST", "erp/finance", f"{path}/approve-difference", {"reason": "L3"})
    assert r.status_code == 409 and r.json()["reason"] == "APPROVER_IS_SUBMITTER", r.text

    fixed = {**body, "version": si["version"], "lines": [{"order_line_id": line, "quantity": "6", "unit_price": "10", "tax": "7.80"}]}
    r = bob.call("PATCH", "erp/finance", path, fixed)
    assert r.status_code == 200, r.text
    assert bob.post("erp/finance", f"{path}/match")["state"] == "matched"
    si = bob.post("erp/finance", f"{path}/post")
    assert si["state"] == "posted" and Decimal(si["gross"]) == Decimal("67.80"), si
    item = bob.get("erp/finance", f"/api/finance/open-items/{si['open_item_id']}")
    assert item["kind"] == "payable" and item["partner_id"] == supplier and Decimal(item["amount"]) == Decimal("67.80"), item
    v = voucher_of(bob, world, si["id"])
    assert side(v, "1401", "debit") == Decimal("60") and side(v, "2221.01", "debit") == Decimal("7.8"), v
    assert side(v, "2202", "credit") == Decimal("67.8"), v


def test_finance_authz_maker_is_not_the_approver_and_the_accountant_keeps_cn01(bob, login, world):
    """VC02, VC03: the accountant makes and submits, cannot approve (no such operation), its lead approves and the
    voucher takes a number; security 07: another legal entity's voucher is not visible to the accountant (404)."""
    accountant, lead = login("test.accountant.cn"), login("test.finance.lead")
    v = accountant.post("erp/finance", "/api/finance/vouchers", {
        "legal_entity_id": world.le, "business_date": date.today().isoformat(), "description": "L3 办公用品",
        "lines": [{"account_code": "6602", "side": "debit", "amount": "12.50"},
                  {"account_code": "1002", "side": "credit", "amount": "12.50"}]})
    path = f"/api/finance/vouchers/{v['id']}"
    assert accountant.post("erp/finance", f"{path}/submit")["state"] == "submitted"
    assert accountant.call("POST", "erp/finance", f"{path}/approve", {}).status_code == 403
    r = bob.call("POST", "erp/finance", f"{path}/approve", {})
    assert r.status_code == 200, "the seed user did not make it"
    posted = r.json()
    assert posted["state"] == "posted" and posted["number"].startswith("JV-CN01-"), posted

    mine = accountant.post("erp/finance", "/api/finance/vouchers", {
        "legal_entity_id": world.le, "business_date": date.today().isoformat(), "description": "L3 自己批",
        "lines": [{"account_code": "6602", "side": "debit", "amount": "1"}, {"account_code": "1002", "side": "credit", "amount": "1"}]})
    accountant.post("erp/finance", f"/api/finance/vouchers/{mine['id']}/submit")
    assert lead.post("erp/finance", f"/api/finance/vouchers/{mine['id']}/approve")["state"] == "posted"

    es = next(e["id"] for e in bob.get("mdm/org", "/api/org/legal-entities?limit=200")["legal_entities"] if e["code"] == "ES01")
    theirs = bob.get("erp/finance", f"/api/finance/vouchers?legal_entity_id={es}&limit=1")["vouchers"][0]
    with pytest.raises(Refused, match="404"):
        accountant.get("erp/finance", f"/api/finance/vouchers/{theirs['id']}")

