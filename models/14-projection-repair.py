#!/usr/bin/env python3
"""Reference model for protocol 02: local projections of another component's records.

A source owns records with fields {name, limit} and a whole-record version. It publishes
events that are delayed, reordered, duplicated or lost: complete-state events (all projected
fields at version V), partial events (only the changed field at V) and deletions (tombstone at V).
The consumer keeps one projection row per record: content, version, completeness.

  SAFE        a row marked complete at version V always equals the source's content at V
  CONVERGE    after traffic stops and one reconciliation pass, every row equals the source's
              latest state (or is absent / tombstoned for deleted records)
  R04         the audit counterexample (v12 partial arrives before v11, then complete v12) repairs
  TOMBSTONE   a lower-version snapshot never resurrects a deleted record
  BACKFILL    a rebuild that reads old snapshots never overwrites newer events applied meanwhile
  FAILOVER    after the source fails over asynchronously (its last commits are lost, so versions
              are reused with other content) and one alignment plus one reconciliation pass,
              every row equals the source; versions are compared only within one epoch, the
              epoch check comes before every rule (tombstones included), the new epoch is adopted
              from a direct response and the rows are re-fetched one by one while events arrive

Bad variants (each must be caught): strict-greater rule only (R04); partial applied and marked
complete; no reconciliation; deletion removes the row instead of keeping a tombstone; backfill
overwrites unconditionally (the row version goes backwards); no epoch (versions compared across a
failover); content digests compared but snapshots still accepted only by version.
"""
import random
import sys

SEED = 20261013
RUNS = 3000


def run(rng, variant, fails):
    # source history: id -> list of (version, state or None for deleted)
    src = {}
    history = {}
    events = []
    ids = list(range(rng.randint(2, 5)))
    for i in ids:
        src[i] = {"v": 1, "name": f"n{i}", "limit": 100, "deleted": False}
        history[(i, 1)] = dict(src[i])
        events.append(("full", i, 1, {"name": f"n{i}", "limit": 100}))
    for _ in range(rng.randint(5, 15)):
        i = rng.choice(ids)
        s = src[i]
        if s["deleted"]:
            continue
        s["v"] += 1
        r = rng.random()
        if r < 0.15:
            s["deleted"] = True
            events.append(("delete", i, s["v"], None))
        elif r < 0.55:
            field = rng.choice(["name", "limit"])
            s[field] = f"n{i}.{s['v']}" if field == "name" else rng.choice([50, 100, 200, 300])
            events.append(("partial", i, s["v"], {field: s[field]}))
        else:
            s["name"] = f"n{i}.{s['v']}"
            s["limit"] = rng.choice([50, 100, 200, 300])
            events.append(("full", i, s["v"], {"name": s["name"], "limit": s["limit"]}))
        history[(i, s["v"])] = dict(s)

    # transport: duplicate, lose, reorder
    delivered = []
    for e in events:
        if rng.random() < 0.15:
            continue
        delivered.append(e)
        if rng.random() < 0.2:
            delivered.append(e)
    rng.shuffle(delivered)

    proj = {}   # id -> dict(v, content, complete, deleted, need)

    def apply_snapshot(i, v, content, deleted=False):
        row = proj.get(i)
        if row is None:
            proj[i] = dict(v=v, content=dict(content or {}), complete=True, deleted=deleted, need=0)
            return
        if row["deleted"] and v <= row["v"]:
            return
        accept = v > row["v"] or (variant != "strict_greater" and v == row["v"] and not row["complete"])
        if variant == "backfill_overwrites" and content is not None and getattr(apply_snapshot, "backfill", False):
            accept = True
        if accept:
            row.update(v=v, content=dict(content or {}), complete=True, deleted=deleted, need=0)

    def apply_partial(i, v, delta):
        row = proj.get(i)
        if row is None:
            proj[i] = dict(v=v, content=dict(delta), complete=False, deleted=False, need=v)
            return
        if row["deleted"] or v <= row["v"]:
            return
        row["content"].update(delta)
        row["v"] = v
        if variant == "partial_marks_complete":
            row["complete"] = True
        else:
            row["complete"] = False
            row["need"] = v

    seen_deleted = set()
    last_v = {}

    def check_safe():
        for i in seen_deleted:
            row = proj.get(i)
            if row is not None and not row["deleted"]:
                fails.append("TOMBSTONE")
                return
        for i, row in proj.items():
            if row["v"] < last_v.get(i, 0):
                fails.append("BACKFILL")
                return
            last_v[i] = row["v"]
        for i, row in proj.items():
            if row["complete"] and not row["deleted"]:
                truth = history.get((i, row["v"]))
                if truth is None or truth["deleted"] or {k: truth[k] for k in ("name", "limit")} != row["content"]:
                    fails.append("SAFE")
                    return

    # consume, interleaved with read-through repair of incomplete rows and a backfill
    backfill_at = rng.randrange(len(delivered) + 1)
    for n, e in enumerate(delivered):
        if n == backfill_at:
            # rebuild: read a snapshot of every record as it was a little while ago
            apply_snapshot.backfill = True
            for i in ids:
                old_v = max(1, src[i]["v"] - rng.randint(0, 3))
                h = history.get((i, old_v))
                if h and not h["deleted"]:
                    apply_snapshot(i, old_v, {"name": h["name"], "limit": h["limit"]})
            apply_snapshot.backfill = False
        kind, i, v, payload = e
        if kind == "full":
            apply_snapshot(i, v, payload)
        elif kind == "partial":
            apply_partial(i, v, payload)
        else:
            seen_deleted.add(i)
            if variant == "delete_removes_row":
                proj.pop(i, None)
            else:
                apply_snapshot(i, v, None, deleted=True)
        # repair: fetch the source's current complete state for rows that need it
        for j, row in list(proj.items()):
            if not row["complete"] and not row["deleted"] and rng.random() < 0.3:
                s = src[j]
                if s["deleted"]:
                    apply_snapshot(j, s["v"], None, deleted=True)
                else:
                    apply_snapshot(j, s["v"], {"name": s["name"], "limit": s["limit"]})
        check_safe()

    # reconciliation pass: compare per-record digests with the source, refetch mismatches
    if variant != "no_reconcile":
        for i in ids:
            s = src[i]
            row = proj.get(i)
            want = (s["v"], s["deleted"], None if s["deleted"] else (s["name"], s["limit"]))
            have = None if row is None else (row["v"], row["deleted"],
                                              None if row["deleted"] else (row["content"].get("name"), row["content"].get("limit")))
            if want != have or (row and not row["complete"]):
                if s["deleted"]:
                    apply_snapshot(i, s["v"], None, deleted=True)
                else:
                    apply_snapshot(i, s["v"], {"name": s["name"], "limit": s["limit"]})
    check_safe()

    for i in ids:
        s = src[i]
        row = proj.get(i)
        if s["deleted"]:
            if row is not None and not row["deleted"]:
                fails.append("CONVERGE"); break
        else:
            if row is None or row["deleted"] or not row["complete"] or row["v"] != s["v"] or \
                    row["content"] != {"name": s["name"], "limit": s["limit"]}:
                fails.append("CONVERGE"); break


