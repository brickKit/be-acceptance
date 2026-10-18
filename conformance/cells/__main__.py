"""python -m cells <create|prepare|profile> <cell directory>; profile observes a running cell, stops it and writes
the reports to <cell>/reports/."""
import asyncio
import sys

from cells import prepare


def main() -> int:
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
