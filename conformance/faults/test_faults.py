"""Suite C (step11-plan.md of the assembly project): a fault in the middle of a business step, and the dependencies
going away, while the background load runs. Each test makes its own order, injects the fault from outside
(cells/faults.py), checks that the business outcome is right once the work behind it has run, and appends what it
measured to BE_ACC_FAULT_LOG (one JSON line: the fault, unix times, seconds). Run one at a time (`-k`): they kill
processes and stop base resources. The cell-wide invariants are checked after each by the runner
(`python -m cells invariants`)."""
from __future__ import annotations

import asyncio
import json
import os
import threading
import time
import uuid
from decimal import Decimal

import httpx
import pytest

from cells import faults
from cells.faults import backlog_summary

HOLD_TTL = 300  # erp/inventory and erp/credit: a hold expires after five minutes (protocol 04's starting value)


def log(**fields) -> None:
    line = json.dumps({"at": time.time(), **fields})
    print(line, flush=True)
    if path := os.environ.get("BE_ACC_FAULT_LOG"):
        with open(path, "a") as f:
            f.write(line + "\n")


def eventually(check, seconds: float = 120, every: float = 1):
    deadline, last = time.monotonic() + seconds, None
    while time.monotonic() < deadline:
        try:
            if out := check():
                return out
        except (AssertionError, httpx.HTTPError) as e:
            last = e
        time.sleep(every)
    raise AssertionError(f"not true within {seconds} s: {last}")


def read(bob, component: str, path: str) -> dict:
    """A GET that outlives the test account's token: RS05 waits past its lifetime, so on 401 it logs in again."""
    r = bob.call("GET", component, path)
    if r.status_code == 401:
        from harness import need
        t = httpx.post(f"http://127.0.0.1:{bob.cell.host_port('infra/iam-casdoor')}/api/iam/dev/token",
                       json={"username": bob.name, "password": need("BE_ACC_IAM_TEST_PASSWORD")}, timeout=30)
        t.raise_for_status()
        bob.token = t.json()["access_token"]
        r = bob.call("GET", component, path)
    return bob.ok(r)


def order(bob, world, qty: int = 2) -> dict:
    p, s = world.goods, world.service
    return bob.post("erp/sales", "/api/sales/orders", {
        "legal_entity_id": world.le, "customer_id": world.large, "currency": "CNY", "owner_id": bob.principal,
        "lines": [{"product_id": p["id"], "quantity": str(qty), "unit": p["base_unit"], "warehouse_id": world.shanghai,
                   "tax_rate": "0.13"},
                  {"product_id": s["id"], "quantity": "1", "unit": s["base_unit"], "tax_rate": "0.06"}]})


def in_background(bob, method: str, component: str, path: str) -> threading.Thread:
    """A request whose process is about to be killed: its answer (if any) does not matter."""
    def go():
        try:
            bob.call(method, component, path)
        except httpx.HTTPError:
            pass
    t = threading.Thread(target=go, daemon=True)
    t.start()
    return t


def participants(cell, order_id: str) -> dict[str, list[str]]:
    """The participants' branch states under the order's processes, by component."""
    async def read():
        import asyncpg
        from urllib.parse import urlsplit
        from harness import need
        conn = await asyncpg.connect(urlsplit(need("BE_ACC_PG_ADMIN"))._replace(path="/" + cell.database).geturl())
        try:
            rows = await conn.fetch("""
                SELECT cb.component, coalesce(ib.state, cr.state) FROM erp_sales.confirmation c
                JOIN erp_sales.confirmation_branch cb ON cb.confirmation_id = c.id
                LEFT JOIN erp_inventory.branch ib ON cb.component = 'erp/inventory' AND ib.id = cb.branch_id
                LEFT JOIN erp_credit.branch cr ON cb.component = 'erp/credit' AND cr.id = cb.branch_id
                WHERE c.order_id = $1 AND c.kind = 'confirm'""", order_id)
            out: dict[str, list[str]] = {}
            for comp, state in rows:
                out.setdefault(comp, []).append(state)
            return out
        finally:
            await conn.close()
    return asyncio.run(read())


def crash_in(cell, point: faults.Point, component: str, start) -> dict:
    """Arm the point, start the step, crash the component's process once the step sleeps there; returns the times."""
    asyncio.run(point.arm())
    start()
    asyncio.run(point.wait_sleeping())
    before, t_kill = faults.starts(cell, component), time.time()
    killed = faults.kill(cell, component)
    return {"killed": killed, "kill_at": t_kill, "before": before}


