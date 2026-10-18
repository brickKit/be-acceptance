"""One reading of the machine: every process's resident memory and CPU time from /proc, every container's cgroup
memory and CPU from /sys/fs/cgroup, and which processes are the container platform's. Readings are cumulative CPU and
instantaneous memory; series.py turns a series of them into figures."""
from __future__ import annotations

import os
import pathlib
import re
import subprocess
from dataclasses import dataclass, field

TICK = os.sysconf("SC_CLK_TCK")
CGROUP = pathlib.Path("/sys/fs/cgroup")
# the processes of the Docker platform itself: daemons, one shim per container, one proxy per published port
PLATFORM = re.compile(r"^(dockerd|containerd|containerd-shim(-runc-v2)?|docker-proxy)$")


@dataclass(frozen=True)
class Proc:
    pid: int
    name: str
    rss: int  # bytes
    cpu: float  # seconds of user + system time so far, the waited-for children's included


def proc(pid: int) -> Proc | None:
    try:
        status = pathlib.Path(f"/proc/{pid}/status").read_text()
        stat = pathlib.Path(f"/proc/{pid}/stat").read_text()
    except (FileNotFoundError, ProcessLookupError, PermissionError):
        return None
    rss = int(re.search(r"VmRSS:\s+(\d+) kB", status).group(1)) * 1024 if "VmRSS:" in status else 0
    name = re.search(r"Name:\s+(\S+)", status).group(1)
    fields = stat[stat.rindex(")") + 2:].split()  # after "pid (comm) "
    # utime + stime, plus cutime + cstime: the children this process has waited for (a shell running commands,
    # a worker pool) keep counting after they exit
    return Proc(pid, name, rss, sum(int(x) for x in fields[11:15]) / TICK)


@dataclass
class Container:
    name: str
    id: str
    memory: int  # cgroup memory.current, bytes (includes page cache)
    cpu: float  # cgroup cpu.stat usage, seconds
    procs: list[Proc] = field(default_factory=list)

    @property
    def rss(self) -> int:
        return sum(p.rss for p in self.procs)

    @property
    def proc_cpu(self) -> float:
        return sum(p.cpu for p in self.procs)


def container(name: str) -> Container | None:
    r = subprocess.run(["docker", "inspect", "-f", "{{.Id}}", name], capture_output=True, text=True)
    cid = r.stdout.strip()
    if r.returncode != 0 or not cid:
        return None
    d = CGROUP / "system.slice" / f"docker-{cid}.scope"
    try:
        memory = int((d / "memory.current").read_text())
        usage = int(re.search(r"usage_usec (\d+)", (d / "cpu.stat").read_text()).group(1)) / 1e6
        pids = [int(x) for x in (d / "cgroup.procs").read_text().split()]
    except FileNotFoundError:
        return None
    return Container(name, cid, memory, usage, [p for p in map(proc, pids) if p])


def platform() -> list[Proc]:
    out = []
    for entry in pathlib.Path("/proc").iterdir():
        if entry.name.isdigit():
            p = proc(int(entry.name))
            if p and PLATFORM.match(p.name):
                out.append(p)
    return out
