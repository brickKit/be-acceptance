#!/usr/bin/env python3
"""Reference model for protocol 06: closing a period across components.

Participants (inventory, billing) emit facts with a business date and a per-participant sequence;
costing turns inventory facts into cost facts; finance posts everything. A closing batch for
period P asks each participant to seal P and report its coverage (the last sequence of P-dated
facts), waits until finance has processed every participant up to its coverage and costing has
processed inventory up to inventory's coverage, then locks P. Facts are delayed, the batch crashes
and resumes, late facts keep arriving. Checks:

  COMPLETE    when P is locked, finance has posted every fact a participant accepted into P
  NO_LEAK     after a participant sealed P, no new fact is accepted into P (late facts go to P+1)
  NO_PARTIAL  a cancelled batch leaves every participant unsealed, and P is not locked
  COSTED      when P is locked, every P-dated inventory fact has its cost fact posted in P

Bad variants (each must be caught): lock without waiting for coverage; seal and coverage report in
separate steps (a fact slips in between); ignore the costing dependency; post late facts into the
closed period.
"""
import random
import sys

SEED = 20261013
RUNS = 3000


def run(rng, variant, fails):
    P = 1
    parts = {"inv": dict(seq=0, sealed=False, accepted={}), "bill": dict(seq=0, sealed=False, accepted={})}
    finance = {"posted": set(), "cost_posted": set(), "locked": False}
    costing = {"done": set()}
    transport = []          # (participant, seq, period) waiting for finance / costing
    cost_transport = []     # inventory seqs whose cost fact has not reached finance
    coverage = {}
    batch = "none"
    for step in range(80):
        r = rng.random()
        if r < 0.45:        # a business fact arrives at a participant, dated P (or P+1)
            name = rng.choice(list(parts))
            p = parts[name]
            period = P if rng.random() < 0.7 else P + 1
            if period == P and p["sealed"]:
                period = P + 1 if variant != "late_into_closed" else P
            p["seq"] += 1
            p["accepted"][p["seq"]] = period
            transport.append((name, p["seq"], period))
            if variant == "late_into_closed" and finance["locked"] and period == P:
                fails.append("NO_LEAK")
        elif r < 0.7 and transport:   # deliver some facts (finance and costing)
            for _ in range(rng.randint(1, 3)):
                if not transport:
                    break
                item = transport.pop(rng.randrange(len(transport)))
                name, seq, period = item
                if name == "inv":
                    costing["done"].add(seq)
                    cost_transport.append(seq)          # costing emits a cost fact later
                finance["posted"].add((name, seq))
            for _ in range(rng.randint(0, 2)):
                if cost_transport:
                    finance["cost_posted"].add(cost_transport.pop(rng.randrange(len(cost_transport))))
        elif r < 0.8 and batch in ("none", "cancelled"):
            batch = "sealing"
        elif r < 0.85 and batch in ("sealing", "waiting"):
            batch = "cancelled"
            for p in parts.values():
                p["sealed"] = False
            coverage.clear()
        elif batch == "sealing":
            for name, p in parts.items():
                if p["sealed"]:
                    continue
                if variant == "seal_split":
                    cov = max([s for s, per in p["accepted"].items() if per == P], default=0)
                    coverage[name] = cov
                    # a fact slips in before the seal lands
                    if rng.random() < 0.5:
                        p["seq"] += 1; p["accepted"][p["seq"]] = P; transport.append((name, p["seq"], P))
                    p["sealed"] = True
                else:
                    p["sealed"] = True
                    coverage[name] = max([s for s, per in p["accepted"].items() if per == P], default=0)
            batch = "waiting"
        elif batch == "waiting":
            ready = True
            if variant != "no_wait":
                for name, cov in coverage.items():
                    need = {s for s, per in parts[name]["accepted"].items() if per == P and s <= cov}
                    if not need <= {s for (n, s) in finance["posted"] if n == name}:
                        ready = False
                if variant != "ignore_costing":
                    need_cost = {s for s, per in parts["inv"]["accepted"].items() if per == P and s <= coverage.get("inv", 0)}
                    if not need_cost <= finance["cost_posted"]:
                        ready = False
            if ready and rng.random() < 0.7:
                finance["locked"] = True
                batch = "closed"
                for name, p in parts.items():
                    acc = {s for s, per in p["accepted"].items() if per == P}
                    if not acc <= {s for (n, s) in finance["posted"] if n == name}:
                        fails.append("COMPLETE"); return
                inv_p = {s for s, per in parts["inv"]["accepted"].items() if per == P}
                if not inv_p <= finance["cost_posted"]:
                    fails.append("COSTED"); return
        if batch == "cancelled":
            if any(p["sealed"] for p in parts.values()) or finance["locked"]:
                fails.append("NO_PARTIAL"); return
        if batch == "closed" and variant != "late_into_closed":
            for p in parts.values():
                if not p["sealed"]:
                    fails.append("NO_LEAK"); return


def main():
    variants = ["good", "no_wait", "seal_split", "ignore_costing", "late_into_closed"]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            run(rng, v, fails)
        res[v] = sorted(set(fails))
    print(f"good: {'ok' if not res['good'] else 'FAIL ' + str(res['good'])} (COMPLETE, NO_LEAK, NO_PARTIAL, COSTED over {RUNS} runs)")
    caught = True
    for v in variants[1:]:
        print(f"bad variant {v}: {'caught ' + str(res[v]) if res[v] else 'NOT CAUGHT'}")
        caught &= bool(res[v])
    if res["good"] or not caught:
        sys.exit(1)


if __name__ == "__main__":
    main()
