"""L3, the mobile aggregation (C11, infra/bff-mobile): a persisted query by its hash, each field read from its owner's
user surface with the caller's own credential, so what an account sees through it is what the owner answers that
account directly; "mine" is the verified caller; the approval goes from the task's action entry straight to its owner
(the front end's path, never through the aggregation). The calls go to the member's host port with a bearer token,
as every L3 scenario does; the edge in front of a cell comes with the first front end (C12)."""
from __future__ import annotations

import uuid

import httpx
import pytest

from cells.bffqueries import hash_of
from conftest import eventually

BFF = "infra/bff-mobile"
PATH = "/api/bff-mobile/graphql"


@pytest.fixture(autouse=True)
def _needs_bff(cell):
    for c in (BFF, "infra/workflow", "infra/notification", "mdm/product", "erp/inventory", "mdm/partner", "erp/sales",
              "crm/opportunity", "erp/purchase"):
        if c not in cell.placed:
            pytest.skip(f"the cell has no {c}")


def ask(user, name: str, variables: dict | None = None, hash_: str | None = None) -> httpx.Response:
    body = {"extensions": {"persistedQuery": {"version": 1, "sha256Hash": hash_ or hash_of(name)}}}
    if variables is not None:
        body["variables"] = variables
    # BE-Request as the browser entry sends it through the edge (P11.2); the member itself does not need it
    return user.http.post(user.url(BFF, PATH), json=body,
                          headers={"Authorization": f"Bearer {user.token}", "BE-Request": "1"})


def data(user, name: str, variables: dict | None = None) -> dict:
    r = ask(user, name, variables)
    assert r.status_code == 200, f"{name}: {r.status_code} {r.text[:300]}"
    out = r.json()
    assert not out.get("errors"), f"{name}: {out['errors']}"
    return out["data"]


def every(user, name: str, field: str, variables: dict | None = None) -> list[dict]:
    """Every item of a list field, page by page (the owner's cursors passed back as they are)."""
    items, cursor = [], None
    while True:
        page = data(user, name, {**(variables or {}), "cursor": cursor, "limit": 50})[field]
        items += page["items"]
        if not (cursor := page["nextCursor"]):
            return items


def test_bff_entry_refusals(login):
    """BM01-BM03: without the entry operation 403; an unknown hash, a limit over 50: 400, before any owner is asked."""
    r = ask(login("test.nobody"), "home")
    assert r.status_code == 403 and r.json()["reason"] == "PERMISSION_DENIED", r.text
    lead = login("test.buyer.lead")
    r = ask(lead, "home", hash_="0" * 64)
    assert r.status_code == 400 and r.json()["reason"] == "PERSISTED_QUERY_NOT_FOUND", r.text
    assert r.json()["domain"] == BFF
    r = ask(lead, "home", {"limit": 51})
    assert r.status_code == 400 and r.json()["reason"] == "REQUEST_INVALID", r.text


def test_bff_approve_from_the_mobile_worklist(bob, login, world):
    """BM04, WF05, P11.11: a submitted requisition is in the purchasing lead's mobile worklist with the action entry
    under erp/purchase's public prefix; the decision sent there approves it; the task leaves the mobile worklist and
    reads as decided."""
    lead = login("test.buyer.lead")
    req = bob.post("erp/purchase", "/api/purchase/requisitions", {"legal_entity_id": world.le,
                   "department_id": world.departments["PUR"],
                   "lines": [{"product_id": world.goods["id"], "quantity": "2", "unit": world.goods["base_unit"]}]})
    assert bob.post("erp/purchase", f"/api/purchase/requisitions/{req['id']}/submit")["state"] == "submitted"

    def direct():  # the worklist's own answer to the lead, once the projection arrived
        for e in lead.get("infra/workflow", "/api/workflow/tasks?task_type=erp.purchase.requisition.approval&limit=200")["tasks"]:
            if e["task"]["document"]["id"] == req["id"]:
                return e
        return None
    entry = eventually(direct, seconds=60)
    tid = entry["task"]["id"]

    task = data(lead, "task", {"component": "erp/purchase", "id": tid})["task"]
    assert task["action"] == {"component": "erp/purchase", "path": f"/api/purchase/flow/tasks/{tid}/decisions"}, task
    assert task["version"] == entry["task"]["version"] and task["state"] == entry["task"]["state"]
    assert task["summary"]["title"] == entry["task"]["summary"]["title"]

    def listed() -> list[str]:
        return [t["id"] for t in every(lead, "home", "myTasks", {"taskType": "erp.purchase.requisition.approval"})]
    assert tid in listed()

    # the front end's decision: straight to the owner at the entry the aggregation gave
    r = lead.http.post(lead.url(task["action"]["component"], task["action"]["path"]),
                       json={"version": task["version"], "decision": "approve"},
                       headers={"Authorization": f"Bearer {lead.token}", "Idempotency-Key": f"l3:{uuid.uuid4()}"})
    assert r.status_code == 200, r.text
    assert bob.get("erp/purchase", f"/api/purchase/requisitions/{req['id']}")["state"] == "approved"
    eventually(lambda: tid not in listed(), seconds=60)
    eventually(lambda: data(lead, "task", {"component": "erp/purchase", "id": tid})["task"]["state"] == "decided",
               seconds=60)


