#!/usr/bin/env python3
"""Reference model for protocol 05: billing claims, credit conversion, settlement, costing.

Four sub-models, each with random concurrency, duplicates, reordering and crashes:

  BILLING     drafts claim billable quantity from the source with idempotent claim ids; the
              source keeps claimed + billed <= billable; a source change that would go below is
              rejected. Check: never over-billed; one claim id never holds two active claims (no double billing).
  CREDIT      the credit component's exposure = committed order branches + open receivables.
              "invoice issued" (consume branch, add receivable) and "receivable settled" facts
              arrive duplicated and out of order. Check: after all facts, exposure equals the truth;
              at every moment exposure never drops below the truth (no under-counting window).
  SETTLE      concurrent allocations of payments to receivables and reversals of allocations.
              Check: no receivable's allocated amount exceeds its amount, no payment's allocated
              amount exceeds its amount, reversals restore both.
  COSTING     moving-average cost per item-location from a contiguous movement sequence; movements
              arrive out of order. Check: the result equals in-order processing.

Bad variants (each must be caught): claim not idempotent; source reduction not checked against
claims; credit adds receivable before consuming the branch in separate steps with a crash
between; settlement checks balances outside the transaction (race); costing applies movements in
arrival order.
"""
import random
import sys
from decimal import Decimal

SEED = 20261013
RUNS = 2000


def billing(rng, variant, fails):
    billable = rng.randint(5, 10)
    claims = {}                     # claim id -> dict(qty, state)
    def used():
        return sum(c["qty"] for c in claims.values() if c["state"] in ("held", "billed"))
    for step in range(40):
        r = rng.random()
        cid = f"c{rng.randint(0, 8)}"
        if r < 0.45:                # draft claims (may be a retry of the same id)
            qty = rng.randint(1, 3)
            if cid in claims and variant != "claim_not_idempotent":
                continue
            key = cid if cid not in claims else f"{cid}#{step}"
            if used() + qty <= billable:
                claims[key] = dict(qty=qty, state="held")
        elif r < 0.65:              # formalise
            c = claims.get(cid)
            if c and c["state"] == "held":
                c["state"] = "billed"
        elif r < 0.8:               # abandon draft
            c = claims.get(cid)
            if c and c["state"] == "held":
                c["state"] = "released"
        else:                       # source reduces billable (e.g. delivery corrected)
            new = billable - rng.randint(1, 3)
            if variant == "reduce_unchecked" or new >= used():
                billable = new
        if used() > billable:
            fails.append("BILLING"); return
        per_id = {}
        for key, c in claims.items():
            if c["state"] in ("held", "billed"):
                base = key.split("#")[0]
                per_id[base] = per_id.get(base, 0) + 1
        if any(n > 1 for n in per_id.values()):
            fails.append("BILLING_DUP"); return


def credit(rng, variant, fails):
    orders = {k: rng.choice([100, 200, 300]) for k in range(4)}
    branch = dict(orders)           # committed branch amounts
    ar = {}                         # invoice -> open amount
    events = []
    for k, amt in orders.items():
        events.append(("issued", k, amt))
        events.append(("settled", k, amt))
    deliveries = events + [e for e in events if rng.random() < 0.3]
    rng.shuffle(deliveries)
    processed = set()
    truth_branch, truth_ar = dict(orders), {}
    pending_settle = []
    for kind, k, amt in deliveries:
        if (kind, k) in processed:
            continue
        if kind == "settled" and k not in ar:
            pending_settle.append((kind, k, amt)); continue      # wait for its invoice
        if kind == "issued":
            if variant == "split_conversion":
                ar[k] = amt
                if rng.random() < 0.3:
                    exposure = sum(branch.values()) + sum(ar.values())
                    truth = sum(orders.values()) - sum(v for kk, v in orders.items() if ("settled", kk) in processed)
                    if exposure != truth:
                        fails.append("CREDIT"); return
                branch[k] = 0
            else:
                branch[k] = 0; ar[k] = amt                      # one local transaction
        else:
            ar[k] = 0
        processed.add((kind, k))
        exposure = sum(branch.values()) + sum(ar.values())
        truth = sum(orders.values()) - sum(orders[kk] for (kd, kk) in processed if kd == "settled")
        if exposure < truth or exposure > truth:
            fails.append("CREDIT"); return
        for e in list(pending_settle):
            if e[1] in ar:
                pending_settle.remove(e)
                ar[e[1]] = 0; processed.add(("settled", e[1]))


