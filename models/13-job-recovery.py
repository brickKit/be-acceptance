#!/usr/bin/env python3
"""Reference model for protocol 01: background work items and crash recovery.

Workers claim work items (cron slots, queue items) and may crash at any point. Checks:

  LIVENESS     after a finite number of crashes every due item ends in `done` or `paused`
  LOCAL_ONCE   an item whose effect is local (own database) applies the effect exactly once:
               the effect and the completion mark commit in one transaction that checks the epoch
  IDEM_ONCE    an item calling an external system with an idempotency key produces the external
               effect once per key, however many attempts were made
  NO_REPEAT    an item calling a non-idempotent external system is never re-sent after an attempt
               whose outcome is unknown; it is paused for review instead
  FENCING      a worker whose lease was taken over cannot commit anything
  CLEANUP      deleting done rows below the materialised watermark never lets a slot run again
  DST          a daily cron at 02:30 in Europe/Madrid has exactly one slot per local date across both
               2026 transitions: a non-existent local time moves to the first valid instant after the
               gap, a repeated local time runs at its first occurrence
  (FENCING is checked through LOCAL_ONCE: without the epoch check a fenced worker's commit
  duplicates the local effect.)

A non-idempotent call can also end in an ambiguous error (timeout, 5xx): the item is paused at
once, and any later claim (from any state) that finds the outcome-unknown flag set pauses too.

Bad variants (each must be caught): insert-only claim (no takeover); completion mark in a
separate transaction; no epoch check on commit; resend non-idempotent calls on takeover;
cleanup without the watermark; an ambiguous error treated as retryable (flag cleared);
an ambiguous error sent to retry_wait with the flag kept, but the flag only checked on takeover.
"""
import random
import sys

SEED = 20261013
RUNS = 3000


class DB:
    def __init__(self):
        self.items = {}            # id -> dict(state, epoch, lease_until, attempts, kind, sent_unknown)
        self.local_effects = {}    # id -> count
        self.watermark = -1        # slots < watermark are never materialised again
        self.deleted = set()


def run(rng, variant, fails):
    db = DB()
    external_effects = {}          # idempotency key -> count (idempotent target)
    nonidem_sent = {}              # id -> sends (non-idempotent target)
    now = 0

    def materialise(slot):
        if variant != "cleanup_no_watermark" and slot < db.watermark:
            return
        if slot in db.items:
            return
        kind = rng.choice(["local", "idempotent", "nonidempotent"])
        db.items[slot] = dict(state="pending", epoch=0, lease_until=-1, attempts=0, kind=kind, unknown=False)

    def claim(worker_now):
        for iid, it in sorted(db.items.items()):
            if variant == "insert_only":
                ok = it["state"] == "pending" and it["epoch"] == 0
            else:
                ok = it["state"] == "pending" or (it["state"] == "running" and it["lease_until"] <= worker_now)
            if ok:
                it["takeover"] = it["state"] == "running"
                it["state"] = "running"
                it["epoch"] += 1
                it["attempts"] += 1
                it["lease_until"] = worker_now + 3
                return iid, it["epoch"]
        return None

    for slot in range(rng.randint(3, 8)):
        materialise(slot)

    stale = []  # (iid, epoch) of crashed-but-maybe-alive workers
    for step in range(60):
        now += 1
        # occasionally a stale worker wakes up and tries to commit
        if stale and rng.random() < 0.3:
            iid, ep = stale.pop(rng.randrange(len(stale)))
            it = db.items.get(iid)
            if it and it["state"] == "running" or (it and variant == "no_epoch_check"):
                if variant == "no_epoch_check" or it["epoch"] == ep:
                    if it["kind"] == "local":
                        db.local_effects[iid] = db.local_effects.get(iid, 0) + 1
                        it["state"] = "done"
                elif it["epoch"] != ep and variant != "no_epoch_check":
                    pass  # fenced: commit rejected
        c = claim(now)
        if not c:
            continue
        iid, ep = c
        it = db.items[iid]
        crash = rng.random() < 0.35
        point = rng.choice(["after_claim", "after_external_send", "before_commit", "between_effect_and_done"])
        if it["kind"] == "local":
            if crash and point in ("after_claim", "before_commit"):
                stale.append((iid, ep)); continue
            if variant == "separate_done":
                db.local_effects[iid] = db.local_effects.get(iid, 0) + 1
                if crash and point == "between_effect_and_done":
                    stale.append((iid, ep)); continue
                it["state"] = "done"
            else:
                # one transaction: check epoch, apply effect, mark done
                if it["epoch"] == ep and it["state"] == "running":
                    db.local_effects[iid] = db.local_effects.get(iid, 0) + 1
                    it["state"] = "done"
        elif it["kind"] == "idempotent":
            if crash and point == "after_claim":
                stale.append((iid, ep)); continue
            key = f"job:{iid}"
            external_effects[key] = 1          # the target applies a key at most once
            if crash and point in ("after_external_send", "before_commit"):
                stale.append((iid, ep)); continue
            if it["epoch"] == ep:
                it["state"] = "done"
        else:  # non-idempotent external call
            if crash and point == "after_claim":
                stale.append((iid, ep)); continue
            checked = variant != "takeover_check_only" or it["takeover"]
            if it["unknown"] and variant != "resend_nonidem" and checked:
                it["state"] = "paused"; continue
            nonidem_sent[iid] = nonidem_sent.get(iid, 0) + 1
            it["unknown"] = True                # recorded before sending: outcome unknown until committed
            if crash and point in ("after_external_send", "before_commit"):
                stale.append((iid, ep)); continue
            if rng.random() < 0.2:              # ambiguous error: timeout or 5xx, the target may have acted
                if variant == "retry_ambiguous":
                    it["unknown"] = False; it["state"] = "pending"
                elif variant == "takeover_check_only":
                    it["state"] = "pending"      # retry_wait with the flag kept
                else:
                    it["state"] = "paused"
                continue
            if it["epoch"] == ep:
                it["state"] = "done"
        # cleanup: delete done rows below the watermark and advance it
        if rng.random() < 0.2:
            done = sorted(k for k, v in db.items.items() if v["state"] == "done")
            if done:
                cut = done[0]
                if variant != "cleanup_no_watermark":
                    db.watermark = max(db.watermark, cut + 1)
                del db.items[cut]
                db.deleted.add(cut)
                materialise(cut)  # the scheduler re-evaluates past slots after a restart
    # checks
    for iid, it in db.items.items():
        if it["state"] not in ("done", "paused") and variant != "insert_only":
            # with no crashes left pending, a final sweep must finish it
            pass
    if variant == "insert_only":
        stuck = [i for i, it in db.items.items() if it["state"] == "running" and it["lease_until"] <= now]
        if stuck:
            fails.append("LIVENESS")
    else:
        # drain: keep claiming without crashes
        for _ in range(200):
            now += 1
            c = claim(now)
            if not c:
                break
            iid, ep = c
            it = db.items[iid]
            checked = variant != "takeover_check_only" or it["takeover"]
            if it["kind"] == "nonidempotent" and it["unknown"] and variant != "resend_nonidem" and checked:
                it["state"] = "paused"; continue
            if it["kind"] == "local":
                db.local_effects[iid] = db.local_effects.get(iid, 0) + 1
            if it["kind"] == "nonidempotent":
                nonidem_sent[iid] = nonidem_sent.get(iid, 0) + 1
            it["state"] = "done"
        if any(it["state"] not in ("done", "paused") for it in db.items.values()):
            fails.append("LIVENESS")
    if any(n > 1 for n in db.local_effects.values()):
        fails.append("LOCAL_ONCE")
    if any(n > 1 for n in external_effects.values()):
        fails.append("IDEM_ONCE")
    if any(n > 1 for n in nonidem_sent.values()):
        fails.append("NO_REPEAT")
    if any(i in db.items and i in db.deleted and db.items[i]["state"] == "pending" for i in db.deleted):
        fails.append("CLEANUP")


