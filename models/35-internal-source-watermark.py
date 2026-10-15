#!/usr/bin/env python3
"""Reference model for protocol 02's source watermark as used by security 03 (internal sources).

The source (the employee component) runs transactions that insert outbox rows: ordinary changes,
the employee's leave at s0, and probe echoes. Some transactions roll back; the leave transaction
is sometimes long. Rows are numbered "after commit" inside a lock on the partition's counter row;
positions are (epoch, sequence). The relay numbers and publishes while the message system is up
and is blocked while it is down. The stream can stall for minutes, fall behind gradually, fail the
leave's first delivery (redelivered 120 s later) or drop the leave altogether. The source database
can fail over asynchronously, before or after the leave, losing its last 5 or 40 seconds of commits
(the counter rewinds); tightenings such as the leave are committed synchronously (D65), so they are
not lost. The new primary's epoch is larger. Authz adopts only a larger epoch declared by a pull
response, abandons the pull in flight, ignores smaller epochs (a late duplicate of an old response),
and does not advance the watermark until its own re-fetch of the source (5-60 s, reading the new
primary at that moment) completes. Authz pulls when its
watermark is half of D_SRC old: the pull is the probe (the source commits an echo, numbers, and
returns the rows after Authz's position up to the echo; the response can take a while). Authz
subscribes to every event of the partition and advances only by contiguous positions. The
component uses a view for W after it asked, on a clock that counts suspension; some
appointments have scheduled ends. Checks:

  BOUND_SRC   no decision relying on the employee being active happens later than
              s0 + D_SRC + W
  BOUND_END   no decision relying on a scheduled appointment happens after its end date
  AVAILABLE   at every moment the watermark is at most D_SRC old: the source stays reachable, so
              stalls and lag must not close anything (allowance: start-up, and once per real
              failover for Authz's re-fetch)

Besides the random runs, directed scenarios construct the key interleavings on purpose, each with
50 seeds for the other parameters: a failover 10 s after the leave while the stream lags (the leave
survives in the old epoch, not yet applied); a failover 3 s after the leave losing 40 s; and a late
duplicate of a pre-failover response arriving after the new epoch was adopted.

Bad variants (each must be caught): no watermark; watermark from the pull response's arrival
time; contiguous position advanced past an entry waiting for redelivery; relay numbers rows in
insert order and skips rows not yet committed (publishing them later); no pull; pull only while the
stream is stalled; pull only after the watermark has expired; pull returns numbered rows without
numbering first; relay and pull number without the lock (duplicate positions); no numbering epoch
(the counter rewinds after a failover); a gap in positions treated as filtered out; any other epoch
seen on the stream switches epochs; any different epoch in a response is adopted (no order);
new-epoch echoes advance the watermark before the re-fetch completes; the re-fetch reads a lagging
replica; tightenings committed asynchronously (a failover can lose the leave); clock stops while the
host is suspended; scheduled ends not in the view.
"""
import random
import sys

SEED = 20261013
RUNS = 3000
D_SRC = 60          # D_src_high (D64)
W = 5               # view validity
BOUND = None       # None: the real bound s0 + D_SRC + W; a number: the non-vacuity check below
REDELIVER = 120     # longer than D_SRC: a redelivery delay alone must not be able to hide a leave
GAP_TIMEOUT = 120   # the bad relay publishes skipped rows only after this
SETTLE = 25         # start-up and epoch-change allowance for AVAILABLE (longer than a pull)
HORIZON = 400


