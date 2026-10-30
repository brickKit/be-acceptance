"""Suite C's invariants (cells/invariants.py): each judge passes the states a settled cell has and names each kind of
breakage a fault could leave."""
from cells.invariants import judge_billing, judge_orders, judge_processes, judge_runtime, judge_sums


def test_settled_states_hold():
    branches = [
        ("SO-1", "confirmed", "erp/inventory", "committed"), ("SO-1", "confirmed", "erp/credit", "committed"),
        ("SO-2", "confirmed", "erp/inventory", "consumed"),  # partly delivered
        ("SO-3", "completed", "erp/inventory", "consumed"), ("SO-3", "completed", "erp/credit", "released"),
        ("SO-4", "draft", "erp/inventory", "released"), ("SO-4", "draft", "erp/credit", "rejected"),
        ("SO-5", "cancelled", "erp/inventory", "released"),
    ]
    holding = [("erp/inventory", "b1", "committed", ["confirmed"]), ("erp/credit", "b2", "committed", ["confirmed"]),
               ("erp/inventory", "b3", "committed", ["cancelled", "confirmed"])]  # a cancel process carries the IDs too
    assert judge_processes([], []) == []
    assert judge_orders(branches, holding) == []
    assert judge_sums("stock", []) == []
    billing = [("INV-1", "issued", "done", "formalized"), ("i2", "draft", "none", "pending"),
               ("i3", "draft", "none", "confirmed"), ("i4", "void", "none", "released")]
    assert judge_billing(billing, []) == []
    assert judge_runtime({"erp_sales": 0}, {"erp_sales": {"pending": 7, "done": 30}}) == []


def test_an_open_process_or_a_confirming_order_is_named():
    assert judge_processes([("SO-1", "confirm", "committing")], [("SO-2", "confirming")]) == [
        "SO-1: confirm process still committing", "SO-2: order still confirming"]


def test_lost_or_leaked_occupancy_is_named():
    # RS06 gone wrong: confirmed with nothing occupied; RS05 gone wrong: a draft still holding
    got = judge_orders(
        [("SO-1", "confirmed", "erp/inventory", None), ("SO-2", "confirmed", "erp/credit", "expired"),
         ("SO-3", "draft", "erp/credit", "committed"), ("SO-4", "completed", "erp/inventory", "committed")],
        [("erp/inventory", "b9", "held", []), ("erp/credit", "b8", "committed", []),
         ("erp/credit", "b7", "committed", ["draft"])])
    assert got == [
        "SO-1: confirmed but its erp/inventory branch is missing",
        "SO-2: confirmed but its erp/credit branch is expired",
        "SO-3: draft but its erp/credit branch is committed",
        "SO-4: completed but its erp/inventory branch is committed",
        "erp/inventory b9: still held with no process open",
        "erp/credit b8: committed for orders unknown to sales",
        "erp/credit b7: committed for orders ['draft']",
    ]


def test_sums_that_drift_from_their_branches_are_named():
    assert judge_sums("stock", [("p@w", "5.000000", "3.000000")]) == ["stock p@w: 5.000000 recorded, 3.000000 by the branches"]


def test_invoice_and_claim_disagreements_are_named():
    got = judge_billing(
        [("INV-1", "issued", "pending", "confirmed"),   # BL04 gone wrong: issued, never formalized
         ("c2", None, None, "formalized"),              # the source says billed, the invoice is missing
         ("i3", "void", "none", "pending"), ("i4", "confirmed", "pending", "confirmed")],
        [("line-1", "2.000000", "1.000000")])
    assert got == [
        "invoice INV-1: issued, formalization pending, claim confirmed",
        "claim c2: formalized but the invoice is missing",
        "invoice i3: void but its claim is pending",
        "invoice i4: formalization still pending",
        "order line line-1: billed 2.000000, issued invoices bill 1.000000",
    ]


def test_unpublished_events_and_dead_or_waiting_work_are_named():
    got = judge_runtime({"erp_sales": 3, "erp_billing": 0},
                        {"erp_billing": {"dead": 1, "done": 9}, "erp_sales": {"queued": 2, "pending": 7}})
    assert got == ["erp_sales: 3 outbox rows not published", "erp_billing: 1 work items dead",
                   "erp_sales: 2 work items queued"]
