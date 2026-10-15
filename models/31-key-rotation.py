#!/usr/bin/env python3
"""Reference model for protocol 19: rotating a signing key used by many verifiers.

An issuer signs tokens (lifetime L) with its current key. Verifiers cache the published key
set and refresh it at random times (some refreshes fail). The operator rotates keys
periodically; sometimes a key leaks and is revoked urgently. Checks:

  NO_REJECT   a token signed with a key that is neither expired nor revoked is accepted by
              every verifier whose cache is within its maximum staleness; the only exception is
              the declared window after repeated leaks within S, when no distributed key is left
  REVOKE_BOUND after an urgent revocation at time t, no verifier accepts the revoked key after
              t + S (S = maximum cache staleness)

The protocol keeps a standby key published in advance, so both scheduled and urgent switches
use a key every fresh verifier already has.

Bad variants (each must be caught): switch signing to a brand-new key before verifiers can
have it; retire the old public key before tokens signed with it have expired; verifiers keep using
the last successfully fetched key set forever when refreshes fail.
"""
import random
import sys

SEED = 20261013
RUNS = 3000
L = 5        # token lifetime
S = 4        # maximum cache staleness a verifier may use


def run(rng, variant, fails):
    published = {"k0", "k1"}
    signer = "k0"
    standby = ("k1", 0)                 # next key, published in advance: (key, published_at)
    revoked = {}                         # key -> time revoked
    retiring = []                        # (key, switched_away_at)
    verifiers = [dict(keys=set(published), fetched=0) for _ in range(3)]
    tokens = []                          # (key, issued_at)
    young = set()                        # keys used before distribution: declared emergency window
    n = 2

    def new_standby(t):
        nonlocal n
        k = f"k{n}"; n += 1
        published.add(k)
        return (k, t)

    for t in range(1, 80):
        # operator: scheduled rotation
        if rng.random() < 0.08:
            k, pub = standby
            if variant == "switch_early" or t - pub > S:
                if variant == "switch_early":
                    k, pub = new_standby(t)              # a brand-new key used at once
                    retiring.append((signer, t)); signer = k
                else:
                    retiring.append((signer, t)); signer = k; standby = new_standby(t)
        for old, at in list(retiring):
            wait = 0 if variant == "retire_early" else L + S
            if t - at > wait:
                published.discard(old); retiring.remove((old, at))
        # leak: urgent revocation of the current key; switch to a published, unrevoked key that
        # every fresh verifier already has (the standby, or else a key still being retired)
        if rng.random() < 0.02 and signer not in revoked:
            revoked[signer] = t
            published.discard(signer)
            k, pub = standby
            if t - pub > S or variant == "switch_early":
                signer = k; standby = new_standby(t)
            else:
                older = [o for o, at in retiring if o not in revoked]
                if older:
                    signer = older[-1]; retiring[:] = [(o, at) for o, at in retiring if o != signer]
                else:
                    signer = k; standby = new_standby(t)
                    young.add(signer)                     # repeated leak within S: declared window
        # issue a token
        if rng.random() < 0.6:
            tokens.append((signer, t))
        # verifiers refresh (some refreshes fail)
        for v in verifiers:
            if rng.random() < 0.4 and rng.random() < 0.8:
                v["keys"] = set(published); v["fetched"] = t
        # verification
        for v in verifiers:
            stale = t - v["fetched"]
            if stale > S and variant != "stale_forever":
                continue                                  # must refetch: answers "temporarily unavailable"
            for key, issued in tokens:
                if t - issued > L:
                    continue
                accepted = key in v["keys"]
                if key in revoked:
                    if accepted and t > revoked[key] + S:
                        fails.append("REVOKE_BOUND"); return
                elif not accepted and not (key in young and t - issued <= S):
                    fails.append("NO_REJECT"); return


def main():
    variants = ["good", "switch_early", "retire_early", "stale_forever"]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            run(rng, v, fails)
        res[v] = sorted(set(fails))
    print(f"good: {'ok' if not res['good'] else 'FAIL ' + str(res['good'])} (NO_REJECT, REVOKE_BOUND over {RUNS} runs)")
    caught = True
    for v in variants[1:]:
        print(f"bad variant {v}: {'caught ' + str(res[v]) if res[v] else 'NOT CAUGHT'}")
        caught &= bool(res[v])
    if res["good"] or not caught:
        sys.exit(1)


if __name__ == "__main__":
    main()