def run(rng, variant, fails, stats, fixed=None):
    fixed = fixed or {}
    s0 = rng.randint(80, 250)
    scheduled_end = rng.choice([None, rng.randint(60, 300)])
    stall = (rng.randint(40, 200), rng.choice([0, 0, 120, 900]))    # message system down: (start, length)
    suspend = (rng.randint(20, 300), rng.choice([0, 0, 60, 600]))
    leave_fails_first = rng.random() < 0.3
    drop_leave = rng.random() < 0.2   # the message system loses the leave (retention)
    long_tx = rng.random() < 0.3      # the leave transaction inserted at s0 - 30, commits at s0
    lag_from = rng.choice([None, None, rng.randint(0, 200)])   # from here the stream falls behind:
    # an entry published at p arrives p - lag_from later, so entries keep arriving, ever later
    # asynchronous failover of the source database, losing the last `lost` seconds of commits;
    # sometimes shortly before the leave, sometimes after it (the leave can be lost and comes back
    # only when the source replays its evidence, at the end of its own alignment)
    failover = rng.choice([rng.randint(30, s0 - 40), rng.randint(s0 - 10, s0 - 1),
                           rng.randint(s0 + 1, s0 + 30)]) if rng.random() < 0.5 else None
    lost = rng.choice([5, 40])        # replication lag at the failover: commits lost
    recon = rng.randint(5, 60)        # Authz's re-fetch of the source after adopting a new epoch
    latency = rng.choice([0, 0, 1, 20])                         # pull response time
    dup_late = rng.random() < 0.3     # a pre-failover response is delivered again after adoption
    if "failover_after" in fixed:
        failover = s0 + fixed["failover_after"]
    if "lost" in fixed:
        lost = fixed["lost"]
    if "lag_before" in fixed:
        lag_from = s0 - fixed["lag_before"]
    recon = fixed.get("recon", recon)
    latency = fixed.get("latency", latency)
    dup_late = fixed.get("dup_late", dup_late)
    if fixed:
        stall = (0, 0)
        drop_leave = leave_fails_first = False

    # source transactions: dict(ins, com (None if rolled back), kind, pay, alive)
    txs = []
    for t in range(0, HORIZON, 7):
        com = None if rng.random() < 0.15 else t + rng.choice([0, 0, 1, 3])
        txs.append(dict(ins=t, com=com, kind="change", pay=t, alive=True))
    txs.append(dict(ins=s0 - 30 if long_tx else s0, com=s0, kind="leave", pay=s0, alive=True))
    src = dict(epoch=0, db_epoch=0, seq=0)
    numbered = {}                     # tx index -> (epoch, seq)
    published = set()                 # tx indexes published to the stream
    pub_at = {}
    skipped = []                      # (tx index, first seen) for insert_number_skip

    def visible(i, now):
        x = txs[i]
        return x["alive"] and x["com"] is not None and x["com"] <= now

    def assign(i, new):
        src["seq"] += 1
        numbered[i] = (src["epoch"], src["seq"])
        new.append(i)

    def number(now):
        if src["db_epoch"] != src["epoch"]:
            src["epoch"], src["seq"] = src["db_epoch"], 0
        new = []
        order = sorted(range(len(txs)), key=lambda i: txs[i]["ins"])
        if variant == "insert_number_skip":
            skipped_ids = [x[0] for x in skipped]
            for i in order:
                x = txs[i]
                if i in numbered or i in skipped_ids or x["ins"] > now or x["com"] is None or not x["alive"]:
                    continue
                if x["com"] <= now:
                    assign(i, new)
                else:
                    skipped.append((i, now))                   # cursor moves past it
            for i, at in list(skipped):
                if visible(i, now) and now >= at + GAP_TIMEOUT:
                    skipped.remove((i, at)); assign(i, new)
            return new
        for i in order:
            if i not in numbered and visible(i, now):
                assign(i, new)
        return new

    a = dict(epoch=0, applied=0, watermark=None, leave=False, waiting=None, aligned_at=0, need_pull=False)
    inflight = None
    old_response = None               # the last response applied before the failover
    late = None                       # when its late duplicate arrives
    view = None
    comp_clock = 0.0

    def source_has_leave(now):
        return any(x["kind"] == "leave" and visible(i, now) for i, x in enumerate(txs))

    def adopt(epoch, t):
        # a new epoch: positions and versions mean nothing; a pull still in flight is abandoned;
        # the watermark stays unproven until the re-fetch (taking `recon`) completes
        a.update(epoch=epoch, applied=0, watermark=None, waiting=None, abandon=True, need_pull=True,
                 aligned_at=t + recon)

    def takes(ep):
        return ep > a["epoch"] if variant != "adopt_any_different" else ep != a["epoch"]

    for t in range(HORIZON):
        stalled = stall[0] <= t < stall[0] + stall[1]
        if failover == t:
            for i, x in enumerate(txs):
                if x["kind"] == "leave" and variant != "async_tightening":
                    continue                                    # tightenings commit synchronously (D65)
                if x["com"] is not None and failover - lost < x["com"] <= failover:
                    x["alive"] = False                          # lost with the old primary
                    numbered.pop(i, None); published.discard(i)
            same = [s for (e, s) in numbered.values() if e == src["epoch"]]
            src["seq"] = max(same, default=0)
            if variant != "no_epoch":
                src["db_epoch"] += 1                            # new timeline: a new numbering epoch
        if t == a["aligned_at"] and t > 0:
            # the re-fetch reads the new primary now (the bad variant reads a replica 30 s behind)
            a["leave"] = source_has_leave(t - 30 if variant == "align_from_replica" else t)
        this_tick = []
        if not stalled:                                         # the relay is blocked while the stream is down
            this_tick = number(t)
            for i in sorted(numbered, key=lambda i: numbered[i]):
                if i not in published:
                    published.add(i); pub_at[i] = t
            # delivery: every event of the partition, in position order
            for i in sorted(published, key=lambda i: numbered[i]):
                ep, sq = numbered[i]
                if ep != a["epoch"]:
                    if variant == "switch_on_any_epoch":
                        adopt(ep, t)                            # any other epoch on the stream
                    else:
                        a["need_pull"] = True                   # only a pull response decides
                        continue
                if sq <= a["applied"]:
                    continue
                if lag_from is not None and pub_at[i] + max(0, pub_at[i] - lag_from) > t:
                    break                                       # not delivered yet
                kind = txs[i]["kind"]
                if kind == "leave" and drop_leave:
                    if variant == "skip_gap":
                        continue                                # the gap is taken as "filtered out"
                    break
                if variant != "skip_gap" and sq != a["applied"] + 1:
                    break                                       # a gap: wait (contiguous positions only)
                if a["waiting"] and variant != "applied_watermark":
                    break
                if kind == "leave" and leave_fails_first:
                    leave_fails_first = False
                    a["waiting"] = (t + REDELIVER, sq)
                    if variant == "applied_watermark":
                        a["applied"] = sq                       # later entries keep being applied
                        continue
                    break
                a["applied"] = sq
                if kind == "leave":
                    a["leave"] = True
                elif kind == "echo" and (t >= a["aligned_at"] or variant == "advance_before_reconcile"):
                    a["watermark"] = max(a["watermark"] or 0, txs[i]["pay"])
        if a["waiting"] and t >= a["waiting"][0]:
            a["leave"] = True
            if variant != "applied_watermark":
                a["applied"] = max(a["applied"], a["waiting"][1])
            a["waiting"] = None
        if a.pop("abandon", False):
            inflight = None
        # pull = probe, triggered by the watermark's age
        wm = a["watermark"]
        age = None if wm is None else t - wm
        if variant in ("no_pull", "no_watermark"):
            trigger = False
        elif variant == "pull_after_expiry":
            trigger = age is None or age > D_SRC
        elif variant == "pull_only_when_stalled":
            trigger = age is None or (stalled and age >= D_SRC // 2)
        else:
            trigger = age is None or age >= D_SRC // 2 or a["need_pull"]
        if trigger and inflight is None and variant not in ("no_pull", "no_watermark"):
            a["need_pull"] = False
        if trigger and inflight is None:
            txs.append(dict(ins=t, com=t, kind="echo", pay=t, alive=True))   # committed before numbering
            e = len(txs) - 1
            if variant == "pull_without_numbering":
                rows = sorted(i for i in numbered if numbered[i][0] == src["epoch"])
                echo_sq = max([numbered[i][1] for i in rows], default=a["applied"])
            elif variant == "unlocked_numbering" and this_tick:
                numbered[e] = numbered[this_tick[0]]            # read the counter before the relay committed
                rows, echo_sq = [e], numbered[e][1]
            else:
                number(t)
                echo_sq = numbered[e][1]
                rows = sorted((i for i in numbered if numbered[i][0] == src["epoch"] and numbered[i][1] <= echo_sq),
                              key=lambda i: numbered[i])
            rows = [(numbered[i][1], txs[i]["kind"]) for i in rows]   # the response's content is fixed when sent
            inflight = (t + latency, t, src["epoch"], rows, echo_sq)
        if late is not None and late == t and old_response is not None:
            inflight, late = (t,) + old_response[1:], None       # the late duplicate arrives now
        if inflight is not None and inflight[0] <= t:
            _, m_p, ep, rows, echo_sq = inflight
            inflight = None
            if ep != a["epoch"]:
                if takes(ep):
                    adopt(ep, t)                                # the source declares a larger epoch
                    a.pop("abandon", None)
                    if dup_late and old_response is not None:
                        late = t + 5
            else:
                if failover is None or t < failover:
                    old_response = (t, m_p, ep, rows, echo_sq)
                for sq, kind in rows:
                    if sq > a["applied"] and kind == "leave":
                        a["leave"] = True; a["waiting"] = None
                a["applied"] = max(a["applied"], echo_sq)
                if t >= a["aligned_at"] or variant == "advance_before_reconcile":
                    a["watermark"] = t if variant == "arrival_watermark" else m_p
        in_failover = failover is not None and failover <= t <= failover + D_SRC // 2 + recon + 2 * latency + 15
        if t >= SETTLE and not in_failover:
            if a["watermark"] is None or t - a["watermark"] > D_SRC:
                fails.append("AVAILABLE"); return
        # the component
        suspended = suspend[0] <= t < suspend[0] + suspend[1]
        if not (suspended and variant == "clock_stops"):
            comp_clock += 1
        if suspended:
            continue
        if t % 2 == 0:
            fresh = a["watermark"] is not None and a["watermark"] >= t - D_SRC
            if variant == "no_watermark" or fresh:
                end = None if variant == "drop_end_date" else scheduled_end
                view = (comp_clock, not a["leave"], end)
        long_stall = stalled and stall[1] > D_SRC + 2 * W and t > stall[0] + D_SRC + W
        if view is not None and rng.random() < 0.5:
            sent_local, active, end = view
            if comp_clock < sent_local + W and active and (end is None or t < end):
                if long_stall:
                    stats["during_stall"] += 1
                if t > s0 + (D_SRC + W if BOUND is None else BOUND):
                    fails.append("BOUND_SRC"); return
                if scheduled_end is not None and t >= scheduled_end:
                    fails.append("BOUND_END"); return


def main():
    variants = ["good", "no_watermark", "arrival_watermark", "applied_watermark", "insert_number_skip",
                "no_pull", "pull_only_when_stalled", "pull_after_expiry", "pull_without_numbering",
                "unlocked_numbering", "no_epoch", "skip_gap", "switch_on_any_epoch", "adopt_any_different",
                "advance_before_reconcile", "align_from_replica", "async_tightening", "clock_stops", "drop_end_date"]
    directed = [dict(failover_after=10, lost=5, lag_before=20, recon=60, latency=0),
                dict(failover_after=3, lost=40, recon=10, latency=0),
                dict(failover_after=-15, lost=5, recon=20, latency=20, dup_late=True)]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        stats = {"during_stall": 0}
        for _ in range(RUNS):
            run(rng, v, fails, stats)
        for n, fixed in enumerate(directed):
            for k in range(50):
                f = []
                run(random.Random(SEED + 1000 * n + k), v, f, {"during_stall": 0}, fixed)
                fails += [x + f"@D{n + 1}" for x in f]
        res[v] = ({f: fails.count(f) for f in sorted(set(fails))}, stats)
    g, gs = res["good"]
    print(f"good: {'ok' if not g else 'FAIL ' + str(g)} (BOUND_SRC, BOUND_END, AVAILABLE over {RUNS} runs; "
          f"{gs['during_stall']} decisions during long stream stalls)")
    caught = True
    for v in variants[1:]:
        f, st = res[v]
        print(f"bad variant {v}: {'caught ' + str(f) if f else 'NOT CAUGHT'}")
        caught &= bool(f)
    # non-vacuity: the correct protocol checked against a bound tightened to s0 + D_SRC/2 must fail,
    # otherwise BOUND_SRC could not detect anything
    global BOUND
    BOUND = D_SRC // 2
    rng = random.Random(SEED)
    tight = []
    for _ in range(RUNS):
        run(rng, "good", tight, {"during_stall": 0})
    BOUND = None
    print(f"non-vacuity: with the bound tightened to s0 + D_SRC/2 the correct protocol fails BOUND_SRC "
          f"in {tight.count('BOUND_SRC')} of {RUNS} runs")
    if g or not caught or not tight:
        sys.exit(1)


if __name__ == "__main__":
    main()