def run_failover(rng, variant, fails):
    """The source fails over and loses its last commits (versions are reused afterwards, deletions
    can be undone); the consumer had applied them. Epoch 1 brings complete, partial and delete
    events; the consumer adopts epoch 1 from a direct response and re-fetches row by row while
    events keep arriving."""
    ids = list(range(rng.randint(2, 5)))
    src = {i: dict(v=1, c=f"n{i}", d=False) for i in ids}
    proj = {i: dict(v=1, c=f"n{i}", d=False, epoch=0, complete=True) for i in ids}
    hist = []
    for _ in range(rng.randint(3, 10)):                        # epoch 0: consumer keeps up
        i = rng.choice(ids)
        hist.append((i, dict(src[i])))
        if rng.random() < 0.2:
            src[i] = dict(v=src[i]["v"] + 1, c=None, d=True)
        else:
            src[i] = dict(v=src[i]["v"] + 1, c=f"a{rng.randint(0, 9)}", d=False)
        proj[i] = dict(src[i], epoch=0, complete=True)
    for _ in range(rng.randint(1, min(4, len(hist)))):        # lost with the old primary
        i, before = hist.pop()
        src[i] = before
    events = []
    for _ in range(rng.randint(1, 6)):                        # epoch 1: versions are reused
        i = rng.choice(ids)
        v = src[i]["v"] + 1
        r = rng.random()
        if r < 0.2:
            src[i] = dict(v=v, c=None, d=True)
            events.append(("delete", 1, i, v, None))
        elif r < 0.5 and not src[i]["d"]:
            src[i] = dict(v=v, c=f"p{rng.randint(0, 9)}", d=False)
            events.append(("partial", 1, i, v, src[i]["c"]))
        else:
            src[i] = dict(v=v, c=f"b{rng.randint(0, 9)}", d=False)
            events.append(("full", 1, i, v, src[i]["c"]))
    events = [e for e in events if rng.random() > 0.2]          # lost, duplicated, reordered
    events += [e for e in events if rng.random() < 0.2]
    rng.shuffle(events)
    epochs = variant not in ("no_epoch", "digest_version_rule")
    adopted = 0 if epochs else 1
    pending = []                                                # rows still to re-fetch after adoption

    def accept(kind, epoch, i, v, c):
        row = proj[i]
        if not epochs:                                          # versions compared across the failover
            if variant == "no_epoch" or kind != "partial":
                if v > row["v"] and not (row["d"] and v <= row["v"]):
                    proj[i] = dict(v=v, c=c, d=kind == "delete", epoch=1, complete=kind != "partial")
            return
        if variant == "tombstone_before_epoch" and row["d"] and v <= row["v"]:
            return                                              # the tombstone rule checked first
        if epoch != adopted:
            return                                              # other epochs are not applied
        if row["epoch"] != epoch:                               # the epoch check comes before everything
            proj[i] = dict(v=v, c=c, d=kind == "delete", epoch=epoch, complete=kind != "partial")
            return
        if row["d"] and v <= row["v"]:
            return
        if v > row["v"] or (v == row["v"] and not row["complete"] and kind != "partial"):
            proj[i] = dict(v=v, c=c, d=kind == "delete", epoch=epoch, complete=kind != "partial")

    def refetch(i):                                             # written through the receive rule
        s_ = src[i]
        accept("delete" if s_["d"] else "full", adopted, i, s_["v"], s_["c"])

    adopt_at = rng.randrange(len(events) + 1)
    for n, (kind, epoch, i, v, c) in enumerate(events):
        if n == adopt_at and epochs:
            adopted = 1                                         # a direct response declares epoch 1
            pending = list(ids); rng.shuffle(pending)
        if pending and rng.random() < 0.5:
            refetch(pending.pop())                              # the re-fetch proceeds row by row
        accept(kind, epoch, i, v, c)
    if epochs and adopted == 0:
        adopted = 1
        pending = list(ids)
    for i in pending:
        refetch(i)
    for i in ids:                                               # the next regular reconciliation
        row = proj[i]
        if variant == "no_epoch":
            differs = row["v"] != src[i]["v"]
        else:
            differs = (row["v"], row["c"], row["d"]) != (src[i]["v"], src[i]["c"], src[i]["d"]) or not row["complete"]
        if differs:
            refetch(i)
    if any((proj[i]["v"], proj[i]["c"], proj[i]["d"]) != (src[i]["v"], src[i]["c"], src[i]["d"]) for i in ids):
        fails.append("FAILOVER")


