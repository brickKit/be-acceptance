"""Minimal models of document rules; no SDK, broker, or database is exercised."""


def cron_claim_then_crash():
    slots = set()
    slot = ("daily_report", "2026-10-13T00:00:00Z")
    slots.add(slot)
    completed = False  # The process dies after committing the claim.
    reclaim_allowed = slot not in slots
    assert not completed and not reclaim_allowed
    print("CRON: claim committed, work unfinished, insert-only recovery blocked")


def list_and_single_record():
    view_scope_matches = False
    route_scope_matches = True
    list_includes = route_scope_matches  # E10's list predicate.
    can_visible = view_scope_matches  # P6.6 and E10.
    can_allowed = can_visible and route_scope_matches
    assert list_includes != can_visible and not can_allowed
    print("AUTHZ: for K != view_key, List=True, Can.visible=False, Can.allowed=False")


def audit_transport_ack_is_not_archive_receipt():
    outbox_retained_days = 14
    stream_retained_days = 7
    audit_consumer_stored = False
    elapsed_days = 15
    copies = int(elapsed_days < outbox_retained_days)
    copies += int(elapsed_days < stream_retained_days)
    copies += int(audit_consumer_stored)
    assert copies == 0
    print("AUDIT: absent archival consumer plus transport expiry leaves zero audit copies")


def partial_snapshot_and_full_readback():
    local = {"version": 10, "name": "old", "credit_limit": "100.00"}
    full_v12 = {"version": 12, "name": "new", "credit_limit": "50.00"}
    # v11 changes credit_limit; v12 changes name and arrives first.
    local.update(version=12, name="new")
    if local["version"] < full_v12["version"]:
        local.update(full_v12)
    assert local["credit_limit"] != full_v12["credit_limit"]
    print("SNAPSHOT: partial v12 blocks full v12 repair; credit_limit remains at v10")


if __name__ == "__main__":
    cron_claim_then_crash()
    list_and_single_record()
    audit_transport_ack_is_not_archive_receipt()
    partial_snapshot_and_full_readback()
