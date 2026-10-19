"""Runs a campaign: every cell of the list, every repetition, unattended. For each: create the BrickKit project from
cell.yaml, prepare (fresh schemas, certificates, bus), build (existing images are kept), up, measure, down. One cell
failing is written down and the campaign goes on; nothing is retried silently. Results go to
<out>/<repetition>/<cell>/ (raw data of the experiment record)."""
from __future__ import annotations

import asyncio
import datetime as dt
import json
import pathlib
import subprocess
import traceback

import yaml

from cells import create, prepare, runner
from cells.spec import Cell


def brickkit(d: pathlib.Path, *args: str, timeout: int = 1800) -> None:
    r = subprocess.run(["brickkit", *args], cwd=d, capture_output=True, text=True, timeout=timeout)
    if r.returncode != 0:
        raise RuntimeError(f"brickkit {' '.join(args)}: {r.stdout[-2000:]}{r.stderr[-2000:]}")


def clear_other_cells() -> list[str]:
    """Experiment cells share host ports: any other cell still running (left by an interrupted run) is taken down
    first. Only compose projects named brickkit-d-* (cells) are touched."""
    r = subprocess.run(["docker", "ps", "--format", '{{.Label "com.docker.compose.project"}}'], capture_output=True,
                       text=True)
    projects = sorted({p for p in r.stdout.split() if p.startswith("brickkit-d-")})
    for p in projects:
        subprocess.run(["docker", "compose", "-p", p, "down", "--remove-orphans"], capture_output=True, timeout=300)
    return projects


async def one(cells_root: pathlib.Path, entry: dict, out: pathlib.Path) -> None:
    d = cells_root / entry["cell"]
    if cleared := clear_other_cells():
        print(f"   took down cells still running: {', '.join(cleared)}", flush=True)
    create.create(d)
    await prepare.prepare(d)
    brickkit(d, "build")
    brickkit(d, "up")
    try:
        if Cell.load(d).secured:
            from cells import secure

            await secure.secure(d)
        await runner.run(str(d), str(out), entry.get("ops") or ([] if entry.get("idle_only") else list(runner.OPS)),
                         [str(x) for x in entry.get("loads", ["50", "200", "ramp"])], entry.get("idle", 600),
                         entry.get("warmup", 10), entry.get("seconds", 60))
    finally:
        brickkit(d, "down", timeout=300)


async def campaign(spec_file: str, out_root: str) -> None:
    spec = yaml.safe_load(pathlib.Path(spec_file).read_text())
    cells_root = (pathlib.Path(spec_file).parent / spec.get("cells_root", ".")).resolve()
    root = pathlib.Path(out_root)
    for rep in range(1, spec.get("runs", 1) + 1):
        for entry in spec["cells"]:
            out = root / f"run{rep}" / entry["cell"]
            if (out / "done.json").exists():
                continue  # a campaign can be resumed: finished cells are kept
            out.mkdir(parents=True, exist_ok=True)
            started = dt.datetime.now(dt.timezone.utc).isoformat()
            print(f"== run {rep} {entry['cell']} {started}", flush=True)
            try:
                await one(cells_root, entry, out)
                (out / "done.json").write_text(json.dumps({"started": started, "ended":
                                                           dt.datetime.now(dt.timezone.utc).isoformat()}))
            except Exception as e:  # noqa: BLE001 - written down; the campaign goes on
                (out / "failed.txt").write_text("".join(traceback.format_exception(e)))
                print(f"!! run {rep} {entry['cell']} failed: {e}", flush=True)
