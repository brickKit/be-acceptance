"""Secures a running cell that runs the security members (step 5), as a deployment's first administrator would:

1. the administrator (test.alice) and the load user (test.bob) log in once through the identity member's development
   exchange (A101), which binds their platform principals;
2. the authorization member's install claim names the administrator (AUTHZ_BOOTSTRAP_ADMIN), and `brickkit up`
   restarts it with that;
3. the administrator grants the load user every operation of every probe, through the effect point;
4. once every probe and master data component takes the load user's token, the driver's credentials are written to
   .secrets/load-user.json (a seed run straight after would otherwise meet 404 from a member whose view has not got
   the grant yet).

The install claim needs a principal, and the identity member binds one only at the first login: hence the two-step
start (step 5's finding; security 08's claim by the external identity would remove it). Needs
BE_ACC_IAM_TEST_PASSWORD (the test users' password in Casdoor, scripts/dev_setup.py of infra-iam-casdoor)."""
from __future__ import annotations

import asyncio
import datetime as dt
import json
import pathlib
import re

import httpx

from cells.create import brickkit
from cells.prepare import DIMENSIONS, ERP_KEYS, MDM_KEYS, PROBE_OPS, probe_prefix
from cells.spec import Cell, file_of
from harness import need

ADMIN, LOAD_USER = "test.alice", "test.bob"
# a write route of each master data component, sent an empty body: 404 while the grant has not reached the
# component's view (not visible), 400 once it has (the operation is checked before the body). A read does not tell:
# a list answers 200 with nothing in it to a user without the read operation.
MDM_WRITE = {"mdm/org": ("POST", "/api/org/legal-entities"), "mdm/currency": ("PUT", "/api/currency/rate-types/x"),
             "mdm/partner": ("POST", "/api/partner/partners"), "mdm/product": ("POST", "/api/product/products")}


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


def claim(c: Cell, admin: str) -> None:
    f = c.dir / "config" / f"{file_of('infra/authz')}.yaml"
    text = re.sub(r"^AUTHZ_BOOTSTRAP_ADMIN: .*\n", "", f.read_text(), flags=re.M)
    f.write_text(text + f"AUTHZ_BOOTSTRAP_ADMIN: {admin}\n")


async def secure(directory: str | pathlib.Path) -> dict:
    c = Cell.load(directory)
    if not c.secured:
        raise ValueError(f"{c.name} does not run the security members")
    password = need("BE_ACC_IAM_TEST_PASSWORD")
    iam = f"http://127.0.0.1:{c.host_port('infra/iam-casdoor')}"
    authz = f"http://127.0.0.1:{c.host_port('infra/authz')}"
    await ready(iam)
    _, admin = await token(iam, ADMIN, password)
    _, user = await token(iam, LOAD_USER, password)
    claim(c, admin)
    if c.row == "L":  # a new mode: local session: the processes restart with the claim
        from cells import localrun
        localrun.up(c.dir, c.dir / ".brickkit" / "local-session.log")
    else:
        brickkit(c.dir, "up")  # the authorization member restarts with the claim
    await ready(authz)
    admin_token, _ = await token(iam, ADMIN, password)
    headers = {"Authorization": "Bearer " + admin_token}
    async with httpx.AsyncClient(base_url=authz, timeout=15, headers=headers) as h:
        for _ in range(60):  # the administrator's grant reaches the member's own view within a verification
            r = await h.get("/api/authz/records", params={"type": "management_unit"})
            if r.status_code == 200:
                break
            await asyncio.sleep(0.5)
        r.raise_for_status()
        root = next(u["id"] for u in r.json()["records"] if u["body"].get("root"))
        start = (dt.datetime.now(dt.timezone.utc) - dt.timedelta(minutes=1)).strftime("%Y-%m-%dT%H:%M:%S.%fZ")
        segments = [{"resource_type": probe_prefix(p), "operation": f"{probe_prefix(p)}.{op}", "atoms": {}, "read_fields": [],
                     "valid": {"from": start}} for p in c.probes for op in PROBE_OPS]
        # the master data components' operations too: the seed data goes through their user surfaces
        segments += [{"resource_type": op.rsplit(".", 1)[0], "operation": op, "atoms": {}, "read_fields": [],
                      "valid": {"from": start}} for comp in c.mdm for op in MDM_KEYS[comp]["permission"]
                     if not op.endswith(".system.read")]
        # the business components' user operations, every dimension open: the L3 scenario and the seed data go
        # through their user surfaces (system operations are for services; adjustments are not in this version)
        for comp in c.erp:
            for op in ERP_KEYS[comp]["permission"]:
                if ".system." in op or op == "erp.inventory.adjustment.create":
                    continue
                rt = op.rsplit(".", 1)[0]
                segments.append({"resource_type": rt, "operation": op,
                                 "atoms": {d: {"kind": "all"} for d in DIMENSIONS.get(rt, [])}, "read_fields": [],
                                 "valid": {"from": start}})
        # every member is ready before its definitions are accepted (P8.24): one that started while the authorization
        # member was unreachable submits them again within 30 s (and the claim above restarted that member), so until
        # then its resource types are not defined. That refusal is final and binds its change key (security 08), so
        # each attempt carries a new key.
        for attempt in range(120):
            r = await h.post("/api/authz/changes", json={"change_key": f"cell-load-user-{attempt}", "action": "grant.create",
                                                         "unit": root, "grantee": user, "segments": segments})
            if r.status_code != 400 or "is not defined" not in r.text:
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
