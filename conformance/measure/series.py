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

BASE = ("be-postgres", "be-nats", "be-rustfs")


@dataclass
class Window:
    containers: list[str]
    interval: float = 1.0
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
                out["containers"][name] = {"memory": c.memory, "cpu": c.cpu, "rss": c.rss, "proc_cpu": c.proc_cpu,
                                           "procs": len(c.procs)}
        plat = readings.platform()
        out["platform"] = {"rss": sum(p.rss for p in plat), "cpu": sum(p.cpu for p in plat), "procs": len(plat)}
        return out

    def figures(self, requests: int = 0) -> dict:
        """Memory median and peak, CPU cores and (with requests) CPU seconds per 1000 requests, per container, for the
        base resources and the platform."""
        if len(self.samples) < 2:
            raise ValueError("a window needs at least two samples")
        first, last = self.samples[0], self.samples[-1]
        elapsed = last["t"] - first["t"]

        def fig(series_mem: list[int], cpu0: float, cpu1: float) -> dict:
            cpu = cpu1 - cpu0
            f = {"memory_median": statistics.median(series_mem), "memory_peak": max(series_mem),
                 "cpu_seconds": cpu, "cores": cpu / elapsed}
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
                "cgroup": fig([r["memory"] for r in rows], rows[0]["cpu"], rows[-1]["cpu"])}
        out["platform"] = fig([s["platform"]["rss"] for s in self.samples], first["platform"]["cpu"],
                              last["platform"]["cpu"])
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
