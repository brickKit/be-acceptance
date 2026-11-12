"""L3, approvals in the unified worklist (C7): erp/purchase and erp/payment approve on the SDK's flow kernel (be-protocol
chapter 17) and publish their tasks by the shared task contract; infra/workflow shows each to its candidates (the
seed's approvers hold the positions: test.buyer.lead PUR-MGR, test.finance.lead FIN-MGR, in CN01), with the action
entry of the owning component; the decision goes straight to that component; the one who submitted does not decide;
a retry with the same key makes no second decision; what follows (ordering from the requisition, the entry's posting)
starts only as approved. Each test makes its own documents on top of the seed data."""
from __future__ import annotations

import uuid
from datetime import date

import pytest

from conftest import eventually


@pytest.fixture(autouse=True)
def _needs_worklist(cell):
    for c in ("infra/workflow", "erp/purchase", "erp/payment", "mdm/employee"):
        if c not in cell.placed:
            pytest.skip(f"the cell has no {c}")


def worklist_entry(user, task_type: str, document: str) -> dict:
    """The user's pending task of the document, as the worklist shows it (once the projection arrived)."""
    def find():
        for e in user.get("infra/workflow", f"/api/workflow/tasks?task_type={task_type}&limit=200")["tasks"]:
            if e["task"]["document"]["id"] == document:
                return e
        return None
    return eventually(find, seconds=60)


def decide(user, entry: dict, decision: str, key: str | None = None, comment: str | None = None):
    """A decision sent straight to the task's own component (its action entry, never an address from the projection)."""
    body = {"version": entry["task"]["version"], "decision": decision}
    if comment:
        body["comment"] = comment
    headers = {"Authorization": f"Bearer {user.token}", "Idempotency-Key": key or f"l3:{uuid.uuid4()}"}
    return user.http.post(user.url(entry["action"]["component"], entry["action"]["path"]), json=body, headers=headers)


def test_workflow_requisition_approved_from_the_worklist(bob, login, world):
    """PR02, PR03, PR06; WF03-WF05: the submitted requisition is in the purchasing lead's worklist; its submitter may
    not decide it; the lead's decision goes to erp/purchase, a retry with the same key answers the same and makes no
    second decision; approved, an order takes its line; the task leaves the worklist for what the lead did."""
    lead = login("test.buyer.lead")
    req = bob.post("erp/purchase", "/api/purchase/requisitions", {"legal_entity_id": world.le,
                   "department_id": world.departments["PUR"],
                   "lines": [{"product_id": world.goods["id"], "quantity": "3", "unit": world.goods["base_unit"]}]})
    assert bob.post("erp/purchase", f"/api/purchase/requisitions/{req['id']}/submit")["state"] == "submitted"
    entry = worklist_entry(lead, "erp.purchase.requisition.approval", req["id"])
    assert entry["action"] == {"component": "erp/purchase", "path": f"/be/flow/tasks/{entry['task']['id']}/decisions"}
    assert entry["stale"] is False and lead.principal in entry["task"]["candidates"], entry
    assert bob.principal not in entry["task"]["candidates"], "the submitter's person is left out"

    r = decide(bob, entry, "approve")
    assert r.status_code == 403 and r.json()["reason"] == "SELF_APPROVAL", r.text
    key = f"l3:{uuid.uuid4()}"
    first = decide(lead, entry, "approve", key)
    assert first.status_code == 200, first.text
    again = decide(lead, entry, "approve", key)
    assert again.status_code == 200 and again.json()["version"] == first.json()["version"], again.text
    task = lead.get("erp/purchase", f"/be/flow/tasks/{entry['task']['id']}")
    assert task["state"] == "decided" and task["decision"]["by"] == lead.principal, task
    assert bob.get("erp/purchase", f"/api/purchase/requisitions/{req['id']}")["state"] == "approved"

    # released as approved: an order takes the requisition line (PR06)
    supplier = next(o["supplier_id"] for o in bob.get("erp/purchase", f"/api/purchase/orders?legal_entity_id={world.le}"
                    "&state=completed&limit=50")["orders"] if o["currency"] == "CNY")
    line = bob.get("erp/purchase", f"/api/purchase/requisitions/{req['id']}")["lines"][0]
    o = bob.post("erp/purchase", "/api/purchase/orders", {"legal_entity_id": world.le, "supplier_id": supplier,
                 "currency": "CNY", "owner_id": bob.principal,
                 "lines": [{"product_id": world.goods["id"], "quantity": "3", "unit": world.goods["base_unit"],
                            "unit_price": "10", "warehouse_id": world.shanghai, "requisition_line_id": line["id"]}]})
    assert o["lines"][0]["requisition_line_id"] == line["id"], o

    def done():
        mine = lead.get("infra/workflow", "/api/workflow/tasks?task_type=erp.purchase.requisition.approval&limit=200")["tasks"]
        did = lead.get("infra/workflow", "/api/workflow/tasks/done?task_type=erp.purchase.requisition.approval&limit=200")["tasks"]
        return (all(e["task"]["id"] != entry["task"]["id"] for e in mine)
                and any(e["task"]["id"] == entry["task"]["id"] for e in did)) or None
    eventually(done, seconds=60)
    seen = lead.get("infra/workflow", f"/api/workflow/tasks/{entry['task']['id']}?component=erp/purchase")
    assert seen["task"]["state"] == "decided" and seen["task"]["instance"]["state"] == "approved", seen


def test_workflow_entry_rejected_then_posted(bob, login, world):
    """BE02; WF03, WF05: a bank charge's task is in the finance lead's worklist (CN01's FIN-MGR); rejected with a
    comment it goes back to draft; submitted again, approved from the worklist, it is posted with its accounting fact."""
    lead = login("test.finance.lead")
    basic = next(a["id"] for a in bob.get("erp/payment", f"/api/payment/bank-accounts?limit=200&legal_entity_id={world.le}")
                 ["bank_accounts"] if a["name"].endswith("基本户"))
    e = bob.post("erp/payment", "/api/payment/entries", {"legal_entity_id": world.le, "kind": "charge",
                 "bank_account_id": basic, "amount": "12.5", "entry_date": date.today().isoformat(),
                 "description": "L3 待办里的手续费"})
    path = f"/api/payment/entries/{e['id']}"
    assert bob.post("erp/payment", f"{path}/submit")["state"] == "submitted"
    entry = worklist_entry(lead, "erp.payment.entry.approval", e["id"])
    r = decide(lead, entry, "reject")
    assert r.status_code == 400 and r.json()["reason"] == "REQUEST_INVALID", "a rejection says why: " + r.text
    r = decide(lead, entry, "reject", comment="L3 金额与对账单不符")
    assert r.status_code == 200, r.text
    back = bob.get("erp/payment", path)
    assert back["state"] == "draft" and back["reason"] == "L3 金额与对账单不符", back

    assert bob.post("erp/payment", f"{path}/submit")["state"] == "submitted"
    again = worklist_entry(lead, "erp.payment.entry.approval", e["id"])
    assert again["task"]["id"] != entry["task"]["id"], "submitted again: a new instance, a new task"
    r = decide(lead, again, "approve")
    assert r.status_code == 200, r.text
    posted = bob.get("erp/payment", path)
    assert posted["state"] == "posted" and posted["approver_id"] == lead.principal, posted
