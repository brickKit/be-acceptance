#!/usr/bin/env python3
"""Reference model for protocol 13: datasets, reports, subscriptions and search.

Owner components publish change events (with gaps and reordering) into a dataset projection.
Users have scopes (sets of regions) that change over time. Reports, search queries and
scheduled subscriptions read the dataset. Checks:

  SCOPE       every row a user receives is inside that user's scope at query time
  WATERMARK   when a report says "complete through t", every event up to t has been applied
  RECIPIENT   a subscription delivers only rows inside the recipient's scope at delivery time
  SMALL_GROUP a sensitive aggregate is never shown for a group smaller than K
  FRESH_FACTS the dataset's authorization column lags the source: a row moved out of a user's
              scope at the source is served to that user only if the move happened within the
              declared freshness F (the dataset watermark is not older than now - F whenever a
              user whose scope is not ALL queries it); users with ALL are unaffected

Bad variants (each must be caught): access decided by the scope recorded when the row was
indexed; watermark = the newest event time seen; subscription rendered with the creator's
scope; no suppression of small groups; restricted users served without checking the watermark
age against F.
"""
import random
import sys

SEED = 20261013
RUNS = 3000
K = 3
REGIONS = ["east", "west", "north"]
F = 4


def run(rng, variant, fails):
    events = []                  # (seq, row_id, region, salary)
    applied = {}                 # row_id -> dict(region, salary, indexed_scope)
    applied_seqs = set()
    scopes = {"u1": {"east"}, "u2": {"east", "west"}, "u3": set(REGIONS)}
    seq = 0
    pending = []
    subscription = dict(creator="u3", recipient="u1")
    source = {}                  # row_id -> (region, time of the last change) at the source
    ev_time = {}                 # seq -> source time
    for step in range(50):
        now = step
        r = rng.random()
        if r < 0.3:                                       # owner emits an event
            seq += 1
            ev = (seq, f"r{rng.randint(0, 9)}", rng.choice(REGIONS), rng.randint(1, 9))
            events.append(ev); pending.append(ev)
            source[ev[1]] = (ev[2], now); ev_time[seq] = now
        elif r < 0.42 and pending:                        # projection applies an event (out of order, lagging)
            ev = pending.pop(rng.randrange(len(pending)))
            s, rid, region, sal = ev
            cur = applied.get(rid)
            if cur is None or cur["seq"] < s:
                applied[rid] = dict(seq=s, region=region, salary=sal,
                                    indexed_scope={u for u, sc in scopes.items() if region in sc})
            applied_seqs.add(s)
        elif r < 0.6:                                     # a user's scope changes
            u = rng.choice(["u1", "u2"])
            scopes[u] = set(rng.sample(REGIONS, rng.randint(0, 2)))
        elif r < 0.75:                                    # a user searches or lists
            u = rng.choice(list(scopes))
            contiguous = 0
            while contiguous + 1 in applied_seqs:
                contiguous += 1
            # every source change up to wm_time is applied (the probe/echo of protocol 02)
            wm_time = ev_time[contiguous + 1] - 1 if contiguous < seq else now
            restricted = scopes[u] != set(REGIONS)
            if restricted and variant != "no_freshness" and now - wm_time > F:
                continue                                  # cannot prove: "data may be incomplete"
            if variant == "index_time_acl":
                rows = [rid for rid, v in applied.items() if u in v["indexed_scope"]]
            else:
                rows = [rid for rid, v in applied.items() if v["region"] in scopes[u]]
            for rid in rows:
                if applied[rid]["region"] not in scopes[u]:
                    fails.append("SCOPE"); return
                src_region, moved_at = source[rid]
                if src_region not in scopes[u] and now - moved_at > F:
                    fails.append("FRESH_FACTS"); return
        elif r < 0.85:                                    # report with a completeness watermark
            if variant == "max_seen_watermark":
                wm = max(applied_seqs) if applied_seqs else 0
            else:
                wm = 0
                while wm + 1 in applied_seqs:
                    wm += 1
            if any(s <= wm and s not in applied_seqs for s, *_ in events):
                fails.append("WATERMARK"); return
            # sensitive aggregate by region
            groups = {}
            for v in applied.values():
                groups.setdefault(v["region"], []).append(v["salary"])
            for region, vals in groups.items():
                if variant != "no_small_group" and len(vals) < K:
                    continue                              # suppressed
                if len(vals) < K:
                    fails.append("SMALL_GROUP"); return
        else:                                             # subscription delivery
            who = subscription["creator"] if variant == "creator_scope" else subscription["recipient"]
            rows = [rid for rid, v in applied.items() if v["region"] in scopes[who]]
            for rid in rows:
                if applied[rid]["region"] not in scopes[subscription["recipient"]]:
                    fails.append("RECIPIENT"); return


def main():
    variants = ["good", "index_time_acl", "max_seen_watermark", "creator_scope", "no_small_group", "no_freshness"]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            run(rng, v, fails)
        res[v] = sorted(set(fails))
    print(f"good: {'ok' if not res['good'] else 'FAIL ' + str(res['good'])} (SCOPE, WATERMARK, RECIPIENT, SMALL_GROUP, FRESH_FACTS over {RUNS} runs)")
    caught = True
    for v in variants[1:]:
        print(f"bad variant {v}: {'caught ' + str(res[v]) if res[v] else 'NOT CAUGHT'}")
        caught &= bool(res[v])
    if res["good"] or not caught:
        sys.exit(1)


if __name__ == "__main__":
    main()
