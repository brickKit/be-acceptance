"""L3, cash: erp/payment registers money received and paid, and erp/finance settles its open items from the fund facts
(protocol 05): a receipt clears the issued invoice's receivable; a payment request claims the posted supplier
invoice's payable when submitted, is approved and released by others than its applicant (the claim confirmed), and
the payment executed settles it. Each test makes its own documents (fresh idempotency keys) on top of the seed data
(the CN01 basic account, the cashier, the finance lead)."""
from __future__ import annotations

import uuid
from datetime import date
from decimal import Decimal

import pytest

from conftest import World, eventually
from test_purchase import approved_order, receive
from test_trading_chain import exposure, goods, order


@pytest.fixture(autouse=True)
def _needs_payment(cell):
    if "erp/payment" not in cell.placed or "erp/finance" not in cell.placed:
        pytest.skip("the cell has no erp/payment and erp/finance")


@pytest.fixture(scope="module")
def basic(bob, world) -> str:
    """CN01's basic CNY account, as the payment seed made it."""
    accounts = bob.get("erp/payment", f"/api/payment/bank-accounts?legal_entity_id={world.le}&currency=CNY")["bank_accounts"]
    return next(a["id"] for a in accounts if a["name"] == "基本户")


def open_item(bob, world: World, partner: str, source: str) -> dict:
    def found():
        items = bob.get("erp/finance", f"/api/finance/open-items?legal_entity_id={world.le}&partner_id={partner}&limit=200")["open_items"]
        return next((x for x in items if x["source"]["id"] == source), None)
    return eventually(found)


def settled_total(bob, world: World, partner: str) -> Decimal:
    items = bob.get("erp/finance", f"/api/finance/open-items?legal_entity_id={world.le}&partner_id={partner}"
                                   f"&kind=receivable&limit=200")["open_items"]
    return sum((Decimal(x["settled"]) for x in items if x["currency"] == "CNY"), Decimal(0))


def test_payment_finance_receipt_settles_the_customers_receivables(bob, world, basic):
    """RC01, ST03: the money received from a customer settles its receivables, the earliest due first (the seed's
    older ones of this customer may take it before the invoice issued here): their settled total grows by it."""
    exposed = exposure(bob, world, world.large)
    o = order(bob, world, world.large, [goods(world, 1)])
    ans = bob.post("erp/sales", f"/api/sales/orders/{o['id']}/confirm")
    line = ans["order"]["lines"][0]
    bob.post("erp/sales", "/api/sales/deliveries", {"order_id": o["id"], "lines": [{"order_line_id": line["id"], "quantity": "1"}]})
    inv = bob.post("erp/billing", "/api/billing/invoices", {"order_id": o["id"]})
    issued = bob.post("erp/billing", f"/api/billing/invoices/{inv['id']}/issue")
    gross = Decimal(issued["totals"]["gross"])
    open_item(bob, world, world.large, inv["id"])
    before = settled_total(bob, world, world.large)
    r = bob.post("erp/payment", "/api/payment/receipts", {"legal_entity_id": world.le, "bank_account_id": basic,
                 "amount": str(gross), "received_on": date.today().isoformat(), "bank_reference": f"L3-{uuid.uuid4().hex[:10]}",
                 "payer_id": world.large})
    assert r["state"] == "received" and r["fact_id"], r
    eventually(lambda: settled_total(bob, world, world.large) == before + gross)
    eventually(lambda: bob.get("erp/sales", f"/api/sales/orders/{o['id']}")["state"] == "completed")
    eventually(lambda: exposure(bob, world, world.large) == exposed)


def test_payment_finance_request_claims_and_settles_the_payable(bob, login, world, basic):
    """PR02-PR05, CL01-CL07: submitted, the request claims the payable; the applicant does not approve; approved by the
    lead, released by the cashier (the claim confirmed), executed: finance settles the payable from the fund fact."""
    lead, cashier = login("test.finance.lead"), login("test.cashier.cn")
    orders = bob.get("erp/purchase", f"/api/purchase/orders?legal_entity_id={world.le}&state=completed&limit=50")["orders"]
    supplier = next(o["supplier_id"] for o in orders if o["currency"] == "CNY")
    po = approved_order(bob, login("test.buyer.lead"), world, supplier, 4)
    assert receive(bob, po, "4").status_code == 200
    si = bob.post("erp/finance", "/api/finance/supplier-invoices", {"legal_entity_id": world.le, "supplier_id": supplier,
                  "supplier_number": f"L3P-{po['number']}", "invoice_date": po["business_date"], "due_date": po["business_date"],
                  "currency": "CNY", "lines": [{"order_line_id": po["lines"][0]["id"], "quantity": "4", "unit_price": "10",
                                                "tax": "5.20"}]})
    bob.post("erp/finance", f"/api/finance/supplier-invoices/{si['id']}/match")
    si = bob.post("erp/finance", f"/api/finance/supplier-invoices/{si['id']}/post")
    payable = si["open_item_id"]

    proposal = bob.get("erp/payment", f"/api/payment/proposals?legal_entity_id={world.le}&kind=payable&partner_id={supplier}"
                                      f"&due_on_or_before=9999-12-31&limit=200")["items"]
    assert any(x["open_item_id"] == payable and Decimal(x["available"]) == Decimal("45.2") for x in proposal), proposal
    req = bob.post("erp/payment", "/api/payment/requests", {"legal_entity_id": world.le, "kind": "payment", "payee_id": supplier,
                   "bank_account_id": basic, "purpose": "L3 货款", "lines": [{"open_item_id": payable, "amount": "45.2"}]})
    path = f"/api/payment/requests/{req['id']}"
    req = bob.post("erp/payment", f"{path}/submit")
    assert req["state"] == "submitted", req
    item = bob.get("erp/finance", f"/api/finance/open-items/{payable}")
    assert Decimal(item["pay_claimed"]) == Decimal("45.2") and Decimal(item["available"]) == 0, item
    r = bob.call("POST", "erp/payment", f"{path}/approve", {})
    assert r.status_code == 403 and r.json()["reason"] == "SELF_APPROVAL", r.text  # the flow kernel's check (C7)
    assert lead.post("erp/payment", f"{path}/approve")["state"] == "approved"
    assert cashier.post("erp/payment", f"{path}/release")["state"] == "ready"
    paid = cashier.post("erp/payment", f"{path}/execute", {"paid_amount": "45.2", "paid_on": date.today().isoformat(),
                        "bank_reference": f"L3-{uuid.uuid4().hex[:10]}"})
    assert paid["state"] == "paid", paid
    settled = eventually(lambda: (x := bob.get("erp/finance", f"/api/finance/open-items/{payable}"))
                         and Decimal(x["settled"]) == Decimal("45.2") and x)
    assert Decimal(settled["pay_claimed"]) == 0 and Decimal(settled["open_balance"]) == 0, settled
