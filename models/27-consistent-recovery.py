#!/usr/bin/env python3
"""Reference model for protocol 15: restoring a whole deployment to a point T.

A producer writes facts and outbox rows; a relay publishes them to a stream; a consumer applies
them through an inbox. An adapter executes external commands (an external provider keeps its
own record). Security revocations are written to the database and to an independent evidence
log. At a random time the deployment is restored to an earlier point T. Checks after recovery:

  NO_GHOST     the consumer has applied no event whose source fact does not exist after restore
  NO_LOSS      every source fact that exists after restore has been applied by the consumer
  NO_REEXEC    no external command is executed twice by the provider
  EXT_KNOWN    every command the provider executed is known locally after recovery
  REVOKED      a subject whose last effective change before the failure was a revocation is
               revoked after recovery (unless that record was a declared loss)
  NO_OVER_DESTROY  nothing is destroyed after recovery that was never destroyed
  DESTROYED    a destruction that took effect is re-executed or listed for review
  FLAGGED      a subject left more restricted than it really was is flagged, listed for review or
               covered by a declared loss (over-tightening is allowed only when visible)
  ZERO_ADMIN   while confirmed records are replayed, the subjects (all administrators) are never all
               revoked at once unless that really happened after T; when the final state has no
               administrator but reality does, the deployment is in D62's pending recovery
               (intent-only revocations are executed and flagged even when they leave none)

A directed scenario (50 seeds) replays an administrator handover after T: grant g1, then revoke g0.

Tightenings (revocations, destructions) are written in two parts: an intent outside the local
transaction (to the evidence store; if unreachable, the full record in an external alert; if both
fail, a declared loss), then the local commit, whose conclusion ("in effect" with the committed
object version, or "cancelled") reaches the evidence store through the outbox. Grants are recorded
only after commit, through the outbox, with their version. Several tightenings can wait to commit
at once and commit in any order, so the evidence's append order differs from the order of effect.
The process can crash before or after the commit, conclusions and grant records can be
undelivered, and an intent can be written before T and committed after it. Recovery replays each
record whose id is not in the restored change history, in the owner's original commit order,
intent-only tightenings (order unknown) last: confirmed ones re-executed, cancelled ones ignored, intent-only
revocations executed and flagged, intent-only destructions listed for review. Records are
re-executed as the original changes, without re-running business preconditions.

Bad variants (each must be caught): keep the old stream (ghost events delivered); recreate the
stream without republishing; republish only a fixed time window instead of from each
consumer's recorded progress; no consumer repair for facts beyond the republish retention;
records written while the evidence store was unavailable are not carried in an alert;
commit before writing the intent (a crash in between loses it); resend commands found in
"sending" without querying the provider; restore security state from the backup only; choose
the records to replay by comparing their time with T; execute intent-only destructions; execute
intent-only revocations without flagging them; replay in the evidence's append order; replay per object with objects in any order (an administrator
handover passes through zero administrators); re-check "the granter must be an administrator" when
replaying a grant; leave no administrator without entering D62's pending recovery; keep an
intent-only-revoked subject as administrator until review.
"""
import random
import sys

SEED = 20261013
RUNS = 3000
WINDOW = 4             # the fixed window of the bad variant (shorter than the retention)
CAP = 8                # published outbox rows are kept this long for republishing


