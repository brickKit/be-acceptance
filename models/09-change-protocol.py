#!/usr/bin/env python3
"""Reference model for the 8th security candidate: the state and change protocol.

Random operation sequences against an abstract authorization store check:

  EFFECTIVE   effective access = stored grant scope ∩ current source constraints, recomputed at
              compile time; right after any tightening, before the closure job runs, nobody holds
              access outside the new source constraints
  CLOSURE     the closure job only does bookkeeping: running it never changes effective access;
              a grant partly outside a tightened ceiling stays active and is flagged for review
              (holding it entirely would cut access that is still within the ceiling)
  LASTADMIN   normal changes never leave the deployment without a usable admin holding;
              a forced security disable may, and then the deployment is in recovery state
  TERMINAL    an ended record never becomes active again; a re-grant is a new instance
  EFFECT      a widening re-checks the requester at the effect point: a requester who lost
              management eligibility between request and effect cannot make it effective
  DEPARTURE   an admin's normal departure does not end the ordinary grants that admin issued
  BATCH       a batch reports every item as applied, rejected or needs review; no item silently
              disappears, and applied items are exactly those that passed the effect point

Bad variants (each must make at least one check fail):
  hold_partial             the closure job pauses a grant that is only partly outside the ceiling
  ignore_ceiling           effective access ignores the governance ceiling
  ignore_appointment       effective access ignores the bound appointment's status
  effect_no_recheck        the effect point does not re-check the requester's eligibility
  skip_last_admin          normal changes skip the last-admin invariant
  departure_ends_grants    an admin's normal departure ends the grants that admin issued
  reactivate_ended         a re-grant reactivates the ended record instead of creating a new one
  delegation_no_intersect  a delegation edge grants its cap without intersecting the delegator's access

Scopes are finite sets of atoms here; the 7th candidate's segment algebra is assumed.
This is a model of the protocol, not of any SDK, database or provider.
"""
import random
import sys

SEED = 20261013
BAD = set()
BAD_VARIANTS = ["hold_partial", "ignore_ceiling", "ignore_appointment", "effect_no_recheck",
                "skip_last_admin", "departure_ends_grants", "reactivate_ended", "delegation_no_intersect"]
RUNS = 3000
STEPS = 40
ATOMS = list("abcdefgh")
PEOPLE = ["p1", "p2", "p3", "p4"]


class Store:
    def __init__(self, rng):
        self.rng = rng
        self.next_id = 1
        self.records = {}          # id -> dict
        self.recovery = False
        self.account_active = {p: True for p in PEOPLE}
        self.appt_active = {}      # appointment id -> bool
        # bootstrap: p1 holds admin of the root management unit (ceiling = ALL)
        self.root = self.new("unit", ceiling=set(ATOMS), state="active")
        self.appt_active["ap-p1"] = True
        self.new("holding", subject="p1", unit=self.root, appt="ap-p1", admin=True, state="active")

    def new(self, kind, **kw):
        rid = f"{kind}{self.next_id}"
        self.next_id += 1
        self.records[rid] = dict(kind=kind, **kw)
        return rid

    def of(self, kind, state=None):
        return [k for k, r in self.records.items() if r["kind"] == kind and (state is None or r["state"] == state)]

    # --- source constraints, computed live (what bundle compilation does) -------------------
    def unit_ceiling(self, u):
        r = self.records[u]
        return set(r["ceiling"]) if r["state"] == "active" else set()

    def holding_usable(self, h):
        r = self.records[h]
        return (r["state"] == "active" and self.account_active[r["subject"]]
                and self.appt_active.get(r["appt"], False))

    def effective(self, person):
        """Union over active grants of: grant scope ∩ governance ceiling ∩ (bound appointment active)."""
        out = set()
        for g in self.of("grant", "active"):
            r = self.records[g]
            if r["subject"] != person or not self.account_active[person]:
                continue
            if r.get("appt") and not self.appt_active.get(r["appt"], False) and "ignore_appointment" not in BAD:
                continue
            out |= set(r["scope"]) if "ignore_ceiling" in BAD else set(r["scope"]) & self.unit_ceiling(r["unit"])
        # delegation: edge cap ∩ delegator's current effective access
        for e in self.of("edge", "active"):
            r = self.records[e]
            if r["to"] == person and self.account_active[person]:
                out |= set(r["cap"]) if "delegation_no_intersect" in BAD else set(r["cap"]) & self.effective_direct(r["frm"])
        return out

    def effective_direct(self, person):
        out = set()
        for g in self.of("grant", "active"):
            r = self.records[g]
            if r["subject"] == person and self.account_active[person] and \
                    (not r.get("appt") or self.appt_active.get(r["appt"], False) or "ignore_appointment" in BAD):
                out |= set(r["scope"]) if "ignore_ceiling" in BAD else set(r["scope"]) & self.unit_ceiling(r["unit"])
        return out

    def usable_admins(self):
        return [h for h in self.of("holding", "active") if self.records[h]["admin"] and self.holding_usable(h)]

    # --- the effect point -------------------------------------------------------------------
    def requester_can_manage(self, person, unit):
        return any(self.records[h]["subject"] == person and self.records[h]["unit"] == unit
                   and self.holding_usable(h) for h in self.of("holding", "active"))

    def effect_grant(self, req):
        """Widening: re-verify requester, containment and target at the effect point."""
        r = req
        if self.recovery:
            return "rejected"
        if not self.requester_can_manage(r["by"], r["unit"]) and "effect_no_recheck" not in BAD:
            return "rejected"
        if not set(r["scope"]) <= self.unit_ceiling(r["unit"]):
            return "rejected"
        if r.get("appt") and not self.appt_active.get(r["appt"], False):
            return "rejected"
        self.new("grant", subject=r["subject"], unit=r["unit"], scope=set(r["scope"]), appt=r.get("appt"),
                 issued_by=r["by"], state="active")
        return "applied"

    def would_break_last_admin(self, change):
        if "skip_last_admin" in BAD:
            return False
        snapshot = (dict(self.account_active), dict(self.appt_active),
                    {k: dict(v) for k, v in self.records.items()})
        change()
        ok = len(self.usable_admins()) >= 1
        self.account_active, self.appt_active, recs = snapshot
        self.records = recs
        return not ok

    def end(self, rid, reason):
        r = self.records[rid]
        if r["state"] in ("active", "held", "scheduled"):
            r["state"] = "ended"
            r["reason"] = reason
            return True
        return False

    # --- closure: bookkeeping only ---------------------------------------------------------
    def closure(self):
        for g in self.of("grant", "active"):
            r = self.records[g]
            if r.get("appt") and not self.appt_active.get(r["appt"], False):
                self.end(g, "source_ended")
            elif not set(r["scope"]) & self.unit_ceiling(r["unit"]):
                self.end(g, "source_narrowed")   # wholly outside: its effect is already empty
            elif not set(r["scope"]) <= self.unit_ceiling(r["unit"]):
                if "hold_partial" in BAD:
                    r["state"] = "held"          # wrong: pausing cuts access still within the ceiling
                else:
                    r["review"] = True           # partly outside: stays active, effect is the intersection
        for h in self.of("holding", "active"):
            if not self.appt_active.get(self.records[h]["appt"], False):
                self.end(h, "source_ended")


