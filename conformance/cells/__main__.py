"""python -m cells <create|prepare|secure|profile|run> <cell directory> [options]

secure, after `brickkit up` of a cell that runs the security members: the install claim, the load user's grants and
the driver's credentials (cells/secure.py).

profile observes a running cell, stops it and writes the reports to <cell>/reports/.
run <cell> <output> [--ops a,b] [--loads 50,200,ramp] [--idle 600] [--warmup 10] [--seconds 60] measures a running
cell: idle, then every edge of its chain at every operation and load.
campaign <campaign.yaml> <output> runs every cell of the campaign, every repetition, unattended (resumable).
tables <output> prints the record's tables from a campaign's results."""
import asyncio
import sys

from cells import prepare


def main() -> int:
    if sys.argv[1:2] == ["tables"] and len(sys.argv) == 3:
        from cells import tables

        print(tables.all_tables(sys.argv[2]), end="")
        return 0
    if sys.argv[1:2] == ["campaign"] and len(sys.argv) == 4:
        from cells import campaign

        asyncio.run(campaign.campaign(sys.argv[2], sys.argv[3]))
        return 0
    if sys.argv[1:2] == ["run"] and len(sys.argv) >= 4:
        import argparse

        from cells import runner

        a = argparse.ArgumentParser()
        a.add_argument("cell"), a.add_argument("out")
        a.add_argument("--ops", default=",".join(runner.OPS)), a.add_argument("--loads", default="50,200,ramp")
        a.add_argument("--idle", type=int, default=600), a.add_argument("--warmup", type=int, default=10)
        a.add_argument("--seconds", type=int, default=60)
        o = a.parse_args(sys.argv[2:])
        asyncio.run(runner.run(o.cell, o.out, o.ops.split(","), o.loads.split(","), o.idle, o.warmup, o.seconds))
        return 0
    if sys.argv[1:2] == ["secure"] and len(sys.argv) == 3:
        from cells import secure

        print(asyncio.run(secure.secure(sys.argv[2])))
        return 0
    if len(sys.argv) != 3 or sys.argv[1] not in ("create", "prepare", "profile"):
        print(__doc__, file=sys.stderr)
        return 64
    if sys.argv[1] == "create":
        from cells import create

        create.create(sys.argv[2])
        print(f"created {sys.argv[2]}")
        return 0
    if sys.argv[1] == "profile":
        from cells import report

        return report.profile(sys.argv[2])
    c = asyncio.run(prepare.prepare(sys.argv[2]))
    print(f"prepared {c.name}: {len(c.probes)} probes, chain {' -> '.join(c.chain)}")
    return 0


sys.exit(main())