def r04(variant):
    # v10 limit 100; v11 limit 50 (lost); v12 renames (partial, first); then full v12 arrives
    row = dict(v=10, content={"name": "old", "limit": 100}, complete=True)
    row["content"]["name"] = "new"; row["v"] = 12
    row["complete"] = variant == "partial_marks_complete"
    full = {"name": "new", "limit": 50}
    accept = 12 > row["v"] or (variant != "strict_greater" and 12 == row["v"] and not row["complete"])
    if accept:
        row["content"] = full; row["complete"] = True
    return row["content"] == full


def main():
    variants = ["good", "strict_greater", "partial_marks_complete", "no_reconcile", "delete_removes_row",
                "backfill_overwrites", "no_epoch", "digest_version_rule", "tombstone_before_epoch"]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            run(rng, v, fails)
        for _ in range(RUNS):
            run_failover(rng, v, fails)
        if not r04(v):
            fails.append("R04")
        res[v] = sorted(set(fails))
    print(f"good: {'ok' if not res['good'] else 'FAIL ' + str(res['good'])} (SAFE, CONVERGE, R04, TOMBSTONE, BACKFILL, FAILOVER over {RUNS} runs each)")
    caught_all = True
    for v in variants[1:]:
        print(f"bad variant {v}: {'caught ' + str(res[v]) if res[v] else 'NOT CAUGHT'}")
        caught_all &= bool(res[v])
    if res["good"] or not caught_all:
        sys.exit(1)


if __name__ == "__main__":
    main()
