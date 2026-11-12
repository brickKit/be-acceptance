"""Secures a running cell that runs the security members (step 5), as a deployment's first administrator would:

1. the administrator (test.alice) and the load user (test.bob) log in once through the identity member's development
   exchange (A101), which binds their platform principals;
2. the administrator consumes the authorization member's install claim with the one-time code prepare.py made
   (infra/authz 3.0.20, A130): no restart;
3. the administrator grants the load user every operation of every probe, through the effect point;
4. once every probe and master data component takes the load user's token, the driver's credentials are written to
   .secrets/load-user.json (a seed run straight after would otherwise meet 404 from a member whose view has not got
   the grant yet).

Needs
BE_ACC_IAM_TEST_PASSWORD (the test users' password in Casdoor, scripts/dev_setup.py of infra-iam-casdoor)."""
from __future__ import annotations

import asyncio
import datetime as dt
import json
import pathlib

import httpx
from besdk import canon

from cells.prepare import DIMENSIONS, ERP_KEYS, MDM_KEYS, PROBE_OPS, probe_prefix
from cells.spec import Cell
from harness import need

ADMIN, LOAD_USER = "test.alice", "test.bob"
# a write route of each master data component, sent an empty body: 404 while the grant has not reached the
# component's view (not visible), 400 once it has (the operation is checked before the body). A read does not tell:
# a list answers 200 with nothing in it to a user without the read operation.
MDM_WRITE = {"mdm/org": ("POST", "/api/org/legal-entities"), "mdm/currency": ("PUT", "/api/currency/rate-types/x"),
             "mdm/partner": ("POST", "/api/partner/partners"), "mdm/product": ("POST", "/api/product/products"),
             "mdm/employee": ("POST", "/api/employee/employees")}


async def ready(base: str, limit: float = 120) -> None:
    async with httpx.AsyncClient(timeout=5) as h:
        for _ in range(int(limit * 2)):
            try:
                if (await h.get(base + "/readyz")).status_code == 200:
                    return
            except httpx.HTTPError:
                pass
            await asyncio.sleep(0.5)
    raise RuntimeError(f"{base} is not ready after {limit:.0f} s")


async def token(iam: str, username: str, password: str) -> tuple[str, str]:
    async with httpx.AsyncClient(timeout=10) as h:
        r = await h.post(iam + "/api/iam/dev/token", json={"username": username, "password": password})
    if r.status_code != 200:
        raise RuntimeError(f"the development exchange refused {username}: {r.status_code} {r.text[:200]}")
    d = r.json()
    return d["access_token"], d["principal"]


