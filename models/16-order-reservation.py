#!/usr/bin/env python3
"""Reference model for protocol 04: order confirmation with inventory and credit reservations.

Participants (inventory, credit) own branches: none -> held (with TTL) -> committed -> released,
held -> expired, held -> released, none -> cancelled (tombstone), none -> rejected. Every command
carries a stable branch id and is idempotent. The sales process for each order reserves all
branches, commits all of them, and only then confirms the order locally. Responses are lost at
random, the coordinator crashes at random, held branches expire, cancels race with reserves.
Checks:

  NO_OVERSELL   held + committed never exceed stock or credit limit
  CONFIRMED     a confirmed order has every branch committed, at confirmation and afterwards
                (until the order itself is cancelled)
  NO_ORPHAN     after the system settles, no branch is held or committed for an order that is not
                confirmed
  CANCEL_WINS   a reserve arriving after the branch was cancelled is rejected
  IDEMPOTENT    repeated commands never hold a branch twice

Bad variants (each must be caught): confirm after reserves only (rely on held); participant
expires committed branches on its own TTL; no cancel tombstone; commit allowed from expired;
reserve not idempotent (a retry allocates again).
"""
import random
import sys

SEED = 20261013
RUNS = 3000
TTL = 4


class Participant:
    def __init__(self, capacity, variant):
        self.capacity = capacity
        self.branches = {}   # bid -> dict(state, amount, expires)
        self.variant = variant

    def used(self):
        return sum(b["amount"] for b in self.branches.values() if b["state"] in ("held", "committed"))

    def tick(self, now):
        for b in self.branches.values():
            if b["state"] == "held" and now >= b["expires"]:
                b["state"] = "expired"
            if self.variant == "self_expire_committed" and b["state"] == "committed" and now >= b["expires"] + 6:
                b["state"] = "expired"

    def reserve(self, bid, amount, now):
        b = self.branches.get(bid)
        if b is not None and self.variant != "reserve_not_idempotent":
            return b["state"]
        if b is not None and b["state"] == "cancelled":
            return "cancelled"
        if self.used() + amount > self.capacity:
            self.branches[bid] = dict(state="rejected", amount=0, expires=0)
            return "rejected"
        key = bid if self.variant != "reserve_not_idempotent" or b is None else f"{bid}#{len(self.branches)}"
        self.branches[key] = dict(state="held", amount=amount, expires=now + TTL)
        return "held"

    def commit(self, bid):
        b = self.branches.get(bid)
        if b is None:
            return "unknown"
        if b["state"] == "held" or (self.variant == "commit_from_expired" and b["state"] == "expired"):
            b["state"] = "committed"
        return b["state"]

    def cancel(self, bid):
        b = self.branches.get(bid)
        if b is None:
            if self.variant != "no_tombstone":
                self.branches[bid] = dict(state="cancelled", amount=0, expires=0)
            return "cancelled"
        if b["state"] in ("held", "committed"):
            b["state"] = "released"
        return b["state"]


