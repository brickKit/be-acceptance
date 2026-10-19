"""Prepares a cell for `brickkit up`, as a deployment's provisioning would: the probes' databases and the shells'
login roles (fresh schemas every time), the deployment's certificates, the object storage bucket, a clean bus, and
every component's config file (the chain wired with $endpoint: references). Secrets go to the cell's .secrets/ and
are referenced from config; the object storage keys come from the environment (BE_ACC_S3_ACCESS_KEY, _SECRET_KEY)."""
from __future__ import annotations

import io
import pathlib
import shutil
import uuid
from urllib.parse import urlsplit

import asyncpg

from besdk import testing
from cells.spec import SHELL_ROLES, Cell, file_of, schema_of, stream_of

# The security members' own resource types and permission keys (their component definitions), for the registry the
# authorization member checks submissions against.
INFRA_KEYS = {
    "infra/authz": {"resource_type": ["infra.authz.security_record", "infra.authz.system"],
                    "permission": ["infra.authz.security_record.read", "infra.authz.security_record.change",
                                   "infra.authz.system.read", "infra.authz.system.verify", "infra.authz.system.submit"]},
    "infra/audit": {"resource_type": ["infra.audit.record", "infra.audit.intake"],
                    "permission": ["infra.audit.record.read", "infra.audit.intake.read", "infra.audit.intake.submit",
                                   "infra.audit.intake.watermark"]},
}
PROBE_OPS = ["read", "echo", "work", "write", "call"]  # the probes' operations (probe/defs.go, probe_py)


def probe_prefix(component: str) -> str:
    """The probes' resource type and operation prefix: test/probe-go2 -> test.probe_go2."""
    return component.replace("/", ".").replace("-", "_")
from harness import empty_prefixes, ensure_bucket, need

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
    for probe in c.placed:
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
    for probe in c.placed:
        cert, key, ca = world.certs(probe)
        shutil.move(cert, tls / f"{file_of(probe)}.cert.pem")
        shutil.move(key, tls / f"{file_of(probe)}.key.pem")
    # the files are mounted into containers that run as other users: readable, as a secret store would deliver them
    for f in tls.iterdir():
        f.chmod(0o644)


async def clean_bus(c: Cell) -> None:
    if c.bus == "pgqueue":
        await pg_bus(c)
        return
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


async def pg_bus(c: Cell) -> None:
    """The PostgreSQL queue in the experiment database's own schema be_bus, owned by the bus role be_bus (fresh every
    time); every component reaches it with that role, as they share one NATS."""
    from besdk import obs
    from besdk.events import pgqueue

    admin = urlsplit(need("BE_ACC_PG_ADMIN"))
    conn = await asyncpg.connect(admin._replace(path="/" + c.database).geturl())
    try:
        if not await conn.fetchval("SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'be_bus')"):
            await conn.execute("CREATE ROLE be_bus LOGIN")
        await conn.execute("ALTER ROLE be_bus LOGIN PASSWORD 'be_bus-pw'")
        await conn.execute("DROP SCHEMA IF EXISTS be_bus CASCADE")
        await conn.execute("CREATE SCHEMA be_bus AUTHORIZATION be_bus")
        await conn.execute(f'GRANT CONNECT ON DATABASE "{c.database}" TO be_bus')
    finally:
        await conn.close()
    host = f"be_bus:be_bus-pw@{admin.hostname}:{admin.port or 5432}"
    url = f"pgqueue://{host}/{c.database}?schema=be_bus&sslmode=disable"
    await pgqueue.migrate_bus(url, obs.new_logger(obs.Member("bus", "0"), "error", io.StringIO()))
    (c.dir / ".secrets").mkdir(exist_ok=True)
    (c.dir / ".secrets" / "bus").write_text(
        f"pgqueue://be_bus:be_bus-pw@host.docker.internal:5432/{c.database}?schema=be_bus&sslmode=disable")


def registry(c: Cell) -> str:
    """The registry's owners of every resource type and permission key the cell's components define (TSV)."""
    rows = ["kind\tkey\towner_component"]
    for comp, keys in INFRA_KEYS.items():
        if comp in c.placed:
            rows += [f"{kind}\t{k}\t{comp}" for kind, ks in keys.items() for k in ks]
    for probe in c.probes:
        prefix = probe_prefix(probe)
        rows.append(f"resource_type\t{prefix}\t{probe}")
        rows += [f"permission\t{prefix}.{op}\t{probe}" for op in PROBE_OPS]
    return "\n".join(rows) + "\n"


def signing_key(path: pathlib.Path) -> None:
    """The identity member's Ed25519 signing key (PKCS #8 PEM), new for every preparation."""
    from cryptography.hazmat.primitives import serialization
    from cryptography.hazmat.primitives.asymmetric import ed25519

    k = ed25519.Ed25519PrivateKey.generate()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(k.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8,
                                     serialization.NoEncryption()))
    path.chmod(0o644)  # read by a container user, as the TLS files


