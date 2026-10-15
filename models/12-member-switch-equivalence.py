#!/usr/bin/env python3
"""Reference check for the 10th security candidate: switching the authorization member.

The canonical security records are the only authority; each member compiles them into its own
engine form and must hand back effective segments. A switch is admitted only if, for every
subject x resource type x operation, the digest of the normalised effective segment set is the
same on both members.

  LOSSLESS   a member that stores segments as relation tuples plus per-tuple conditions and
             reconstructs them passes the digest comparison on every random store
  LOSSY      each lossy translation is detected by the comparison on some store:
               - drops amount intervals (condition support missing)
               - flattens dimensions per subject (the I18 cross-product)
               - drops write fields
               - ignores validity windows
  LOCATED    a detected difference names the subject, type and operation, so the switch report
             can list exactly what the new member cannot represent
  COUNT_ONLY a comparison of segment counts only (instead of digests) detects none of the
             lossy translations: the digest comparison is necessary (PV20)

This is a model of the comparison, not of OpenFGA, SpiceDB, OPA, Cerbos, Cedar or Casbin.
"""
import hashlib
import json
import random
import sys

SEED = 20261013
STORES = 2000
LEGALS, WHS, SUBJECTS = ["L1", "L2", "L3"], ["W1", "W2", "W3"], ["u1", "u2", "u3"]
OPS = ["read", "approve"]


def seg(rng):
    return {
        "op": rng.choice(OPS),
        "legal": sorted(rng.sample(LEGALS, rng.randint(1, 2))),
        "wh": sorted(rng.sample(WHS, rng.randint(1, 2))),
        "amount": None if rng.random() < 0.5 else {"currency": "CNY", "hi": str(rng.choice([50, 100, 200]))},
        "read": sorted(rng.sample(["name", "price", "cost"], rng.randint(1, 3))),
        "write": [],
        "until": None if rng.random() < 0.6 else "2027-01-01T00:00:00.000000Z",
    }


def store(rng):
    out = []
    for s in SUBJECTS:
        for _ in range(rng.randint(0, 3)):
            g = seg(rng)
            if g["op"] == "approve" and rng.random() < 0.5:
                g["write"] = [g["read"][0]]
            out.append((s, g))
    return out


def digest(segments):
    norm = sorted(json.dumps(x, sort_keys=True, separators=(",", ":")) for x in segments)
    return hashlib.sha256(("be.effective.v1\n" + "\n".join(norm)).encode()).hexdigest()


def counts(member, records):
    """subject -> op -> number of effective segments: the inadequate count-only comparison."""
    return {(s, op): len(member([g for (subj, g) in records if subj == s and g["op"] == op]))
            for s in SUBJECTS for op in OPS}


def effective(member, records):
    """subject -> op -> digest of the effective segment set produced by the member."""
    view = {}
    for s in SUBJECTS:
        for op in OPS:
            segs = member([g for (subj, g) in records if subj == s and g["op"] == op])
            view[(s, op)] = digest(segs)
    return view


def standard(segs):
    return [dict(g) for g in segs]


def graph_lossless(segs):
    # compile to tuples: one tuple per (segment, legal, warehouse) carrying a condition with the
    # rest of the segment and a segment id; reconstruct by grouping on the segment id
    tuples = []
    for i, g in enumerate(segs):
        for l in g["legal"]:
            for w in g["wh"]:
                tuples.append({"seg": i, "legal": l, "wh": w,
                               "cond": {k: g[k] for k in ("op", "amount", "read", "write", "until")}})
    out = {}
    for t in tuples:
        e = out.setdefault(t["seg"], dict(t["cond"], legal=set(), wh=set()))
        e["legal"].add(t["legal"]); e["wh"].add(t["wh"])
    return [dict(v, legal=sorted(v["legal"]), wh=sorted(v["wh"])) for v in out.values()]


def drops_amount(segs):
    return [dict(g, amount=None) for g in segs]


def flattens(segs):
    if not segs:
        return []
    legal = sorted({l for g in segs for l in g["legal"]})
    wh = sorted({w for g in segs for w in g["wh"]})
    return [dict(g, legal=legal, wh=wh) for g in segs]


def drops_write(segs):
    return [dict(g, write=[]) for g in segs]


def ignores_until(segs):
    return [dict(g, until=None) for g in segs]


def main():
    rng = random.Random(SEED)
    stores = [store(rng) for _ in range(STORES)]
    fails = []
    for st in stores:
        if effective(standard, st) != effective(graph_lossless, st):
            fails.append("LOSSLESS")
            break
    lossy = {"drops_amount": drops_amount, "flattens": flattens, "drops_write": drops_write,
             "ignores_until": ignores_until}
    detected = {}
    located_ok = True
    for name, m in lossy.items():
        hits = 0
        for st in stores:
            a, b = effective(standard, st), effective(m, st)
            diff = [k for k in a if a[k] != b[k]]
            if diff:
                hits += 1
                # LOCATED: every reported key really differs in content
                for (s, op) in diff:
                    ga = sorted(json.dumps(x, sort_keys=True) for x in standard([g for (u, g) in st if u == s and g["op"] == op]))
                    gb = sorted(json.dumps(x, sort_keys=True) for x in m([g for (u, g) in st if u == s and g["op"] == op]))
                    if ga == gb:
                        located_ok = False
        detected[name] = hits
    count_hits = {name: sum(1 for st in stores if counts(standard, st) != counts(m, st))
                  for name, m in lossy.items()}
    print(f"LOSSLESS: {'ok' if 'LOSSLESS' not in fails else 'FAIL'} (graph-style member equals the standard member on {STORES} random stores)")
    for name, hits in detected.items():
        print(f"LOSSY {name}: detected on {hits} of {STORES} stores")
    print(f"LOCATED: {'ok' if located_ok else 'FAIL'}")
    for name, hits in count_hits.items():
        print(f"COUNT_ONLY {name}: detected on {hits} of {STORES} stores")
    count_blind = not any(count_hits.values())
    print(f"COUNT_ONLY: {'ok (count comparison detects none of the lossy translations)' if count_blind else 'FAIL'}")
    if fails or not all(detected.values()) or not located_ok or not count_blind:
        sys.exit(1)


if __name__ == "__main__":
    main()
