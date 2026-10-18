"""Observes a running cell from outside, for the core profile: one observation per probe (its exits from its own
image, endpoints and metrics on its exposed port, its system port from inside its container, the objects in its
schema, what it publishes, its own lines of the container's output) and one per shell (its own port, every line of
its output, its aggregated metrics). The containers are stopped last, to see the graceful stop."""
from __future__ import annotations

import asyncio
import hashlib
import json
import pathlib
import subprocess
import uuid

import httpx

from cells.spec import PROBE_PORTS, Cell, file_of, schema_of, stream_of
from profile import observe as po

COMPOSE = ".brickkit/generated/compose.yaml"


def run(args: list[str], cwd: pathlib.Path | None = None, timeout: int = 120) -> tuple[int, str]:
    r = subprocess.run(args, cwd=cwd, capture_output=True, text=True, timeout=timeout)
    return r.returncode, r.stdout + r.stderr


def config_value(c: Cell, component: str, key: str) -> str:
    for line in (c.dir / "config" / f"{file_of(component)}.yaml").read_text().splitlines():
        if line.startswith(key + ":"):
            return line.split(":", 1)[1].strip()
    return ""


def exits(c: Cell, probe: str) -> dict:
    image = f"{file_of(probe)}:{c.versions_of_probes[probe]}"
    unknown, _ = run(["docker", "run", "--rm", image, "bogus"])
    code, out = run(["docker", "run", "--rm", "-e", f"COMPONENT_ID={probe}", "-e",
                     f"COMPONENT_VERSION={c.versions_of_probes[probe]}", "-e", "PORT=8080", image])
    service = f"{file_of(probe)}-{c.versions_of_probes[probe].replace('.', '-')}-migration"
    again, _ = run(["docker", "compose", "-p", f"brickkit-{c.project}", "--project-directory", str(c.dir), "-f",
                    str(c.dir / COMPOSE), "run", "--rm", "-T", service], cwd=c.dir, timeout=300)
    return {"exit_unknown_arg": unknown, "bad_config": {"code": code, "output": out}, "migrate_again": again}


async def endpoints(c: Cell, component: str) -> dict:
    base = f"http://127.0.0.1:{c.host_port(component)}"
    async with httpx.AsyncClient(base_url=base, timeout=10) as h:
        get, head = await h.get("/healthz"), await h.head("/healthz")
        ready, describe = await h.get("/readyz"), await h.get("/.well-known/be-component")
        sequential = await po.kept_alive(h)
    out = {"health": {"get": [get.status_code, get.text], "head": [head.status_code, head.text]},
           "ready": [ready.status_code, ready.text], "describe": describe.json() if describe.status_code == 200 else {},
           "kept_alive_ms": sequential}
    if component in PROBE_PORTS:
        code, _ = run(["docker", "exec", c.container_of(component), "nc", "-z", "127.0.0.1",
                       str(PROBE_PORTS[component][1])])
        out["grpc_listening"] = code == 0
    return out


async def traffic(c: Cell, probe: str, token: str) -> int:
    """Every operation once, a call to the next probe on both surfaces, a small and a big write. Returns how many HTTP
    requests went to this probe."""
    base = f"http://127.0.0.1:{c.host_port(probe)}"
    sent = [("/api/probe/echo", {"payload": "x" * 100}), ("/api/probe/work", {"ms": 5}),
            ("/api/probe/write", {"bytes": 10}), ("/api/probe/write", {"bytes": 70 * 1024})]
    if c.next_of(probe):
        sent += [("/api/probe/call", {"op": "echo", "surface": s, "bytes": 10}) for s in ("grpc", "http")]
    async with httpx.AsyncClient(base_url=base, timeout=15) as h:
        for path, body in sent:
            await h.post(path, json=body, headers={"Authorization": "Bearer " + token})
    return len(sent)


def claims(c: Cell, messages: list[dict]) -> None:
    """Claim-check objects fetched as a subscriber in this deployment would (the presigned address names
    host.docker.internal): from inside a probe's container."""
    container = c.container_of(c.probes[0])
    for m in messages:
        if m["headers"].get("ce-beclaim") == "true":
            uri = json.loads(m["data"])["claim"]["uri"]
            body = subprocess.run(["docker", "exec", container, "wget", "-qO-", uri], capture_output=True,
                                  timeout=60).stdout
            m["fetched"] = {"size": len(body), "sha256": hashlib.sha256(body).hexdigest(),
                            "row_digest": hashlib.sha256(m["headers"].get("ce-id", "").encode() + b"\0" + body).hexdigest()}