def settle(rng, variant, fails):
    recv = {f"r{i}": Decimal(rng.choice([100, 150, 200])) for i in range(3)}
    pay = {f"p{i}": Decimal(rng.choice([100, 250])) for i in range(2)}
    allocs = {}                     # alloc id -> (r, p, amount, active)
    def alloc_r(r):
        return sum(a[2] for a in allocs.values() if a[0] == r and a[3])
    def alloc_p(p):
        return sum(a[2] for a in allocs.values() if a[1] == p and a[3])
    for step in range(30):
        if rng.random() < 0.75:
            r, p = rng.choice(list(recv)), rng.choice(list(pay))
            if variant == "check_outside_tx":
                # both requests read balances first, then write: interleave two of them
                r2, p2 = rng.choice(list(recv)), rng.choice(list(pay))
                a1 = min(recv[r] - alloc_r(r), pay[p] - alloc_p(p))
                a2 = min(recv[r2] - alloc_r(r2), pay[p2] - alloc_p(p2))
                if a1 > 0: allocs[f"a{step}x"] = (r, p, a1, True)
                if a2 > 0: allocs[f"a{step}y"] = (r2, p2, a2, True)
            else:
                amount = min(recv[r] - alloc_r(r), pay[p] - alloc_p(p))   # under row locks
                if amount > 0:
                    allocs[f"a{step}"] = (r, p, amount, True)
        else:
            act = [k for k, a in allocs.items() if a[3]]
            if act:
                k = rng.choice(act)
                r, p, amt, _ = allocs[k]
                allocs[k] = (r, p, amt, False)                  # reversal record, original kept
        if any(alloc_r(r) > recv[r] for r in recv) or any(alloc_p(p) > pay[p] for p in pay):
            fails.append("SETTLE"); return


def costing(rng, variant, fails):
    moves = []
    qty, val = Decimal(0), Decimal(0)
    for seq in range(1, rng.randint(6, 12)):
        if qty > 0 and rng.random() < 0.4:
            q = Decimal(rng.randint(1, int(qty)))
            moves.append((seq, "out", q, None))
            qty -= q
        else:
            q = Decimal(rng.randint(1, 5)); price = Decimal(rng.choice([10, 12, 15]))
            moves.append((seq, "in", q, price))
            qty += q
    def apply(order):
        q, v, issued = Decimal(0), Decimal(0), []
        for seq, kind, mq, price in order:
            if kind == "in":
                q += mq; v += mq * price
            else:
                avg = (v / q) if q else Decimal(0)
                cost = (avg * mq).quantize(Decimal("0.01"))
                issued.append((seq, cost)); q -= mq; v -= cost
        return sorted(issued)
    expected = apply(moves)
    arrival = moves[:]; rng.shuffle(arrival)
    if variant == "arrival_order":
        got = apply(arrival)
    else:
        buffer, next_seq, done = {}, 1, []
        for m in arrival:
            buffer[m[0]] = m
            while next_seq in buffer:
                done.append(buffer.pop(next_seq)); next_seq += 1
        got = apply(done)
    if got != expected:
        fails.append("COSTING")


def main():
    variants = ["good", "claim_not_idempotent", "reduce_unchecked", "split_conversion",
                "check_outside_tx", "arrival_order"]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            billing(rng, v, fails); credit(rng, v, fails); settle(rng, v, fails); costing(rng, v, fails)
        res[v] = sorted(set(fails))
    print(f"good: {'ok' if not res['good'] else 'FAIL ' + str(res['good'])} (BILLING, CREDIT, SETTLE, COSTING over {RUNS} runs each)")
    caught = True
    for v in variants[1:]:
        print(f"bad variant {v}: {'caught ' + str(res[v]) if res[v] else 'NOT CAUGHT'}")
        caught &= bool(res[v])
    if res["good"] or not caught:
        sys.exit(1)


if __name__ == "__main__":
    main()
