"""Runs probes as real processes, as a deployment would: their databases provisioned, their certificates issued by a
test deployment (besdk.testing), the bus and object storage of the assembly's base resources, and each probe's
migrate entry before it serves. Needs BE_ACC_PG_ADMIN, BE_ACC_NATS_URL and BE_ACC_S3_URL/_ACCESS_KEY/_SECRET_KEY."""
from __future__ import annotations

import asyncio
import io
import os
import pathlib
import signal
import socket
import subprocess
import uuid
from dataclasses import dataclass, field
from urllib.parse import urlsplit

import asyncpg
import httpx

from besdk import obs, testing

ROOT = pathlib.Path(__file__).resolve().parent.parent
DATABASE = "be_acceptance"


def need(name: str) -> str:
    v = os.environ.get(name, "")
    if not v:
        raise AssertionError(f"{name} is not set: the probe tests run against real base resources")
    return v


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


@dataclass
class Spec:
    """One probe kind: its component ID, schema and roles (registry/schemas.tsv), stream and how to start it."""

    id: str
    schema: str
    stream: str
    grpc_port: int
    command: list[str]
    version: str  # metadata.version of its component.yaml, injected as COMPONENT_VERSION like BrickKit does

    @property
    def prefix(self) -> str:
        return self.id.replace("/", "_").replace("-", "_").upper()


def go_binary(out: pathlib.Path) -> list[str]:
    subprocess.run(["go", "build", "-o", str(out), "."], cwd=ROOT / "probes" / "go", check=True, timeout=600)
    return [str(out)]


def py_probe() -> list[str]:
    """The Python probe's locked environment, run with its own interpreter as the image does (no wrapper writing to
    its output)."""
    project = ROOT / "probes" / "py"
    subprocess.run(["uv", "sync", "--frozen", "--no-dev", "--project", str(project)], check=True, timeout=600,
                   capture_output=True, env={k: v for k, v in os.environ.items() if k != "VIRTUAL_ENV"})
    return [str(project / ".venv" / "bin" / "python"), "-m", "probe_py"]


@dataclass
class Running:
    spec: Spec
    url: str
    proc: subprocess.Popen
    log: pathlib.Path

    async def post(self, path: str, body: dict, timeout: float = 15) -> httpx.Response:
        async with httpx.AsyncClient(timeout=timeout) as c:
            return await c.post(self.url + path, json=body)

    async def get(self, path: str) -> httpx.Response:
        async with httpx.AsyncClient(timeout=15) as c:
            return await c.get(self.url + path)

    async def stop(self) -> int:
        self.proc.send_signal(signal.SIGTERM)
        return await asyncio.to_thread(self.proc.wait, 30)

    def logs(self) -> str:
        return self.log.read_text()


