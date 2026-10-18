"""Watches a running campaign and prints one line per event worth acting on: a measurement finished (with its key
figures), a cell failed, a measurement started on a system that never settled, no progress for a while, the campaign
process gone. A heartbeat every 10 minutes says it is still alive. Meant to run under a monitor: every line is a
notification, so it stays quiet otherwise."""
from __future__ import annotations

import json
import pathlib
import subprocess
import sys
import time

POLL, HEARTBEAT = 30, 600


def campaign_running() -> bool:
    r = subprocess.run(["pgrep", "-f", "-x", r".*python -m cells campaign .*"], capture_output=True, text=True)
    return bool(r.stdout.strip())


def describe(f: pathlib.Path) -> str:
    try:
        o = json.loads(f.read_text())
    except (ValueError, OSError):
        return f"{f} (unreadable)"
    if f.name == "idle.json":
        cs = o["resources"]["containers"]
        comp = {k: f"{v['process']['memory_median'] / 2**20:.0f}MiB/{v['process']['cores']:.3f}c"
                for k, v in cs.items() if not k.startswith("be-")}
        return f"idle {o['cell']}: {comp}"
    d, before = o["driver"], o.get("before", {})
    hop, comp = d.get("hop_ms") or {}, (o.get("completion") or {}).get("received") or {}
    figure = (f"hop p50/p99 {hop.get('P50')}/{hop.get('P99')}ms" if hop else
              f"received p50/p99 {comp.get('p50_ms', 0):.0f}/{comp.get('p99_ms', 0):.0f}ms" if comp.get("n") else
              f"sustained {d.get('sustained_rate')}/s by {d.get('stopped_by')}" if "steps" in d else "no latency")
    err = d.get("error_rate")
    warn = "" if before.get("settled", True) else " NOT-SETTLED"
    return (f"{o['cell']} {o['caller'].split('/')[1]}->{o['callee'].split('/')[1]} {o['op']} {o['load']}: {figure}"
            + (f" errors {err:.3f}" if err else "") + warn)


def watch(out: str, stall_minutes: float = 15) -> int:
    root = pathlib.Path(out)
    seen: set[pathlib.Path] = set()
    last_progress = last_beat = time.time()
    first = True
    while True:
        files = {p for pat in ("**/result.json", "**/idle.json", "**/done.json", "**/failed.txt") for p in root.glob(pat)}
        new = sorted(files - seen, key=lambda p: p.stat().st_mtime)
        seen |= files
        if new and not first:
            last_progress = time.time()
            for f in new:
                if f.name == "failed.txt":
                    lines = [x for x in f.read_text().splitlines() if x.strip()]
                    print(f"FAILED {f.parent.relative_to(root)}: {lines[-1][:300] if lines else ''}", flush=True)
                elif f.name == "done.json":
                    print(f"cell done {f.parent.relative_to(root)}", flush=True)
                else:
                    print(describe(f), flush=True)
        first = False
        now = time.time()
        if not campaign_running():
            done = len(list(root.glob("**/done.json")))
            failed = [str(p.parent.relative_to(root)) for p in root.glob("**/failed.txt")]
            print(f"ENDED: the campaign process is gone; cells done {done}, failed {failed}", flush=True)
            return 0
        if now - last_progress > stall_minutes * 60:
            ps = subprocess.run(["docker", "ps", "--format", "{{.Names}} {{.Status}}"], capture_output=True, text=True)
            cells = [x for x in ps.stdout.splitlines() if x.startswith("brickkit-d-")]
            print(f"STALL: no progress for {int((now - last_progress) / 60)} min; containers {cells}", flush=True)
            last_progress = now  # say it again after another stall period, not every poll
        if now - last_beat > HEARTBEAT:
            print(f"alive: {len([p for p in seen if p.name in ('result.json', 'idle.json')])} measurements so far",
                  flush=True)
            last_beat = now
        time.sleep(POLL)


if __name__ == "__main__":
    sys.exit(watch(sys.argv[1]))
