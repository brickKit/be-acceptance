"""Collects what a running component shows from outside: its exits, endpoints, gRPC port, metrics, logs, the objects
in its schema and the messages it publishes. Everything a check needs is a key of the observation; a key that could
not be observed is None, and the checks that need it do not run (never pass)."""
from __future__ import annotations

import asyncio
import hashlib
import json
import os
import pathlib
import re
import signal
import socket
import subprocess
import time
from urllib.parse import urlsplit

import asyncpg
import httpx
import nats
from google.protobuf import wrappers_pb2

from besdk import protocol, reqctx
from besdk.calls.client import Member, options
from harness import DATABASE, Deployment, Running, Spec, need

ALICE = "0190f3a1-0000-7000-8000-0000000000a1"


def artifact_digest(spec: Spec) -> str:
    """The component artifact: the binary for a compiled probe, else the source tree it runs from."""
    exe = pathlib.Path(spec.command[0])
    if exe.name != "python":
        return "sha256:" + hashlib.sha256(exe.read_bytes()).hexdigest()
    root = exe.parent.parent.parent  # <project>/.venv/bin/python
    h = hashlib.sha256()
    for f in sorted(p for p in root.rglob("*") if p.is_file() and ".venv" not in p.parts and "__pycache__" not in p.parts):
        h.update(f.relative_to(root).as_posix().encode() + b"\0" + f.read_bytes())
    return "sha256:" + h.hexdigest()


def sdk_tables() -> list[str]:
    """The tables the SDK's migrations create (be-protocol migrations/)."""
    names: list[str] = []
    for f in sorted(protocol.migrations().iterdir(), key=lambda p: p.name):
        if f.name.endswith(".up.sql"):
            names += re.findall(r"CREATE TABLE (?:IF NOT EXISTS )?(\w+)", f.read_text())
    return sorted(set(names))


async def exits(spec: Spec, env: dict) -> dict:
    """The entries' exit codes: an unknown argument, a configuration missing two required keys, migrate again."""
    def run(args, e):
        r = subprocess.run([*spec.command, *args], env={**os.environ, **e}, capture_output=True, text=True, timeout=120)
        return r.returncode, r.stdout + r.stderr

    bad = {k: v for k, v in env.items() if k not in ("PG_HOST", "BUS_URL")}
    unknown, _ = await asyncio.to_thread(run, ["bogus"], env)
    code, out = await asyncio.to_thread(run, [], {**bad, "PORT": str(_free())})
    again, _ = await asyncio.to_thread(run, ["migrate"], env)
    return {"exit_unknown_arg": unknown, "bad_config": {"code": code, "output": out}, "migrate_again": again}


def _free() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


async def kept_alive(c: httpx.AsyncClient, n: int = 20) -> list[float]:
    """Milliseconds of n requests one after another on one kept-alive connection (P1.19): the self-description, a
    response long enough to leave in more than one write."""
    took = []
    for _ in range(n):
        started = time.perf_counter()
        await c.get("/.well-known/be-component")
        took.append(round((time.perf_counter() - started) * 1000, 3))
    return took


async def endpoints(r: Running) -> dict:
    async with httpx.AsyncClient(base_url=r.url, timeout=10) as c:
        get, head = await c.get("/healthz"), await c.head("/healthz")
        ready, describe = await c.get("/readyz"), await c.get("/.well-known/be-component")
        sequential = await kept_alive(c)
    with socket.socket() as s:
        listening = s.connect_ex(("127.0.0.1", r.spec.grpc_port)) == 0
    return {"health": {"get": [get.status_code, get.text], "head": [head.status_code, head.text]},
            "ready": [ready.status_code, ready.text], "describe": describe.json() if describe.status_code == 200 else {},
            "grpc_listening": listening, "kept_alive_ms": sequential}


async def traffic(d: Deployment, r: Running, caller: str, token: str) -> None:
    """A little of everything, so access lines, metrics and messages exist: HTTP with a credential the logs must not
    show, both system methods over mTLS, a call to the next probe, a small and a big write."""
    for path, body in (("/api/probe/echo", {"payload": "x" * 100}), ("/api/probe/work", {"ms": 5}),
                       ("/api/probe/call", {"op": "echo", "surface": "grpc", "bytes": 10}),
                       ("/api/probe/write", {"bytes": 10}), ("/api/probe/write", {"bytes": 70 * 1024})):
        async with httpx.AsyncClient(timeout=15) as c:
            await c.post(r.url + path, json=body, headers={"Authorization": "Bearer " + token})
    m = Member()
    m.set_tls(d.world.tls(caller))
    try:
        ch = m.grpc(r.spec.id, f"127.0.0.1:{r.spec.grpc_port}")
        with reqctx.deadline_scope(time.monotonic() + 5):
            await ch.unary_unary("/test.probe.v1.Probe/Echo", request_serializer=wrappers_pb2.BytesValue.SerializeToString,
                                 response_deserializer=wrappers_pb2.BytesValue.FromString)(
                wrappers_pb2.BytesValue(value=b"x"), metadata=options(idempotent=True))
    finally:
        await m.close()


