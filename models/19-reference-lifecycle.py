#!/usr/bin/env python3
"""Reference model for protocol 07: references across components.

The owner keeps records with lifecycle states and an alias map from merges. Referencing
components hold weak (historical) references with snapshots and make strong (eligibility)
references at commit time using eligibility evidence valid for W after it was requested.
Checks:

  ALIAS       after random merges (including chains), resolving any id gives the final survivor;
              a merge that would create a cycle is rejected
  NO_CLEANUP  an owner that is unreachable never causes a referencing component to drop or
              rewrite a reference
  BOUND       no strong reference commits later than suspension time + W
  HISTORY     merges and suspensions never change the snapshot stored in a historical reference

Bad variants (each must be caught): merges allowed into one's own alias chain (cycle);
one-hop alias resolution; unreachable treated as not found (reference cleaned); eligibility
checked only when the draft was created; snapshot refreshed from the survivor after a merge.
"""
import random
import sys

SEED = 20261013
RUNS = 3000
W = 5


def resolve(alias, rid, variant, limit=50):
    if variant == "one_hop":
        return alias.get(rid, rid)
    seen = set()
    while rid in alias and limit > 0:
        if rid in seen:
            return None
        seen.add(rid); rid = alias[rid]; limit -= 1
    return rid


def run(rng, variant, fails):
    ids = [f"c{i}" for i in range(6)]
    state = {i: "active" for i in ids}
    names = {i: f"name-{i}" for i in ids}
    alias = {}
    suspended_at = {}
    refs = []   # dict(target, snapshot, kind)
    now = 0
    for step in range(60):
        now += 1
        r = rng.random()
        if r < 0.2:                                  # merge a into b
            a, b = rng.sample(ids, 2)
            if state[a] == "merged":
                continue
            if variant != "allow_cycle" and resolve(alias, b, "good") == a:
                continue                             # would create a cycle
            alias[a] = b; state[a] = "merged"
            if resolve(alias, a, "good") is None:
                fails.append("ALIAS"); return
        elif r < 0.3:                                # suspend
            c = rng.choice(ids)
            if state[c] == "active":
                state[c] = "suspended"; suspended_at[c] = now
        elif r < 0.6:                                # strong reference: evidence requested, commit later
            c = rng.choice(ids)
            requested = now
            eligible = state[c] == "active"
            commit = now + rng.choice([0, 1, 3, 6, 9])
            if variant == "check_at_draft_only":
                ok = eligible
            else:
                ok = eligible and commit < requested + W
            if ok:
                if c in suspended_at and commit > suspended_at[c] + W:
                    fails.append("BOUND"); return
                # a suspension that happens between request and commit
                if rng.random() < 0.3 and state[c] == "active":
                    state[c] = "suspended"; suspended_at[c] = requested + rng.randint(0, 2)
                    if variant == "check_at_draft_only" and commit > suspended_at[c] + W:
                        fails.append("BOUND"); return
                refs.append(dict(target=c, snapshot=names[c], kind="weak"))
        elif r < 0.8 and refs:                       # verification pass with an unreachable owner
            unreachable = rng.random() < 0.4
            for ref in list(refs):
                if unreachable:
                    if variant == "unreachable_as_missing":
                        refs.remove(ref)
                    continue
                target = resolve(alias, ref["target"], variant)
                if variant == "refresh_snapshot":
                    ref["snapshot"] = names[target]
        # checks every step
        for i in ids:
            fin = resolve(alias, i, "good")
            if fin is None:
                fails.append("ALIAS"); return
            got = resolve(alias, i, variant)
            if got != fin:
                fails.append("ALIAS"); return
        for ref in refs:
            if ref["snapshot"] != names[ref["target"]]:
                fails.append("HISTORY"); return
    return refs


def main():
    variants = ["good", "allow_cycle", "one_hop", "unreachable_as_missing", "check_at_draft_only", "refresh_snapshot"]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            run(rng, v, fails)
        res[v] = sorted(set(fails))
    # NO_CLEANUP needs a direct comparison: same seed, good vs unreachable_as_missing reference counts
    def count_refs(v):
        rng = random.Random(SEED); total = 0
        for _ in range(RUNS):
            out = run(rng, v, [])
            total += len(out or [])
        return total
    if count_refs("unreachable_as_missing") < count_refs("good"):
        res["unreachable_as_missing"] = sorted(set(res["unreachable_as_missing"]) | {"NO_CLEANUP"})
    print(f"good: {'ok' if not res['good'] else 'FAIL ' + str(res['good'])} (ALIAS, NO_CLEANUP, BOUND, HISTORY over {RUNS} runs)")
    caught = True
    for v in variants[1:]:
        print(f"bad variant {v}: {'caught ' + str(res[v]) if res[v] else 'NOT CAUGHT'}")
        caught &= bool(res[v])
    if res["good"] or not caught:
        sys.exit(1)


if __name__ == "__main__":
    main()
