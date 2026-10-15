#!/usr/bin/env python3
"""Reference model for protocol 17: shared resource budgets inside a shell.

Three members share one database connection pool. Member A runs a heavy background job that
floods the pool; members B and C serve foreground requests. Each request holds a connection
for a random number of ticks. Checks:

  FG_BOUND    a foreground request of B or C waits at most W ticks for a connection, or is
              rejected immediately with a retryable error; it is never left waiting longer
  BOUNDED     no member's queue grows beyond its declared bound
  BG_PROGRESS the background job still makes progress (it is throttled, not starved forever)

Bad variants (each must be caught): one FIFO queue for the whole pool; no cap on background
use of the pool; unbounded queues; background admitted only while foreground is idle (starves the job).
"""
import random
import sys

SEED = 20261013
RUNS = 3000
POOL = 6
W = 4
QMAX = 8
BG_CAP = 3              # background may hold at most this many connections
RESERVED = 1            # per foreground member, kept free from background


def run(rng, variant, fails):
    held = []                                     # (member, kind, release_at)
    queues = {"A": [], "B": [], "C": []}          # entries: (kind, arrived_at)
    fifo = []                                     # for the fifo variant: (member, kind, arrived_at)
    bg_done = 0
    for t in range(120):
        held = [h for h in held if h[2] > t]
        # arrivals
        for _ in range(rng.randint(2, 4)):        # A floods background work
            if variant == "fifo":
                fifo.append(("A", "bg", t))
            elif variant == "unbounded" or len(queues["A"]) < QMAX:
                queues["A"].append(("bg", t))
        for m in ("B", "C"):
            if rng.random() < 0.35:
                if variant == "fifo":
                    fifo.append((m, "fg", t))
                elif variant == "unbounded" or len(queues[m]) < QMAX:
                    queues[m].append(("fg", t))
                # else: rejected immediately with a retryable error (allowed)
        # allocation
        free = POOL - len(held)
        if variant == "fifo":
            while free > 0 and fifo:
                m, kind, at = fifo.pop(0)
                held.append((m, kind, t + rng.randint(1, 3))); free -= 1
                if kind == "bg":
                    held[-1] = (m, kind, t + rng.randint(4, 8)); bg_done += 1
        else:
            # foreground first, round robin between members
            progressed = True
            while free > 0 and progressed:
                progressed = False
                for m in ("B", "C"):
                    if free > 0 and queues[m]:
                        kind, at = queues[m].pop(0)
                        held.append((m, kind, t + rng.randint(1, 3))); free -= 1; progressed = True
            bg_held = sum(1 for h in held if h[1] == "bg")
            fg_active = any(queues[m] for m in ("B", "C")) or any(h[1] == "fg" for h in held)
            cap = POOL if variant == "no_bg_cap" else BG_CAP
            reserve = 0 if variant == "no_bg_cap" else RESERVED * 2
            while queues["A"] and bg_held < cap and free > reserve:
                if variant == "idle_only" and fg_active:
                    break                                 # background only when foreground is idle
                queues["A"].pop(0)
                held.append(("A", "bg", t + rng.randint(4, 8))); free -= 1; bg_held += 1; bg_done += 1
        # checks
        waiting_fg = [(m, at) for m, k, at in fifo if k == "fg"] if variant == "fifo" else \
                     [(m, at) for m in ("B", "C") for k, at in queues[m]]
        for m, at in waiting_fg:
            if t - at > W:
                fails.append("FG_BOUND"); return
        sizes = [len(fifo)] if variant == "fifo" else [len(q) for q in queues.values()]
        if any(s > QMAX * 3 for s in sizes):
            fails.append("BOUNDED"); return
    if bg_done < 20:
        fails.append("BG_PROGRESS")


def main():
    variants = ["good", "fifo", "no_bg_cap", "unbounded", "idle_only"]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            run(rng, v, fails)
        res[v] = sorted(set(fails))
    print(f"good: {'ok' if not res['good'] else 'FAIL ' + str(res['good'])} (FG_BOUND, BOUNDED, BG_PROGRESS over {RUNS} runs)")
    caught = True
    for v in variants[1:]:
        print(f"bad variant {v}: {'caught ' + str(res[v]) if res[v] else 'NOT CAUGHT'}")
        caught &= bool(res[v])
    if res["good"] or not caught:
        sys.exit(1)


if __name__ == "__main__":
    main()
