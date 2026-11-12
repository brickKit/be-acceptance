"""L3, notifications of the approvals (C8): the flow kernel of erp/purchase sends a notification intent when a task waits
for its candidates and withdraws it when the task leaves pending (be-protocol P18.10); infra/notification takes the
intents of the cell's senders and writes them as in-app messages (the seed's approver test.buyer.lead holds CN01's
PUR-MGR). A message links to the task in the worklist; deciding the task withdraws it; the approver's preference
turns the category off, and a category that must be delivered cannot be turned off. Each test makes its own documents
on top of the seed data."""
from __future__ import annotations

import time

import pytest

from conftest import eventually
from test_workflow import decide, worklist_entry


@pytest.fixture(autouse=True)
def _needs_notifications(cell):
    for c in ("infra/notification", "infra/workflow", "erp/purchase", "mdm/employee"):
        if c not in cell.placed:
            pytest.skip(f"the cell has no {c}")


def submitted_requisition(bob, world) -> dict:
    req = bob.post("erp/purchase", "/api/purchase/requisitions", {"legal_entity_id": world.le,
                   "department_id": world.departments["PUR"],
                   "lines": [{"product_id": world.goods["id"], "quantity": "2", "unit": world.goods["base_unit"]}]})
    assert bob.post("erp/purchase", f"/api/purchase/requisitions/{req['id']}/submit")["state"] == "submitted"
    return req


def message_of(user, link: str) -> dict | None:
    """The user's message linking to the path, if any."""
    for m in user.get("infra/notification", "/api/notification/messages?limit=200")["messages"]:
        if m["link"] == link:
            return m
    return None


def test_notification_an_approval_reaches_its_approver(bob, login, world):
    """P18.10; NT04, NT05, NT08: the submitted requisition's task is a message for the purchasing lead, unread, linking
    to the task in the worklist; the submitter gets none; the lead marks it read; deciding the task withdraws it."""
    lead = login("test.buyer.lead")
    req = submitted_requisition(bob, world)
    entry = worklist_entry(lead, "erp.purchase.requisition.approval", req["id"])
    link = f"/workflow/tasks/{entry['task']['id']}"
    m = eventually(lambda: message_of(lead, link), seconds=60)
    assert m["category"] == "approval_task" and m["sender"] == "erp/purchase", m
    assert m["read_at"] is None and m["withdrawn"] is False, m
    assert m["template"] == "be.flow.task.pending" and m["params"]["task_type"] == "erp.purchase.requisition.approval", m
    assert message_of(bob, link) is None, "the submitter is no candidate"
    read = lead.post("infra/notification", f"/api/notification/messages/{m['id']}/read")
    assert read["read_at"] is not None, read
    r = lead.http.post(lead.url("infra/notification", f"/api/notification/messages/{m['id']}/read"),
                       headers={"Authorization": f"Bearer {bob.token}"})
    assert r.status_code == 404, "another one's message: " + r.text

    assert decide(lead, entry, "approve").status_code == 200
    eventually(lambda: (message_of(lead, link) or {}).get("withdrawn"), seconds=60)


def test_notification_the_approvers_preferences(bob, login, world):
    """NT03, NT07, NT09: the lead turns approval tasks off: a requisition submitted then reaches the worklist but no
    message; turned on again, the next one does; security alerts cannot be turned off."""
    lead = login("test.buyer.lead")
    path = "/api/notification/preferences/approval_task/in_app"

    def put(enabled: bool):
        return lead.http.put(lead.url("infra/notification", path), json={"enabled": enabled},
                             headers={"Authorization": f"Bearer {lead.token}"})

    r = lead.http.put(lead.url("infra/notification", "/api/notification/preferences/security_alert/in_app"),
                      json={"enabled": False}, headers={"Authorization": f"Bearer {lead.token}"})
    assert r.status_code == 400 and r.json()["reason"] == "REQUEST_INVALID", r.text
    assert put(False).status_code == 200
    try:
        off = submitted_requisition(bob, world)
        off_entry = worklist_entry(lead, "erp.purchase.requisition.approval", off["id"])
        # the intent went out with the task's projection; the notification component takes it in its own time (L3
        # cannot read its delivery results): give it a moment before the preference turns back
        time.sleep(5)
    finally:
        assert put(True).status_code == 200
    on = submitted_requisition(bob, world)
    on_entry = worklist_entry(lead, "erp.purchase.requisition.approval", on["id"])
    eventually(lambda: message_of(lead, f"/workflow/tasks/{on_entry['task']['id']}"), seconds=60)
    assert message_of(lead, f"/workflow/tasks/{off_entry['task']['id']}") is None, "turned off when it was delivered"
    prefs = {p["category"]: p for p in lead.get("infra/notification", "/api/notification/preferences")["preferences"]}
    assert prefs["approval_task"]["enabled"] is True and prefs["security_alert"]["mandatory"] is True, prefs
