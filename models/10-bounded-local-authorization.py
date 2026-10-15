#!/usr/bin/env python3
"""Reference model for the default path of D57: bounded local authorization.

A component decides locally against an authorization view. A view is valid for W after the
component *sent* the verification request, measured on the component's own monotonic clock.
The business transaction checks, inside the transaction just before commit, that the view is
still valid. Random timelines (tightening time, authz outages, network delays, transaction
durations, wall-clock offsets, clock rate drift) check:

  BOUND        after a tightening commits at authz at t0, no decision that relies on the old view
               happens later than t0 + W (validity is W * (1 - drift) on the local clock)
  NO_STATIC    no commit happens unless some verification answered by authz was *sent* within the
               last W * (1 + drift): an outage longer than the window stops operations
  AVAILABLE    during outages shorter than the renewal margin, operations still commit
  NO_SYNC      the bound holds for wall-clock offsets up to 300 s, because only the component's
               monotonic clock is used

Bad variants (each must be caught):
  expiry measured from receipt time; expiry from authz's wall-clock timestamp compared with the
  component's wall clock; validity checked only when the request starts; a cached answer replayed
  as a renewal.

This is a model of the timing rule, not of any SDK, network or provider.
"""
import random
import sys

SEED = 20261013
RUNS = 4000
W = 5.0            # validity window for high-risk operations (starting candidate)
PERIOD = 1.0       # re-verification attempt period
DRIFT = 0.001      # bound on the component's monotonic clock rate error
HORIZON = 70.0


def run(rng, variant, out):
    t0 = rng.uniform(10, 40)
    outages = []
    for _ in range(rng.randint(0, 3)):
        a = rng.uniform(0, 60)
        outages.append((a, a + rng.choice([0.5, 1.5, 2.5, 8.0, 20.0])))
    wall_offset = rng.uniform(-300, 300)          # component wall clock minus authz wall clock
    rate = 1 + rng.uniform(-DRIFT, DRIFT)

    def mono(x):
        return x * rate

    def authz_up(x):
        return not any(a <= x < b for a, b in outages)

    view = None          # dict(rev, valid(x) -> bool)
    last_good = None     # last authz-computed answer, for the replay variant
    answered_sends = []  # true send times of verifications that authz answered
    pending = []         # (recv_time, answer)
    t, next_try = 0.0, 0.0
    while t < HORIZON:
        if t >= next_try:
            next_try = t + PERIOD
            send = t
            delay = rng.choice([0.01, 0.05, 0.3, 1.5, 4.0])
            answer_at = send + rng.uniform(0, delay)
            recv = send + delay
            if authz_up(answer_at):
                rev = 1 if answer_at >= t0 else 0
                pending.append((recv, dict(rev=rev, send=send, recv=recv, answer_at=answer_at)))
                answered_sends.append(send)
            elif variant == "replay_cache" and last_good:
                pending.append((recv, dict(last_good, send=send, recv=recv)))
        # deliver answers that have arrived
        for item in [p for p in pending if p[0] <= t]:
            pending.remove(item)
            a = item[1]
            last_good = a
            if variant == "from_receipt":
                until = mono(a["recv"]) + W
                valid = (lambda u: (lambda x: mono(x) < u))(until)
            elif variant == "authz_timestamp":
                until_wall = a["answer_at"] + W                 # authz wall clock
                valid = (lambda u: (lambda x: x + wall_offset < u))(until_wall)
            else:
                until = mono(a["send"]) + W * (1 - DRIFT)
                valid = (lambda u: (lambda x: mono(x) < u))(until)
            view = dict(rev=a["rev"], valid=valid)

        if rng.random() < 0.35:
            start = t
            commit = start + rng.choice([0.01, 0.2, 1.0, 3.0, 6.0])
            used = view
            if used is not None and used["valid"](start) and \
                    (variant == "check_at_start" or used["valid"](commit)):
                out["commits"] += 1
                if used["rev"] == 0 and commit > t0 + W + 1e-9:
                    out["fails"].append(("BOUND", round(t0, 2), round(commit, 2)))
                recent = [s for s in answered_sends if commit - W * (1 + DRIFT) - 1e-9 <= s <= commit]
                if not recent:
                    out["fails"].append(("NO_STATIC", round(commit, 2)))
                if not authz_up(commit):
                    out["commits_during_outage"] += 1
        t = round(t + 0.25, 4)


def main():
    variants = ["good", "from_receipt", "authz_timestamp", "check_at_start", "replay_cache"]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        out = {"fails": [], "commits": 0, "commits_during_outage": 0}
        for _ in range(RUNS):
            run(rng, v, out)
        res[v] = out
    g = res["good"]
    bound_ok = not any(f[0] == "BOUND" for f in g["fails"])
    static_ok = not any(f[0] == "NO_STATIC" for f in g["fails"])
    print(f"BOUND: {'ok' if bound_ok else 'FAIL'}; NO_STATIC: {'ok' if static_ok else 'FAIL'}; "
          f"AVAILABLE: {'ok' if g['commits_during_outage'] > 0 else 'FAIL'} "
          f"({g['commits']} commits, {g['commits_during_outage']} during authz outages)")
    caught_all = True
    for v in variants[1:]:
        caught = bool(res[v]["fails"])
        caught_all &= caught
        kinds = sorted({f[0] for f in res[v]["fails"]})
        print(f"bad variant {v}: {'caught ' + str(kinds) if caught else 'NOT CAUGHT'}")
    if not (bound_ok and static_ok and g["commits_during_outage"] > 0 and caught_all):
        for f in g["fails"][:5]:
            print("FAIL", f, file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