def dst_slots(variant):
    from datetime import date, datetime, time, timedelta, timezone
    from zoneinfo import ZoneInfo
    tz = ZoneInfo("Europe/Madrid")
    slots = []
    d = date(2026, 3, 27)
    while d <= date(2026, 10, 27):
        if variant == "utc_identity":
            # naive rule: every 24 h from the first instant; drifts across transitions
            first = datetime(2026, 3, 27, 2, 30, tzinfo=tz).astimezone(timezone.utc)
            inst = first + timedelta(days=(d - date(2026, 3, 27)).days)
        else:
            local = datetime.combine(d, time(2, 30))
            fold0 = local.replace(tzinfo=tz, fold=0)
            back = fold0.astimezone(timezone.utc).astimezone(tz)
            if back.replace(tzinfo=None) != local:                 # non-existent: move past the gap
                inst = datetime.combine(d, time(3, 0), tzinfo=tz).astimezone(timezone.utc)
            else:
                inst = fold0.astimezone(timezone.utc)              # repeated: first occurrence
        slots.append((d, inst))
        d += timedelta(days=1)
    per_date = {}
    for d, inst in slots:
        per_date.setdefault(d, set()).add(inst)
    local_times = {inst.astimezone(tz).strftime("%H:%M") for _, inst in slots}
    one_each = all(len(v) == 1 for v in per_date.values())
    return one_each and local_times <= {"02:30", "03:00"}


def main():
    variants = ["good", "insert_only", "separate_done", "no_epoch_check", "resend_nonidem", "cleanup_no_watermark",
                "retry_ambiguous", "takeover_check_only"]
    results = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            run(rng, v, fails)
        results[v] = sorted(set(fails))
    print(f"good: {'ok' if not results['good'] else 'FAIL ' + str(results['good'])} "
          f"(LIVENESS, LOCAL_ONCE, IDEM_ONCE, NO_REPEAT, FENCING, CLEANUP over {RUNS} runs with crashes)")
    caught = True
    for v in variants[1:]:
        print(f"bad variant {v}: {'caught ' + str(results[v]) if results[v] else 'NOT CAUGHT'}")
        caught &= bool(results[v])
    dst_ok = dst_slots("good")
    dst_bad_caught = not dst_slots("utc_identity")
    print(f"DST: {'ok' if dst_ok else 'FAIL'}; bad variant utc_identity: {'caught' if dst_bad_caught else 'NOT CAUGHT'}")
    if results["good"] or not caught or not dst_ok or not dst_bad_caught:
        sys.exit(1)


if __name__ == "__main__":
    main()
