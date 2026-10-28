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


def test_a_scaled_baseline_multiplies_every_size():
    """B5 (step 10): baseline_scale: 2 in cell.yaml doubles the orders, the goods and each legal entity's customers;
    the plan keeps its shares, and the unscaled plan is the one every earlier cell had."""
    from cells.baseline import Sizes
    assert Sizes.of(1) == Sizes(20_000, 2_000, {"CN01": 400, "ES01": 100})
    s = Sizes.of(2)
    assert s == Sizes(40_000, 4_000, {"CN01": 800, "ES01": 200})
    plan = order_plan(customers=s.customers, goods=s.goods, orders=s.orders)
    assert len(plan) == 40_000
    got = Counter(o.state for o in plan)
    for state, share in STATES.items():
        assert got[state] == round(40_000 * share), state
    assert max(g for o in plan for g in o.goods) < 4_000
    one = Sizes.of(1)
    assert order_plan() == order_plan(customers=one.customers, goods=one.goods, orders=one.orders)


def test_the_cell_names_its_baseline_scale(tmp_path):
    from cells.spec import Cell
    (tmp_path / "cell.yaml").write_text("cell: T\nalone: [test/probe-go@3.2.20]\nchain: []\nbaseline_scale: 2\n")
    assert Cell.load(tmp_path).baseline_scale == 2
    (tmp_path / "cell.yaml").write_text("cell: T\nalone: [test/probe-go@3.2.20]\nchain: []\n")
    assert Cell.load(tmp_path).baseline_scale == 1