async def secure(directory: str | pathlib.Path) -> dict:
    c = Cell.load(directory)
    if not c.secured:
        raise ValueError(f"{c.name} does not run the security members")
    password = need("BE_ACC_IAM_TEST_PASSWORD")
    iam = f"http://127.0.0.1:{c.host_port('infra/iam-casdoor')}"
    authz = f"http://127.0.0.1:{c.host_port('infra/authz')}"
    await ready(iam)
    admin_token, admin = await token(iam, ADMIN, password)
    _, user = await token(iam, LOAD_USER, password)
    await ready(authz)
    headers = {"Authorization": "Bearer " + admin_token}
    async with httpx.AsyncClient(base_url=authz, timeout=15, headers=headers) as h:
        # the install claim: its one-time code (prepare.py); the member's own view opens once its account
        # projection is aligned (503 until then)
        code = (c.dir / ".secrets" / "install-claim.code").read_text().strip()
        for _ in range(120):
            r = await h.post("/api/authz/claim", json={"code": code})
            if r.status_code != 503:
                break
            await asyncio.sleep(0.5)
        if r.status_code != 200:
            raise RuntimeError(f"the install claim failed: {r.status_code} {r.text[:300]}")
        # the administrator's grant reaches the member's own view within a verification: until then the list is
        # an empty page (no segment is no refusal, security 07)
        root = None
        for _ in range(60):
            r = await h.get("/api/authz/records", params={"type": "management_unit"})
            r.raise_for_status()
            root = next((u["id"] for u in r.json().get("records") or [] if u["body"].get("root")), None)
            if root:
                break
            await asyncio.sleep(0.5)
        if root is None:
            raise RuntimeError(f"the administrator's grant never reached the authorization member's view on {authz}")
        start = (dt.datetime.now(dt.timezone.utc) - dt.timedelta(minutes=1)).strftime("%Y-%m-%dT%H:%M:%S.%fZ")
        segments = [{"resource_type": probe_prefix(p), "operation": f"{probe_prefix(p)}.{op}", "atoms": {}, "read_fields": [],
                     "valid": {"from": start}} for p in c.probes
                    for op in [*PROBE_OPS, "pay"]]  # the payment: both languages since 3.2.48
        # the master data components' operations too: the seed data goes through their user surfaces
        # (a resource with dimensions gets every one of them open: a segment without one is not usable, FX01)
        segments += [{"resource_type": op.rsplit(".", 1)[0], "operation": op,
                      "atoms": {d: {"kind": "all"} for d in DIMENSIONS.get(op.rsplit(".", 1)[0], [])}, "read_fields": [],
                      "valid": {"from": start}} for comp in c.mdm for op in MDM_KEYS[comp]["permission"]
                     if not op.endswith(".system.read")]
        # the business components' user operations, every dimension open: the L3 scenario and the seed data go
        # through their user surfaces (system operations are for services; adjustments are not in this version)
        for comp in c.erp:
            for op in ERP_KEYS[comp]["permission"]:
                if ".system." in op or op == "erp.inventory.movement.adjust":
                    continue
                rt = op.rsplit(".", 1)[0]
                segments.append({"resource_type": rt, "operation": op,
                                 "atoms": {d: {"kind": "all"} for d in DIMENSIONS.get(rt, [])}, "read_fields": [],
                                 "valid": {"from": start}})
        # every member is ready before its definitions are accepted (P8.24): one that started while the authorization
        # member was unreachable submits them again within 30 s (and the claim above restarted that member); until
        # then the grant answers DEFINITION_PENDING, which leaves the change key free, so the same key is sent again
        # security 09a's change request: a widening with its candidate and the candidate's digest (its canonical
        # form under be.security.<record type>, v1)
        candidate = {"record_type": "grant", "governance_unit": root, "grantee": user, "segments": segments}
        grant = {"change_key": "cell-load-user", "direction": "widen", "candidate": candidate,
                 "candidate_digest": canon.digest("be.security.grant", 1, candidate)}
        for _ in range(120):
            r = await h.post("/api/authz/changes", json=grant)
            if r.status_code != 503 or "DEFINITION_PENDING" not in r.text:
                break
            await asyncio.sleep(1)
        if r.status_code != 200:
            raise RuntimeError(f"granting the load user failed: {r.status_code} {r.text[:300]}")
    creds = {"token_url": iam + "/api/iam/dev/token", "username": LOAD_USER, "password": password}
    path = c.dir / ".secrets" / "load-user.json"
    path.write_text(json.dumps(creds))
    user_token, _ = await token(iam, LOAD_USER, password)
    async with httpx.AsyncClient(timeout=10, headers={"Authorization": "Bearer " + user_token}) as h:
        for p in c.probes:
            base = f"http://127.0.0.1:{c.host_port(p)}"
            await ready(base)
            for _ in range(60):  # the grant reaches each probe's view at its next verification (2.5 s)
                r = await h.post(base + "/api/probe/echo", json={"payload": "x"})
                if r.status_code == 200:
                    break
                await asyncio.sleep(0.5)
            if r.status_code != 200:
                raise RuntimeError(f"{p} does not take the load user's token: {r.status_code} {r.text[:200]}")
        for m in c.mdm:
            base = f"http://127.0.0.1:{c.host_port(m)}"
            await ready(base)
            method, path = MDM_WRITE[m]
            for _ in range(60):
                r = await h.request(method, base + path, json={})
                if r.status_code == 400:
                    break
                await asyncio.sleep(0.5)
            if r.status_code != 400:
                raise RuntimeError(f"{m} does not take the load user's token: {r.status_code} {r.text[:200]}")
        if "infra/print" in c.erp:  # its edit route decides the operation before anything: 501 once granted
            base = f"http://127.0.0.1:{c.host_port('infra/print')}"
            await ready(base)
            for _ in range(60):
                r = await h.post(base + "/api/print/templates/invoice/versions", json={})
                if r.status_code == 501:
                    break
                await asyncio.sleep(0.5)
            if r.status_code != 501:
                raise RuntimeError(f"infra/print does not take the load user's token: {r.status_code} {r.text[:200]}")
        for m in c.erp:
            await ready(f"http://127.0.0.1:{c.host_port(m)}")
        # The Go business members check the body before the operation, so no route tells when the grant arrived; each
        # member verifies its view every 2.5 s, so after two periods every one of them has it.
        await asyncio.sleep(5)
    return {"admin": admin, "load_user": user, "credentials": str(path)}
