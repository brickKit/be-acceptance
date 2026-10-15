"""Explore candidate time rules with atomic abstract events, not product code.

No SDK, database, broker, credential, or wall clock is exercised. Atomicity,
complete participant registration, and authoritative evidence are assumptions;
the models do not prove those assumptions hold in an implementation.
"""

from itertools import permutations


def check_then_commit_counterexample():
    check_allowed = True
    grant_currently_active = False  # Revocation follows a successful Check.
    naive_commit = check_allowed
    assert naive_commit and not grant_currently_active
    print("CHECK: cached allow permits a commit after revocation; no barrier")


def acceptance_orderings():
    count = 0
    commits = 0
    for order in permutations(("accept", "revoke", "activate")):
        grant_active = True
        accepted = False
        committed = False
        for event in order:
            if event == "accept":
                accepted = grant_active
            elif event == "revoke":
                grant_active = False
            elif event == "activate":
                committed = accepted  # Same intent, valid deadline, no freeze.
        expected = order.index("accept") < order.index("revoke")
        expected &= order.index("accept") < order.index("activate")
        assert committed == expected, order
        count += 1
        commits += committed
    assert count == 6 and commits == 2
    print("ACCEPT: 6 orders checked; exactly 2 can activate the earlier accept")


def commit_barrier_orderings():
    count = 0
    for order in permutations(("close_admission", "install", "effective", "commit")):
        if not (order.index("close_admission") < order.index("install")
                < order.index("effective")):
            continue
        blocked = False
        effective = False
        for event in order:
            if event == "install":
                blocked = True
            elif event == "effective":
                assert blocked
                effective = True
            elif event == "commit":
                can_commit = not blocked  # One atomic local guard decision.
                assert not (effective and can_commit), order
        count += 1
    assert count == 4
    participants = {"sales", "inventory"}
    acknowledged = {"sales"}
    assert not participants.issubset(acknowledged)
    print("BARRIER: 4 orders checked; a missing participant prevents completion")


def evidence_expiry_and_content_floor():
    verified_at = 100
    window = 30
    cutoff = verified_at + window
    cache_received_at = 120
    forged_extension = cache_received_at + window
    assert forged_extension > cutoff
    assert 131 >= cutoff  # Re-fetching the old evidence cannot authorize this.
    max_clock_error = 1
    assert 128 + max_clock_error < cutoff
    assert not 129 + max_clock_error < cutoff
    old_evidence_revision = 7
    new_content_floor = 8
    assert old_evidence_revision < new_content_floor  # Even within its TTL.
    print("READ: old evidence cannot extend expiry or authorize newer content")


def activation_and_restore():
    activation_deadline = min(130, 125, 140)  # Accept, qualification, intent.
    assert activation_deadline == 125
    assert not 125 < activation_deadline
    permit_generation = "before-restore"
    current_generation = "verified-recovery"
    assert permit_generation != current_generation
    print("RESTORE: minimum activation deadline and new generation reject old proof")


if __name__ == "__main__":
    check_then_commit_counterexample()
    acceptance_orderings()
    commit_barrier_orderings()
    evidence_expiry_and_content_floor()
    activation_and_restore()
