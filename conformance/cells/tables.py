"""Turns a campaign's raw results (<out>/run<N>/<cell>/...) into the record's tables (Markdown): the process base
costs with each shell's increment per member, and for every path, operation and load the latency of the path (the
hop the caller measured, or the write's completion), throughput, sustained rate and CPU per thousand requests of
caller and callee. Every figure is the median over the repetitions, with the range when there are several."""
from __future__ import annotations

import json
import pathlib
import statistics

MIB = 1024 * 1024


def med(xs: list[float], unit: float = 1.0, digits: int = 1) -> str:
    xs = [x / unit for x in xs if x is not None]
    if not xs:
        return "—"
    m = statistics.median(xs)
    if len(xs) == 1 or max(xs) - min(xs) < 10 ** -digits:
        return f"{m:.{digits}f}"
    return f"{m:.{digits}f} ({min(xs):.{digits}f}–{max(xs):.{digits}f})"


PLATFORM_UNITS = ("minikube-node", "local-platform")


def readings(res: dict) -> dict[str, dict]:
    """Every component reading of a sample, by name: a container's process figures (D, P, and the containers of LD
    and LP), a pod's cgroup (K), a process on this machine (L, and the local part of LD and LP); the base resources
    (be-*) and the platforms' units are left out."""
    out = {k: v["process"] for k, v in res.get("containers", {}).items() if not k.startswith("be-")}
    out |= {k: v for k, v in (res.get("units") or {}).items() if k not in PLATFORM_UNITS}
    return out


def platform_of(row: str, res: dict) -> tuple[float, float]:
    """The row's own platform (memory, cores): Docker's daemons (D, LD), Podman's helpers (P, LP), the minikube node
    minus the pods (K), the go run / uv wrappers (L); the processes' readings, no page cache (cells/r9-summary.py in
    the assembly project splits the same way)."""
    plats, units = res.get("platforms") or {"docker": res["platform"]}, res.get("units") or {}
    if row == "K":
        pods = [v for k, v in units.items() if k.startswith("pod:")]
        node = units["minikube-node"]
        return (node["memory_median"] - sum(v["memory_median"] for v in pods),
                node["cores"] - sum(v["cores"] for v in pods))
    if row == "L":
        u = units["local-platform"]
        return (u["memory_median"], u["cores"])
    v = plats["podman" if row in ("P", "LP") else "docker"]
    return (v["memory_median"], v["cores"])


def runs(root: pathlib.Path) -> list[pathlib.Path]:
    return sorted(p for p in root.iterdir() if p.is_dir() and p.name.startswith("run"))


def base_costs(root: pathlib.Path) -> str:
    rows, shells = [], {}
    cells = sorted({c.name for r in runs(root) for c in r.iterdir() if "-base-" in c.name})
    for cell in cells:
        per: dict[str, dict[str, list[float]]] = {}
        for r in runs(root):
            f = r / cell / "idle.json"
            if not f.exists():
                continue
            res = json.loads(f.read_text())["resources"]
            for name, v in readings(res).items():  # base resources: reported with the whole stack
                d = per.setdefault(name, {"rss": [], "cores": [], "cg": []})
                d["rss"].append(v["memory_median"])
                d["cores"].append(v["cores"])
                d["cg"].append(res["containers"][name]["cgroup"]["memory_median"] if name in res["containers"] else None)
        for name, d in per.items():
            rows.append(f"| {cell} | `{name}` | {med(d['rss'], MIB)} | {med(d['cg'], MIB)} | {med(d['cores'], 1, 3)} |")
            if "shell" in cell:
                kind, n = cell.rsplit("-", 1)
                shells.setdefault(kind, {})[int(n)] = statistics.median(d["rss"]) / MIB
    out = ["| 格 | 进程 | 常驻内存 MiB | cgroup 内存 MiB | 空闲 CPU 核 |", "| --- | --- | --- | --- | --- |", *rows]
    if shells:
        out += ["", "| 外壳 | 承载 | 常驻内存 MiB | 比上一档每多一个成员 MiB |", "| --- | --- | --- | --- |"]
        for kind, by in sorted(shells.items()):
            prev = None
            for n in sorted(by):
                inc = "—" if prev is None else f"{(by[n] - by[prev]) / (n - prev):.1f}"
                out.append(f"| {kind.replace('-base-', ' ')} | {n} | {by[n]:.1f} | {inc} |")
                prev = n
    return "\n".join(out)


