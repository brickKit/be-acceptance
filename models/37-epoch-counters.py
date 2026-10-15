#!/usr/bin/env python3
"""Reference model for protocol 14: counters allocated in a database and compared elsewhere carry
the database's epoch; epochs keep fork points.

A database allocates two counters: the claim generation of a work item (protocol 01) and the
security revision of an authorization view (security 03). A resource stores the revision it was
stamped with (its minimal security watermark). At random times the database switches over: a
planned switch loses nothing; a lossy one (a restore, or the committed-but-unconfirmed window)
loses the last few commits, so the new primary allocates the lost values again for other content.
Every switch starts a larger epoch and records the old epoch's fork point (the last value kept).
Checks:

  ONE_HOLDER  a worker whose claim was lost cannot renew or commit while a new holder holds the
              item (no two holders); holders compare the epoch they obtained with their claim
  NO_STALE    a component holding a view revision never hears "still current" for a revision
              whose content is not the current one
  RES_SAFE    a resource stamp is accepted as "not newer than now" only when its revision really
              exists in the current history
  RES_LIVE    after a lossless switch, a stamp that the new epoch still contains stays readable

Bad variants (each must be caught): claims compare the generation only; claims compare the
row's claim epoch with the current one instead of the holder's own (the literal text of an
earlier draft); view checks compare the revision only; stamps from an older epoch are always
unprovable (no fork point); stamps compare the revision only.
"""
import random
import sys

SEED = 20261013
RUNS = 3000


def run(rng, variant, fails):
    epoch = 0
    gen = 0                       # work item generation in the database
    row_epoch = 0                 # the claim epoch stored on the work item's row
    rev = 0                       # security revision in the database
    content = {(0, 0): "v0"}      # (epoch, revision) -> content of that revision in the current history
    fork = {}                     # old epoch -> last revision kept (its fork point)
    log = []                      # committed changes not yet beyond loss: ("gen"|"rev", value)
    workers = {}                  # worker -> (epoch, generation) obtained when claiming
    view = None                   # (epoch, revision, content) the component holds
    stamp = None                  # (epoch, revision) a resource was stamped with

    def holds(w):
        ep, g = workers[w]
        if variant == "generation_only":
            return g == gen
        if variant == "row_epoch_only":
            return g == gen and row_epoch == epoch
        return (ep, g) == (epoch, gen) and row_epoch == ep

    def contains(st):
        """Does the current history contain the stamped revision?"""
        e, r = st
        if e == epoch:
            return r <= rev
        return r <= fork.get(e, -1) and all(fork.get(x, 10 ** 9) >= 0 for x in range(e, epoch))

    for step in range(60):
        r = rng.random()
        if r < 0.2:                                           # a worker claims the item
            gen += 1
            row_epoch = epoch
            workers = {f"w{step}": (epoch, gen)}
            log.append(("gen", gen))
        elif r < 0.4:                                         # a security change
            rev += 1
            content[(epoch, rev)] = f"c{step}"
            log.append(("rev", rev))
        elif r < 0.5:                                         # the component checks its view
            if view is None:
                view = (epoch, rev, content[(epoch, rev)])
            else:
                same = view[1] == rev if variant == "revision_only" else (view[0], view[1]) == (epoch, rev)
                if same:
                    if view[2] != content[(epoch, rev)]:
                        fails.append("NO_STALE"); return
                else:
                    view = (epoch, rev, content[(epoch, rev)])
        elif r < 0.58:                                        # a resource is stamped now
            stamp = (epoch, rev)
        elif r < 0.68:                                        # a reader checks the stamp
            if stamp is not None:
                if variant == "stamp_revision_only":
                    ok = stamp[1] <= rev
                elif variant == "no_fork":
                    ok = stamp[0] == epoch and stamp[1] <= rev
                else:
                    ok = stamp[1] <= rev if stamp[0] == epoch else stamp[1] <= fork.get(stamp[0], -1)
                truth = contains(stamp)
                if ok and not truth:
                    fails.append("RES_SAFE"); return
                if truth and not ok and stamp[0] != epoch and fork.get(stamp[0], -1) == last_before.get(stamp[0]):
                    fails.append("RES_LIVE"); return
        elif r < 0.76:                                        # replication catches up
            log = []
        elif r < 0.84:                                        # a switch: planned (lossless) or lossy
            lossy = log and rng.random() < 0.6
            last_before.setdefault(epoch, rev)
            last_before[epoch] = rev
            if lossy:
                for kind, value in log[-rng.randint(1, len(log)):]:
                    if kind == "gen":
                        gen = min(gen, value - 1)
                    else:
                        rev = min(rev, value - 1)
            fork[epoch] = rev                                 # the old epoch's fork point
            if lossy:
                for k in [k for k in content if k[0] == epoch and k[1] > rev]:
                    del content[k]
            epoch += 1
            content[(epoch, rev)] = content.get((epoch - 1, rev), "v0")
            log = []
            if rng.random() < 0.7:                            # someone claims again at once
                gen += 1
                row_epoch = epoch
                workers[f"n{step}"] = (epoch, gen)
                log.append(("gen", gen))
        for w in list(workers):                               # renewals now and then
            if rng.random() < 0.5 and not holds(w):
                del workers[w]
        if sum(1 for w in workers if holds(w)) > 1:
            fails.append("ONE_HOLDER"); return


last_before = {}


def main():
    global last_before
    variants = ["good", "generation_only", "row_epoch_only", "revision_only", "no_fork", "stamp_revision_only"]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            last_before = {}
            run(rng, v, fails)
        res[v] = {f: fails.count(f) for f in sorted(set(fails))}
    print(f"good: {'ok' if not res['good'] else 'FAIL ' + str(res['good'])} "
          f"(ONE_HOLDER, NO_STALE, RES_SAFE, RES_LIVE over {RUNS} runs)")
    caught = True
    for v in variants[1:]:
        print(f"bad variant {v}: {'caught ' + str(res[v]) if res[v] else 'NOT CAUGHT'}")
        caught &= bool(res[v])
    if res["good"] or not caught:
        sys.exit(1)


if __name__ == "__main__":
    main()
