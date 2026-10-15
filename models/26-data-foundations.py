#!/usr/bin/env python3
"""Reference model for protocol 14: numbering, cursors and allocation.

Three parts, each with its own bad variants:

  NUMBERING   documents of one legal entity and period are posted concurrently; some
              transactions roll back. A gap-free series must end with numbers 1..n, each used
              once, all by committed documents.
  CURSOR      a list is paged while rows are inserted, updated (sort key changes) and deleted.
              Every row that existed and kept its sort key for the whole listing appears
              exactly once.
  ALLOCATION  a total is allocated across lines by weight and rounded to the currency's minor
              unit; the parts must add up to the total exactly.

Bad variants (each must be caught): numbers from a sequence taken before commit (gaps on
rollback); numbers taken when the draft is created (gaps when drafts are deleted); offset
pagination; keyset pagination without the id tie-breaker; rounding each line independently.
"""
import random
import sys
from decimal import Decimal, ROUND_HALF_UP

SEED = 20261013
RUNS = 3000


def numbering(rng, variant):
    counter = 0                 # the series row (gap-free: updated inside the posting transaction)
    seq = 0                     # a database sequence (not rolled back)
    committed = []
    drafts = []
    for _ in range(30):
        r = rng.random()
        if r < 0.3:
            if variant == "number_at_draft":
                seq += 1; drafts.append(seq)
            else:
                drafts.append(None)
        elif r < 0.8 and drafts:
            d = drafts.pop(rng.randrange(len(drafts)))
            rollback = rng.random() < 0.2
            if variant == "sequence":
                seq += 1; n = seq                     # taken outside the transaction's fate
                if not rollback:
                    committed.append(n)
            elif variant == "number_at_draft":
                if not rollback:
                    committed.append(d)
                else:
                    drafts.append(d)
            else:
                if not rollback:                       # lock series row, increment, commit together
                    counter += 1; committed.append(counter)
        elif drafts:
            drafts.pop(rng.randrange(len(drafts)))     # draft deleted
    return sorted(committed) == list(range(1, len(committed) + 1))


def cursor(rng, variant):
    rows = {i: rng.randint(0, 5) for i in range(20)}  # id -> sort key (duplicates common)
    stable = set(rows)
    seen = []
    page = 4
    pos = None                                        # (key, id) or offset
    offset = 0
    next_id = 100
    while True:
        # concurrent changes between pages
        for _ in range(rng.randint(0, 2)):
            c = rng.random()
            if c < 0.4:
                rows[next_id] = rng.randint(0, 5); next_id += 1
            elif c < 0.7 and rows:
                k = rng.choice(list(rows)); rows[k] = rng.randint(0, 5); stable.discard(k)
            elif rows:
                k = rng.choice(list(rows)); del rows[k]; stable.discard(k)
        order = sorted(rows.items(), key=lambda kv: (kv[1], kv[0]))
        if variant == "offset":
            chunk = order[offset:offset + page]; offset += page
        elif variant == "no_tiebreak":
            chunk = [kv for kv in order if pos is None or kv[1] > pos[0]][:page]
        else:
            chunk = [kv for kv in order if pos is None or (kv[1], kv[0]) > pos][:page]
        if not chunk:
            break
        seen += [k for k, _ in chunk]
        pos = (chunk[-1][1], chunk[-1][0])
    return all(seen.count(k) == 1 for k in stable)


def allocation(rng, variant):
    total = Decimal(rng.randint(1, 100000)) / 100
    weights = [rng.randint(1, 9) for _ in range(rng.randint(2, 7))]
    s = sum(weights)
    q = Decimal("0.01")
    if variant == "round_each":
        parts = [(total * w / s).quantize(q, rounding=ROUND_HALF_UP) for w in weights]
    else:                                             # largest remainder
        raw = [total * w / s for w in weights]
        floors = [x.quantize(q, rounding="ROUND_FLOOR") for x in raw]
        rest = int(((total - sum(floors)) / q).to_integral_value())
        order = sorted(range(len(raw)), key=lambda i: (raw[i] - floors[i], -i), reverse=True)
        parts = floors[:]
        for i in order[:rest]:
            parts[i] += q
    return sum(parts) == total


def main():
    cases = [
        ("NUMBERING", numbering, ["good", "sequence", "number_at_draft"]),
        ("CURSOR", cursor, ["good", "offset", "no_tiebreak"]),
        ("ALLOCATION", allocation, ["good", "round_each"]),
    ]
    ok = True
    for name, fn, variants in cases:
        for v in variants:
            rng = random.Random(SEED)
            bad_runs = sum(0 if fn(rng, v) else 1 for _ in range(RUNS))
            if v == "good":
                print(f"{name} good: {'ok' if bad_runs == 0 else f'FAIL in {bad_runs} runs'} over {RUNS} runs")
                ok &= bad_runs == 0
            else:
                print(f"{name} bad variant {v}: {'caught in ' + str(bad_runs) + ' runs' if bad_runs else 'NOT CAUGHT'}")
                ok &= bad_runs > 0
    if not ok:
        sys.exit(1)


if __name__ == "__main__":
    main()
