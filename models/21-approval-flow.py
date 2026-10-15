#!/usr/bin/env python3
"""Reference model for protocol 09: approval instances, revisions, reassignment and timeouts.

A document has revisions; an approval instance is bound to one revision; tasks are assigned by a
rule (holders of a position). Concurrently: approvals, new revisions, appointment changes,
deadlines, inbox actions retried. Checks:

  NO_STALE      execution happens only when the approved revision equals the current revision
  SOD           no approval comes from the initiator or an account linked to the initiator
  ONE_DECISION  each task has at most one decision, however often the action is retried
  NO_FAKE       a deadline never produces an approval or a rejection
  QUALIFIED     a reassigned task always goes to a current holder of the bound position

Bad variants (each must be caught): approval carried over to a new revision; SoD checks only
the initiator's own account; deadline auto-approves; inbox records decisions locally as a second
authority; reassignment to the initiator's manager regardless of the position.
"""
import random
import sys

SEED = 20261013
RUNS = 3000


def run(rng, variant, fails):
    employee = {"a1": "p1", "a1b": "p1", "a2": "p2", "a3": "p3", "a4": "p4"}   # account -> person
    holders = {"approver": {"a2", "a3"}}
    doc = dict(rev=1, initiator="a1", approved_rev=None, executed=[])

    def excluded(account):
        if variant == "sod_account_only":
            return account == doc["initiator"]
        return employee[account] == employee[doc["initiator"]]

    def assign():
        cands = sorted(a for a in holders["approver"] if not excluded(a))
        return rng.choice(cands) if cands else None      # None: paused for manual handling

    inst = dict(rev=1, task=dict(assignee=assign(), decided=None, decisions=0), active=True)
    inbox_decisions = {}
    for step in range(40):
        r = rng.random()
        t = inst["task"]
        if r < 0.3 and inst["active"]:                      # assignee decides (inbox action may be retried)
            actor = t["assignee"]
            if actor is None and variant != "inbox_local":
                continue
            for attempt in range(rng.randint(1, 3)):
                if variant == "inbox_local":
                    inbox_decisions.setdefault(id(t), 0)
                    inbox_decisions[id(t)] += 1
                    t["decisions"] += 1
                    t["decided"] = "approve"; t["by"] = actor
                    continue
                if t["decided"] is not None:
                    break                                    # idempotent: same task, already decided
                if excluded(actor):
                    t["decided"] = "refused_sod"; break
                if actor not in holders["approver"]:
                    break                                    # no longer qualified at commit
                if inst["rev"] != doc["rev"]:
                    break                                    # instance superseded
                t["decided"] = "approve"; t["by"] = actor; t["decisions"] += 1
                doc["approved_rev"] = inst["rev"]
        elif r < 0.45:                                       # new revision with key-field change
            doc["rev"] += 1
            if variant == "carry_over" and doc["approved_rev"] is not None:
                doc["approved_rev"] = doc["rev"]
            inst = dict(rev=doc["rev"], task=dict(assignee=assign(), decided=None, decisions=0), active=True)
        elif r < 0.6:                                        # appointment change: someone loses or gains the position
            a = rng.choice(["a2", "a3", "a4", "a1b"])
            if a in holders["approver"] and len(holders["approver"]) > 1:
                holders["approver"].discard(a)
            else:
                holders["approver"].add(a)
            if t["assignee"] is not None and t["assignee"] not in holders["approver"] and t["decided"] is None:
                if variant == "reassign_manager":
                    t["assignee"] = "a4"                     # the initiator's manager, position or not
                else:
                    t["assignee"] = assign()
        elif r < 0.7:                                        # deadline passes
            if t["decided"] is None:
                if variant == "deadline_approves":
                    t["decided"] = "approve"; t["by"] = "deadline"; doc["approved_rev"] = inst["rev"]
                # remind / escalate only
        else:                                                # business tries to execute
            if doc["approved_rev"] == doc["rev"]:
                doc["executed"].append(doc["rev"])
                if inst["rev"] != doc["rev"] or inst["task"]["decided"] != "approve":
                    fails.append("NO_STALE"); return
        # structural checks after every step
        t = inst["task"]
        if t["decisions"] > 1:
            fails.append("ONE_DECISION"); return
        if t["decided"] == "approve":
            if t.get("by") not in employee:
                fails.append("NO_FAKE"); return
            if employee[t["by"]] == employee[doc["initiator"]]:
                fails.append("SOD"); return
        if t["decided"] is None and t["assignee"] is not None and t["assignee"] not in holders["approver"]:
            fails.append("QUALIFIED"); return


def main():
    variants = ["good", "carry_over", "sod_account_only", "deadline_approves", "inbox_local", "reassign_manager"]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            run(rng, v, fails)
        res[v] = sorted(set(fails))
    print(f"good: {'ok' if not res['good'] else 'FAIL ' + str(res['good'])} (NO_STALE, SOD, ONE_DECISION, NO_FAKE, QUALIFIED over {RUNS} runs)")
    caught = True
    for v in variants[1:]:
        print(f"bad variant {v}: {'caught ' + str(res[v]) if res[v] else 'NOT CAUGHT'}")
        caught &= bool(res[v])
    if res["good"] or not caught:
        sys.exit(1)


if __name__ == "__main__":
    main()
