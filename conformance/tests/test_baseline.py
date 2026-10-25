"""The step 8 baseline's order plan (cells/baseline.py): what the 20,000 historical orders are, before any request."""
from collections import Counter

from cells.baseline import ORDERS, STATES, order_plan

CN_SLOTS = {("SALES-EAST", "east"), ("SALES-EAST-KA", "east"), ("SALES-EAST-SMB", "east"), ("SALES-SOUTH", "south"),
            ("SALES-SOUTH-KA", "south"), ("SALES-WEST", "seed"), ("SALES-NORTH", "seed"), ("OPS", "seed"), ("", "seed")}


def test_the_plan_has_every_order_once_split_over_two_legal_entities():
    plan = order_plan(customers={"CN01": 400, "ES01": 100}, goods=2000)
    assert len(plan) == ORDERS == 20_000
    assert len({o.n for o in plan}) == ORDERS
    assert Counter(o.legal_entity for o in plan) == {"CN01": 16_000, "ES01": 4_000}


def test_the_states_follow_the_shares():
    plan = order_plan(customers={"CN01": 400, "ES01": 100}, goods=2000)
    got = Counter(o.state for o in plan)
    for state, share in STATES.items():
        assert got[state] == round(ORDERS * share), state


def test_departments_and_owners_pair_as_the_sales_seed_does():
    plan = order_plan(customers={"CN01": 400, "ES01": 100}, goods=2000)
    assert {(o.department, o.owner) for o in plan if o.legal_entity == "CN01"} == CN_SLOTS
    assert {o.owner for o in plan if o.legal_entity == "ES01"} == {"es"}


def test_lines_customers_and_goods_stay_in_range_and_dates_in_the_year():
    plan = order_plan(customers={"CN01": 400, "ES01": 100}, goods=2000)
    assert {len(o.goods) for o in plan} == {1, 2, 3}
    assert all(0 <= g < 2000 for o in plan for g in o.goods)
    assert all(len(set(o.goods)) == len(o.goods) for o in plan)
    assert all(0 <= o.customer < (400 if o.legal_entity == "CN01" else 100) for o in plan)
    assert min(o.business_date for o in plan) >= "2026-01-01"
    assert max(o.business_date for o in plan) <= "2026-10-24"


def test_the_plan_is_the_same_every_time():
    a = order_plan(customers={"CN01": 400, "ES01": 100}, goods=2000)
    b = order_plan(customers={"CN01": 400, "ES01": 100}, goods=2000)
    assert a == b
