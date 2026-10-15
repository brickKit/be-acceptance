#!/usr/bin/env python3
"""Reference model for protocol 16: opening data import and go-live cutover.

An import batch loads opening receivables (open invoices) into billing/finance, opening
general-ledger balances, and open sales orders (some partly invoiced in the legacy system).
Chunks commit independently; the job crashes at random and is resumed or rerun; activation
makes the batch effective. Checks:

  ONCE        every source row is loaded exactly once, however often the job is rerun
  HIDDEN      no row of a batch is visible to business before the batch is activated
  CONTROL     a batch is activated only when the receivable control account equals the sum
              of the open invoices
  NO_REBILL   an open order never makes billable an amount the legacy system already invoiced

Bad variants (each must be caught): no source-ID registry; rows effective as soon as their
chunk commits; activation without the control reconciliation; open orders imported with their
full amount instead of the remaining amount.
"""
import random
import sys

SEED = 20261013
RUNS = 3000
CHUNK = 3


def run(rng, variant, fails):
    invoices = [(f"inv{i}", rng.randint(1, 9)) for i in range(rng.randint(3, 8))]
    orders = []
    for i in range(rng.randint(1, 4)):
        total = rng.randint(5, 20)
        invoiced = rng.randint(0, total)
        orders.append((f"so{i}", total, invoiced))
    gl_control = sum(a for _, a in invoices) + (rng.choice([0, 0, 0, 1]))   # sometimes legacy GL disagrees
    registry = set()
    loaded = []               # (source_id, amount, batch_active_flag_at_load)
    billable = {}
    active = False
    rows = [("inv", sid, amt) for sid, amt in invoices] + [("so", sid, tot, inv) for sid, tot, inv in orders]

    attempts = 0
    while attempts < 6 and not active:
        attempts += 1
        pos = 0
        while pos < len(rows):
            chunk = rows[pos:pos + CHUNK]
            if rng.random() < 0.2:
                break                                          # crash before this chunk commits
            for row in chunk:
                sid = row[1]
                if variant != "no_registry" and sid in registry:
                    continue
                registry.add(sid)
                if row[0] == "inv":
                    loaded.append((sid, row[2]))
                else:
                    _, _, tot, inv = row
                    billable[sid] = billable.get(sid, 0) + (tot if variant == "full_amount" else tot - inv)
            pos += CHUNK
            # business looks at receivables between chunks
            visible = loaded if variant == "visible_on_commit" else (loaded if active else [])
            if not active and visible:
                fails.append("HIDDEN"); return
        if pos >= len(rows):
            ar = sum(a for _, a in loaded)
            if variant == "no_control" or ar == gl_control:
                active = True
                if ar != gl_control:
                    fails.append("CONTROL"); return
            else:
                return                                         # stays loading; differences reported
    ids = [sid for sid, _ in loaded]
    if len(ids) != len(set(ids)):
        fails.append("ONCE"); return
    for sid, tot, inv in orders:
        if billable.get(sid, 0) > tot - inv:
            fails.append("NO_REBILL"); return


def main():
    variants = ["good", "no_registry", "visible_on_commit", "no_control", "full_amount"]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            run(rng, v, fails)
        res[v] = sorted(set(fails))
    print(f"good: {'ok' if not res['good'] else 'FAIL ' + str(res['good'])} (ONCE, HIDDEN, CONTROL, NO_REBILL over {RUNS} runs)")
    caught = True
    for v in variants[1:]:
        print(f"bad variant {v}: {'caught ' + str(res[v]) if res[v] else 'NOT CAUGHT'}")
        caught &= bool(res[v])
    if res["good"] or not caught:
        sys.exit(1)


if __name__ == "__main__":
    main()