async def capture(stream: str) -> tuple:
    """Every message on the stream's subjects from now on, as the bus delivers them to any subscriber."""
    nc = await nats.connect(need("BE_ACC_NATS_URL"))
    got: list[dict] = []

    async def cb(msg) -> None:
        got.append({"subject": msg.subject, "headers": dict(msg.headers or {}), "data": msg.data})

    await nc.subscribe(f"be.{stream}.>", cb=cb)
    await nc.flush()
    return nc, got


async def claims(messages: list[dict]) -> None:
    """Fetch every claim-check object as a subscriber would (plain GET), for the envelope check."""
    async with httpx.AsyncClient(timeout=15) as c:
        for m in messages:
            if m["headers"].get("ce-beclaim") == "true":
                ref = json.loads(m["data"])["claim"]
                body = (await c.get(ref["uri"])).content
                m["fetched"] = {"size": len(body), "sha256": hashlib.sha256(body).hexdigest(),
                                "row_digest": hashlib.sha256(m["headers"].get("ce-id", "").encode() + b"\0" + body).hexdigest()}


async def database(schema: str, owner: str, runtime: str) -> dict:
    c = await asyncpg.connect(urlsplit(need("BE_ACC_PG_ADMIN"))._replace(path="/" + DATABASE).geturl())
    try:
        tables = [dict(r) for r in await c.fetch(
            "SELECT c.relname AS name, pg_get_userbyid(c.relowner) AS owner, c.relkind AS kind, "
            "has_table_privilege($2, c.oid, 'SELECT,INSERT,UPDATE,DELETE') AS dml FROM pg_class c "
            "JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relkind IN ('r','p','v','m','S') "
            "AND NOT c.relispartition", schema, runtime)]
        columns = [dict(r) for r in await c.fetch(
            "SELECT table_name, column_name, data_type, is_nullable FROM information_schema.columns WHERE table_schema = $1 "
            "AND table_name IN ('schema_migrations', 'be_sdk_migrations') ORDER BY table_name, ordinal_position", schema)]
        public_exec = [r["proname"] for r in await c.fetch(
            "SELECT p.proname FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = $1 AND "
            "(p.proacl IS NULL OR EXISTS (SELECT 1 FROM aclexplode(p.proacl) a WHERE a.grantee = 0 "
            "AND a.privilege_type = 'EXECUTE'))", schema)]
        in_public = [r["relname"] for r in await c.fetch(
            "SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public' "
            "AND pg_get_userbyid(c.relowner) = ANY($1)", [owner, runtime])]
        elsewhere = [r["nspname"] for r in await c.fetch(
            "SELECT DISTINCT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE "
            "c.relname IN ('schema_migrations', 'be_sdk_migrations') AND pg_get_userbyid(c.relowner) = ANY($1) "
            "AND n.nspname <> $2", [owner, runtime], schema)]
        privs = await c.fetchrow("SELECT has_schema_privilege($1, $2, 'USAGE') AS usage, "
                                 "has_schema_privilege($1, $2, 'CREATE') AS create", runtime, schema)
    finally:
        await c.close()
    return {"tables": tables, "state_columns": columns, "public_exec": public_exec, "in_public": in_public,
            "state_elsewhere": elsewhere, "runtime_usage": privs["usage"], "runtime_create": privs["create"]}


async def stopping(r: Running) -> dict:
    """SIGTERM: during the drain delay /readyz answers 503 NOT_READY with state stopping; then the exit code."""
    r.proc.send_signal(signal.SIGTERM)
    seen = None
    async with httpx.AsyncClient(timeout=2) as c:
        for _ in range(50):
            try:
                resp = await c.get(r.url + "/readyz")
            except httpx.TransportError:
                break
            if resp.status_code == 503:
                seen = [resp.status_code, resp.text]
                break
            await asyncio.sleep(0.02)
    code = await asyncio.to_thread(r.proc.wait, 30)
    return {"stopping_ready": seen, "stop_exit": code}


def secrets_of(env: dict, token: str) -> list[str]:
    """Values that must never appear in the output: the credential sent, the database password, a line of the key."""
    key = pathlib.Path(env["SERVICE_TLS_KEY_FILE"]).read_text().splitlines()
    return [token, env["PG_PASSWORD"], next(line for line in key if line and not line.startswith("-----"))]