def run(rng, fails):
    s = Store(rng)
    s.appt_active.update({f"ap-{p}": True for p in PEOPLE})
    pending = []  # widening requests waiting for their effect point
    ended_ever = set()
    for _ in range(STEPS):
        op = rng.choice(["request", "effect", "tighten_unit", "end_appt", "remove_holding",
                         "add_admin", "forced_disable", "end_grant", "regrant", "delegate",
                         "closure", "batch", "admin_leaves"])
        before_eff = {p: s.effective(p) for p in PEOPLE}
        if op == "request" and not s.recovery:
            by = rng.choice(PEOPLE)
            pending.append(dict(by=by, subject=rng.choice(PEOPLE), unit=s.root,
                                scope=set(rng.sample(ATOMS, rng.randint(1, 3))),
                                appt=rng.choice([None, f"ap-{rng.choice(PEOPLE)}"])))
        elif op == "effect" and pending:
            req = pending.pop(rng.randrange(len(pending)))
            allowed = s.requester_can_manage(req["by"], req["unit"]) and not s.recovery
            res = s.effect_grant(req)
            if res == "applied" and not allowed:
                fails.append(("EFFECT", req))
        elif op == "tighten_unit":
            u = s.records[s.root]
            if len(u["ceiling"]) > 2:
                u["ceiling"].discard(rng.choice(sorted(u["ceiling"])))
                for p in PEOPLE:  # EFFECTIVE: no access outside new ceiling before closure
                    if s.effective_direct(p) - s.unit_ceiling(s.root):
                        fails.append(("EFFECTIVE", "ceiling", p))
        elif op == "end_appt":
            ap = f"ap-{rng.choice(PEOPLE)}"
            change = lambda: s.appt_active.__setitem__(ap, False)
            if s.would_break_last_admin(change):
                continue  # normal change refused by the invariant
            s.appt_active[ap] = False
            for p in PEOPLE:
                for g in s.of("grant", "active"):
                    r = s.records[g]
                    if r["subject"] == p and r.get("appt") == ap:
                        # the grant's own contribution must already be gone
                        others = set()
                        for g2 in s.of("grant", "active"):
                            r2 = s.records[g2]
                            if g2 != g and r2["subject"] == p and (not r2.get("appt") or s.appt_active.get(r2["appt"])):
                                others |= set(r2["scope"]) & s.unit_ceiling(r2["unit"])
                        if s.effective_direct(p) - others:
                            fails.append(("EFFECTIVE", "appointment", g))
        elif op == "remove_holding":
            hs = s.of("holding", "active")
            if hs:
                h = rng.choice(hs)
                if not s.would_break_last_admin(lambda: s.end(h, "revoked")):
                    s.end(h, "revoked")
        elif op == "add_admin" and not s.recovery:
            p = rng.choice(PEOPLE)
            if s.usable_admins():
                s.new("holding", subject=p, unit=s.root, appt=f"ap-{p}", admin=True, state="active")
        elif op == "forced_disable":
            p = rng.choice(PEOPLE)
            s.account_active[p] = False          # security disable ignores the invariant
            if not s.usable_admins():
                s.recovery = True
        elif op == "end_grant":
            gs = s.of("grant")
            if gs:
                g = rng.choice(gs)
                s.end(g, "revoked")
        elif op == "regrant":
            ended = [g for g in s.of("grant") if s.records[g]["state"] == "ended"]
            if ended:
                g = rng.choice(ended)
                # TERMINAL: the old record stays ended; a re-grant is a new instance
                r = s.records[g]
                if "reactivate_ended" in BAD:
                    r["state"] = "active"
                elif s.requester_can_manage(r["issued_by"], r["unit"]) and not s.recovery:
                    s.effect_grant(dict(by=r["issued_by"], subject=r["subject"], unit=r["unit"],
                                        scope=set(r["scope"]) & s.unit_ceiling(r["unit"]) or {ATOMS[0]}, appt=None))
                if s.records[g]["state"] != "ended":
                    fails.append(("TERMINAL", g))
        elif op == "delegate":
            frm, to = rng.sample(PEOPLE, 2)
            s.new("edge", frm=frm, to=to, cap=set(rng.sample(ATOMS, 3)), state="active")
        elif op == "closure":
            s.closure()
            after = {p: s.effective(p) for p in PEOPLE}
            if after != before_eff:
                fails.append(("CLOSURE", before_eff, after))
        elif op == "admin_leaves":
            # normal departure: end the admin's holding, keep grants they issued
            hs = [h for h in s.of("holding", "active") if s.records[h]["admin"]]
            if hs:
                h = rng.choice(hs)
                who = s.records[h]["subject"]
                issued = [g for g in s.of("grant", "active") if s.records[g]["issued_by"] == who]
                if not s.would_break_last_admin(lambda: s.end(h, "departed")):
                    s.end(h, "departed")
                    if "departure_ends_grants" in BAD:
                        for g in issued:
                            s.end(g, "departed")
                    if any(s.records[g]["state"] != "active" for g in issued):
                        fails.append(("DEPARTURE", h))
        elif op == "batch" and not s.recovery:
            items = [dict(by=rng.choice(PEOPLE), subject=rng.choice(PEOPLE), unit=s.root,
                          scope=set(rng.sample(ATOMS, 2)), appt=None) for _ in range(rng.randint(1, 4))]
            report = {}
            for i, it in enumerate(items):
                report[i] = s.effect_grant(it)
            if len(report) != len(items) or any(v not in ("applied", "rejected") for v in report.values()):
                fails.append(("BATCH", report))
        # delegation never exceeds the delegators' current direct access
        for p in PEOPLE:
            via = s.effective(p) - s.effective_direct(p)
            allowed = set()
            for e in s.of("edge", "active"):
                if s.records[e]["to"] == p:
                    allowed |= s.effective_direct(s.records[e]["frm"])
            if via - allowed:
                fails.append(("EFFECTIVE", "delegation", p))
        # TERMINAL: once ended, a record stays ended
        for k, r in s.records.items():
            if r["state"] == "ended":
                ended_ever.add(k)
            elif k in ended_ever:
                fails.append(("TERMINAL", k))
        # LASTADMIN: outside recovery there is always a usable admin
        if not s.recovery and not s.usable_admins():
            fails.append(("LASTADMIN", op))
    return s


