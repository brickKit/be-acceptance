"""L3, the independent expectations of protocols 04 and 05 that a running cell can be driven into through the user
surfaces (step 8, T8.4). The ones that need a lost answer, a crash or a stopped bus stay in the components' L2 tests
or go to suite C; README.md lists where each one is."""
from __future__ import annotations

import uuid
from concurrent.futures import ThreadPoolExecutor
from decimal import Decimal

from conftest import eventually
from test_trading_chain import goods, order, position


def confirm(user, order_id: str, key: str | None = None) -> dict:
    r = user.http.post(user.url("erp/sales", f"/api/sales/orders/{order_id}/confirm"),
                       headers={"Authorization": f"Bearer {user.token}", "Idempotency-Key": key or f"l3:{uuid.uuid4()}"})
    assert r.status_code == 200, f"{r.status_code} {r.text[:300]}"
    return r.json()


def test_rs01_two_orders_race_for_the_last_unit(bob, world):
    """RS01: Beijing holds one tin; two orders for it confirm at once — one holds it, the other is refused for stock."""
    on_hand, reserved = position(bob, world, world.beijing, world.tin["id"])
    free = on_hand - reserved
    assert free >= 1, f"Beijing has {free} tins free: the seed keeps one"
    orders = [order(bob, world, world.large, [goods(world, int(free), world.beijing, world.tin)]) for _ in range(2)]
    with ThreadPoolExecutor(2) as pool:
        answers = list(pool.map(lambda o: confirm(bob, o["id"]), orders))
    outcomes = sorted((a["outcome"], a.get("reason") or "") for a in answers)
    assert outcomes == [("confirmed", ""), ("rejected", "insufficient_stock")], answers
    winner = next(o for o, a in zip(orders, answers) if a["outcome"] == "confirmed")
    assert bob.post("erp/sales", f"/api/sales/orders/{winner['id']}/cancel")["state"] == "cancelled"
    eventually(lambda: position(bob, world, world.beijing, world.tin["id"]) == (on_hand, reserved))


def test_rs02_credit_refusal_releases_inventory_is_in_the_trading_chain():
    """RS02 (stock held, credit refuses: the stock branch is released) is test_trading_chain's
    test_sales_credit_refusal_releases_the_inventory_hold; named here so the expectation has its place."""


def test_rs08_the_same_key_confirms_once(bob, world):
    """RS08: confirming again with the same idempotency key answers the same process and holds the stock once."""
    on_hand, reserved = position(bob, world, world.shanghai, world.goods["id"])
    o = order(bob, world, world.large, [goods(world, 2)])
    key = f"l3:{uuid.uuid4()}"
    first, again = confirm(bob, o["id"], key), confirm(bob, o["id"], key)
    assert first["outcome"] == again["outcome"] == "confirmed", (first, again)
    assert first.get("confirmation_id") == again.get("confirmation_id"), (first, again)
    assert position(bob, world, world.shanghai, world.goods["id"]) == (on_hand, reserved + 2), "held once"
    bob.post("erp/sales", f"/api/sales/orders/{o['id']}/cancel")
    eventually(lambda: position(bob, world, world.shanghai, world.goods["id"]) == (on_hand, reserved))


def test_rs16_opposite_line_orders_confirm_without_deadlock(bob, world, second_goods):
    """RS16: orders with the same two products in opposite order confirm at the same time, ten pairs: every one is
    confirmed, none waits on the other (a fixed lock order)."""
    a, b = world.goods, second_goods
    pairs = [(order(bob, world, world.large, [goods(world, 1, product=a), goods(world, 1, product=b)]),
              order(bob, world, world.large, [goods(world, 1, product=b), goods(world, 1, product=a)])) for _ in range(10)]
    flat = [o for p in pairs for o in p]
    with ThreadPoolExecutor(len(flat)) as pool:
        answers = list(pool.map(lambda o: confirm(bob, o["id"]), flat))
    assert [x["outcome"] for x in answers] == ["confirmed"] * len(flat), [x.get("reason") for x in answers]
    for o in flat:
        bob.post("erp/sales", f"/api/sales/orders/{o['id']}/cancel")


def test_rs19_the_delivery_consumes_its_branch_in_the_same_movement(bob, world):
    """RS19: the delivery's stock issue and the branch's consumption are one movement, which names the branch."""
    on_hand, reserved = position(bob, world, world.shanghai, world.goods["id"])
    o = order(bob, world, world.large, [goods(world, 2)])
    ans = confirm(bob, o["id"])
    assert ans["outcome"] == "confirmed", ans
    line = ans["order"]["lines"][0]
    d = bob.post("erp/sales", "/api/sales/deliveries", {"order_id": o["id"], "lines": [{"order_line_id": line["id"], "quantity": "2"}]})
    assert position(bob, world, world.shanghai, world.goods["id"]) == (on_hand - 2, reserved), "issued and unheld at once"
    moves = bob.get("erp/inventory", f"/api/inventory/movements?limit=50&product_id={world.goods['id']}&warehouse_id={world.shanghai}")["movements"]
    mine = [m for m in moves if m.get("consumption_id") == d["id"]]
    assert len(mine) == 1 and mine[0]["branch_id"], f"one movement consumes the delivery's branch: {mine}"


def test_bl01_two_drafts_race_for_the_same_billable_line(bob, world):
    """BL01: two drafts for the same order at once: one claims what is left, the other finds nothing to claim."""
    o = order(bob, world, world.large, [goods(world, 1)])
    ans = confirm(bob, o["id"])
    line = ans["order"]["lines"][0]
    bob.post("erp/sales", "/api/sales/deliveries", {"order_id": o["id"], "lines": [{"order_line_id": line["id"], "quantity": "1"}]})
    with ThreadPoolExecutor(2) as pool:
        drafts = list(pool.map(lambda _: bob.call("POST", "erp/billing", "/api/billing/invoices", {"order_id": o["id"]}), range(2)))
    codes = sorted(r.status_code for r in drafts)
    assert codes[0] == 200 and codes[1] in (409, 422), [(r.status_code, r.text[:200]) for r in drafts]


def test_bl02_the_same_draft_retried_claims_once(bob, world):
    """BL02: drafting again with the same idempotency key answers the same invoice and claims nothing more."""
    o = order(bob, world, world.large, [goods(world, 1)])
    ans = confirm(bob, o["id"])
    line = ans["order"]["lines"][0]
    bob.post("erp/sales", "/api/sales/deliveries", {"order_id": o["id"], "lines": [{"order_line_id": line["id"], "quantity": "1"}]})
    key = f"l3:{uuid.uuid4()}"
    hdr = {"Authorization": f"Bearer {bob.token}", "Idempotency-Key": key}
    url = bob.url("erp/billing", "/api/billing/invoices")
    first, again = (bob.http.post(url, json={"order_id": o["id"]}, headers=hdr) for _ in range(2))
    assert first.status_code == again.status_code == 200, (first.text[:200], again.text[:200])
    assert first.json()["id"] == again.json()["id"]
    assert Decimal(first.json()["totals"]["gross"]) == Decimal(again.json()["totals"]["gross"])
