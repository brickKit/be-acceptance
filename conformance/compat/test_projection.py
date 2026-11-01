"""Suite D V3, the read model's side (protocol P6.33, protocol 02 PJ36), on a running business-chain cell with its seed
data (BE_ACC_CELL): events a later mdm/org would publish, written into mdm/org's stream as its forwarder would (the
envelope copied from a real event of the stream, so the implementation instance and the epoch are mdm/org's own), and
what erp/sales's department projection does with them. The source's change itself is made in mdm/org's table, as the
later version's own write would have."""
from __future__ import annotations

import asyncio
import json
import os
import pathlib
import sys
import time
import uuid
from datetime import datetime, timezone
from hashlib import sha256

import asyncpg
import nats
import pytest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))

from cells.faults import _conn  # noqa: E402
from cells.spec import Cell  # noqa: E402
from harness import need  # noqa: E402

STREAM, JS, PARTITIONS = "mdm.org", "BE_MDM_ORG", 4
CONSUMER = "erp_sales_org_unit_events"


@pytest.fixture(scope="module")
def cell() -> Cell:
    d = os.environ.get("BE_ACC_CELL", "")
    if not d:
        raise AssertionError("BE_ACC_CELL is not set: these tests run against a running cell")
    c = Cell.load(d)
    if "erp/sales" not in c.placed or "mdm/org" not in c.placed:
        pytest.skip("the projection side needs erp/sales and mdm/org")
    return c


def partition(record_id: str) -> int:
    """P6.1: FNV-1a 64 of the record identity's lower-case string, modulo the partitions."""
    h = 0xCBF29CE484222325
    for b in record_id.lower().encode():
        h = ((h ^ b) * 0x100000001B3) & 0xFFFFFFFFFFFFFFFF
    return h % PARTITIONS


async def template(js) -> dict[str, str]:
    """The headers of a real mdm/org event in the stream: its instance and epoch (P6.9, P6.7)."""
    for p in range(PARTITIONS):
        try:
            m = await js.get_last_msg(JS, f"be.{STREAM}.{p}.mdm.org_unit.changed.v1")
            return dict(m.headers or {})
        except nats.js.errors.NotFoundError:
            continue
    raise AssertionError("mdm/org's stream has no department event to copy the envelope from")


async def publish(js, base: dict, typ: str, record_id: str, version: int, data: dict) -> str:
    eid = str(uuid.uuid7())
    payload = json.dumps(data, ensure_ascii=False).encode()
    h = {k: v for k, v in base.items() if k.startswith("ce-") and k not in ("ce-becausation",)}
    p = partition(record_id)
    h.update({"ce-id": eid, "ce-type": typ, "ce-subject": record_id, "ce-bepartition": str(p),
              "ce-beversion": str(version), "ce-beprojection": "full", "ce-behops": "0",
              "ce-time": datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z"),
              "ce-beseq": str(10**12 + int(time.time() * 1000)),
              "ce-bedigest": sha256(eid.encode() + b"\0" + payload).hexdigest(), "Nats-Msg-Id": eid})
    await js.publish(f"be.{STREAM}.{p}.{typ}", payload, headers=h)
    return eid


async def row(conn: asyncpg.Connection, record_id: str):
    return await conn.fetchrow("SELECT state, version, data FROM erp_sales.be_projection "
                               "WHERE projection = 'org_units' AND record_id = $1", record_id)


async def eventually(fn, within: float = 60.0):
    end, last = time.monotonic() + within, None
    while time.monotonic() < end:
        try:
            return await fn()
        except AssertionError as e:
            last = e
            await asyncio.sleep(0.5)
    raise last


async def test_v3_later_mdm_org_events_reach_the_department_projection(cell):
    conn = await _conn(cell)
    nc = await nats.connect(need("BE_ACC_NATS_URL"))
    js = nc.jetstream()
    try:
        base = await template(js)
        unit, version = await conn.fetchrow(
            "SELECT id::text, version FROM mdm_org.org_unit WHERE state = 'active' ORDER BY id LIMIT 1")

        async def complete_at(v):
            r = await row(conn, unit)
            assert r is not None and (r["state"], r["version"]) == ("complete", v), dict(r or {})
            return r
        await eventually(lambda: complete_at(version))

        # 1. an action this version of erp/sales does not know: the department is fetched, its payload not used
        renamed = await conn.fetchval("UPDATE mdm_org.org_unit SET name = name || '（改）', version = version + 1 "
                                      "WHERE id = $1 RETURNING version", uuid.UUID(unit))
        name = await conn.fetchval("SELECT name FROM mdm_org.org_unit WHERE id = $1", uuid.UUID(unit))
        await publish(js, base, "mdm.org_unit.renamed.v1", unit, renamed, {"unit_id": unit, "new_name": "垃圾"})
        r = await eventually(lambda: complete_at(renamed))
        assert json.loads(r["data"])["name"] == name, "fetched from mdm/org, not taken from the unknown payload"

        # 2. another aggregate on the same stream: not a department
        le = await conn.fetchval("SELECT legal_entity_id::text FROM mdm_org.org_unit WHERE id = $1", uuid.UUID(unit))
        await publish(js, base, "mdm.legal_entity.changed.v1", le, 10**6, {"id": le, "name": "法人"})

        # 3. the known type with a field this version does not know: applied as usual
        changed = await conn.fetchval("UPDATE mdm_org.org_unit SET version = version + 1 WHERE id = $1 RETURNING version",
                                      uuid.UUID(unit))
        data = {**json.loads(r["data"]), "version": changed, "added_later": "x"}
        await publish(js, base, "mdm.org_unit.changed.v1", unit, changed, data)
        await eventually(lambda: complete_at(changed))

        await asyncio.sleep(3)
        assert await row(conn, le) is None, "a legal entity's event is not a department"
        assert await conn.fetchval("SELECT count(*) FROM erp_sales.be_dead_letter") == 0
        info = await js.consumer_info(JS, CONSUMER)
        assert (info.num_pending, info.num_ack_pending) == (0, 0), "the subscription goes on"
    finally:
        await nc.close()
        await conn.close()
