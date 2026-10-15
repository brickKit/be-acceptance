#!/usr/bin/env python3
"""Reference model for protocol 10: external commands and callbacks.

An adapter sends payment-like commands to a provider that does NOT deduplicate by idempotency
key (the worst case the protocol must handle). Requests and responses can be lost; a request
can stay in flight and execute up to D ticks after it was sent; the provider sends callbacks
that can be duplicated, delayed and reordered; an attacker injects unsigned callbacks; the
provider's query API is sometimes unavailable. Checks:

  AT_MOST_ONCE  the provider executes each command at most once
  FACT_TRUE     a "succeeded" business fact is emitted only for a command the provider executed
                and settled as succeeded
  FACT_ONCE     each command emits at most one business fact
  NO_REGRESS    a command's local state never leaves a final state
  CONVERGE      after the provider becomes reachable and in-flight requests settle, every
                command is final and agrees with the provider

Bad variants (each must be caught): retry on timeout without querying first; trust callbacks
without verifying the signature; apply callbacks last-write-wins; emit a fact per callback
(no deduplication by command); treat "not found" as final before in-flight requests settle.
"""
import random
import sys

SEED = 20261013
RUNS = 3000
D = 3            # maximum time a request can stay in flight before it executes or is dropped
FINAL = {"succeeded", "failed"}


def run(rng, variant, fails):
    provider = {}           # ref -> list of executions, each dict(state, settle_at)
    inflight = []           # (land_at, ref)
    callbacks = []          # (deliver_at, ref, state, signed)
    cmds = {}               # ref -> dict(state, sent_at, facts)
    facts = []
    now = 0

    def send(ref):
        cmds[ref]["sent_at"] = now
        if rng.random() < 0.15:
            return "lost_request"
        inflight.append((now + rng.randint(0, D), ref))
        return "maybe" if rng.random() < 0.3 else "ok"

    def apply(ref, state, source):
        c = cmds[ref]
        if variant == "fact_per_callback" and state == "succeeded":
            c["facts"] += 1; facts.append(ref)                 # every callback becomes a fact
            c["state"] = state
            return
        if c["state"] in FINAL and variant != "last_write_wins":
            return                                             # final states are absorbing
        c["state"] = state
        if state == "succeeded" and not c["facts"]:
            c["facts"] += 1
            facts.append(ref)

    def query(ref):
        if rng.random() < 0.25:
            return None                                        # provider unavailable
        ex = provider.get(ref, [])
        if not ex:
            return "not_found"
        e = ex[0]
        return e["state"] if e["settle_at"] <= now else "pending"

    for step in range(60):
        now += 1
        # provider: land in-flight requests, settle executions, send callbacks
        for land_at, ref in list(inflight):
            if land_at <= now:
                inflight.remove((land_at, ref))
                ex = dict(state=rng.choice(["succeeded", "succeeded", "failed"]), settle_at=now + rng.randint(0, 3))
                provider.setdefault(ref, []).append(ex)
                callbacks.append((now + rng.randint(0, 4), ref, "pending", True))
                for _ in range(rng.randint(1, 2)):
                    callbacks.append((ex["settle_at"] + rng.randint(0, 4), ref, ex["state"], True))
        if rng.random() < 0.1 and cmds:                          # forged callback
            callbacks.append((now, rng.choice(list(cmds)), "succeeded", False))
        # deliver callbacks (any order among due ones)
        due = [c for c in callbacks if c[0] <= now]
        rng.shuffle(due)
        for cb in due:
            callbacks.remove(cb)
            _, ref, state, signed = cb
            if not signed and variant != "trust_unsigned":
                continue
            if state == "succeeded" and variant != "trust_unsigned":
                # a callback is a hint: confirm by query before recording a money fact
                q = query(ref)
                if q != "succeeded":
                    continue
            apply(ref, state, "callback")
        # business: create new commands
        if rng.random() < 0.3 and len(cmds) < 6:
            ref = f"r{len(cmds)}"
            cmds[ref] = dict(state="prepared", sent_at=None, facts=0)
            r = send(ref)
            cmds[ref]["state"] = "sent" if r == "ok" else "unknown"
        # adapter: resolve unknown commands
        for ref, c in cmds.items():
            if c["state"] not in ("unknown", "sent"):
                continue
            if variant == "retry_without_query" and c["state"] == "unknown":
                r = send(ref)
                c["state"] = "sent" if r == "ok" else "unknown"
                continue
            q = query(ref)
            if q is None:
                continue                                         # stay unknown; try later
            if q == "not_found":
                settled = now - c["sent_at"] > D or variant == "not_found_is_final"
                if settled and c["state"] == "unknown":
                    r = send(ref)                                # safe: nothing can still land
                    c["state"] = "sent" if r == "ok" else "unknown"
                elif settled and c["state"] == "sent" and not any(r_ == ref for _, r_ in inflight) and variant != "not_found_is_final":
                    c["state"] = "unknown"
                continue
            if q in FINAL:
                apply(ref, q, "query")
        # checks every step
        for ref, ex in provider.items():
            if len(ex) > 1:
                fails.append("AT_MOST_ONCE"); return
        for ref in facts:
            ex = provider.get(ref)
            if not ex or ex[0]["state"] != "succeeded":
                fails.append("FACT_TRUE"); return
        for ref, c in cmds.items():
            if c["facts"] > 1:
                fails.append("FACT_ONCE"); return
            if c.get("was_final") and c["state"] not in FINAL:
                fails.append("NO_REGRESS"); return
            if c["state"] in FINAL:
                if c.get("was_final") and c["state"] != c["was_final"]:
                    fails.append("NO_REGRESS"); return
                c["was_final"] = c["state"]
    # quiescence: provider reachable, in-flight settled, callbacks may be lost
    for _ in range(D + 5):
        now += 1
        for land_at, ref in list(inflight):
            inflight.remove((land_at, ref))
            provider.setdefault(ref, []).append(dict(state="succeeded", settle_at=now))
    for ref, c in cmds.items():
        for _ in range(3):
            if c["state"] in FINAL:
                break
            ex = provider.get(ref, [])
            if ex:
                apply(ref, ex[0]["state"], "query")
            else:
                c["state"] = "failed"                             # confirmed not executed after settle window
        ex = provider.get(ref, [])
        if len(ex) > 1:
            fails.append("AT_MOST_ONCE"); return
        prov_state = ex[0]["state"] if ex else "failed"
        if c["state"] != prov_state:
            fails.append("CONVERGE"); return


def main():
    variants = ["good", "retry_without_query", "trust_unsigned", "last_write_wins", "fact_per_callback", "not_found_is_final"]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            run(rng, v, fails)
        res[v] = sorted(set(fails))
    print(f"good: {'ok' if not res['good'] else 'FAIL ' + str(res['good'])} (AT_MOST_ONCE, FACT_TRUE, FACT_ONCE, NO_REGRESS, CONVERGE over {RUNS} runs)")
    caught = True
    for v in variants[1:]:
        print(f"bad variant {v}: {'caught ' + str(res[v]) if res[v] else 'NOT CAUGHT'}")
        caught &= bool(res[v])
    if res["good"] or not caught:
        sys.exit(1)


if __name__ == "__main__":
    main()
