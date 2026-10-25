"""A sampler over a window and the figures the experiment reports: per container (the component processes it runs,
and its cgroup), the base resources, the platform; memory as median and peak, CPU as cores used over the window and
as CPU seconds per thousand requests when the window's request count is given."""
from __future__ import annotations

import asyncio
import statistics
import time
from dataclasses import dataclass, field

import asyncpg

from measure import readings

# the base resources read when they run; object storage is RustFS by default or SeaweedFS in its place
BASE = ("be-postgres", "be-nats", "be-rustfs", "be-seaweedfs")


@dataclass
class Window:
    containers: list[str]
    interval: float = 1.0
    # other things measured the same way (memory, anon memory, cumulative CPU): processes on this machine (mode:
    # local), Kubernetes pods, a cluster's own overhead; each has read() -> {"memory", "anon", "cpu"} or None
    units: dict = field(default_factory=dict)
    samples: list[dict] = field(default_factory=list)
    connections: list[dict] = field(default_factory=list)

    async def run(self, seconds: float, pg_dsn: str | None = None) -> None:
        end = time.monotonic() + seconds
        while True:
            self.samples.append(self.read())
            if pg_dsn:
                self.connections.append(await connections(pg_dsn))
            left = end - time.monotonic()
            if left <= 0:
                return
            await asyncio.sleep(min(self.interval, left))

    async def run_until(self, stop: asyncio.Event, pg_dsn: str | None = None) -> None:
        """Samples until stop is set (always at least two samples)."""
        while True:
            self.samples.append(self.read())
            if pg_dsn:
                self.connections.append(await connections(pg_dsn))
            if stop.is_set() and len(self.samples) >= 2:
                return
            try:
                await asyncio.wait_for(stop.wait(), self.interval)
            except TimeoutError:
                pass

    def read(self) -> dict:
        out = {"t": time.monotonic(), "containers": {}, "platform": {}}
        for name in [*self.containers, *BASE]:
            c = readings.container(name)
            if c:
                out["containers"][name] = {"memory": c.memory, "anon": c.anon, "cpu": c.cpu, "rss": c.rss,
                                           "proc_cpu": c.proc_cpu, "procs": len(c.procs)}
        out["units"] = {k: u.read() for k, u in self.units.items()}
        for engine, plat in readings.platforms().items():
            out["platforms"] = out.get("platforms", {})
            out["platforms"][engine] = {"rss": sum(p.rss for p in plat), "cpu": sum(p.cpu for p in plat),
                                        "procs": len(plat)}
        out["platform"] = out["platforms"]["docker"]
        return out

    def figures(self, requests: int = 0) -> dict:
        """Memory median and peak, CPU cores and (with requests) CPU seconds per 1000 requests, per container, for the
        base resources and the platform."""
        if len(self.samples) < 2:
            raise ValueError("a window needs at least two samples")
        first, last = self.samples[0], self.samples[-1]
        elapsed = last["t"] - first["t"]

        def fig(series_mem: list[int], cpu0: float, cpu1: float, anon: list[int] | None = None) -> dict:
            cpu = cpu1 - cpu0
            f = {"memory_median": statistics.median(series_mem), "memory_peak": max(series_mem),
                 "cpu_seconds": cpu, "cores": cpu / elapsed}
            if anon:
                f["anon_median"], f["anon_peak"] = statistics.median(anon), max(anon)
            if requests:
                f["cpu_seconds_per_1000"] = cpu / requests * 1000
            return f

        out = {"elapsed": elapsed, "requests": requests, "containers": {}}
        for name in first["containers"]:
            rows = [s["containers"][name] for s in self.samples if name in s["containers"]]
            if len(rows) < 2:
                continue
            out["containers"][name] = {
                "process": fig([r["rss"] for r in rows], rows[0]["proc_cpu"], rows[-1]["proc_cpu"]),
                "cgroup": fig([r["memory"] for r in rows], rows[0]["cpu"], rows[-1]["cpu"],
                              [r["anon"] for r in rows if "anon" in r])}
        out["platform"] = fig([s["platform"]["rss"] for s in self.samples], first["platform"]["cpu"],
                              last["platform"]["cpu"])
        out["platforms"] = {e: fig([s["platforms"][e]["rss"] for s in self.samples], first["platforms"][e]["cpu"],
                                   last["platforms"][e]["cpu"]) for e in first.get("platforms", {})}
        for k in first.get("units", {}):
            rows = [s["units"][k] for s in self.samples if s["units"].get(k)]
            if len(rows) >= 2:
                out.setdefault("units", {})[k] = fig([r["memory"] for r in rows], rows[0]["cpu"], rows[-1]["cpu"],
                                                     [r["anon"] for r in rows])
        if self.connections:
            roles = {r for c in self.connections for r in c}
            out["db_connections"] = {r: {"median": statistics.median(c.get(r, 0) for c in self.connections),
                                         "peak": max(c.get(r, 0) for c in self.connections)} for r in sorted(roles)}
        return out


async def connections(dsn: str) -> dict[str, int]:
    """Connections per role (pg_stat_activity), the sampling session excluded."""
    c = await asyncpg.connect(dsn)
    try:
        rows = await c.fetch("SELECT usename, count(*) AS n FROM pg_stat_activity WHERE usename IS NOT NULL "
                             "AND pid <> pg_backend_pid() GROUP BY usename")
    finally:
        await c.close()
    return {r["usename"]: r["n"] for r in rows}