def security_configs(c: Cell) -> None:
    """The three security members' config files (step 5): the identity member in front of the local Casdoor (its
    development exchange on: a test deployment, A101), the authorization member with the cell's registry, the audit
    component. The install claim (AUTHZ_BOOTSTRAP_ADMIN) is added by secure.py once the admin has a principal."""
    cfg, sec = c.dir / "config", c.dir / ".secrets"
    signing_key(sec / "iam" / "signing-key.pem")
    (sec / "authz-registry.tsv").write_text(registry(c))
    (sec / "authz-registry.tsv").chmod(0o644)
    common = ["PG_HOST: $var:PG_HOST", "PG_PORT: $var:PG_PORT", "PG_DATABASE: $var:PG_DATABASE", "DEPLOYMENT_ID: $var:DEPLOYMENT_ID"]

    def db(comp: str) -> list[str]:
        s = schema_of(comp)
        return [f"PG_USER: {s}_rw", f"PG_PASSWORD: file://.secrets/db/{s}_rw", f"PG_SCHEMA: {s}",
                f"PG_OWNER_USER: {s}_owner", f"PG_OWNER_PASSWORD: file://.secrets/db/{s}_owner"]

    def guarded(comp: str) -> list[str]:
        f = file_of(comp)
        return ["IAM_ISSUER: $var:IAM_ISSUER", "IAM_JWKS_URL: $var:IAM_JWKS_URL", "AUTHZ_GRPC_URL: $var:AUTHZ_GRPC_URL",
                "AUDIT_GRPC_URL: $var:AUDIT_GRPC_URL", f"SERVICE_TLS_CERT_FILE: file://.secrets/tls/{f}.cert.pem",
                f"SERVICE_TLS_KEY_FILE: file://.secrets/tls/{f}.key.pem", "SERVICE_TLS_CA_FILE: file://.secrets/tls/ca.pem"]

    files = {
        "infra/iam-casdoor": [*common, *db("infra/iam-casdoor"), "IAM_TOKEN_ISSUER: $var:IAM_ISSUER",
                              "IAM_SIGNING_KEY: file://.secrets/iam/signing-key.pem",
                              "IAM_CALLBACK_URL: http://localhost:8200/api/iam/callback", "IAM_COOKIE_SECURE: \"false\"",
                              "CASDOOR_URL: http://host.docker.internal:8000", "CASDOOR_PUBLIC_URL: http://localhost:8000",
                              "CASDOOR_CLIENT_ID: be-iam-client", "CASDOOR_CLIENT_SECRET: ${BE_ACC_IAM_CLIENT_SECRET}",
                              "IAM_DEV_EXCHANGE: \"true\"", "IAM_DEV_CLIENT_ID: be-iam-dev-client",
                              "IAM_DEV_CLIENT_SECRET: ${BE_ACC_IAM_DEV_CLIENT_SECRET}"],
        "infra/authz": [*common, *db("infra/authz"), *guarded("infra/authz"), f"AUTHZ_INSTANCE: {uuid.uuid4()}",
                        "AUTHZ_REGISTRY: file://.secrets/authz-registry.tsv"],
        "infra/audit": [*common, *db("infra/audit"), *guarded("infra/audit")],
    }
    for comp, lines in files.items():
        if comp in c.placed:
            (cfg / f"{file_of(comp)}.yaml").write_text(HEADER.format(cell=c.name) + "\n".join(lines) + "\n")


def configs(c: Cell) -> None:
    cfg = c.dir / "config"
    # a bus URL may carry credentials, so the component declares it secret: it is delivered from a file
    if c.bus == "jetstream":
        (c.dir / ".secrets" / "bus").write_text("nats://host.docker.internal:4222")
    (cfg / "vars.yaml").write_text(HEADER.format(cell=c.name) + f"""PG_HOST: host.docker.internal
PG_PORT: "5432"
PG_DATABASE: {c.database}
OBJECT_STORE_URL: http://host.docker.internal:9000
OBJECT_STORE_BUCKET: be-acceptance
DEPLOYMENT_ID: {c.deployment}
""" + ("""IAM_ISSUER: $endpoint:infra/iam-casdoor
IAM_JWKS_URL: $endpoint:infra/iam-casdoor/api/iam/jwks
AUTHZ_GRPC_URL: $endpoint:infra/authz:grpc
AUDIT_GRPC_URL: $endpoint:infra/audit:grpc
""" if c.secured else ""))
    if c.secured:
        security_configs(c)
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
        if c.concurrency != 1:
            lines.append(f"BUS_SUBSCRIPTION_CONCURRENCY: {c.concurrency}")
        if c.secured:  # the routes need a token and their operation; the view and the audit records are real
            lines += ["PROBE_GUARD: \"true\"", "IAM_ISSUER: $var:IAM_ISSUER", "IAM_JWKS_URL: $var:IAM_JWKS_URL",
                      "AUTHZ_GRPC_URL: $var:AUTHZ_GRPC_URL", "AUDIT_GRPC_URL: $var:AUDIT_GRPC_URL"]
        (cfg / f"{f}.yaml").write_text(HEADER.format(cell=c.name) + "\n".join(lines) + "\n")


async def prepare(directory: str | pathlib.Path) -> Cell:
    c = Cell.load(directory)
    await databases(c)
    certificates(c)
    await ensure_bucket()
    await empty_prefixes([schema_of(p) + "/" for p in c.probes])  # the prefixes configs() gives the probes
    await clean_bus(c)
    configs(c)
    return c
