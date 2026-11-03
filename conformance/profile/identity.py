"""The identity profile of a component (be-protocol chapter 08, CK-ID-01..06): the suite is the identity provider (the
test deployment signs the access tokens and serves its key set) and the authorization member (it issues the views),
and drives a probe with its guard on: key rotation, credentials kept out of the logs, the system surface refusing
user credentials and wrong certificates, a stale token refused before any effect, a downstream 401 not passed on.
Each judge returns the problems; each bad() turns a good observation into a known violation.

Not here: CK-ID-07 (the identity member's SessionService) and the edge's CK-EDGE-01..07: the browser-path run of
foundations item 2 T6 (a real Casdoor, infra/iam-casdoor and Traefik with be-edge). CK-ID-01's "keys unreachable
for an hour" stays with the SDKs' own tests (an hour is not a black-box wait)."""
from __future__ import annotations

import asyncio
import time

import asyncpg
import grpc
import httpx
from google.protobuf import wrappers_pb2

from harness import Deployment, Running, Spec
from profile.checks import mutated
from profile.events import admin_url
from profile.model import Check

ALICE = "0190f3a1-0000-7000-8000-0000000000a1"
PEER = "test/probe-go2"  # a component the probes' system methods admit
STRANGER = "test/stranger"
ECHO = "/test.probe.v1.Probe/Echo"
STALE_SDKS = {"go", "py"}  # the SDKs that implement P8.25 and P8.26 (be-sdk-python since a46)


def prefix(component: str) -> str:
    return component.replace("/", ".").replace("-", "_")


def sdk_of(spec: Spec) -> str:
    return "go" if spec.id.startswith("test/probe-go") else "py"


def grants(spec: Spec) -> list[dict]:
    """Every operation of the probe on every record (the probe's resource has no dimensions)."""
    p = prefix(spec.id)
    return [{"resource_type": p, "operation": f"{p}.{op}", "atoms": {}, "read_fields": [],
             "valid": {"from": "2026-01-01T00:00:00Z"}} for op in ("read", "echo", "work", "write", "call", "note", "pull")]


async def echo(r: Running, token: str) -> tuple[int, str]:
    async with httpx.AsyncClient(timeout=15) as c:
        resp = await c.post(r.url + "/api/probe/echo", json={"payload": "x"}, headers={"Authorization": "Bearer " + token})
    return resp.status_code, reason_of(resp)


def reason_of(resp: httpx.Response) -> str:
    try:
        return resp.json().get("reason", "")
    except ValueError:
        return ""


async def system_call(d: Deployment, r: Running, cert: tuple | None, metadata: tuple = ()) -> str:
    """Echo on the probe's system surface with the given client certificate (cert, key, ca files) or none, the
    channel checking the server against the deployment's root; the gRPC code, or OK."""
    roots = d.world.ca_file.read_bytes()
    if cert:
        creds = grpc.ssl_channel_credentials(root_certificates=roots, private_key=open(cert[1], "rb").read(),
                                             certificate_chain=open(cert[0], "rb").read())
    else:
        creds = grpc.ssl_channel_credentials(root_certificates=roots)
    dns = r.spec.id.replace("/", ".") + ".component." + d.world.deployment  # the component's DNS name (P8.5)
    async with grpc.aio.secure_channel(f"127.0.0.1:{r.spec.grpc_port}", creds,
                                       options=[("grpc.ssl_target_name_override", dns)]) as ch:
        call = ch.unary_unary(ECHO, request_serializer=wrappers_pb2.BytesValue.SerializeToString,
                              response_deserializer=wrappers_pb2.BytesValue.FromString)
        try:
            await call(wrappers_pb2.BytesValue(value=b"x"), timeout=5, metadata=metadata)
            return "OK"
        except grpc.aio.AioRpcError as e:
            return e.code().name


async def until(f, timeout: float = 15.0, every: float = 0.2):
    """Calls f until it returns a truthy value; the last value."""
    end, v = time.monotonic() + timeout, None
    while time.monotonic() < end:
        v = await f()
        if v:
            return v
        await asyncio.sleep(every)
    return v


async def outbox_rows(spec: Spec) -> int:
    c = await asyncpg.connect(admin_url())
    try:
        return await c.fetchval(f"SELECT count(*) FROM {spec.schema}.be_outbox WHERE stream = $1", spec.stream)
    finally:
        await c.close()


