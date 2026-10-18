"""Runs the core profile against a running cell and writes one report per probe and shell (protocol 18)."""
from __future__ import annotations

import asyncio
import json
import platform
import subprocess

from cells import observe
from cells.spec import Cell, file_of
from harness import need
from profile import checks
from profile.model import SUITE_VERSION, Report, Verdict, run


def image_digest(image: str) -> str:
    r = subprocess.run(["docker", "image", "inspect", "-f", "{{.Id}}", image], capture_output=True, text=True)
    return r.stdout.strip()


def profile(directory: str) -> int:
    c = Cell.load(directory)
    obs = asyncio.run(observe.observe(c, need("BE_ACC_PG_ADMIN")))
    out = c.dir / "reports"
    out.mkdir(exist_ok=True)
    env = {"python": platform.python_version(), "cell": c.name, "bus": c.bus, "engine": "docker"}
    versions = {**c.versions, **c.versions_of_probes}
    worst = []
    for comp, o in obs.items():
        version = versions[comp]
        report = Report(comp, image_digest(f"{file_of(comp)}:{version}"), (o.get("describe") or {}).get("protocol", ""),
                        SUITE_VERSION, {**env, "version": version}, "shell" if comp in c.shells else "core",
                        [run(k, o) for k in (checks.SHELL if comp in c.shells else checks.CORE)])
        (out / f"{file_of(comp)}.json").write_text(json.dumps(report.as_dict(), ensure_ascii=False, indent=2))
        print(report.summary())
        worst.append(report.verdict)
    return 0 if all(v == Verdict.CONFORMS for v in worst) else 1
