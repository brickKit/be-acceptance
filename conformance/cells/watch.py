"""Watches a running campaign for a monitor, where every printed line is a notification. Lines come at once only for
what needs acting on: a cell failed, no progress for a while, the campaign process gone, or a measurement that looks
wrong (taken on a system that never settled, errors at a fixed rate, the fixed rate not reached, far off the same
measurement in earlier runs). Otherwise one summary every 5 minutes: progress, the estimated time left, anomalies so far.

    python cells/watch.py <output> <campaign.yaml> [--from-now]

Expected limits at 200/s (a container's CPU saturated with only limit errors; only slow arrival where 50/s was
fine) are counted in the summary instead of waking anyone. --from-now, for a re-armed monitor, skips what is there.
"""
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
# Plausible upper bounds at a fixed rate on one machine, per operation: the p50 of the hop the caller measures (calls)
# or of the request (writes, ms), and the p99 of the event's arrival (ms). Far above what the trials read (hop 1-7 ms,
# write 3-8 ms, arrival p99 under 0.5 s); a figure past them is wrong somewhere even when every request succeeded
# (the Python SDK's 46 ms writes with Nagle on passed every test).
LIMITS = {"echo-1k-grpc": 10, "echo-1k-http": 10, "echo-60k-grpc": 25, "echo-60k-http": 25, "work-grpc": 30,
          "work-http": 30, "write": 30, "relay": 30, "big-event": 60}
ARRIVAL_P99 = 2000
# A relay is the event's arrival plus a work item and one call; what the relay adds past the arrival is a few
# milliseconds. It sat at about 250 ms in every cell (a worker polling every 500 ms without a wake, P7.21) while only
# the arrival was checked. The arrival itself is checked on its own (on the PostgreSQL queue it includes the
# subscriber's poll), so the relay's own part is the difference of the two p50s.
RELAY_ADDED_P50 = 60
LIMIT_ERRORS = ("OUTBOUND_LIMIT", "SERVICE_BUSY", "DEADLINE_EXCEEDED")


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


def anomalies(o: dict, load: str, earlier: list[dict], op: str = "") -> list[str]:
    d, out = o["driver"], []
    if not o.get("before", {}).get("settled", True):
        out.append(f"the system had not settled before it ({o['before'].get('left')})")
    if load in FIXED:
        if d.get("error_rate"):
            out.append(f"errors at a fixed rate: {d.get('errors')}")
        if (d.get("throughput") or 0) < 0.95 * FIXED[load]:
            out.append(f"throughput {d.get('throughput')}/s at {load}/s offered")
        p50 = (d.get("hop_ms") or d.get("latency_ms") or {}).get("P50")
        if op in LIMITS and p50 is not None and p50 > LIMITS[op]:
            out.append(f"p50 {p50:.1f} ms at a fixed rate, above the plausible {LIMITS[op]} ms for {op}")
        comp = o.get("completion") or {}
        arrival = (comp.get("received") or {}).get("p99_ms")
        if arrival is not None and arrival > ARRIVAL_P99:
            out.append(f"events arrive with p99 {arrival:.0f} ms at a fixed rate")
        relayed = comp.get("relayed") or {}
        if relayed.get("p99_ms") is not None and relayed["p99_ms"] > ARRIVAL_P99:
            out.append(f"relays complete with p99 {relayed['p99_ms']:.0f} ms at a fixed rate")
        received = (comp.get("received") or {}).get("p50_ms")
        if op == "relay" and relayed.get("p50_ms") is not None and received is not None \
                and relayed["p50_ms"] - received > RELAY_ADDED_P50:
            out.append(f"relays complete {relayed['p50_ms'] - received:.0f} ms after their events arrive (p50), "
                       f"above the plausible {RELAY_ADDED_P50} ms")
    now = figures(o)
    for name, worse in (("p99", lambda a, b: a > 3 * b and a - b > 20), ("p50", lambda a, b: a > 2 * b and a - b > 5),
                        ("sustained", lambda a, b: a < 0.5 * b)):
        before = [e[name] for e in earlier if e.get(name) is not None]
        if now.get(name) is not None and before and worse(now[name], statistics.median(before)):
            out.append(f"{name} {now[name]:.1f} against {statistics.median(before):.1f} in earlier runs")
    return out


