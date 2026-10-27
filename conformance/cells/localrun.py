"""Processes on this machine (step 9): in the L row every component and shell, in the LD and LP rows the named ones
(the rest in Docker or Podman), run as processes from their cloned source (BrickKit's mode: local), watched by a
`brickkit up` session in the foreground, which starts the containers too. This module starts that
session in the background and waits for the processes, stops it (SIGINT, as Ctrl+C), forwards the host ports the
other rows use (10000 + port, where Docker's own proxy sits) to the processes' own ports, and finds each process for
the sampler.

python -m cells.localrun <up|down|forward> <cell directory> [log]"""
from __future__ import annotations

import asyncio
import os
import pathlib
import re
import signal
import subprocess
import sys
import time
import urllib.request

from cells.spec import PORTS, SHELL_PORTS, Cell
from measure import readings

SESSION = re.compile(r"(^|/)brickkit$")


def sessions() -> list[int]:
    """The pids of running `brickkit up` sessions."""
    out = []
    for p in pathlib.Path("/proc").iterdir():
        if p.name.isdigit():
            try:
                argv = (p / "cmdline").read_bytes().split(b"\0")
            except (FileNotFoundError, PermissionError, ProcessLookupError):
                continue
            if len(argv) > 1 and SESSION.search(argv[0].decode(errors="replace")) and argv[1] == b"up":
                out.append(int(p.name))
    return out


def ports(c: Cell) -> dict[str, int]:
    """Each process on this machine and its own port: a local shell's, or a component's when it runs alone (members
    listen in their shell's process on their own ports too). The rest of an LD or LP cell is in containers."""
    out = {s: SHELL_PORTS[s] for s in c.shells if s in c.local}
    out |= {m: PORTS[m][0] for s in out.copy() for m in c.shells[s]}
    out |= {p: PORTS[p][0] for p in c.alone if p in c.local}
    return out


def healthy(port: int) -> bool:
    try:
        with urllib.request.urlopen(f"http://127.0.0.1:{port}/healthz", timeout=2) as r:
            return r.status == 200
    except OSError:
        return False


def down(timeout: float = 60) -> None:
    for pid in sessions():
        os.kill(pid, signal.SIGINT)
    end = time.monotonic() + timeout
    while sessions() and time.monotonic() < end:
        time.sleep(1)
    for pid in sessions():
        os.kill(pid, signal.SIGKILL)


def up(directory: str | pathlib.Path, log: str | pathlib.Path, timeout: float = 900) -> None:
    """A new session in the background (an earlier one stops first); returns once every process answers /healthz.
    The first start compiles every Go process (go run), so the timeout is long."""
    c = Cell.load(directory)
    down()
    with open(log, "a") as f:
        subprocess.Popen(["brickkit", "up"], cwd=c.dir, stdout=f, stderr=subprocess.STDOUT, stdin=subprocess.DEVNULL,
                         start_new_session=True)
    end = time.monotonic() + timeout
    # the processes on their own ports; in LD and LP the containers too, on their host ports
    want = ports(c) | {x: c.host_port(x) for x in [*c.shells, *c.alone] if x not in c.local}
    while time.monotonic() < end:
        if not sessions():
            raise RuntimeError(f"brickkit up ended before every process was healthy; see {log}")
        if all(healthy(p) for p in want.values()):
            return
        time.sleep(2)
    raise RuntimeError(f"not healthy after {timeout} s: {[k for k, p in want.items() if not healthy(p)]}")


