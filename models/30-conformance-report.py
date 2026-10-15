#!/usr/bin/env python3
"""Reference model for protocol 18: the conformance suite must not report a false pass.

A component declares profiles; each profile has MUST checks. A component may have real defects
in some checked areas. Checks can be broken (always pass) and can fail to run (environment
missing). The suite runs each check's self-test (a known-bad fixture that must fail) before
trusting it. Checks:

  NO_FALSE_PASS  a component with a defect in an area covered by a declared profile is never
                 reported "conformant" for that profile
  HONEST_SCOPE   a "conformant" report never contains a MUST check of the declared profiles
                 that did not run

Bad variants (each must be caught): a check that did not run counts as passed; no self-test
(a broken check is trusted); the report covers only the profiles that happened to run.
"""
import random
import sys

SEED = 20261013
RUNS = 3000
PROFILES = {"core": ["config", "health", "errors"], "events": ["outbox", "inbox"], "authz": ["segments", "fields"]}


def run(rng, variant, fails):
    declared = ["core"] + [p for p in ("events", "authz") if rng.random() < 0.6]
    areas = [a for p in declared for a in PROFILES[p]]
    defects = {a for a in areas if rng.random() < 0.15}
    broken = {a for a in areas if rng.random() < 0.1}          # check always passes
    not_run = {a for a in areas if rng.random() < 0.1}         # environment missing
    if variant == "partial_scope":
        ran_profiles = [p for p in declared if not any(a in not_run for a in PROFILES[p])]
    else:
        ran_profiles = declared
    results = {}
    for p in ran_profiles:
        for a in PROFILES[p]:
            if a in not_run:
                results[a] = "pass" if variant == "skip_is_pass" else "not_run"
                continue
            if variant != "no_self_test" and a in broken:
                results[a] = "check_broken"                    # self-test caught the broken check
                continue
            results[a] = "fail" if (a in defects and a not in broken) else "pass"
    conformant = bool(ran_profiles) and all(r == "pass" for r in results.values())
    if conformant:
        if any(a in defects for a in areas):
            fails.append("NO_FALSE_PASS"); return
        if any(a in not_run for a in areas):
            fails.append("HONEST_SCOPE"); return


def main():
    variants = ["good", "skip_is_pass", "no_self_test", "partial_scope"]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            run(rng, v, fails)
        res[v] = sorted(set(fails))
    print(f"good: {'ok' if not res['good'] else 'FAIL ' + str(res['good'])} (NO_FALSE_PASS, HONEST_SCOPE over {RUNS} runs)")
    caught = True
    for v in variants[1:]:
        print(f"bad variant {v}: {'caught ' + str(res[v]) if res[v] else 'NOT CAUGHT'}")
        caught &= bool(res[v])
    if res["good"] or not caught:
        sys.exit(1)


if __name__ == "__main__":
    main()
