"""Watches a running campaign for a monitor, where every printed line is a notification. Lines come at once only for
what needs acting on: a cell failed, no progress for a while, the campaign process gone, or a measurement that looks
wrong (taken on a system that never settled, errors at a fixed rate, the fixed rate not reached, far off the same
measurement in earlier runs). Otherwise one summary every 5 minutes: progress, the estimated time left, anomalies so far.

    python cells/watch.py <output> <campaign.yaml>"""
from __future__ import annotations

import json
import pathlib
import statistics
import subprocess
import sys
import time

import yaml

POLL, SUMMARY, STALL = 30, 300, 900
FIXED = {"50": 50.0, "200": 200.0}


def campaign_running() -> bool:
    r = subprocess.run(["pgrep", "-f", "-x", r".*python -m cells campaign .*"], capture_output=True, text=True)
    return bool(r.stdout.strip())


def planned(spec_file: str) -> int:
    """How many measurements (idle windows and result files) the campaign writes in all."""
    sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))
    from cells import runner
    from cells.spec import Cell
    spec = yaml.safe_load(pathlib.Path(spec_file).read_text())
    root = (pathlib.Path(spec_file).parent / spec.get("cells_root", ".")).resolve()
    per_run = 0
    for e in spec["cells"]:
        c = Cell.load(root / e["cell"])
        ops = e.get("ops") or ([] if e.get("idle_only") else list(runner.OPS))
        per_run += 1 + len([p for p in c.chain if c.next_of(p)]) * len(ops) * len(e.get("loads", ["50", "200", "ramp"]))
    return per_run * spec.get("runs", 1)


def key(f: pathlib.Path, root: pathlib.Path) -> tuple[str, str]:
    """(run, the same measurement in any run): run1/D-X/a--b/write/50/result.json -> ("run1", "D-X/a--b/write/50")."""
    parts = f.relative_to(root).parts
    return parts[0], "/".join(parts[1:-1])


def figures(o: dict) -> dict:
    d = o["driver"]
    hop, comp = d.get("hop_ms") or {}, (o.get("completion") or {}).get("received") or {}
    if hop:
        return {"p50": hop.get("P50"), "p99": hop.get("P99")}
    if comp.get("n"):
        return {"p50": comp.get("p50_ms"), "p99": comp.get("p99_ms")}
    if "steps" in d:
        return {"sustained": d.get("sustained_rate")}
    return {"p50": (d.get("latency_ms") or {}).get("P50"), "p99": (d.get("latency_ms") or {}).get("P99")}


def anomalies(o: dict, load: str, earlier: list[dict]) -> list[str]:
    d, out = o["driver"], []
    if not o.get("before", {}).get("settled", True):
        out.append(f"the system had not settled before it ({o['before'].get('left')})")
    if load in FIXED:
        if d.get("error_rate"):
            out.append(f"errors at a fixed rate: {d.get('errors')}")
        if (d.get("throughput") or 0) < 0.95 * FIXED[load]:
            out.append(f"throughput {d.get('throughput')}/s at {load}/s offered")
    now = figures(o)
    for name, worse in (("p99", lambda a, b: a > 3 * b and a - b > 20), ("p50", lambda a, b: a > 2 * b and a - b > 5),
                        ("sustained", lambda a, b: a < 0.5 * b)):
        before = [e[name] for e in earlier if e.get(name) is not None]
        if now.get(name) is not None and before and worse(now[name], statistics.median(before)):
            out.append(f"{name} {now[name]:.1f} against {statistics.median(before):.1f} in earlier runs")
    return out


def watch(out: str, spec_file: str) -> int:
    root, total = pathlib.Path(out), planned(spec_file)
    seen: dict[pathlib.Path, float] = {}
    by_key: dict[str, list[dict]] = {}
    found, started = 0, time.time()
    last_progress = last_summary = time.time()
    while True:
        files = {p for pat in ("**/result.json", "**/idle.json", "**/failed.txt") for p in root.glob(pat)}
        for f in sorted(files - seen.keys(), key=lambda p: p.stat().st_mtime):
            seen[f] = f.stat().st_mtime
            if f.name == "failed.txt":
                lines = [x for x in f.read_text().splitlines() if x.strip()]
                print(f"FAILED {f.parent.relative_to(root)}: {lines[-1][:300] if lines else ''}", flush=True)
                continue
            if f.name == "result.json":
                try:
                    o = json.loads(f.read_text())
                except (ValueError, OSError):
                    print(f"UNREADABLE {f.relative_to(root)}", flush=True)
                    continue
                run, k = key(f, root)
                if problems := anomalies(o, f.parent.name, by_key.get(k, [])):
                    found += 1
                    print(f"ANOMALY {run}/{k}: {'; '.join(problems)}", flush=True)
                by_key.setdefault(k, []).append(figures(o))
            last_progress = time.time()
        now = time.time()
        if not campaign_running():
            done = len([p for p in seen if p.name != "failed.txt"])
            failed = [str(p.parent.relative_to(root)) for p in seen if p.name == "failed.txt"]
            print(f"ENDED: the campaign process is gone; {done}/{total} measurements, failed {failed}, "
                  f"anomalies {found}", flush=True)
            return 0
        if now - last_progress > STALL:
            ps = subprocess.run(["docker", "ps", "--format", "{{.Names}} {{.Status}}"], capture_output=True, text=True)
            cells = [x for x in ps.stdout.splitlines() if x.startswith("brickkit-d-")]
            print(f"STALL: no progress for {int((now - last_progress) / 60)} min; containers {cells}", flush=True)
            last_progress = now  # said again after another stall period, not every poll
        if now - last_summary > SUMMARY:
            print(summary(root, seen, total, started, found), flush=True)
            last_summary = now
        time.sleep(POLL)


def summary(root: pathlib.Path, seen: dict[pathlib.Path, float], total: int, started: float, found: int) -> str:
    done = sorted(t for p, t in seen.items() if p.name != "failed.txt")
    recent = [t for t in done if t >= started]
    left = ""
    if len(recent) >= 2:
        per = (recent[-1] - recent[0]) / (len(recent) - 1)
        left = f", about {max(total - len(done), 0) * per / 60:.0f} min left"
    current = max(seen, key=seen.get).relative_to(root).parts[:2] if seen else ("-",)
    return (f"progress {len(done)}/{total} ({100 * len(done) / max(total, 1):.0f}%){left}; at {'/'.join(current)}; "
            f"anomalies {found}")


if __name__ == "__main__":
    sys.exit(watch(sys.argv[1], sys.argv[2]))