async def forward(c: Cell) -> None:
    """Relays host port 10000 + port to the process on port, for every component and shell, until killed."""
    async def relay(reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        try:
            while data := await reader.read(65536):
                writer.write(data)
                await writer.drain()
        except (ConnectionError, OSError):
            pass
        finally:
            writer.close()

    def handler(port: int):
        async def handle(r: asyncio.StreamReader, w: asyncio.StreamWriter) -> None:
            try:
                ur, uw = await asyncio.open_connection("127.0.0.1", port)
            except OSError:
                w.close()
                return
            await asyncio.gather(relay(r, uw), relay(ur, w))
        return handle

    servers = [await asyncio.start_server(handler(p), "127.0.0.1", 10000 + p) for p in ports(c).values()]
    await asyncio.gather(*(s.serve_forever() for s in servers))


def listener(port: int) -> int | None:
    """The pid listening on a TCP port on this machine (IPv4 or IPv6)."""
    inodes = set()
    for table in ("/proc/net/tcp", "/proc/net/tcp6"):
        for line in pathlib.Path(table).read_text().splitlines()[1:]:
            f = line.split()
            if f[3] == "0A" and int(f[1].rsplit(":", 1)[1], 16) == port:
                inodes.add(f[9])
    for p in pathlib.Path("/proc").iterdir():
        if not p.name.isdigit():
            continue
        try:
            for fd in (p / "fd").iterdir():
                link = os.readlink(fd)
                if link.startswith("socket:[") and link[8:-1] in inodes:
                    return int(p.name)
        except (FileNotFoundError, PermissionError, ProcessLookupError):
            continue
    return None


def children(pid: int) -> list[int]:
    out = [pid]
    for p in pathlib.Path("/proc").iterdir():
        if p.name.isdigit():
            try:
                ppid = int(re.search(r"PPid:\s+(\d+)", (p / "status").read_text()).group(1))
            except (FileNotFoundError, ProcessLookupError, AttributeError):
                continue
            if ppid == pid:
                out += children(int(p.name))
    return out


def parent(pid: int) -> int:
    return int(re.search(r"PPid:\s+(\d+)", pathlib.Path(f"/proc/{pid}/status").read_text()).group(1))


def mem(pid: int) -> tuple[int, int]:
    """VmRSS and RssAnon + RssShmem of a process, bytes."""
    s = pathlib.Path(f"/proc/{pid}/status").read_text()

    def kb(k: str) -> int:
        m = re.search(rf"{k}:\s+(\d+) kB", s)
        return int(m.group(1)) * 1024 if m else 0
    return kb("VmRSS"), kb("RssAnon") + kb("RssShmem")


class ProcUnit:
    """A set of processes read as one: memory and anon summed, CPU summed (with waited-for children)."""

    def __init__(self, pids: list[int]):
        self.pids = pids

    def read(self) -> dict | None:
        memory = anon = 0
        cpu = 0.0
        alive = 0
        for pid in self.pids:
            p = readings.proc(pid)
            if p is None:
                continue
            try:
                r, a = mem(pid)
            except FileNotFoundError:
                continue
            memory, anon, cpu, alive = memory + r, anon + a, cpu + p.cpu, alive + 1
        return {"memory": memory, "anon": anon, "cpu": cpu} if alive else None


def local_units(c: Cell) -> dict:
    """Per process: the program serving the port and its children (uv's python); the wrappers between it and the
    session (go run, uv) and the session itself are this row's platform."""
    units, wrappers = {}, []
    session = set(sessions())
    for name, port in ports(c).items():
        if name in c.placed and any(name in ms for ms in c.shells.values()):
            continue  # a member: inside its shell's process
        pid = listener(port)
        if pid is None:
            raise RuntimeError(f"nothing listens on port {port} ({name})")
        units[f"proc:{name}"] = ProcUnit(children(pid))
        p = parent(pid)
        while p > 1 and p not in session:
            wrappers.append(p)
            p = parent(p)
    units["local-platform"] = ProcUnit(sorted(set(wrappers) | session))
    return units


if __name__ == "__main__":
    cmd, d = sys.argv[1], sys.argv[2]
    if cmd == "up":
        up(d, sys.argv[3])
    elif cmd == "down":
        down()
    elif cmd == "forward":
        asyncio.run(forward(Cell.load(d)))