@dataclass
class Deployment:
    """The probes of one test: a world (certificates), a bus and their processes."""

    world: testing.World
    bus_url: str
    tmp: pathlib.Path
    running: list[Running] = field(default_factory=list)

    async def env(self, spec: Spec, extra: dict) -> dict:
        await ensure_bucket()
        db = await testing.provision(need("BE_ACC_PG_ADMIN"), DATABASE, spec.schema, spec.schema + "_owner",
                                     spec.schema + "_rw")
        return {**db, **self.world.env(spec.id), "COMPONENT_ID": spec.id, "COMPONENT_VERSION": spec.version,
                "BUS_URL": self.bus_url, "IMPLEMENTATION_INSTANCE": str(uuid.uuid4()), "STOP_DRAIN_DELAY": "100ms",
                "OBJECT_STORE_URL": need("BE_ACC_S3_URL"), "OBJECT_STORE_BUCKET": "be-acceptance",
                "OBJECT_STORE_PREFIX": spec.schema + "/", "OBJECT_STORE_ACCESS_KEY": need("BE_ACC_S3_ACCESS_KEY"),
                "OBJECT_STORE_SECRET_KEY": need("BE_ACC_S3_SECRET_KEY"), **extra}

    async def start(self, spec: Spec, env: dict, port: int = 0) -> Running:
        port = port or free_port()
        full = {**os.environ, **env, "PORT": str(port)}
        log = self.tmp / f"{spec.schema}.log"
        mig = await asyncio.to_thread(subprocess.run, [*spec.command, "migrate"], env=full, capture_output=True,
                                      text=True, timeout=120)
        assert mig.returncode == 0, mig.stdout + mig.stderr
        proc = subprocess.Popen(spec.command, env=full, stdout=log.open("w"), stderr=subprocess.STDOUT)
        r = Running(spec, f"http://127.0.0.1:{port}", proc, log)
        self.running.append(r)
        async with httpx.AsyncClient() as c:
            for _ in range(1500):
                if proc.poll() is not None:
                    raise AssertionError(f"{spec.id} exited {proc.returncode}: {r.logs()}")
                try:
                    if (await c.get(r.url + "/readyz")).status_code == 200:
                        return r
                except httpx.TransportError:
                    pass
                await asyncio.sleep(0.02)
        raise AssertionError(f"{spec.id} never ready: {r.logs()}")

    async def stop(self) -> None:
        codes = await asyncio.gather(*(r.stop() for r in self.running))
        bad = [(r.spec.id, c, r.logs()[-2000:]) for r, c in zip(self.running, codes) if c != 0]
        assert not bad, bad


def address(spec: Spec, port: int) -> dict:
    """A probe's next-probe configuration as a cell writes it with $endpoint: references (protocol P4.26): its
    component ID, user surface and system surface."""
    return {"PROBE_NEXT": spec.id, "PROBE_NEXT_URL": f"http://127.0.0.1:{port}",
            "PROBE_NEXT_GRPC_URL": f"http://127.0.0.1:{spec.grpc_port}"}


async def fresh_bus(kind: str, streams: list[str]) -> str:
    """A bus without earlier messages of these streams: JetStream with them deleted, or a new PostgreSQL queue."""
    if kind == "jetstream":
        from besdk.events import jetstream

        url = need("BE_ACC_NATS_URL")
        tr = await jetstream.Transport.connect(url, "acceptance-cleanup")
        try:
            for s in streams:
                try:
                    await tr.delete_stream(s)
                except Exception:  # noqa: BLE001 - not there yet
                    pass
        finally:
            await tr.close()
        return url
    from besdk.events import pgqueue

    admin = need("BE_ACC_PG_ADMIN")
    schema = "acc_bus_" + uuid.uuid4().hex[:8]
    c = await asyncpg.connect(admin)
    try:
        if not await c.fetchval("SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", DATABASE):
            await c.execute(f'CREATE DATABASE "{DATABASE}"')
    finally:
        await c.close()
    c = await asyncpg.connect(urlsplit(admin)._replace(path="/" + DATABASE).geturl())
    try:
        await c.execute(f"CREATE SCHEMA {schema}")
    finally:
        await c.close()
    url = urlsplit(admin)._replace(path="/" + DATABASE, scheme="pgqueue", query=f"schema={schema}&sslmode=disable").geturl()
    await pgqueue.migrate_bus(url, obs.new_logger(obs.Member("bus", "0"), "error", io.StringIO()))
    return url


async def ensure_bucket(name: str = "be-acceptance") -> None:
    """The object storage bucket of the probes' claim checks, provisioned as a deployment would."""
    import aioboto3

    async with aioboto3.Session().client("s3", endpoint_url=need("BE_ACC_S3_URL"), region_name="us-east-1",
                                         aws_access_key_id=need("BE_ACC_S3_ACCESS_KEY"),
                                         aws_secret_access_key=need("BE_ACC_S3_SECRET_KEY")) as s3:
        try:
            await s3.head_bucket(Bucket=name)
        except Exception:  # noqa: BLE001 - missing
            await s3.create_bucket(Bucket=name)


def manifest_version(kind: str) -> str:
    import yaml

    return yaml.safe_load((ROOT / "probes" / kind / "component.yaml").read_text())["metadata"]["version"]
