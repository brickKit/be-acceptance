#!/usr/bin/env python3
"""Reference model for protocol 20: drafts kept on a terminal while the backend is unreachable.

Users share a warehouse terminal. While offline they edit drafts of a record; the server-side
record keeps changing and permissions are revoked at random. When the connection returns, a
draft is submitted. Checks:

  ISOLATION     a user never sees or submits another user's draft
  CONFIRMED     a draft is submitted only after its own user confirms it
  CURRENT       the server applies a submission only if the submitting user is authorized now
                and the draft was based on the record's current version

Bad variants (each must be caught): drafts keyed by terminal only; automatic submission when
the connection returns; the server trusts the version and permission recorded in the draft.
"""
import random
import sys

SEED = 20261013
RUNS = 3000


def run(rng, variant, fails):
    record = dict(version=1)
    allowed = {"u1": True, "u2": True}
    drafts = []                    # dict(owner, base_version, allowed_at_save, confirmed)
    online = True
    current_user = "u1"
    for step in range(50):
        r = rng.random()
        if r < 0.15:
            online = not online
        elif r < 0.25:
            current_user = rng.choice(["u1", "u2"])         # logout and another user logs in
        elif r < 0.45:                                       # current user edits a draft
            drafts.append(dict(owner=current_user, base=record["version"],
                               allowed_at_save=allowed[current_user], confirmed=False))
        elif r < 0.55:
            record["version"] += 1                           # someone changes the record on the server
        elif r < 0.6:
            u = rng.choice(["u1", "u2"]); allowed[u] = not allowed[u]
        elif online and drafts:                              # submission
            visible = drafts if variant == "terminal_keyed" else [d for d in drafts if d["owner"] == current_user]
            if not visible:
                continue
            d = rng.choice(visible)
            if d["owner"] != current_user:
                fails.append("ISOLATION"); return
            if variant != "auto_submit":
                if rng.random() < 0.5:
                    continue                                 # user has not confirmed yet
                d["confirmed"] = True
            if not d["confirmed"] and variant != "auto_submit":
                continue
            if variant == "auto_submit" and not d["confirmed"]:
                fails.append("CONFIRMED"); return
            # server-side check
            if variant == "trust_draft":
                ok = d["allowed_at_save"]
            else:
                ok = allowed[current_user] and d["base"] == record["version"]
            drafts.remove(d)
            if ok:
                if not allowed[current_user] or d["base"] != record["version"]:
                    fails.append("CURRENT"); return
                record["version"] += 1


def main():
    variants = ["good", "terminal_keyed", "auto_submit", "trust_draft"]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            run(rng, v, fails)
        res[v] = sorted(set(fails))
    print(f"good: {'ok' if not res['good'] else 'FAIL ' + str(res['good'])} (ISOLATION, CONFIRMED, CURRENT over {RUNS} runs)")
    caught = True
    for v in variants[1:]:
        print(f"bad variant {v}: {'caught ' + str(res[v]) if res[v] else 'NOT CAUGHT'}")
        caught &= bool(res[v])
    if res["good"] or not caught:
        sys.exit(1)


if __name__ == "__main__":
    main()