def run(rng, variant, fails):
    inv = Participant(rng.randint(2, 6), variant)
    cred = Participant(rng.randint(200, 600), variant)
    orders = []
    for k in range(rng.randint(2, 5)):
        orders.append(dict(id=k, qty=rng.randint(1, 3), amount=rng.choice([100, 150, 200]),
                           state="draft", cancel_requested=False, step=0))
    now = 0

    def branches(o):
        return [(inv, f"o{o['id']}-inv", o["qty"]), (cred, f"o{o['id']}-cred", o["amount"])]

    def call(fn, *a):
        res = fn(*a)
        return None if rng.random() < 0.2 else res     # response lost

    for _ in range(60):
        now += 1
        inv.tick(now); cred.tick(now)
        for o in orders:
            if o["state"] in ("confirmed_final", "rejected", "cancelled"):
                continue
            if rng.random() < 0.1 and o["state"] in ("draft", "confirming", "confirmed"):
                o["cancel_requested"] = True
            if rng.random() < 0.3:
                continue                                  # coordinator crashed this tick; resumes later
            if o["cancel_requested"]:
                ok = True
                for p, bid, _ in branches(o):
                    r = call(p.cancel, bid)
                    if r is None:
                        ok = False
                if ok:
                    o["state"] = "cancelled"
                continue
            if o["state"] == "draft":
                o["state"] = "confirming"
            if o["state"] == "confirming" and not o.get("reserved"):
                results = [call(p.reserve, bid, amt, now) for p, bid, amt in branches(o)]
                if any(r in ("rejected", "cancelled", "expired") for r in results):
                    o["cancel_requested"] = True          # release whatever was held
                    o["reject_reason"] = "insufficient"
                    continue
                if any(r is None for r in results):
                    continue                              # unknown: query/retry next tick
                if variant == "confirm_after_reserve":
                    o["state"] = "confirmed"; o["confirmed_at"] = now
                    check_confirmed(o, inv, cred, fails, strict=False)
                    continue
                o["reserved"] = True                      # commit in a later tick
                continue
            if o["state"] == "confirming" and o.get("reserved"):
                commits = [call(p.commit, bid) for p, bid, _ in branches(o)]
                if any(c == "expired" for c in commits):
                    o["cancel_requested"] = True; o["reject_reason"] = "expired"
                    continue
                if all(c == "committed" for c in commits):
                    o["state"] = "confirmed"; o["confirmed_at"] = now
                    check_confirmed(o, inv, cred, fails)
            elif o["state"] == "confirmed":
                check_confirmed(o, inv, cred, fails)
        if inv.used() > inv.capacity or cred.used() > cred.capacity:
            fails.append("NO_OVERSELL")
        idempotent(inv, cred, fails)                      # checked every tick, while holds are live
    # settle: finish pending cancels without loss
    for o in orders:
        if o["cancel_requested"] and o["state"] != "cancelled":
            for p, bid, _ in branches(o):
                p.cancel(bid)
            o["state"] = "cancelled"
        if o["state"] == "confirming":
            for p, bid, _ in branches(o):
                p.cancel(bid)
            o["state"] = "cancelled"
    inv.tick(now + TTL + 10); cred.tick(now + TTL + 10)
    for o in orders:
        if o["state"] == "confirmed":
            check_confirmed(o, inv, cred, fails)
        else:
            for p, bid, _ in branches(o):
                for key, b in p.branches.items():
                    if key.split("#")[0] == bid and b["state"] in ("held", "committed"):
                        fails.append("NO_ORPHAN")
    # CANCEL_WINS: a late reserve after cancel
    o = orders[0]
    p, bid, amt = branches(o)[0]
    if o["state"] == "cancelled":
        if p.reserve(bid, amt, now) == "held":
            fails.append("CANCEL_WINS")
    idempotent(inv, cred, fails)


def idempotent(inv, cred, fails):
    """IDEMPOTENT: one branch identity never holds or commits twice."""
    for p in (inv, cred):
        seen = {}
        for key, b in p.branches.items():
            base = key.split("#")[0]
            if b["state"] in ("held", "committed"):
                seen[base] = seen.get(base, 0) + 1
        if any(c > 1 for c in seen.values()):
            fails.append("IDEMPOTENT")
            return


def check_confirmed(o, inv, cred, fails, strict=True):
    for p, bid in ((inv, f"o{o['id']}-inv"), (cred, f"o{o['id']}-cred")):
        b = p.branches.get(bid)
        if b is None or b["state"] != "committed":
            if o.get("cancel_requested"):
                continue
            fails.append("CONFIRMED")
            return


def main():
    variants = ["good", "confirm_after_reserve", "self_expire_committed", "no_tombstone",
                "commit_from_expired", "reserve_not_idempotent"]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            run(rng, v, fails)
        res[v] = sorted(set(fails))
    print(f"good: {'ok' if not res['good'] else 'FAIL ' + str(res['good'])} "
          f"(NO_OVERSELL, CONFIRMED, NO_ORPHAN, CANCEL_WINS, IDEMPOTENT over {RUNS} runs)")
    caught = True
    for v in variants[1:]:
        print(f"bad variant {v}: {'caught ' + str(res[v]) if res[v] else 'NOT CAUGHT'}")
        caught &= bool(res[v])
    if res["good"] or not caught:
        sys.exit(1)


if __name__ == "__main__":
    main()
