"""The resource readings against known loads (step 4, T4.4): a container that burns 3 s of CPU and one that holds
200 MiB must read as such, in both the process and the cgroup figures; the platform's processes are found; a durable
nobody runs shows its pending messages and their age."""
import asyncio
import os
import subprocess
import uuid

import nats as natslib
import pytest

from besdk.events import jetstream
from besdk.events.core import Stream, SubscriptionSpec
from measure.backlog import jetstream as backlog
from measure.series import Window


def start(image: str, *cmd: str) -> str:
    name = "measure-" + uuid.uuid4().hex[:8]
    subprocess.run(["docker", "run", "-d", "--rm", "--name", name, image, *cmd], check=True, capture_output=True)
    return name


@pytest.fixture
def cleanup():
    names = []
    yield names
    for n in names:
        subprocess.run(["docker", "rm", "-f", n], capture_output=True)


async def test_cpu_reads_as_burnt(cleanup):
    # exactly 3 s of CPU in one process (a shell loop on `date +%s` burns anywhere from 2 to 3 s)
    name = start("python:3.15.0-alpine", "python", "-c",
                 "import time; time.sleep(1); e = time.process_time() + 3\nwhile time.process_time() < e: pass\n"
                 "time.sleep(60)")
    cleanup.append(name)
    w = Window([name], interval=0.5)
    await w.run(7)
    f = w.figures(requests=1000)["containers"][name]
    for kind in ("process", "cgroup"):
        assert 2.9 <= f[kind]["cpu_seconds"] <= 3.3, (kind, f[kind])
    assert abs(f["process"]["cpu_seconds_per_1000"] - f["process"]["cpu_seconds"]) < 1e-9


async def test_memory_reads_as_held(cleanup):
    name = start("python:3.15.0-alpine", "python", "-c",
                 "b = bytearray(200 * 1024 * 1024); import time; time.sleep(60)")
    cleanup.append(name)
    await asyncio.sleep(3)
    w = Window([name], interval=0.5)
    await w.run(2)
    f = w.figures()["containers"][name]
    mib = 1024 * 1024
    assert 200 * mib <= f["process"]["memory_peak"] <= 240 * mib, f["process"]
    # the 200 MiB are anonymous memory, charged to the container's cgroup; the process's RSS also counts shared file
    # pages (the interpreter and libraries) that may be charged elsewhere, so neither figure bounds the other
    assert 200 * mib <= f["cgroup"]["memory_peak"] <= 260 * mib, f["cgroup"]


async def test_platform_processes_are_counted():
    w = Window([])
    await w.run(1.5)
    f = w.figures()
    assert f["platform"]["memory_median"] > 0 and w.samples[-1]["platform"]["procs"] >= 2


async def test_backlog_counts_what_no_one_acknowledged():
    url, stream = os.environ["BE_ACC_NATS_URL"], "test.backlog_" + uuid.uuid4().hex[:6]
    tr = await jetstream.Transport.connect(url, "backlog-test")
    try:
        await tr.ensure_stream(Stream(stream, 1))
        sub = await tr.subscribe(SubscriptionSpec("idle", [stream]), lambda d: asyncio.sleep(3600))
        await sub.stop()  # a durable that is not running: everything stays pending
        nc = await natslib.connect(url)
        js = nc.jetstream()
        for i in range(3):
            await js.publish(f"be.{stream}.0.demo.thing.v1", b"{}")
        await nc.close()
        await asyncio.sleep(1.2)
        got = (await backlog(url, [stream]))[stream]["idle"]
        assert got["pending"] == 3 and got["oldest_age"] >= 1.0, got
    finally:
        await tr.delete_stream(stream)
        await tr.close()


def test_a_platform_counts_only_the_hosts_own_processes():
    """A container platform's processes are the host's own; the same names inside a container (a minikube node runs
    its own dockerd, containerd and shims) are that container's, counted with it, never twice (step 9, 9-01)."""
    from measure.readings import on_host
    node = "/system.slice/docker-" + "a" * 64 + ".scope"
    assert on_host("/system.slice/docker.service")
    assert on_host("/system.slice/containerd.service")
    assert on_host("/user.slice/user-1000.slice/user@1000.service/user.slice/libpod-conmon-" + "b" * 64 + ".scope")
    assert not on_host(node + "/system.slice/docker.service"), "the node's own dockerd"
    assert not on_host(node + "/kubepods/burstable/pod1/" + "c" * 64), "a pod's process"
    assert not on_host("/user.slice/user-1000.slice/user@1000.service/user.slice/libpod-" + "d" * 64 + ".scope/container")


def test_a_docker_container_is_found_where_its_process_is(tmp_path, monkeypatch):
    """Docker's containers may sit under another slice than system.slice: with the daemon's cgroup-parent set to
    be-cap.slice (step 10, B3 and B4) they are under /be.slice/be-cap.slice; the scope comes from the main process's
    own cgroup, not from a fixed path."""
    from measure import readings
    cid = "e" * 64
    scope = tmp_path / "be.slice" / "be-cap.slice" / f"docker-{cid}.scope"
    scope.mkdir(parents=True)
    monkeypatch.setattr(readings, "CGROUP", tmp_path)
    monkeypatch.setattr(readings, "cgroup_path", lambda pid: f"/be.slice/be-cap.slice/docker-{cid}.scope")
    monkeypatch.setattr(readings, "_found", {})

    def run(cmd, **kw):  # only Docker knows the container
        ok = cmd[0] == "docker"
        return subprocess.CompletedProcess(cmd, 0 if ok else 125, f"{cid} 4242\n" if ok else "", "")
    monkeypatch.setattr(readings.subprocess, "run", run)
    assert readings.find("be-postgres") == (cid, scope)


def test_a_container_whose_init_moved_itself_is_found_at_its_scope(tmp_path, monkeypatch):
    """The minikube node's main process is systemd, which moves itself into init.scope inside the container's cgroup;
    the container is the docker-<id>.scope above it, where the node's pods are (step 10, B7 smoke)."""
    from measure import readings
    cid = "f" * 64
    scope = tmp_path / "be.slice" / "be-cap.slice" / f"docker-{cid}.scope"
    (scope / "init.scope").mkdir(parents=True)
    monkeypatch.setattr(readings, "CGROUP", tmp_path)
    monkeypatch.setattr(readings, "cgroup_path", lambda pid: f"/be.slice/be-cap.slice/docker-{cid}.scope/init.scope")
    monkeypatch.setattr(readings, "_found", {})

    def run(cmd, **kw):
        ok = cmd[0] == "docker"
        return subprocess.CompletedProcess(cmd, 0 if ok else 125, f"{cid} 4242\n" if ok else "", "")
    monkeypatch.setattr(readings.subprocess, "run", run)
    assert readings.find("bek") == (cid, scope)