async def observe(d: Deployment, spec: Spec, env: dict, peer: Spec | None = None) -> dict:
    """Starts the probe with its guard on and drives the identity checks; peer (a second probe kind, for CK-ID-06) is
    started with another issuer, so it refuses every token the suite signs."""
    w = d.world
    w.grant(ALICE, *grants(spec))
    if peer:
        w.grant(ALICE, *grants(peer))
    obs: dict = {"sdk": sdk_of(spec), "tokens": []}
    r = await d.start(spec, {**env, "PROBE_GUARD": "true"})
    tok = w.token(ALICE)
    obs["tokens"].append(tok)
    first = await until(lambda: _ok(echo(r, tok)))
    obs["baseline"] = first

    # CK-ID-01: a new key is fetched once for several requests at the same time; an unknown kid right after does
    # not fetch again (at most every 30 seconds) and is refused
    before = w.jwks_fetches
    w.rotate()
    rotated = [w.token(ALICE) for _ in range(5)]
    obs["tokens"] += rotated
    answers = await asyncio.gather(*(echo(r, t) for t in rotated))
    after_rotation = w.jwks_fetches
    ghost = w.token(ALICE, kid="ghost-1")
    obs["tokens"].append(ghost)
    ghosts = [await echo(r, ghost), await echo(r, ghost)]
    obs["rotation"] = {"concurrent": [a[0] for a in answers], "fetches_new_kid": after_rotation - before,
                       "ghost": [g[0] for g in ghosts], "ghost_reasons": [g[1] for g in ghosts],
                       "fetches_ghost": w.jwks_fetches - after_rotation}

    # CK-ID-03: a user's credential on the system surface, without a service certificate
    bearer = (("authorization", "Bearer " + tok),)
    obs["user_on_system"] = await system_call(d, r, None, bearer)
    # CK-ID-04: no certificate, another deployment's root, a component of this deployment not allowed; and a
    # component that is allowed (the positive)
    other = w.other_root()
    obs["certificates"] = {
        "none": await system_call(d, r, None),
        "other_root": await system_call(d, r, w.certs(PEER, ca=other)),
        "other_deployment": await system_call(d, r, w.certs(PEER, deployment="0190f3a1-0000-7000-8000-00000000dfff")),
        "stranger": await system_call(d, r, w.certs(STRANGER)),
        "permitted": await system_call(d, r, w.certs(PEER)),
    }

    if obs["sdk"] in STALE_SDKS:
        # CK-ID-05: after an identity-side security change the renewed view makes the old token stale; a write with
        # it changes nothing; a token at the new version and another subject go on
        old = w.token(ALICE)
        w.security_change(ALICE)

        last: list = []

        async def stale():
            last[:] = [await echo(r, old)]
            return last[0][0] == 401
        await until(stale)
        obs["stale_wait"] = last[0]  # the last answer: what the probe said when the wait ended
        rows = await outbox_rows(spec)
        async with httpx.AsyncClient(timeout=15) as c:
            resp = await c.post(r.url + "/api/probe/write", json={"bytes": 10},
                                headers={"Authorization": "Bearer " + old})
        obs["stale_write"] = {"status": resp.status_code, "reason": reason_of(resp),
                              "rows_before": rows, "rows_after": await outbox_rows(spec)}
        new = w.token(ALICE)
        obs["tokens"] += [old, new]
        obs["fresh"] = (await echo(r, new))[0]

    obs["log"] = r.logs()  # before CK-ID-06 starts the probe again (the same log file)

    if obs["sdk"] in STALE_SDKS and peer:
        # CK-ID-06: a call for the user that the next probe refuses with 401 (it trusts another issuer)
        penv = await d.env(peer, {})
        pr = await d.start(peer, {**penv, "PROBE_GUARD": "true", "IAM_ISSUER": "https://another-issuer.test/"})
        obs["downstream"] = await _downstream(d, spec, env, peer, pr, w.token(ALICE))
    return obs


async def _ok(answer) -> tuple[int, str] | None:
    code, reason = await answer
    return (code, reason) if code == 200 else None


async def _downstream(d: Deployment, spec: Spec, env: dict, peer: Spec, pr: Running, token: str) -> dict:
    """The probe under test, started again with the peer as its next probe, calls it for the user over HTTP."""
    for running in list(d.running):
        if running.spec.id == spec.id:
            await running.stop()
            d.running.remove(running)
    r = await d.start(spec, {**env, "PROBE_GUARD": "true", "PROBE_NEXT": peer.id, "PROBE_NEXT_URL": pr.url})
    await until(lambda: _ok(echo(r, token)))
    async with httpx.AsyncClient(timeout=15) as c:
        resp = await c.post(r.url + "/api/probe/call", json={"surface": "http", "op": "echo", "bytes": 1},
                            headers={"Authorization": "Bearer " + token})
        direct = await c.post(pr.url + "/api/probe/echo", json={"payload": "x"},
                              headers={"Authorization": "Bearer " + token})
    body = resp.json() if resp.headers.get("content-type", "").startswith("application/problem") else {}
    return {"status": resp.status_code, "reason": body.get("reason", ""), "peer": peer.id,
            "dependency": (body.get("metadata") or {}).get("dependency", ""),
            "peer_direct": direct.status_code, "peer_reason": reason_of(direct)}


# ------------------------------------------------------------------------------------------------------ judges