def test_rs06_coordinator_crashes_after_every_branch_confirmed(cell, bob, world):
    """Protocol 04 RS06: inventory and credit have confirmed the occupation, sales crashes before it commits. On
    recovery it confirms the order once, with the same branches: nothing lost, nothing occupied twice."""
    o = order(bob, world)
    p = faults.Point(cell, "branch-committed", o["id"])
    t = crash_in(cell, p, "erp/sales", lambda: in_background(bob, "POST", "erp/sales", f"/api/sales/orders/{o['id']}/confirm"))
    ended = asyncio.run(p.release())
    up = faults.wait_running(cell, "erp/sales", t.pop("before"))
    done = eventually(lambda: (x := read(bob, "erp/sales", f"/api/sales/orders/{o['id']}"))["state"] == "confirmed" and x)
    settled = time.time() - t["kill_at"]
    branches = participants(cell, o["id"])
    log(fault="RS06", order=o["number"], **t, ended=ended, up_after=round(up, 1), settled_after=round(settled, 1),
        branches=branches)
    assert done["state"] == "confirmed"
    assert branches == {"erp/inventory": ["committed"], "erp/credit": ["committed"]}, branches


def test_rs05_coordinator_down_past_the_hold_period(cell, bob, world):
    """Protocol 04 RS05: every branch held, sales crashes and cannot go on for longer than the hold period. On recovery
    a branch has expired: sales releases the others and the order stays unconfirmed (processing_timeout)."""
    o = order(bob, world)
    p = faults.Point(cell, "process-committing", o["id"])
    t = crash_in(cell, p, "erp/sales", lambda: in_background(bob, "POST", "erp/sales", f"/api/sales/orders/{o['id']}/confirm"))
    up = faults.wait_running(cell, "erp/sales", t.pop("before"))
    # the restarted process's recovery meets the same point and waits there: the order cannot move until the holds expire
    time.sleep(max(0.0, t["kill_at"] + HOLD_TTL + 60 - time.time()))
    ended = asyncio.run(p.release())
    done = eventually(lambda: (x := read(bob, "erp/sales", f"/api/sales/orders/{o['id']}"))["state"] == "draft" and x, 300)
    settled = time.time() - t["kill_at"]
    branches = eventually(lambda: (b := participants(cell, o["id"])) and
                          not any(s in ("held", "committed") for ss in b.values() for s in ss) and b, 120)
    log(fault="RS05", order=o["number"], **t, ended=ended, up_after=round(up, 1), settled_after=round(settled, 1),
        reason=done.get("reason"), branches=branches)
    assert done.get("reason") == "processing_timeout", done
    assert any("expired" in ss for ss in branches.values()), branches


def test_bl04_billing_crashes_before_the_source_is_formalized(cell, bob, world):
    """Protocol 05 BL04: the invoice is issued, billing crashes while sales formalizes the claim. On recovery billing
    formalizes again: the invoice is issued and formalized once, the order completes."""
    o = order(bob, world)
    ans = bob.post("erp/sales", f"/api/sales/orders/{o['id']}/confirm")
    assert ans["outcome"] == "confirmed", ans
    line = next(x for x in ans["order"]["lines"] if x["kind"] == "goods")
    bob.post("erp/sales", "/api/sales/deliveries", {"order_id": o["id"], "lines": [{"order_line_id": line["id"], "quantity": "2"}]})
    inv = bob.post("erp/billing", "/api/billing/invoices", {"order_id": o["id"]})
    p = faults.Point(cell, "claim-formalized", o["id"])
    t = crash_in(cell, p, "erp/billing", lambda: in_background(bob, "POST", "erp/billing", f"/api/billing/invoices/{inv['id']}/issue"))
    ended = asyncio.run(p.release())
    up = faults.wait_running(cell, "erp/billing", t.pop("before"))
    issued = eventually(lambda: (x := read(bob, "erp/billing", f"/api/billing/invoices/{inv['id']}"))["state"] == "issued" and x)
    done = eventually(lambda: (x := read(bob, "erp/sales", f"/api/sales/orders/{o['id']}"))["state"] == "completed" and x)
    settled = time.time() - t["kill_at"]
    log(fault="BL04", order=o["number"], invoice=issued.get("number"), **t, ended=ended, up_after=round(up, 1),
        settled_after=round(settled, 1))
    assert issued["number"] and Decimal(issued["totals"]["gross"]) > 0
    assert done["state"] == "completed"