def expected_limit(o: dict, load: str, problems: list[str], fine_at_50: bool) -> str:
    """The category of an expected limit, counted in the summary instead of waking anyone, or "" when the problems
    need a look. Only at 200/s: anything at 50/s is a problem (the queue defect of 2026-10-18 showed there)."""
    if load != "200":
        return ""
    errors = o["driver"].get("errors") or {}
    cores = max((v["process"]["cores"] for k, v in o["resources"]["containers"].items() if k.startswith("brickkit-")),
                default=0)
    if cores >= 0.9 and all(any(e in k for e in LIMIT_ERRORS) for k in errors):
        return "CPU saturated"
    if not errors and fine_at_50 and all(x.startswith(("events arrive", "relays complete")) for x in problems):
        return "subscription one at a time"
    return ""


def watch(out: str, spec_file: str, from_now: bool = False) -> int:
    """from_now: the results already there were reported by an earlier watcher (a monitor re-armed); they only feed
    the comparisons."""
    root, total = pathlib.Path(out), planned(spec_file)
    seen: dict[pathlib.Path, float] = {}
    by_key: dict[str, list[dict]] = {}
    clean50: set[str] = set()  # operations whose 50/s measurement had nothing to report
    limits: dict[str, int] = {}  # expected limits by category
    found, started = 0, time.time()
    last_progress = last_summary = time.time()
    if from_now:
        for f in sorted(root.glob("**/result.json"), key=lambda p: p.stat().st_mtime):
            seen[f] = f.stat().st_mtime
            try:
                o = json.loads(f.read_text())
            except (ValueError, OSError):
                continue
            run, k = key(f, root)
            if f.parent.name == "50" and not anomalies(o, "50", [], f.parent.parent.name):
                clean50.add(k.rsplit("/", 1)[0])
            by_key.setdefault(k, []).append(figures(o))
        for f in [*root.glob("**/idle.json"), *root.glob("**/failed.txt")]:
            seen[f] = f.stat().st_mtime
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
                problems = anomalies(o, f.parent.name, by_key.get(k, []), f.parent.parent.name)
                if f.parent.name == "50":
                    (clean50.discard if problems else clean50.add)(k.rsplit("/", 1)[0])
                if problems:
                    if category := expected_limit(o, f.parent.name, problems, k.rsplit("/", 1)[0] in clean50):
                        limits[category] = limits.get(category, 0) + 1
                    else:
                        found += 1
                        print(f"ANOMALY {run}/{k}: {'; '.join(problems)}", flush=True)
                by_key.setdefault(k, []).append(figures(o))
            last_progress = time.time()
        now = time.time()
        if not campaign_running():
            done = len([p for p in seen if p.name != "failed.txt"])
            failed = [str(p.parent.relative_to(root)) for p in seen if p.name == "failed.txt"]
            print(f"ENDED: the campaign process is gone; {done}/{total} measurements, failed {failed}, "
                  f"anomalies {found}, expected limits {limits or 'none'}", flush=True)
            return 0
        if now - last_progress > STALL:
            ps = subprocess.run(["docker", "ps", "--format", "{{.Names}} {{.Status}}"], capture_output=True, text=True)
            cells = [x for x in ps.stdout.splitlines() if x.startswith("brickkit-d-")]
            print(f"STALL: no progress for {int((now - last_progress) / 60)} min; containers {cells}", flush=True)
            last_progress = now  # said again after another stall period, not every poll
        if now - last_summary > SUMMARY:
            print(summary(root, seen, total, started, found) + f"; expected limits {limits or 'none'}", flush=True)
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
    sys.exit(watch(sys.argv[1], sys.argv[2], "--from-now" in sys.argv[3:]))
