"""python -m cells <create|prepare|secure|profile|run> <cell directory> [options]

secure, after `brickkit up` of a cell that runs the security members: the install claim, the load user's grants and
the driver's credentials (cells/secure.py).

profile observes a running cell, stops it and writes the reports to <cell>/reports/.
run <cell> <output> [--ops a,b] [--loads 50,200,ramp] [--idle 600] [--warmup 10] [--seconds 60] measures a running
cell: idle, then every edge of its chain at every operation and load.
campaign <campaign.yaml> <output> runs every cell of the campaign, every repetition, unattended (resumable).
world <cell> <file> writes the load driver's world for business chains (load -flow) from a seeded cell.
digest <cell> <file> writes the cell's result digest once it has settled; compare <a> <b> lists what differs.
chain <cell> <out> --world <file> [--rate 7] [--users 20] [--idle 600] [--warmup 300] [--seconds 900] measures a business-chain cell: idle, then the mix.
baseline <cell> <file> builds the step 8 baseline data in a seeded cell and writes the mixed load's world (load -mix).
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
    if sys.argv[1:2] == ["world"] and len(sys.argv) == 4:
        from cells import world

        print(world.world(sys.argv[2], sys.argv[3]))
        return 0
    if sys.argv[1:2] == ["chain"] and len(sys.argv) >= 4:
        import argparse

        from cells import chainrun

        a = argparse.ArgumentParser()
        a.add_argument("cell"), a.add_argument("out"), a.add_argument("--world", required=True)
        a.add_argument("--rate", type=float, default=7), a.add_argument("--users", type=int, default=20)
        a.add_argument("--idle", type=int, default=600), a.add_argument("--warmup", type=int, default=300)
        a.add_argument("--seconds", type=int, default=900)
        o = a.parse_args(sys.argv[2:])
        asyncio.run(chainrun.run(o.cell, o.out, o.world, o.rate, o.users, o.idle, o.warmup, o.seconds))
        return 0
    if sys.argv[1:2] == ["digest"] and len(sys.argv) == 4:
        from cells import digest

        d = digest.write(sys.argv[2], sys.argv[3])
        print(f"digest of {sys.argv[2]}: {sum(len(v) for v in d.values())} items in {sys.argv[3]}")
        return 0
    if sys.argv[1:2] == ["compare"] and len(sys.argv) == 4:
        import json

        from cells import digest

        diff = digest.compare(*(json.loads(open(f).read()) for f in sys.argv[2:4]))
        print("\n".join(diff) or "the same")
        return 1 if diff else 0
    if sys.argv[1:2] == ["baseline"] and len(sys.argv) == 4:
        import json

        from cells import baseline

        out = asyncio.run(baseline.build(sys.argv[2], sys.argv[3]))
        print(json.dumps(out, ensure_ascii=False))
        return 1 if out["failures"] else 0
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