@pytest.mark.parametrize("component", ["erp/sales", "erp/inventory", "erp/billing"], ids=["sales", "inventory", "billing"])
def test_a_process_crashes_under_load(cell, component):
    """R1: the process that runs the component is killed at an arbitrary moment of the load; it comes back by itself.
    What the load saw is in its per-second series; what must hold afterwards, in the runner's invariants."""
    before, t_kill = faults.starts(cell, component), time.time()
    killed = faults.kill(cell, component)
    up = faults.wait_running(cell, component, before)
    log(fault="crash", component=component, killed=killed, kill_at=t_kill, up_after=round(up, 1))


def test_rs18_confirming_while_the_bus_is_down(cell, bob, world):
    """Protocol 04 RS18, R2: NATS stops for two minutes; orders still confirm (their events wait in the outbox), and
    once NATS is back the outbox drains (the runner's invariants check it). Meanwhile a department of the test's own is
    renamed in mdm/org: once NATS is back, erp/sales' projection of it catches up. The outbox backlog is sampled every
    second: its peak, and how long after the start it was back to what was in flight before the stop."""
    code = "RS18-" + uuid.uuid4().hex[:8].upper()
    unit = bob.post("mdm/org", "/api/org/units", {"legal_entity_id": world.le, "parent_id": world.departments["SALES-EAST"],
                                                   "code": code, "name": "「本地测试」RS18 " + code})
    projected = lambda: asyncio.run(faults.projected_version(cell, "org_units", unit["id"])) or 0  # noqa: E731
    eventually(lambda: projected() >= unit["version"], 60)
    with faults.Backlog(cell) as backlog:
        time.sleep(10)  # what is in flight before the stop
        t_stop = time.time()
        faults.base("stop", "be-nats")
        try:
            ans = bob.post("erp/sales", f"/api/sales/orders/{order(bob, world)['id']}/confirm")
            renamed = bob.ok(bob.call("PATCH", "mdm/org", f"/api/org/units/{unit['id']}",
                                      {"name": "「本地测试」RS18 " + code + " renamed", "version": unit["version"]}))
            time.sleep(max(0.0, t_stop + 120 - time.time()))
        finally:
            faults.base("start", "be-nats")
        t_start = time.time()
        caught = eventually(lambda: projected() >= renamed["version"] and time.time(), 300)
        eventually(lambda: backlog_summary(backlog.samples, t_stop, t_start)["drained_after"] is not None, 300)
    b = backlog_summary(backlog.samples, t_stop, t_start)
    log(fault="RS18", stop_at=t_stop, start_at=t_start, outcome=ans["outcome"], unit=code,
        projection_after=round(caught - t_start, 1), backlog_before=b["before"], backlog_peak=b["peak"],
        backlog_peak_at=b["peak_at"], drained_after=b["drained_after"])
    assert ans["outcome"] == "confirmed", ans


def test_authorization_goes_away(cell, bob, world):
    """R2, security 03: the authorization member stops (in a shell, the whole shell). Writes go on while the
    components' views are valid, then are refused; once it is back they are allowed again. Measures both times."""
    def write() -> tuple[int, str]:
        r = bob.call("POST", "erp/sales", "/api/sales/orders", {
            "legal_entity_id": world.le, "customer_id": world.large, "currency": "CNY", "owner_id": bob.principal,
            "lines": [{"product_id": world.service["id"], "quantity": "1", "unit": world.service["base_unit"],
                       "tax_rate": "0.06"}]})
        try:
            reason = r.json().get("reason", "") if r.status_code != 200 else ""
        except ValueError:
            reason = r.text[:80]
        return r.status_code, reason

    t_stop = time.time()
    stopped = faults.stop(cell, "infra/authz")
    refused_after, refused_with = None, None
    try:
        while time.time() - t_stop < 600:
            if (ans := write())[0] != 200:  # (status, reason): a shell takes its other members down with it
                refused_after, refused_with = time.time() - t_stop, f"{ans[0]} {ans[1]}"
                break
            time.sleep(1)
    finally:
        faults.stop(cell, "infra/authz", "start")
    t_start = time.time()
    allowed = eventually(lambda: write()[0] == 200 and time.time() - t_start, 300)
    log(fault="authz", stopped=stopped, stop_at=t_stop, refused_after=refused_after and round(refused_after, 1),
        refused_with=refused_with, start_at=t_start, allowed_after=round(allowed, 1))


def test_the_database_restarts(cell, bob):
    """R2: PostgreSQL restarts; every component reconnects by itself. Measures the time to the first good answer."""
    t0 = time.time()
    faults.base("restart", "be-postgres")
    back = eventually(lambda: bob.call("GET", "erp/sales", "/api/sales/orders?limit=1").status_code == 200
                      and time.time() - t0, 300)
    log(fault="pg-restart", restart_at=t0, answered_after=round(back, 1))
