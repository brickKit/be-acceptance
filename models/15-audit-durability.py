#!/usr/bin/env python3
"""Reference model for protocol 03: durable audit records.

Components write required audit records to a local audit table (writes in the business
transaction; sensitive reads committed before data is released). A transfer job sends them to
the audit store, idempotent by record id; the store returns a receipt. The store may be down,
may be restored from its last backup, and the transfer job may crash. Checks:

  NO_LOSS     every committed required record exists, at every moment, in the local table or in
              the audit store; after the store is up and the transfer drains, it is in the store
  NO_DUP      the store holds each record once, however often the transfer retried
  READ_FIRST  no sensitive read releases data unless its audit record has committed

Bad variants (each must be caught): delete the local copy on broker ack (the current design's
transport expiry); delete on store receipt without waiting for the store's backup horizon;
non-idempotent insert in the store; release read data before the audit record commits.
"""
import random
import sys

SEED = 20261013
RUNS = 3000


def run(rng, variant, fails):
    local = {}          # rid -> dict(receipt_at_store_seq or None)
    store = {}          # rid -> count
    store_seq = 0       # monotonically increasing store write sequence
    store_log = []      # rids in write order (for restore)
    backup_seq = 0      # store state up to this sequence is in a durable backup
    store_up = True
    committed = set()   # rids whose business transaction / read committed
    released_reads = []
    next_id = 0

    def store_insert(rid):
        nonlocal store_seq
        if variant == "non_idempotent" or rid not in store:
            store[rid] = store.get(rid, 0) + 1
            store_seq += 1
            store_log.append((store_seq, rid))
        return store_seq

    for step in range(80):
        r = rng.random()
        if r < 0.35:            # business write with audit in the same transaction
            rid = next_id; next_id += 1
            local[rid] = {"receipt": None, "acked": False}
            committed.add(rid)
        elif r < 0.45:          # sensitive read
            rid = next_id; next_id += 1
            if variant == "release_before_audit":
                released_reads.append(rid)
                if rng.random() < 0.3:
                    continue    # crash before the audit record commits
                local[rid] = {"receipt": None, "acked": False}
                committed.add(rid)
            else:
                local[rid] = {"receipt": None, "acked": False}
                committed.add(rid)
                if rng.random() < 0.3:
                    continue    # crash after commit, before release: no data left the system
                released_reads.append(rid)
        elif r < 0.55:
            store_up = not store_up if rng.random() < 0.5 else store_up
        elif r < 0.6:           # store restored from its last backup
            keep = [(s, rid) for (s, rid) in store_log if s <= backup_seq]
            store.clear(); store_log[:] = keep
            for _, rid in keep:
                store[rid] = store.get(rid, 0) + 1
            store_seq = keep[-1][0] if keep else 0
            # receipts above the backup point are no longer valid
            for rec in local.values():
                if rec["receipt"] is not None and rec["receipt"] > backup_seq:
                    rec["receipt"] = None
        elif r < 0.68:          # store takes a backup
            backup_seq = store_seq
        elif r < 0.85:          # transfer job pass (may crash midway)
            for rid in sorted(local):
                rec = local[rid]
                rec["acked"] = True          # the broker accepted the transport message
                if not store_up:
                    break
                if rec["receipt"] is None or variant == "non_idempotent":
                    seq = store_insert(rid)
                    if rng.random() < 0.2:
                        break                # crash after the store wrote, before the receipt landed
                    rec["receipt"] = seq
        else:                   # cleanup of local copies
            for rid in list(local):
                rec = local[rid]
                if variant == "delete_on_ack":
                    ok = rec["acked"]
                elif variant == "delete_on_receipt":
                    ok = rec["receipt"] is not None
                else:
                    ok = rec["receipt"] is not None and rec["receipt"] <= backup_seq
                if ok:
                    del local[rid]
        # NO_LOSS at every step
        for rid in committed:
            if rid not in local and rid not in store:
                fails.append("NO_LOSS"); return
        if any(c > 1 for c in store.values()):
            fails.append("NO_DUP"); return
        for rid in released_reads:
            if rid not in committed:
                fails.append("READ_FIRST"); return
    # drain with the store up
    store_up = True
    for rid in sorted(local):
        if local[rid]["receipt"] is None:
            local[rid]["receipt"] = store_insert(rid)
    if any(rid not in store for rid in committed):
        fails.append("NO_LOSS")
    if any(c > 1 for c in store.values()):
        fails.append("NO_DUP")


def main():
    variants = ["good", "delete_on_ack", "delete_on_receipt", "non_idempotent", "release_before_audit"]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            run(rng, v, fails)
        res[v] = sorted(set(fails))
    print(f"good: {'ok' if not res['good'] else 'FAIL ' + str(res['good'])} (NO_LOSS, NO_DUP, READ_FIRST over {RUNS} runs "
          f"with store outages, restores from backup and transfer crashes)")
    caught = True
    for v in variants[1:]:
        print(f"bad variant {v}: {'caught ' + str(res[v]) if res[v] else 'NOT CAUGHT'}")
        caught &= bool(res[v])
    if res["good"] or not caught:
        sys.exit(1)


if __name__ == "__main__":
    main()