def lines_of(output: str, component: str) -> str:
    """A component's own lines of a container's output (a shell's output carries every member's lines)."""
    keep = []
    for line in output.splitlines():
        try:
            if json.loads(line).get("component") == component:
                keep.append(line)
        except (ValueError, AttributeError):
            pass
    return "\n".join(keep) + "\n"


async def stop(c: Cell, containers: list[str], probes: list[str]) -> dict:
    """docker stop (SIGTERM, then the stop timeout) on every container at once; meanwhile each probe's /readyz must
    answer 503 NOT_READY with state stopping."""
    async def watch(probe: str):
        async with httpx.AsyncClient(timeout=2) as h:
            for _ in range(100):
                try:
                    r = await h.get(f"http://127.0.0.1:{c.host_port(probe)}/readyz")
                except httpx.TransportError:
                    return None
                if r.status_code == 503:
                    return [r.status_code, r.text]
                await asyncio.sleep(0.02)
        return None

    watchers = [asyncio.create_task(watch(p)) for p in probes]
    await asyncio.sleep(0.05)
    stops = [asyncio.create_task(asyncio.to_thread(run, ["docker", "stop", "-t", "40", ct], None, 60)) for ct in containers]
    seen = await asyncio.gather(*watchers)
    await asyncio.gather(*stops)
    out = {"stopping_ready": dict(zip(probes, seen)), "exit": {}, "output": {}}
    for ct in containers:
        _, code = run(["docker", "inspect", "-f", "{{.State.ExitCode}}", ct])
        out["exit"][ct] = int(code.strip() or -1)
        _, logs = run(["docker", "logs", ct])
        out["output"][ct] = logs
    return out


async def observe(c: Cell, admin_db: str) -> dict:
    """Observations for every probe and shell of a running cell; the cell is stopped at the end."""
    token = "sentinel-" + uuid.uuid4().hex
    obs: dict[str, dict] = {}
    for p in c.probes:
        s = schema_of(p)
        obs[p] = {"component": p, "stream": stream_of(p), "partitions": 4, "owner": s + "_owner",
                  "impl": config_value(c, p, "IMPLEMENTATION_INSTANCE"), "sdk_tables": po.sdk_tables(), **exits(c, p)}
    for sh in c.shells:
        obs[sh] = {"component": sh, "shell": True, "members": c.shells[sh]}
    caps = {p: await po.capture(stream_of(p)) for p in c.probes}
    for comp in [*c.probes, *c.shells]:
        obs[comp].update(await endpoints(c, comp))
    for p in c.probes:
        obs[p]["http_requests"] = await traffic(c, p, token)
    await asyncio.sleep(4)  # forwarding, the big writes through object storage, the relays
    async with httpx.AsyncClient(timeout=10) as h:
        for comp in [*c.probes, *c.shells]:
            text = (await h.get(f"http://127.0.0.1:{c.host_port(comp)}/metrics")).text
            obs[comp]["shell_metrics" if comp in c.shells else "metrics"] = text
    for p in c.probes:
        nc, got = caps[p]
        await nc.close()
        claims(c, got)
        obs[p]["messages"] = got
        s = schema_of(p)
        obs[p]["db"] = await po.database(s, s + "_owner", s + "_rw")
        key = (c.dir / ".secrets" / "tls" / f"{file_of(p)}.key.pem").read_text().splitlines()
        obs[p]["secrets"] = [token, (c.dir / ".secrets" / "db" / (s + "_rw")).read_text(),
                             next(line for line in key if line and not line.startswith("-----"))]
    containers = sorted({c.container_of(x) for x in [*c.probes, *c.shells]})
    stopped = await stop(c, containers, c.probes)
    for p in c.probes:
        ct = c.container_of(p)
        obs[p].update(stopping_ready=stopped["stopping_ready"][p], stop_exit=stopped["exit"][ct],
                      logs=lines_of(stopped["output"][ct], p))
    for sh in c.shells:
        ct = c.container_of(sh)
        obs[sh].update(stop_exit=stopped["exit"][ct], logs=stopped["output"][ct])
    return obs