def paths(root: pathlib.Path) -> str:
    table: dict[tuple, dict[str, list]] = {}
    for r in runs(root):
        for f in r.glob("*/*/*/*/result.json"):
            o = json.loads(f.read_text())
            key = (o["cell"], o["caller"], o["callee"], o["op"], o["load"])
            d = table.setdefault(key, {k: [] for k in ("p50", "p99", "thr", "sus", "cpu_caller", "cpu_callee", "err")})
            drv = o["driver"]
            if "steps" in drv:
                d["sus"].append(drv.get("sustained_rate"))
            else:
                lat = (o.get("completion") or {}).get("relayed" if o["op"] == "relay" else "received") or {}
                if lat.get("n"):
                    d["p50"].append(lat["p50_ms"]), d["p99"].append(lat["p99_ms"])
                elif drv.get("hop_ms"):
                    d["p50"].append(drv["hop_ms"]["P50"]), d["p99"].append(drv["hop_ms"]["P99"])
                d["thr"].append(drv.get("throughput")), d["err"].append(drv.get("error_rate"))
            res, where = readings(o["resources"]), o["containers"]
            # in one process (both in one shell) the CPU cannot be split: it is reported once, as the caller's
            for role, comp in (("cpu_caller", o["caller"]), ("cpu_callee", o["callee"])):
                ct = next(c for c, ms in where.items() if comp in ms)
                if ct in res and (role == "cpu_caller" or len(where) == 2):
                    d[role].append(res[ct].get("cpu_seconds_per_1000"))
    out = ["| 格 | 路径 | 操作 | 负载 | p50 ms | p99 ms | 吞吐 /s | 持续速率 /s | 每千次 CPU 秒（调用方 / 被调方） | 错误率 |",
           "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |"]
    for (cell, caller, callee, op, load), d in sorted(table.items()):
        callee_cpu = med(d["cpu_callee"], 1, 2) if d["cpu_callee"] else "同一进程"
        out.append(f"| {cell} | {caller.split('/')[1]} → {callee.split('/')[1]} | {op} | {load} | {med(d['p50'], 1, 2)} | "
                   f"{med(d['p99'], 1, 2)} | {med(d['thr'], 1, 0)} | {med(d['sus'], 1, 0)} | "
                   f"{med(d['cpu_caller'], 1, 2)} / {callee_cpu} | {med(d['err'], 1, 3)} |")
    return "\n".join(out)


def stack(root: pathlib.Path) -> str:
    """The whole stack at idle per cell: components, base resources, the platform (experiment plan, "整套")."""
    out = ["| 格 | 组件 MiB / 核 | 基础资源 MiB / 核 | 平台 MiB / 核 | 数据库连接（峰值） |", "| --- | --- | --- | --- | --- |"]
    cells = sorted({c.name for r in runs(root) for c in r.iterdir() if c.is_dir()})
    for cell in cells:
        comp, base, plat, conns = [], [], [], []
        for r in runs(root):
            f = r / cell / "idle.json"
            if not f.exists():
                continue
            res = json.loads(f.read_text())["resources"]
            cs, mine = res["containers"], readings(res).values()
            comp.append((sum(v["memory_median"] for v in mine), sum(v["cores"] for v in mine)))
            base.append((sum(v["cgroup"]["memory_median"] for k, v in cs.items() if k.startswith("be-")),
                         sum(v["cgroup"]["cores"] for k, v in cs.items() if k.startswith("be-"))))
            plat.append(platform_of(json.loads(f.read_text()).get("row", "D"), res))
            conns.append(sum(v["peak"] for k, v in (res.get("db_connections") or {}).items() if k != "postgres"))
        if comp:
            fmt = lambda xs: f"{med([x[0] for x in xs], MIB, 0)} / {med([x[1] for x in xs], 1, 3)}"  # noqa: E731
            out.append(f"| {cell} | {fmt(comp)} | {fmt(base)} | {fmt(plat)} | {med(conns, 1, 0)} |")
    return "\n".join(out)


def all_tables(root: str) -> str:
    r = pathlib.Path(root)
    return "\n\n".join([base_costs(r), stack(r), paths(r)]) + "\n"