def run_all():
    rng = random.Random(SEED)
    fails = []
    recoveries = 0
    for _ in range(RUNS):
        s = run(rng, fails)
        recoveries += s.recovery
    return fails, recoveries


def main():
    fails, recoveries = run_all()
    names = ["EFFECTIVE", "CLOSURE", "LASTADMIN", "TERMINAL", "EFFECT", "DEPARTURE", "BATCH"]
    bad = {n for n, *_ in fails}
    for n in names:
        print(f"{n}: {'FAIL' if n in bad else 'ok'}")
    print(f"{RUNS} runs x {STEPS} steps; {recoveries} runs ended in recovery state after a forced disable")
    if fails:
        for f in fails[:5]:
            print("FAIL", f, file=sys.stderr)
        sys.exit(1)
    caught = 0
    for v in BAD_VARIANTS:
        BAD.clear(); BAD.add(v)
        vf, _ = run_all()
        kinds = sorted({f[0] for f in vf})
        print(f"bad variant {v}: {'detected by ' + ', '.join(kinds) if vf else 'NOT DETECTED'}")
        caught += bool(vf)
    BAD.clear()
    if caught != len(BAD_VARIANTS):
        print("FAIL: some bad variants were not detected", file=sys.stderr)
        sys.exit(1)
    print(f"all {len(BAD_VARIANTS)} bad variants detected")


if __name__ == "__main__":
    main()