def run(rng, variant, fails, handover=False):
    """handover=True is the directed scenario: g1 starts without administration; after T an
    administrator grants it to g1 and then revokes g0 (an administrator handover)."""
    t = 0
    subjects = [f"g{i}" for i in range(2)]          # two administrators: handovers matter
    db = dict(facts=set(), outbox={}, inbox=set(), applied=set(), cmds={},
              access={g: "granted" for g in subjects}, destroyed=set(), reflected=set())
    stream = []                                   # (event_id, publish_time)
    provider = {}                                 # cmd -> times executed
    records = {}                                  # security records outside the deployment: id -> dict(kind,
    # target, seq (append order), at, channel: evidence / alert / lost, status: intent / confirmed /
    # cancelled, ver: the committed object version, known once a conclusion or a grant record arrives)
    real = dict(access={g: "granted" for g in subjects}, destroyed=set(), last={}, hist=[])  # what took effect
    if handover:
        db["access"]["g1"] = real["access"]["g1"] = "revoked"
    pending = []                                  # tightening intents written, local commit not yet attempted
    outbox_sec = []                               # conclusions and grant records waiting in the outbox
    appended = [0]
    snapshots = {}

    def snapshot():
        return dict(facts=set(db["facts"]), outbox={k: dict(v) for k, v in db["outbox"].items()},
                    inbox=set(db["inbox"]), applied=set(db["applied"]),
                    cmds={k: v for k, v in db["cmds"].items()}, access=dict(db["access"]),
                    destroyed=set(db["destroyed"]), reflected=set(db["reflected"]))

    def append(rid, kind, target, status, ver=None):
        ev_up, alert_up = (True, True) if handover else (rng.random() < 0.8, rng.random() < 0.9)
        channel = "evidence" if ev_up else "alert" if alert_up else "lost"
        appended[0] += 1
        records[rid] = dict(kind=kind, target=target, seq=appended[0], at=t, channel=channel,
                            status=status, ver=ver)

    def commit(rid, kind, target):
        u = rng.random()
        if handover:
            u = 1.0                                            # the directed scenario commits
        if kind != "grant" and u < 0.12:                       # commit fails (conflict, check)
            outbox_sec.append((rid, "cancelled", None))
            return
        if kind != "grant" and u < 0.2:
            return                                             # crash before the commit: no effect
        ver = real["cseq"] = real.get("cseq", 0) + 1           # the owner's commit sequence, same transaction
        if kind == "destroy":
            db["destroyed"].add(target); real["destroyed"].add(target)
        else:
            db["access"][target] = real["access"][target] = "revoked" if kind == "revoke" else "granted"
            real["last"][target] = rid
        db["reflected"].add(rid)                               # record id in the object's change history
        real["hist"].append((t, sum(1 for g in subjects if real["access"][g] == "granted")))
        if kind == "grant":
            outbox_sec.append((rid, "grant", (target, ver)))   # a loosening is recorded after commit
        else:
            outbox_sec.append((rid, "confirmed", ver))

    def relay_sec():
        for item in list(outbox_sec):
            if handover or rng.random() < 0.6:
                outbox_sec.remove(item)
                rid, what, x = item
                if what == "grant":
                    append(rid, "grant", x[0], "confirmed", x[1])
                elif rid in records:
                    records[rid]["status"] = what
                    if what == "confirmed":
                        records[rid]["ver"] = x

    for step in range(40):
        t += 1
        if pending and rng.random() < 0.6:                     # commits happen in any order
            commit(*pending.pop(rng.randrange(len(pending))))
        relay_sec()
        if handover and step == 4:
            commit("s4", "grant", "g1")                        # grant the administration to g1 ...
        if handover and step == 5:
            append("s5", "revoke", "g0", "intent")             # ... then revoke g0
            commit("s5", "revoke", "g0")
        r = rng.random()
        if r < 0.25:                                           # producer commits a fact + outbox row
            e = f"e{step}"
            db["facts"].add(e); db["outbox"][e] = dict(published=None)
        elif r < 0.45:                                         # relay publishes
            for e, row in db["outbox"].items():
                if row["published"] is None:
                    row["published"] = t; stream.append((e, t)); break
        elif r < 0.65 and stream:                              # consumer applies the next event
            for e, _ in stream:
                if e not in db["inbox"]:
                    db["inbox"].add(e); db["applied"].add(e); break
        elif r < 0.78:                                         # adapter sends an external command
            c = f"c{step}"
            db["cmds"][c] = "sending"
            if rng.random() < 0.7:
                provider[c] = provider.get(c, 0) + 1          # provider executed it
                if rng.random() < 0.6:
                    db["cmds"][c] = "succeeded"
        elif handover:
            pass                                               # no random security changes
        elif r < 0.85 and len(pending) < 3:                    # a security change
            rid = f"s{step}"
            u = rng.random()
            kind = "revoke" if u < 0.5 else "grant" if u < 0.75 else "destroy"
            target = rng.choice(subjects) if kind != "destroy" else f"o{step}"
            if kind == "grant":
                commit(rid, kind, target)                      # recorded through the outbox afterwards
            elif variant == "commit_then_evidence":
                commit(rid, kind, target)                      # committed first ...
                if rng.random() >= 0.15:                       # ... unless it crashes before the intent
                    append(rid, kind, target, "intent")
            else:
                append(rid, kind, target, "intent")   # intent first, outside the transaction
                pending.append((rid, kind, target))            # committed in this or a later step
        for e, row in list(db["outbox"].items()):              # republish retention has a fixed cap
            if row["published"] is not None and t - row["published"] > CAP:
                del db["outbox"][e]
        snapshots[t] = snapshot()

    # restore the whole deployment to T
    T = 3 if handover else rng.randint(1, t)
    db = snapshots[T]
    if variant == "keep_stream":
        pass                                                   # ghosts published after T stay in the stream
    else:
        stream = []                                            # streams are recreated empty
        if variant == "fixed_window":
            for e, row in db["outbox"].items():
                if row["published"] is not None and row["published"] > T - WINDOW:
                    stream.append((e, row["published"]))      # a fixed time window only
        elif variant != "no_republish":
            # republish from each consumer's progress recorded in its restored database
            for e, row in db["outbox"].items():
                if row["published"] is not None and e not in db["inbox"]:
                    stream.append((e, row["published"]))
        for e, row in db["outbox"].items():
            if row["published"] is None:
                row["published"] = T; stream.append((e, T))
    # external reconciliation: query the provider before anything is resent
    for c, state in list(db["cmds"].items()):
        if state == "sending":
            if variant == "resend_without_query":
                provider[c] = provider.get(c, 0) + 1; db["cmds"][c] = "succeeded"
            elif c in provider:
                db["cmds"][c] = "succeeded"
            else:
                db["cmds"][c] = "failed"
    if variant != "resend_without_query":
        for c in provider:                                     # provider has it, we do not: import
            if c not in db["cmds"]:
                db["cmds"][c] = "succeeded"
    # security: replay each record not reflected in the restored database, per object by version
    flagged, review = set(), set()
    comparable = False
    pending_recovery = False                                  # D62: no administrator left
    if variant != "backup_security_only":
        todo = []
        for rid, rec in records.items():
            if rec["channel"] == "lost" or (rec["channel"] == "alert" and variant == "no_flag"):
                continue
            if variant == "time_select":
                if rec["at"] <= T:
                    continue
            elif rid in db["reflected"]:
                continue
            if rec["status"] == "cancelled":
                continue
            todo.append((rid, rec))
        if variant == "append_order":
            todo.sort(key=lambda x: x[1]["seq"])
        elif variant == "by_object_order":                     # per object in order, objects in any order
            rank = {g: rng.random() for g in subjects}
            todo.sort(key=lambda x: (x[1]["ver"] is None, rank.get(x[1]["target"], 0), x[1]["ver"] or 0))
        else:                                                  # by commit sequence; intent-only (unknown) last
            todo.sort(key=lambda x: (x[1]["ver"] is None, x[1]["ver"] or 0, x[1]["seq"]))
        hist_after = [c for (tt, c) in real["hist"] if tt > T]
        at_T = [c for (tt, c) in real["hist"] if tt <= T]
        real_zero = 0 in hist_after or (at_T and at_T[-1] == 0)
        comparable = not any(r["channel"] == "lost" for r in records.values()) and variant != "no_flag" \
            and not any(what == "grant" for _, what, _ in outbox_sec)
        for rid, rec in todo:
            kind, target, status = rec["kind"], rec["target"], rec["status"]
            if status == "intent" and kind == "revoke" and variant != "unflagged_intent":
                flagged.add(target)
            if status == "intent" and kind == "destroy" and variant != "destroy_on_intent":
                review.add(target)
                continue
            if status == "intent" and kind == "revoke" and variant == "review_keeps_admin" and \
                    not any(db["access"][g] == "granted" for g in subjects if g != target):
                review.add(target)                             # bad: kept as administrator until review
                continue
            if variant == "revalidate_grants" and kind == "grant" and \
                    not any(db["access"][g] == "granted" for g in subjects if g != target):
                continue                                       # "the granter must be an admin" re-checked
            if kind == "destroy":
                db["destroyed"].add(target)
            else:
                db["access"][target] = "revoked" if kind == "revoke" else "granted"
            db["reflected"].add(rid)
            if status != "intent" and comparable and not real_zero and \
                    not any(db["access"][g] == "granted" for g in subjects):
                fails.append("ZERO_ADMIN"); return                # no admin at a moment that never existed
    if variant != "backup_security_only" and variant != "intent_zeroes" and \
            not any(db["access"][g] == "granted" for g in subjects):
        pending_recovery = True                                # any cause of zero administrators
    # consumer drains the stream
    for e, _ in stream:
        if e not in db["inbox"]:
            db["inbox"].add(e); db["applied"].add(e)
    # beyond the republish retention: the consumer repairs only the facts whose outbox rows
    # were already removed by the retention cap at T (the same rule for every variant)
    if variant != "no_consumer_repair":
        for e in db["facts"] - db["applied"]:
            if e not in db["outbox"]:
                db["inbox"].add(e); db["applied"].add(e)

    if db["applied"] - db["facts"]:
        fails.append("NO_GHOST"); return
    if db["facts"] - db["applied"]:
        fails.append("NO_LOSS"); return
    if any(n > 1 for n in provider.values()):
        fails.append("NO_REEXEC"); return
    if set(provider) - set(db["cmds"]):
        fails.append("EXT_KNOWN"); return
    lost = {r["target"] for r in records.values() if r["channel"] == "lost"}
    lost |= {x[0] for rid, what, x in outbox_sec if what == "grant"}   # grants never recorded: declared loss
    real_admins = sum(1 for g in subjects if real["access"][g] == "granted")
    if variant != "backup_security_only" and comparable and real_admins > 0 and not pending_recovery and \
            not any(db["access"][g] == "granted" for g in subjects):
        fails.append("ZERO_ADMIN"); return                    # no administrator and no pending recovery
    for g, rid in real["last"].items():
        if real["access"][g] == "revoked" and records.get(rid, {}).get("channel") != "lost" \
                and db["access"][g] != "revoked":
            fails.append("REVOKED"); return
    if db["destroyed"] - real["destroyed"]:
        fails.append("NO_OVER_DESTROY"); return
    for o in real["destroyed"]:
        if records.get(f"s{o[1:]}", {}).get("channel") != "lost" and o not in db["destroyed"] | review:
            fails.append("DESTROYED"); return
    for g in subjects:
        if db["access"][g] == "revoked" and real["access"][g] == "granted" and g not in flagged | review | lost:
            fails.append("FLAGGED"); return


def main():
    variants = ["good", "keep_stream", "no_republish", "fixed_window", "no_consumer_repair", "resend_without_query", "backup_security_only", "no_flag", "commit_then_evidence", "time_select", "destroy_on_intent",
                "unflagged_intent", "append_order", "by_object_order", "revalidate_grants", "intent_zeroes",
                "review_keeps_admin"]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            run(rng, v, fails)
        for k in range(50):                                    # the directed handover scenario
            f = []
            run(random.Random(SEED + k), v, f, handover=True)
            fails += [x + "@handover" for x in f]
        res[v] = sorted(set(fails))
    print(f"good: {'ok' if not res['good'] else 'FAIL ' + str(res['good'])} (NO_GHOST, NO_LOSS, NO_REEXEC, EXT_KNOWN, REVOKED, NO_OVER_DESTROY, DESTROYED, FLAGGED, ZERO_ADMIN over {RUNS} runs)")
    caught = True
    for v in variants[1:]:
        print(f"bad variant {v}: {'caught ' + str(res[v]) if res[v] else 'NOT CAUGHT'}")
        caught &= bool(res[v])
    if res["good"] or not caught:
        sys.exit(1)


if __name__ == "__main__":
    main()
