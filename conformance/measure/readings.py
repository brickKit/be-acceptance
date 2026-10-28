"""One reading of the machine: every process's resident memory and CPU time from /proc, every container's cgroup
memory and CPU from /sys/fs/cgroup, and which processes are the container platform's. Readings are cumulative CPU and
instantaneous memory; series.py turns a series of them into figures. A container is looked up in Docker, then in
Podman (rootless, cgroup v2); its cgroup is the one its main process is in."""
from __future__ import annotations

import os
import pathlib
import re
import subprocess
from dataclasses import dataclass, field

TICK = os.sysconf("SC_CLK_TCK")
CGROUP = pathlib.Path("/sys/fs/cgroup")
# the processes of each container platform itself. Docker: daemons, one shim per container, one proxy per published
# port. Podman (rootless): one monitor per container, the user-mode network (pasta, or slirp4netns), the port
# forwarder, the network and DNS helpers; podman itself only while a command runs.
PLATFORMS = {
    "docker": re.compile(r"^(dockerd|containerd|containerd-shim(-runc-v2)?|docker-proxy)$"),
    "podman": re.compile(r"^(conmon|pasta(\.avx2)?|slirp4netns|rootlessport(-child)?|netavark|aardvark-dns|podman|catatonit)$"),
}
PLATFORM = PLATFORMS["docker"]


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
    # cgroup memory.stat anon + shmem, bytes: the memory the processes hold (shared memory included: PostgreSQL's
    # buffers are shmem), without the page cache that memory.current also counts
    anon: int = 0
    procs: list[Proc] = field(default_factory=list)

    @property
    def rss(self) -> int:
        return sum(p.rss for p in self.procs)

    @property
    def proc_cpu(self) -> float:
        return sum(p.cpu for p in self.procs)


_found: dict[str, tuple[str, pathlib.Path]] = {}


def cgroup_of(pid: int) -> pathlib.Path | None:
    """The cgroup v2 directory a process is in; a container's processes may sit in a leaf below its scope (crun puts
    them in "container"), so the scope is taken when the leaf has that name."""
    rel = cgroup_path(pid)
    if not rel:
        return None
    d = CGROUP / rel.lstrip("/")
    return d.parent if d.name == "container" else d


def find(name: str) -> tuple[str, pathlib.Path] | None:
    """A container's ID and cgroup directory, from Docker or Podman; remembered while the container lives. The
    directory is where the container's main process is, so a container under another slice than system.slice (Docker's
    cgroup-parent, step 10 B3 and B4) is found as well."""
    if name in _found and _found[name][1].exists():
        return _found[name]
    for engine in ("docker", "podman"):
        r = subprocess.run([engine, "inspect", "-f", "{{.Id}} {{.State.Pid}}", name], capture_output=True, text=True)
        parts = r.stdout.split()
        if r.returncode == 0 and len(parts) == 2 and parts[1] != "0":
            d = cgroup_of(int(parts[1]))
            if d and d.exists():
                _found[name] = (parts[0], d)
                return _found[name]
    return None


def cgroup(name: str, cid: str, d: pathlib.Path) -> Container | None:
    try:
        memory = int((d / "memory.current").read_text())
        stat = (d / "memory.stat").read_text()
        anon = sum(int(re.search(rf"^{k} (\d+)", stat, re.M).group(1)) for k in ("anon", "shmem"))
        usage = int(re.search(r"usage_usec (\d+)", (d / "cpu.stat").read_text()).group(1)) / 1e6
        pids = [int(x) for x in (d / "cgroup.procs").read_text().split()]
        for sub in d.iterdir():  # a scope's own leaves (Podman's "container")
            if sub.is_dir() and (sub / "cgroup.procs").exists():
                pids += [int(x) for x in (sub / "cgroup.procs").read_text().split()]
    except (FileNotFoundError, AttributeError):
        return None
    return Container(name, cid, memory, usage, anon, [p for p in map(proc, pids) if p])


def container(name: str) -> Container | None:
    f = find(name)
    return cgroup(name, *f) if f else None


IN_CONTAINER = re.compile(r"/(docker|libpod)-[0-9a-f]{64}\.scope(/|$)|/kubepods")


def on_host(cgroup_path: str) -> bool:
    """Whether a process with this cgroup path is the host's own, not inside a container: a minikube node runs its
    own dockerd, containerd and shims under the node's scope, already counted with the node (Podman's conmon sits in
    libpod-conmon-<id>.scope, on the host)."""
    return not IN_CONTAINER.search(cgroup_path)


def platform(engine: str = "docker") -> list[Proc]:
    return platforms()[engine]


def platforms() -> dict[str, list[Proc]]:
    """Each container platform's own processes on the host, in one pass over /proc."""
    out: dict[str, list[Proc]] = {k: [] for k in PLATFORMS}
    for entry in pathlib.Path("/proc").iterdir():
        if entry.name.isdigit():
            p = proc(int(entry.name))
            if p:
                for k, rx in PLATFORMS.items():
                    if rx.match(p.name) and on_host(cgroup_path(p.pid)):
                        out[k].append(p)
    return out


def cgroup_path(pid: int) -> str:
    try:
        return pathlib.Path(f"/proc/{pid}/cgroup").read_text().strip().split("::", 1)[1]
    except (FileNotFoundError, IndexError, ProcessLookupError):
        return ""