def judge_rotation(obs: dict) -> list[str]:
    r, out = obs["rotation"], []
    if obs["baseline"] is None:
        out.append("precondition: a valid token was never accepted")
    if r["concurrent"] != [200] * len(r["concurrent"]):
        out.append(f"requests signed with the new key at once: {r['concurrent']}, want all 200")
    if r["fetches_new_kid"] != 1:
        out.append(f"the new key set was fetched {r['fetches_new_kid']} times for them, want once")
    if r["ghost"] != [401, 401] or set(r["ghost_reasons"]) != {"TOKEN_INVALID"}:
        out.append(f"an unknown kid: {r['ghost']} {r['ghost_reasons']}, want 401 TOKEN_INVALID")
    if r["fetches_ghost"] != 0:
        out.append(f"an unknown kid within 30 seconds of a fetch fetched again ({r['fetches_ghost']})")
    return out


def judge_credentials_in_logs(obs: dict) -> list[str]:
    out = []
    for t in obs["tokens"]:
        sig = t.rsplit(".", 1)[-1]
        if t in obs["log"] or sig in obs["log"]:
            out.append("a credential (or its signature) is in the logs")
            break
    if not obs["tokens"] or len(obs["log"]) < 100:
        out.append("precondition: the probe logged its requests and tokens were used")
    return out


REFUSED = {"UNAVAILABLE", "UNAUTHENTICATED", "PERMISSION_DENIED"}


def judge_user_on_system(obs: dict) -> list[str]:
    code = obs["user_on_system"]
    return [] if code in REFUSED else [f"a user's credential without a service certificate got {code}"]


def judge_certificates(obs: dict) -> list[str]:
    c, out = obs["certificates"], []
    for k in ("none", "other_root", "other_deployment"):
        if c[k] not in REFUSED:
            out.append(f"{k}: {c[k]}, want refused")
    if c["stranger"] != "PERMISSION_DENIED":
        out.append(f"a component not allowed: {c['stranger']}, want PERMISSION_DENIED")
    if c["permitted"] != "OK":
        out.append(f"precondition: an allowed component got {c['permitted']}")
    return out


def judge_stale(obs: dict) -> list[str]:
    out, s = [], obs["stale_write"]
    if tuple(obs["stale_wait"] or ()) != (401, "TOKEN_STALE"):  # a list once recorded as JSON
        out.append(f"the old token after the change: {obs['stale_wait']}, want 401 TOKEN_STALE")
    if (s["status"], s["reason"]) != (401, "TOKEN_STALE"):
        out.append(f"a write with it: {s['status']} {s['reason']}")
    if s["rows_after"] != s["rows_before"]:
        out.append(f"the stale write left {s['rows_after'] - s['rows_before']} outbox rows")
    if obs["fresh"] != 200:
        out.append(f"a token at the new version: {obs['fresh']}")
    return out


def judge_downstream(obs: dict) -> list[str]:
    d, out = obs["downstream"], []
    if (d["peer_direct"], d["peer_reason"]) != (401, "TOKEN_INVALID"):
        out.append(f"precondition: the peer refuses the token directly ({d['peer_direct']} {d['peer_reason']})")
    if (d["status"], d["reason"]) != (503, "AUTHORIZATION_UNPROVABLE"):
        out.append(f"the call for the user: {d['status']} {d['reason']}, want 503 AUTHORIZATION_UNPROVABLE")
    if d["dependency"] != d.get("peer", PEER) and d["status"] == 503:
        out.append(f"metadata.dependency is {d['dependency']!r}")
    return out


def not_stale_sdk(obs: dict) -> str:
    if obs.get("sdk") in STALE_SDKS:
        return ""
    return "be-sdk-python does not implement P8.25 and P8.26 yet (its protocol copy is alpha.45; foundations item 6)"


IDENTITY = [
    Check("CK-ID-01", "公钥轮换：新 kid 只取一次、30 秒内不认识的 kid 不再取", ("P8.2",), ("rotation", "baseline"),
          judge_rotation, lambda o: mutated(o, lambda x: x["rotation"].update(fetches_new_kid=5))),
    Check("CK-ID-02", "日志里没有凭据", ("P8.3",), ("tokens", "log"), judge_credentials_in_logs,
          lambda o: mutated(o, lambda x: x.update(log=x["log"] + x["tokens"][0]))),
    Check("CK-ID-03", "用户凭据到不了系统面", ("P8.4", "P8.5"), ("user_on_system",), judge_user_on_system,
          lambda o: mutated(o, lambda x: x.update(user_on_system="OK"))),
    Check("CK-ID-04", "无证书、别的根、别的部署、未获准的组件都被拒", ("P8.5", "P8.6"), ("certificates",), judge_certificates,
          lambda o: mutated(o, lambda x: x["certificates"].update(stranger="OK"))),
    Check("CK-ID-05", "令牌陈旧在任何副作用之前被拒", ("P8.25",), ("stale_write", "stale_wait", "fresh"), judge_stale,
          lambda o: mutated(o, lambda x: x["stale_write"].update(rows_after=x["stale_write"]["rows_before"] + 1)),
          applies=not_stale_sdk),
    Check("CK-ID-06", "代表用户的调用被下游答 401 时不原样返回", ("P8.26",), ("downstream",), judge_downstream,
          lambda o: mutated(o, lambda x: x["downstream"].update(status=401, reason="TOKEN_INVALID")),
          applies=not_stale_sdk),
]
