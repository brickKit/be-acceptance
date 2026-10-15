#!/usr/bin/env python3
"""Reference model for protocol 08: extension fields.

Customer administrators define, rename, deprecate, delete and re-create extension fields on an
entity while records keep being written. Values are stored keyed by a stable field id. Checks:

  NO_REINTERPRET  a stored value is only ever read back under the field definition (id and type)
                  it was written with; deleting a field and re-creating one with the same name
                  never surfaces the old values under the new field
  TYPE_SAFE       every value read back conforms to the type of the field it is read under
  RENAME_SAFE     renaming a field keeps all of its values

Bad variants (each must be caught): values keyed by field name; type changed in place.
"""
import random
import sys

SEED = 20261013
RUNS = 3000
TYPES = {"text": str, "int": int}


def run(rng, variant, fails):
    fields = {}            # id -> dict(name, type, state)
    next_id = 1
    records = []           # each: dict(ext={key: (value, written_under_id, written_type)})

    def key_of(fid):
        return fields[fid]["name"] if variant == "keyed_by_name" else fid

    def active_by_name(name):
        for fid, f in fields.items():
            if f["name"] == name and f["state"] == "active":
                return fid
        return None

    for step in range(60):
        r = rng.random()
        if r < 0.2:                                   # define
            name = rng.choice(["color", "grade", "region"])
            if active_by_name(name) is None:
                fields[next_id] = dict(name=name, type=rng.choice(list(TYPES)), state="active")
                next_id += 1
        elif r < 0.3 and fields:                      # rename
            fid = rng.choice(list(fields))
            new = rng.choice(["color2", "grade2", "region2"])
            if fields[fid]["state"] == "active" and active_by_name(new) is None:
                fields[fid]["name"] = new             # a name-keyed store loses values here
        elif r < 0.4 and fields:                      # delete (after deprecation)
            fid = rng.choice(list(fields))
            fields[fid]["state"] = "deleted"
        elif r < 0.45 and fields:                     # change type
            fid = rng.choice(list(fields))
            f = fields[fid]
            if f["state"] == "active":
                if variant == "in_place_type":
                    f["type"] = "int" if f["type"] == "text" else "text"
                else:                                 # new field + migration; old deprecated
                    fields[next_id] = dict(name=f["name"] + "_v2", type="int" if f["type"] == "text" else "text", state="active")
                    next_id += 1
                    f["state"] = "deprecated"
        else:                                         # write a record with values for active fields
            ext = {}
            for fid, f in fields.items():
                if f["state"] == "active" and rng.random() < 0.6:
                    val = rng.randint(0, 9) if f["type"] == "int" else f"v{rng.randint(0, 9)}"
                    ext[key_of(fid)] = (val, fid, f["type"])
            records.append(dict(ext=ext))
        # read every record under the current active definitions
        for rec in records:
            for fid, f in fields.items():
                if f["state"] not in ("active", "deprecated"):
                    continue
                stored = rec["ext"].get(key_of(fid))
                if stored is None:
                    continue
                val, wid, wtype = stored
                if wid != fid:
                    fails.append("NO_REINTERPRET"); return
                if not isinstance(val, TYPES[f["type"]]):
                    fails.append("TYPE_SAFE"); return
        # renamed fields keep values: every value written under fid is still found under fid
        for rec in records:
            for k, (val, wid, wtype) in rec["ext"].items():
                if wid in fields and fields[wid]["state"] in ("active", "deprecated"):
                    if rec["ext"].get(key_of(wid)) is None:
                        fails.append("RENAME_SAFE"); return


def main():
    variants = ["good", "keyed_by_name", "in_place_type"]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            run(rng, v, fails)
        res[v] = sorted(set(fails))
    print(f"good: {'ok' if not res['good'] else 'FAIL ' + str(res['good'])} (NO_REINTERPRET, TYPE_SAFE, RENAME_SAFE over {RUNS} runs)")
    caught = True
    for v in variants[1:]:
        print(f"bad variant {v}: {'caught ' + str(res[v]) if res[v] else 'NOT CAUGHT'}")
        caught &= bool(res[v])
    if res["good"] or not caught:
        sys.exit(1)


if __name__ == "__main__":
    main()