def test_bff_home_counts_as_the_owner(login):
    """BM04: the unread count and the first page are the notification component's own answer to the same account."""
    lead = login("test.buyer.lead")
    home = data(lead, "home", {"limit": 5})
    direct = lead.get("infra/notification", "/api/notification/messages?limit=5")
    assert home["myNotifications"]["unread"] == direct["unread"]
    assert [m["id"] for m in home["myNotifications"]["items"]] == [m["id"] for m in direct["messages"]]


def test_bff_product_and_its_stock_as_the_owners(bob, world):
    """BM04, BM05: a product found by its code, its detail with the stock of each warehouse: the positions are
    erp/inventory's own answer, named from the warehouses the caller sees."""
    found = data(bob, "products", {"q": world.goods["code"], "limit": 10})["products"]["items"]
    assert world.goods["id"] in [p["id"] for p in found], found
    detail = data(bob, "product", {"id": world.goods["id"]})["product"]
    assert detail["code"] == world.goods["code"]
    positions = bob.get("erp/inventory", f"/api/inventory/positions?product_id={world.goods['id']}&limit=200")["positions"]
    assert {(s["warehouse"]["id"], s["onHand"], s["reserved"], s["available"]) for s in detail["stock"]} == \
        {(p["warehouse_id"], p["on_hand"], p["reserved"], p["available"]) for p in positions}
    named = {w["id"]: w for w in bob.get("erp/inventory", "/api/inventory/warehouses?limit=200")["warehouses"]}
    for s in detail["stock"]:
        w = named.get(s["warehouse"]["id"])
        assert s["warehouse"]["name"] == (w["name"] if w else None), s
    assert world.shanghai in {s["warehouse"]["id"] for s in detail["stock"]}


def test_bff_customers_as_the_owner(bob, world):
    """BM04: the customers of CN01 are mdm/partner's own list with the customer role in that legal entity."""
    mine = data(bob, "customers", {"le": world.le, "limit": 20})["customers"]
    direct = bob.get("mdm/partner", f"/api/partner/partners?role=customer&legal_entity_id={world.le}&limit=20")
    assert [p["id"] for p in mine["items"]] == [p["id"] for p in direct["partners"]]
    assert mine["nextCursor"] == direct["next_cursor"]
    assert world.large in [p["id"] for p in every(bob, "customers", "customers", {"le": world.le})]


def test_bff_mine_is_the_verified_caller(bob, login):
    """BM04: my orders and my opportunities are the owners' lists filtered by the caller's own principal: a
    salesperson's are theirs, the seed user's are not theirs."""
    sales = login("test.sales.cn")
    orders = data(sales, "orders", {"limit": 50})["myOrders"]
    direct = sales.get("erp/sales", f"/api/sales/orders?owner_id={sales.principal}&limit=50")
    assert [o["id"] for o in orders["items"]] == [o["id"] for o in direct["orders"]]
    assert orders["items"], "the seed gives test.sales.cn orders of their own"
    for mine, theirs in zip(orders["items"], direct["orders"]):
        assert mine["total"] == theirs["totals"]["gross"] and mine["customer"]["partnerId"] == theirs["customer"]["partner_id"]

    opps = every(sales, "opportunities", "myOpportunities")
    direct_opps, cursor = [], ""
    while True:
        page = sales.get("crm/opportunity", f"/api/opportunity/opportunities?owner_id={sales.principal}&limit=50&cursor={cursor}")
        direct_opps += page["opportunities"]
        if not (cursor := page.get("next_cursor") or ""):
            break
    assert [o["id"] for o in opps] == [o["id"] for o in direct_opps]
    assert len(opps) > 50, "more than one page (the seed gives test.sales.cn 74)"
    bobs = {o["id"] for o in every(bob, "opportunities", "myOpportunities")}
    assert not bobs & {o["id"] for o in opps}, "the seed user's opportunities are not the salesperson's"
