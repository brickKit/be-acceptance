"""L3, who sees and does what across the trading chain (step 8, T8.4; security 07, the seed's planned scopes in the
assembly's docs/zh/03-seed-data.md): an account without grants sees nothing, a salesperson sees their legal entity and
departments only and cannot write outside them, and only the billing role issues invoices. Every cell, single or
shell, must answer the same."""
from __future__ import annotations

from test_trading_chain import goods, order


def ids(rows: list[dict]) -> set[str]:
    return {r["id"] for r in rows}


def test_an_account_without_grants_sees_no_documents(login):
    nobody = login("test.nobody")
    for component, path, key in (("erp/sales", "/api/sales/orders?limit=50", "orders"),
                                 ("erp/sales", "/api/sales/deliveries?limit=50", "deliveries"),
                                 ("erp/billing", "/api/billing/invoices?limit=50", "invoices")):
        r = nobody.call("GET", component, path)
        assert r.status_code == 404 or (r.status_code == 200 and r.json()[key] == []), f"{path}: {r.status_code} {r.text[:200]}"


def test_sales_es_sees_no_cn01_order(login, world):
    es = login("test.sales.es")
    r = es.call("GET", "erp/sales", f"/api/sales/orders?limit=50&legal_entity_id={world.le}")
    assert r.status_code == 404 or (r.status_code == 200 and r.json()["orders"] == []), r.text[:200]


def test_sales_cn_sees_its_department_subtree_only(login, bob, world):
    cn = login("test.sales.cn")
    east = bob.get("mdm/org", f"/api/org/units/{world.departments['SALES-EAST']}")["path"]
    subtree = {u["id"] for u in bob.get("mdm/org", f"/api/org/units?limit=200&legal_entity_id={world.le}")["units"]
               if u["path"].startswith(east)}
    seen = cn.get("erp/sales", f"/api/sales/orders?limit=200&legal_entity_id={world.le}")["orders"]
    assert seen, "test.sales.cn sees its own department's orders"
    outside = [o for o in seen if o.get("department_id") not in subtree]
    assert not outside, f"orders outside SALES-EAST: {[o['number'] for o in outside[:5]]}"


def test_sales_cn_cannot_read_or_write_outside_its_scope(login, bob, world):
    cn = login("test.sales.cn")
    west = order(bob, world, world.large, [goods(world, 1)], department=world.departments["SALES-WEST"])
    assert cn.call("GET", "erp/sales", f"/api/sales/orders/{west['id']}").status_code == 404, "out of scope reads as absent"
    body = {"legal_entity_id": world.le, "customer_id": world.large, "currency": "CNY", "owner_id": bob.principal,
            "department_id": world.departments["SALES-WEST"], "lines": [goods(world, 1)]}
    r = cn.call("POST", "erp/sales", "/api/sales/orders", body)
    assert r.status_code in (403, 404), f"writing in SALES-WEST: {r.status_code} {r.text[:200]}"
    bob.post("erp/sales", f"/api/sales/orders/{west['id']}/cancel")


def test_only_the_billing_role_issues_invoices(login, bob, world):
    o = order(bob, world, world.large, [goods(world, 1)])
    assert bob.post("erp/sales", f"/api/sales/orders/{o['id']}/confirm")["outcome"] == "confirmed"
    line = bob.get("erp/sales", f"/api/sales/orders/{o['id']}")["lines"][0]
    bob.post("erp/sales", "/api/sales/deliveries", {"order_id": o["id"], "lines": [{"order_line_id": line["id"], "quantity": "1"}]})
    inv = bob.post("erp/billing", "/api/billing/invoices", {"order_id": o["id"]})
    for name in ("test.sales.cn", "test.sales.multi", "test.sales.es"):
        r = login(name).call("POST", "erp/billing", f"/api/billing/invoices/{inv['id']}/issue")
        assert r.status_code in (403, 404), f"{name} issued an invoice: {r.status_code} {r.text[:200]}"
    assert bob.post("erp/billing", f"/api/billing/invoices/{inv['id']}/void")["state"] == "void"
