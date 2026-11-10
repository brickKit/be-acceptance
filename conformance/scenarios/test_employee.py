"""L3, the employee member with the authorization member (C1; security 04's appointments, security 08's natural
person): a grant bound to an appointment gives access while the appointment is open and nothing once it ends, a new
appointment gets nothing by itself; a disposition of one account holds every account of the same person (the seed links
test.mdm.admin and test.mdm.viewer to one employee). Every run leaves the seed data as it found it but for an ended
appointment and an ended grant; it runs again (scenarios after the authorization member moves)."""
from __future__ import annotations

import datetime as dt
import uuid
import zoneinfo

import pytest
from besdk import canon

from conftest import eventually

@pytest.fixture(autouse=True)
def _needs_employee(cell):
    if "mdm/employee" not in cell.placed:
        pytest.skip("the cell has no mdm/employee")


def start() -> str:
    return (dt.datetime.now(dt.timezone.utc) - dt.timedelta(minutes=1)).strftime("%Y-%m-%dT%H:%M:%S.%fZ")


def employee_read() -> dict:
    return {"resource_type": "mdm.employee", "operation": "mdm.employee.read", "atoms": {"legal_entity": {"kind": "all"}},
            "read_fields": ["id", "employee_no", "name", "state"], "valid": {"from": start()}}


def root_unit(alice) -> str:
    return next(r["id"] for r in alice.get("infra/authz", "/api/authz/records?type=management_unit")["records"]
                if r["body"].get("root"))


def test_an_appointments_end_ends_what_it_bound(login, bob):
    alice, cn = login("test.alice"), login("test.sales.cn")
    emp = bob.get("mdm/employee", f"/api/employee/accounts/{cn.principal}/employee")
    reads = lambda: cn.call("GET", "mdm/employee", f"/api/employee/employees/{emp['id']}").status_code  # noqa: E731
    assert reads() == 404, "test.sales.cn reads no employee by its own grants"
    # a new appointment today on a sales representative's position it never held
    le = bob.get("mdm/org", f"/api/org/legal-entities/{emp['legal_entity_id']}")
    held = {a["position_id"] for a in bob.get("mdm/employee", f"/api/employee/employees/{emp['id']}/appointments")["appointments"]}
    position = next(p for p in bob.get("mdm/org", f"/api/org/positions?state=active&limit=200&legal_entity_id={le['id']}")["positions"]
                    if p["code"].endswith("-REP") and p["id"] not in held)
    today = dt.datetime.now(zoneinfo.ZoneInfo(le["timezone"])).date().isoformat()
    appt = bob.post("mdm/employee", f"/api/employee/employees/{emp['id']}/appointments",
                    {"position_id": position["id"], "start_date": today, "note": "L3"})
    assert appt["state"] == "active"
    # the administrator grants on it: the account reads the employee while the appointment is open
    candidate = {"record_type": "grant", "governance_unit": root_unit(alice), "grantee": cn.principal,
                 "appointment": appt["id"], "segments": [employee_read()]}
    # the authorization member applies the new appointment from the employee member's stream a moment later: until then
    # it answers 503 (not provable, the change key left free) and the same request goes again
    change = {"change_key": f"l3-appt-{uuid.uuid4()}", "direction": "widen", "candidate": candidate,
              "candidate_digest": canon.digest("be.security.grant", 1, candidate)}

    def grant():
        r = alice.call("POST", "infra/authz", "/api/authz/changes", change)
        assert r.status_code in (200, 503), f"{r.status_code} {r.text[:300]}"
        return r.json() if r.status_code == 200 else None

    granted = eventually(grant, seconds=30)
    eventually(lambda: reads() == 200, seconds=30)
    # the appointment ends: nothing at once (the next verification), and the books end the grant
    bob.post("mdm/employee", f"/api/employee/appointments/{appt['id']}/end")
    eventually(lambda: reads() == 404, seconds=60)
    eventually(lambda: granted["record"] not in {r["id"] for r in alice.get("infra/authz", "/api/authz/records?type=grant")["records"]},
               seconds=60)  # the effective records only: booked ended (source_ended)
    # a new appointment gets nothing by itself
    other = next(p for p in bob.get("mdm/org", f"/api/org/positions?state=active&limit=200&legal_entity_id={le['id']}")["positions"]
                 if p["code"].endswith("-REP") and p["id"] not in held | {position["id"]})
    again = bob.post("mdm/employee", f"/api/employee/employees/{emp['id']}/appointments",
                     {"position_id": other["id"], "start_date": today, "note": "L3"})
    assert reads() == 404, "a new appointment is not the old one"
    bob.post("mdm/employee", f"/api/employee/appointments/{again['id']}/end")


def test_a_disposition_holds_the_person_not_the_account(login, bob):
    alice, owner, viewer = login("test.alice"), login("test.mdm.admin"), login("test.mdm.viewer")
    one = bob.get("mdm/employee", f"/api/employee/accounts/{owner.principal}/employee")
    assert bob.get("mdm/employee", f"/api/employee/accounts/{viewer.principal}/employee")["id"] == one["id"], \
        "the seed links both accounts to one employee"
    reads = lambda: owner.call("GET", "mdm/employee", f"/api/employee/employees/{one['id']}").status_code  # noqa: E731
    assert reads() == 200
    # the viewer account is disposed on the employee reads: the person's other account loses them too
    d = alice.post("infra/authz", "/api/authz/dispositions", {"change_key": f"l3-disp-{uuid.uuid4()}", "subject": viewer.principal,
                                                              "suspended_segments": [employee_read()]})
    try:
        eventually(lambda: reads() == 404, seconds=60)
    finally:
        alice.post("infra/authz", f"/api/authz/dispositions/{d['record']}/release",
                   {"change_key": f"l3-rel-{uuid.uuid4()}", "disposition_revision": d["new_revision"]})
    eventually(lambda: reads() == 200, seconds=60)
