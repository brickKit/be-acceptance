#!/usr/bin/env python3
"""Reference model for protocol 11: moving archive units between tiers.

One archive unit (for example a month of closed orders) lives in the online (hot) store. An
archiver seals it, copies it to the cold store, verifies, publishes the cold route in the unit
catalog, waits for readers on the old route and then deletes the hot rows. Later the unit may
be destroyed when its retention ends. Concurrently: late writes to the unit, a worker whose
lease expires while a successor takes over, long readers, list queries across both tiers,
and legal holds being placed and released. Checks:

  NO_LOST_WRITE  every write accepted into the unit is in the published copy; writes after
                 the seal become correction records
  READ_COMPLETE  every read of a sealed unit returns exactly its rows: no gap, no duplicate
  FENCED         a worker whose lease has expired never changes the catalog
  HOLD_WINS      a unit under legal hold is never destroyed

Bad variants (each must be caught): only the archiver checks the seal (writers do not); no
generation check when publishing; hot rows deleted without waiting for readers; list query
reads both tiers without a catalog snapshot; destroy uses a hold check made earlier, outside
the deciding transaction.
"""
import random
import sys

SEED = 20261013
RUNS = 3000


def run(rng, variant, fails):
    hot = [f"row{i}" for i in range(4)]
    cold = None
    accepted = set(hot)                 # writes accepted into the unit
    corrections = []
    sealed_rows = None
    cat = dict(state="open", route="hot", gen=0, held=False, hot_readers=0, destroyed=False)
    workers = []                        # dict(gen, todo, copy, phase)
    readers = []                        # dict(route, listing)
    destroy_decision = None

    def publish_allowed(w):
        return variant == "no_fencing" or w["gen"] == cat["gen"]

    for step in range(120):
        actions = ["write", "hold"]
        if cat["state"] == "open":
            actions.append("seal")
        if cat["state"] == "sealed" and not any(w["gen"] == cat["gen"] for w in workers):
            actions.append("claim")
        if workers:
            actions += ["work", "work", "work", "expire"]
        if cat["state"] in ("sealed", "published") and not cat["destroyed"]:
            actions.append("read_start")
        if readers:
            actions += ["read_end", "read_end"]
        if cat["state"] == "published" and hot:
            actions.append("delete_hot")
        if cat["state"] == "published" and not hot and not cat["destroyed"]:
            actions.append("destroy")
        a = rng.choice(actions)

        if a == "write" and not cat["destroyed"]:
            row = f"late{step}"
            if cat["state"] == "open" or (variant == "archiver_only_seal" and cat["route"] == "hot"):
                hot.append(row); accepted.add(row)
            else:
                corrections.append(row)                 # redirected: a correction record
        elif a == "seal":
            cat["state"] = "sealed"; sealed_rows = sorted(hot)
        elif a == "claim":
            cat["gen"] += 1
            workers.append(dict(gen=cat["gen"], todo=list(hot), copy=[], phase="copying"))
        elif a == "expire":
            w = rng.choice(workers)
            if w["gen"] == cat["gen"] and cat["state"] == "sealed":
                cat["gen"] += 1                          # lease lost; the old worker may still run
        elif a == "work":
            w = rng.choice(workers)
            if w["phase"] == "copying":
                if w["todo"]:
                    w["copy"].append(w["todo"].pop(0))
                else:
                    # verify against the source as it is now
                    w["phase"] = "verified" if sorted(w["copy"]) == sorted(hot) else "copying"
                    if w["phase"] == "copying":
                        w["todo"] = [x for x in hot if x not in w["copy"]]
            elif w["phase"] == "verified":
                workers.remove(w)
                if not publish_allowed(w) or cat["state"] != "sealed":
                    continue
                if w["gen"] != cat["gen"]:
                    fails.append("FENCED"); return
                cold = list(w["copy"]); cat["route"] = "cold"; cat["state"] = "published"
                workers.clear()
        elif a == "read_start":
            listing = rng.random() < 0.5
            readers.append(dict(route=cat["route"], listing=listing))
            if cat["route"] == "hot":
                cat["hot_readers"] += 1
        elif a == "read_end":
            rd = readers.pop(rng.randrange(len(readers)))
            if rd["listing"] and variant == "no_catalog_snapshot":
                rows = list(hot) + list(cold or [])
            elif rd["route"] == "hot":
                rows = list(hot)
            else:
                rows = list(cold or [])
            if rd["route"] == "hot":
                cat["hot_readers"] -= 1
            if not cat["destroyed"] and sorted(rows) != sorted(sealed_rows):
                fails.append("READ_COMPLETE"); return
        elif a == "delete_hot":
            if variant == "delete_without_leases" or cat["hot_readers"] == 0:
                hot.clear()
        elif a == "hold":
            cat["held"] = rng.random() < 0.4
        elif a == "destroy":
            if variant == "stale_hold_check":
                if destroy_decision is None:
                    destroy_decision = not cat["held"]; continue
                ok = destroy_decision
            else:
                ok = not cat["held"]
            if ok:
                if cat["held"]:
                    fails.append("HOLD_WINS"); return
                cat["destroyed"] = True
        # no accepted write may be missing from the routed copy
        if cat["route"] == "cold" and not cat["destroyed"]:
            if accepted - set(cold):
                fails.append("NO_LOST_WRITE"); return


def main():
    variants = ["good", "archiver_only_seal", "no_fencing", "delete_without_leases", "no_catalog_snapshot", "stale_hold_check"]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            run(rng, v, fails)
        res[v] = sorted(set(fails))
    print(f"good: {'ok' if not res['good'] else 'FAIL ' + str(res['good'])} (NO_LOST_WRITE, READ_COMPLETE, FENCED, HOLD_WINS over {RUNS} runs)")
    caught = True
    for v in variants[1:]:
        print(f"bad variant {v}: {'caught ' + str(res[v]) if res[v] else 'NOT CAUGHT'}")
        caught &= bool(res[v])
    if res["good"] or not caught:
        sys.exit(1)


if __name__ == "__main__":
    main()
