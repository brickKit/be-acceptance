"""Prepares a cell for `brickkit up`, as a deployment's provisioning would: the probes' databases and the shells'
login roles (fresh schemas every time), the deployment's certificates, the object storage bucket, a clean bus, and
every component's config file (the chain wired with $endpoint: references). Secrets go to the cell's .secrets/ and
are referenced from config; the object storage keys come from the environment (BE_ACC_S3_ACCESS_KEY, _SECRET_KEY)."""
from __future__ import annotations

import pathlib
import shutil
import uuid
from urllib.parse import urlsplit

import asyncpg

from besdk import testing
from cells.spec import SHELL_ROLES, Cell, file_of, schema_of, stream_of
from harness import ensure_bucket, need

HEADER = "# Written by be-acceptance/conformance/cells/prepare.py for cell {cell}; regenerate instead of editing.\n"


async def databases(c: Cell) -> None:
    admin = need("BE_ACC_PG_ADMIN")
    secrets = c.dir / ".secrets" / "db"
    secrets.mkdir(parents=True, exist_ok=True)
    conn = await asyncpg.connect(admin)
    try:
        if not await conn.fetchval("SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", c.database):
            await conn.execute(f'CREATE DATABASE "{c.database}"')
    finally:
        await conn.close()
    conn = await asyncpg.connect(urlsplit(admin)._replace(path="/" + c.database).geturl())
    try:
        for shell in c.shells:
            role = SHELL_ROLES[shell]
            if not await conn.fetchval("SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role):
                await conn.execute(f"CREATE ROLE {role} LOGIN")
            await conn.execute(f"ALTER ROLE {role} LOGIN PASSWORD '{role}-pw'")
            (secrets / role).write_text(f"{role}-pw")
    finally:
        await conn.close()
    hosted_by = {m: shell for shell, ms in c.shells.items() for m in ms}
    for probe in c.probes:
        s = schema_of(probe)
        shell_role = SHELL_ROLES[hosted_by[probe]] if probe in hosted_by else ""
        await testing.provision(admin, c.database, s, s + "_owner", s + "_rw", shell_role)
        for role in (s + "_owner", s + "_rw"):
            (secrets / role).write_text(f"{role}-pw")


def certificates(c: Cell) -> None:
    tls = c.dir / ".secrets" / "tls"
    if tls.exists():
        shutil.rmtree(tls)
    tls.mkdir(parents=True)
    world = testing.World(tls, c.deployment)
    for probe in c.probes:
        cert, key, ca = world.certs(probe)
        shutil.move(cert, tls / f"{file_of(probe)}.cert.pem")
        shutil.move(key, tls / f"{file_of(probe)}.key.pem")
    # the files are mounted into containers that run as other users: readable, as a secret store would deliver them
    for f in tls.iterdir():
        f.chmod(0o644)


async def clean_bus(c: Cell) -> None:
    if c.bus != "jetstream":
        raise NotImplementedError("the PostgreSQL queue cells come with T4.7")
    from besdk.events import jetstream

    tr = await jetstream.Transport.connect(need("BE_ACC_NATS_URL"), "cell-prepare")
    try:
        for probe in c.probes:
            try:
                await tr.delete_stream(stream_of(probe))
            except Exception:  # noqa: BLE001 - not there
                pass
    finally:
        await tr.close()


def configs(c: Cell) -> None:
    cfg = c.dir / "config"
    # a bus URL may carry credentials, so the component declares it secret: it is delivered from a file
    (c.dir / ".secrets" / "bus").write_text("nats://host.docker.internal:4222")
    (cfg / "vars.yaml").write_text(HEADER.format(cell=c.name) + f"""PG_HOST: host.docker.internal
PG_PORT: "5432"
PG_DATABASE: {c.database}
OBJECT_STORE_URL: http://host.docker.internal:9000
OBJECT_STORE_BUCKET: be-acceptance
DEPLOYMENT_ID: {c.deployment}
""")
    for shell in c.shells:
        role = SHELL_ROLES[shell]
        (cfg / f"{file_of(shell)}.yaml").write_text(HEADER.format(cell=c.name) + f"""PG_SHELL_USER: {role}
PG_SHELL_PASSWORD: file://.secrets/db/{role}
""")
    for probe in c.probes:
        s, f = schema_of(probe), file_of(probe)
        lines = [f"PG_HOST: $var:PG_HOST", "PG_PORT: $var:PG_PORT", "PG_DATABASE: $var:PG_DATABASE",
                 f"PG_USER: {s}_rw", f"PG_PASSWORD: file://.secrets/db/{s}_rw", f"PG_SCHEMA: {s}",
                 f"PG_OWNER_USER: {s}_owner", f"PG_OWNER_PASSWORD: file://.secrets/db/{s}_owner",
                 "BUS_URL: file://.secrets/bus", f"IMPLEMENTATION_INSTANCE: {uuid.uuid4()}",
                 "OBJECT_STORE_URL: $var:OBJECT_STORE_URL", "OBJECT_STORE_BUCKET: $var:OBJECT_STORE_BUCKET",
                 f"OBJECT_STORE_PREFIX: {s}/", "OBJECT_STORE_ACCESS_KEY: ${BE_ACC_S3_ACCESS_KEY}",
                 "OBJECT_STORE_SECRET_KEY: ${BE_ACC_S3_SECRET_KEY}", "DEPLOYMENT_ID: $var:DEPLOYMENT_ID",
                 f"SERVICE_TLS_CERT_FILE: file://.secrets/tls/{f}.cert.pem",
                 f"SERVICE_TLS_KEY_FILE: file://.secrets/tls/{f}.key.pem",
                 "SERVICE_TLS_CA_FILE: file://.secrets/tls/ca.pem"]
        if nxt := c.next_of(probe):
            lines += [f"PROBE_NEXT: {nxt}", f"PROBE_NEXT_URL: $endpoint:{nxt}", f"PROBE_NEXT_GRPC_URL: $endpoint:{nxt}:grpc"]
        if prev := c.previous_of(probe):
            lines.append(f"PROBE_PEER_STREAM: {stream_of(prev)}")
        (cfg / f"{f}.yaml").write_text(HEADER.format(cell=c.name) + "\n".join(lines) + "\n")


async def prepare(directory: str | pathlib.Path) -> Cell:
    c = Cell.load(directory)
    await databases(c)
    certificates(c)
    await ensure_bucket()
    await clean_bus(c)
    configs(c)
    return c
