"""The events profile (be-protocol chapter 06) against one component: the Go probe produces on its own stream and
consumes a stream the suite injects into. What the outbox holds is read from the component's schema; what a consumer
does is seen through its tables after crafted messages: a duplicate, one past the hop limit, a claim check whose
object does not match. Each judge returns the problems; each bad() turns a good observation into a known violation.

Not here: CK-EV-04 (an acknowledgement lost between publishing and marking: fault injection), CK-EV-10 (a read
model that fetches back from its source: the watermark source profile), CK-BUS-01..08 (the adapters themselves: the
SDKs' transport suite), the two-replica half of CK-EV-02 (one host cannot run a component twice: the grpc port is
part of its definition)."""
from __future__ import annotations

import asyncio
import hashlib
import json
import time
import uuid
from urllib.parse import urlsplit

import asyncpg
import httpx

import os
import signal
import subprocess

from harness import DATABASE, Bus, Deployment, Running, Spec, free_port, need
from profile.checks import mutated
from profile.model import Check

INJECTED = "test.probe_inj"  # the stream the suite writes and the probe consumes
WRITTEN = "test.probe.written.v1"
RETENTION_S = 7 * 24 * 3600


def admin_url() -> str:
    return urlsplit(need("BE_ACC_PG_ADMIN"))._replace(path="/" + DATABASE).geturl()


async def eventually(fn, timeout: float = 20.0, every: float = 0.2):
    """fn's first truthy result within timeout, else its last one."""
    deadline = time.monotonic() + timeout
    while True:
        got = await fn()
        if got or time.monotonic() > deadline:
            return got
        await asyncio.sleep(every)


async def write(r: Running, token: str, **body) -> tuple[int, dict]:
    async with httpx.AsyncClient(timeout=15) as c:
        resp = await c.post(r.url + "/api/probe/write", json={"bytes": 10, **body},
                            headers={"Authorization": "Bearer " + token})
    try:
        return resp.status_code, resp.json()
    except ValueError:
        return resp.status_code, {}


# ---------------------------------------------------------------------------------------------- injection

def digest(event_id: str, payload: bytes) -> str:
    return hashlib.sha256(event_id.encode() + b"\0" + payload).hexdigest()


def uuid7() -> str:
    ms = int(time.time() * 1000)
    rnd = uuid.uuid4().int & ((1 << 74) - 1)
    v = (ms << 80) | (0x7 << 76) | ((rnd >> 62) << 64) | (0b10 << 62) | (rnd & ((1 << 62) - 1))
    return str(uuid.UUID(int=v))


def envelope(template: dict, payload: bytes, *, hops: int = 0, claim: bool = False, event_id: str = "") -> dict:
    """A message on the injected stream made from a real one of the probe (same source, partition, record): a new event
    ID, the hop count asked for, the digest over the payload (P6.4); a claim check carries the reference as payload
    and the digest over the object (P6.14)."""
    h = dict(template["headers"])
    eid = event_id or uuid7()
    h.update({"ce-id": eid, "ce-bestream": INJECTED, "ce-behops": str(hops), "ce-type": WRITTEN})
    h.pop("ce-becausation", None)
    h.pop("ce-beclaim", None)
    if claim:
        h["ce-beclaim"] = "true"
    else:
        h["ce-bedigest"] = digest(eid, payload)
    subject = f"be.{INJECTED}.{h['ce-bepartition']}.{WRITTEN}"
    return {"subject": subject, "headers": h, "data": payload}


async def ensure_injected(bus: Bus) -> None:
    from besdk.events.core import Stream

    if bus.kind == "jetstream":
        from besdk.events import jetstream

        tr = await jetstream.Transport.connect(bus.url, "acceptance-inject")
    else:
        from besdk.events import pgqueue

        tr = await pgqueue.Transport.connect(bus.url, "acceptance-inject")
    try:
        await tr.ensure_stream(Stream(INJECTED, 4))
    finally:
        await tr.close()
    if INJECTED not in bus.streams:
        bus.streams.append(INJECTED)


async def inject(bus: Bus, m: dict, transport_id: str) -> None:
    """Publishes as the bus stores it, with the transport's own message identity given: two messages with one event ID
    and two transport IDs are delivered twice (a redelivery past the deduplication window)."""
    if bus.kind == "jetstream":
        import nats

        nc = await nats.connect(need("BE_ACC_NATS_URL"))
        try:
            await nc.jetstream().publish(m["subject"], m["data"], headers={**m["headers"], "Nats-Msg-Id": transport_id})
        finally:
            await nc.close()
        return
    c = await asyncpg.connect(admin_url())
    try:
        await c.execute(f'INSERT INTO "{bus.schema}".be_bus_message (msg_id, subject, headers, data) VALUES ($1, $2, $3, $4)',
                        transport_id, m["subject"], json.dumps(m["headers"]), m["data"])
    finally:
        await c.close()


async def stored(bus: Bus) -> int:
    """How many messages the bus holds on INJECTED: the duplicate must be stored twice for its check to mean anything."""
    if bus.kind == "jetstream":
        import nats

        nc = await nats.connect(need("BE_ACC_NATS_URL"))
        try:
            return (await nc.jetstream().stream_info("BE_" + INJECTED.upper().replace(".", "_"))).state.messages
        finally:
            await nc.close()
    c = await asyncpg.connect(admin_url())
    try:
        return await c.fetchval(f'SELECT count(*) FROM "{bus.schema}".be_bus_message WHERE subject LIKE $1',
                                f"be.{INJECTED}.%")
    finally:
        await c.close()


async def upload(body: bytes, prefix: str) -> str:
    """An object in the probes' bucket under a producer's prefix, as a producer's claim check leaves it; returns a
    presigned GET as the claim's URI (P6.14: a consumer reads it with no credentials)."""
    import aioboto3

    key = f"{prefix}acc-inj-{uuid.uuid4().hex}"
    async with aioboto3.Session().client("s3", endpoint_url=need("BE_ACC_S3_URL"), region_name="us-east-1",
                                         aws_access_key_id=need("BE_ACC_S3_ACCESS_KEY"),
                                         aws_secret_access_key=need("BE_ACC_S3_SECRET_KEY")) as s3:
        await s3.put_object(Bucket="be-acceptance", Key=key, Body=body)
        return await s3.generate_presigned_url("get_object", Params={"Bucket": "be-acceptance", "Key": key},
                                               ExpiresIn=3600)


# ---------------------------------------------------------------------------------------------- observation

async def observe(d: Deployment, r: Running, spec: Spec, token: str) -> dict:
    """Everything the events checks judge, from one running probe that consumes INJECTED."""
    schema, stream = spec.schema, spec.stream
    c = await asyncpg.connect(admin_url())
    obs: dict = {"bus": d.bus.kind, "stream": stream}
    try:
        async def outbox(where: str = "TRUE", *args):
            return [dict(x) for x in await c.fetch(
                f"SELECT record_id, partition, seq, epoch_generation, epoch_timeline, published_at IS NOT NULL AS published,"
                f" hops, causation::text FROM {schema}.be_outbox WHERE stream = $1 AND {where} ORDER BY ord", stream, *args)]

        async def count(table: str) -> int:
            return await c.fetchval(f"SELECT count(*) FROM {schema}.{table}")

        async def forwarded() -> bool:
            return await c.fetchval(f"SELECT count(*) = 0 FROM {schema}.be_outbox WHERE published_at IS NULL") is True

        # CK-EV-01: a rolled-back business transaction leaves no outbox row; a committed one does
        before = (await count("be_outbox"), await count("probe_writes"))
        code, _ = await write(r, token, fail=True)
        after = (await count("be_outbox"), await count("probe_writes"))
        ok, body = await write(r, token)
        obs["rollback"] = {"code": code, "before": before, "after": after, "commit_code": ok,
                           "committed": await outbox("record_id = $2", body.get("id", ""))}

        # CK-EV-02: one write holds its transaction after emitting while others commit; positions follow the commits
        held = asyncio.create_task(write(r, token, hold_ms=1500))
        await asyncio.sleep(0.2)
        quick = [(await write(r, token))[1].get("id") for _ in range(20)]
        held_id = (await held)[1].get("id")
        await eventually(forwarded)
        rows = await outbox()
        obs["numbering"] = {"rows": rows, "held": held_id, "quick": quick}

        # CK-EV-05: the published rows stay for the redelivery retention
        obs["retained"] = {"rows": await outbox(), "retention_s": RETENTION_S}

        # CK-EV-03: the deployment's recovery generation advances (an operator restored the database); the next write
        gen = await c.fetchval(f"SELECT generation FROM {schema}.be_recovery_generation")
        await c.execute(f"UPDATE {schema}.be_recovery_generation SET generation = generation + 1")
        _, body = await write(r, token)
        await eventually(forwarded)
        new = await outbox("record_id = $2", body.get("id", ""))
        history = [dict(x) for x in await c.fetch(
            f"SELECT partition, old_generation, fork_seq, new_generation FROM {schema}.be_epoch_history WHERE stream = $1",
            stream)]
        obs["epoch"] = {"old_generation": gen, "row": new[0] if new else None, "history": history,
                        "old_rows": [x for x in await outbox() if x["epoch_generation"] == gen]}

        # the consumer: crafted messages on INJECTED, made from one of the probe's own envelopes
        template = await own_message(d.bus, stream)
        await ensure_injected(d.bus)

        async def seen(eid: str) -> int:
            return await c.fetchval(f"SELECT count(*) FROM {schema}.probe_seen WHERE event_id = $1::uuid", eid)

        async def dead(eid: str) -> list[dict]:
            return [dict(x) for x in await c.fetch(
                f"SELECT error, attempts FROM {schema}.be_dead_letter WHERE event_id = $1", eid)]

        rid = template["headers"]["ce-subject"]
        # CK-EV-08: one event delivered twice
        payload = json.dumps({"id": rid, "relay": False}).encode()
        dup = envelope(template, payload)
        before = await stored(d.bus)
        for _ in range(2):
            await inject(d.bus, dup, uuid.uuid4().hex)
        await eventually(lambda: seen(dup["headers"]["ce-id"]))
        await asyncio.sleep(2)  # the second delivery arrives and is handled by now
        obs["duplicate"] = {"stored": await stored(d.bus) - before, "seen": await seen(dup["headers"]["ce-id"])}

        # CK-EV-07: an event at the hop limit whose handling emits one more
        far = envelope(template, json.dumps({"id": rid, "relay": False, "emit": True}).encode(), hops=10)
        await inject(d.bus, far, uuid.uuid4().hex)
        letters = await eventually(lambda: dead(far["headers"]["ce-id"]), timeout=30)
        obs["hops"] = {"dead": letters, "seen": await seen(far["headers"]["ce-id"]),
                       "emitted": await outbox("causation = $2::uuid", far["headers"]["ce-id"]),
                       "over": await outbox("hops > 10")}

        # CK-EV-06: a claim check whose object does not match its digest
        body = json.dumps({"id": rid, "relay": False, "blob": "w" * 70000}).encode()
        uri = await upload(body, schema + "/")
        async with httpx.AsyncClient(timeout=15) as h:
            readable = (await h.get(uri)).status_code
        eid = uuid7()
        ref = {"claim": {"uri": uri, "size": len(body), "sha256": hashlib.sha256(b"not the object").hexdigest()}}
        bad = envelope(template, json.dumps(ref).encode(), claim=True, event_id=eid)
        bad["headers"]["ce-bedigest"] = digest(eid, b"not the object")
        await inject(d.bus, bad, uuid.uuid4().hex)
        letters = await eventually(lambda: dead(eid), timeout=30)
        good = envelope(template, payload)  # the subscription goes on
        await inject(d.bus, good, uuid.uuid4().hex)
        obs["claim"] = {"readable": readable, "dead": letters, "seen": await seen(eid),
                        "after": await eventually(lambda: seen(good["headers"]["ce-id"]))}
    finally:
        await c.close()
    obs["bus_retention"] = await retention(d.bus, stream)
    return obs


async def own_message(bus: Bus, stream: str) -> dict:
    """One message of the probe's own stream as the bus holds it."""
    if bus.kind == "jetstream":
        import nats

        nc = await nats.connect(need("BE_ACC_NATS_URL"))
        try:
            js = nc.jetstream()
            name = "BE_" + stream.upper().replace(".", "_")
            msg = await js.get_msg(name, seq=1)
            return {"subject": msg.subject, "headers": dict(msg.headers or {}), "data": msg.data}
        finally:
            await nc.close()
    c = await asyncpg.connect(admin_url())
    try:
        row = await c.fetchrow(f'SELECT subject, headers, data FROM "{bus.schema}".be_bus_message WHERE subject LIKE $1 '
                               "ORDER BY seq LIMIT 1", f"be.{stream}.%")
        return {"subject": row["subject"], "headers": json.loads(row["headers"]), "data": row["data"]}
    finally:
        await c.close()


async def retention(bus: Bus, stream: str) -> dict:
    """CK-BUS-09's configuration half: JetStream keeps a stream's messages for the transport retention (MaxAge); the
    PostgreSQL queue deletes past it in each process (checked by the SDKs' tests: the first purge runs a minute after
    the transport opens)."""
    if bus.kind != "jetstream":
        return {"kind": bus.kind}
    import nats

    nc = await nats.connect(need("BE_ACC_NATS_URL"))
    try:
        info = await nc.jetstream().stream_info("BE_" + stream.upper().replace(".", "_"))
        return {"kind": bus.kind, "max_age_s": info.config.max_age}
    finally:
        await nc.close()


# ---------------------------------------------------------------------------------------------- the bus at start

async def late_proxy(host: str, port: int, delay: float) -> tuple[int, asyncio.Task]:
    """A port that listens only after delay, then forwards to the bus: the machine boots both, the bus comes later."""
    listen = free_port()

    async def pipe(r, w):
        try:
            while data := await r.read(65536):
                w.write(data)
                await w.drain()
        except (ConnectionError, asyncio.CancelledError):
            pass
        finally:
            w.close()

    async def handle(r, w):
        ur, uw = await asyncio.open_connection(host, port)
        await asyncio.gather(pipe(r, uw), pipe(ur, w))

    async def later():
        await asyncio.sleep(delay)
        return await asyncio.start_server(handle, "127.0.0.1", listen)

    return listen, asyncio.create_task(later())


def with_address(url: str, port: int, password: str | None = None) -> str:
    u = urlsplit(url)
    auth = u.netloc.rsplit("@", 1)[0] + "@" if "@" in u.netloc else ""
    if password is not None and auth:
        auth = auth.split(":", 1)[0] + ":" + password + "@"
    return u._replace(netloc=f"{auth}127.0.0.1:{port}").geturl()


async def readiness(url: str, seconds: float) -> list[tuple[float, int]]:
    """(seconds since start, /readyz status or 0 when nothing answers) every 0.2 s."""
    out, start = [], time.monotonic()
    async with httpx.AsyncClient(timeout=1) as c:
        while (t := time.monotonic() - start) < seconds:
            try:
                out.append((round(t, 1), (await c.get(url + "/readyz")).status_code))
            except httpx.HTTPError:
                out.append((round(t, 1), 0))
            await asyncio.sleep(0.2)
    return out


async def stop(proc: subprocess.Popen) -> int:
    proc.send_signal(signal.SIGTERM)
    return await asyncio.to_thread(proc.wait, 30)


async def bus_start(d: Deployment, spec: Spec, env: dict) -> dict:
    """CK-BUS-10 (P6.34): the probe started before its bus stays starting and gets ready once the bus listens; with
    no bus at all a stop signal ends it with 0; credentials the bus refuses (PostgreSQL queue) or a wrong scheme end
    it with 78. The probe's other process must have stopped first (one grpc port per component)."""
    bus = urlsplit(d.bus.url)
    out: dict = {}

    def launch(extra: dict) -> tuple[subprocess.Popen, str]:
        port = free_port()
        full = {**os.environ, **env, **extra, "PORT": str(port)}
        return subprocess.Popen(spec.command, env=full, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL), \
            f"http://127.0.0.1:{port}"

    proxy, server = await late_proxy(bus.hostname, bus.port or (4222 if d.bus.kind == "jetstream" else 5432), 3.0)
    proc, url = launch({"BUS_URL": with_address(d.bus.url, proxy)})
    try:
        out["late"] = await readiness(url, 8.0)
    finally:
        out["late_exit"] = await stop(proc)
        (await server).close()

    proc, url = launch({"BUS_URL": with_address(d.bus.url, free_port())})  # nothing ever listens there
    try:
        out["absent"] = await readiness(url, 2.0)
        out["absent_alive"] = proc.poll() is None
    finally:
        out["absent_exit"] = await stop(proc)

    refused = (d.bus.url.replace("nats://", "natsx://", 1) if d.bus.kind == "jetstream"
               else with_address(d.bus.url, bus.port or 5432, password="not-the-password"))
    proc, _ = launch({"BUS_URL": refused})
    try:
        out["refused_exit"] = await asyncio.to_thread(proc.wait, 30)
    except subprocess.TimeoutExpired:
        out["refused_exit"] = await stop(proc)
    return out


# ---------------------------------------------------------------------------------------------- judges

def judge_rollback(obs: dict) -> list[str]:
    o, p = obs["rollback"], []
    if o["code"] < 400:
        p.append(f"a write asked to roll back answered {o['code']}")
    if o["after"] != o["before"]:
        p.append(f"a rolled-back write left rows behind (outbox, writes {o['before']} -> {o['after']})")
    if o["commit_code"] != 201 or len(o["committed"]) != 1:
        p.append(f"a committed write answered {o['commit_code']} with {len(o['committed'])} outbox rows")
    return p


def judge_numbering(obs: dict) -> list[str]:
    o, p = obs["numbering"], []
    by: dict[tuple, list[int]] = {}
    for x in o["rows"]:
        if x["seq"] is None:
            p.append(f"row {x['record_id']} is not numbered")
            continue
        by.setdefault((x["partition"], x["epoch_generation"], x["epoch_timeline"]), []).append(x["seq"])
    for key, seqs in by.items():
        if sorted(seqs) != list(range(1, len(seqs) + 1)):
            p.append(f"positions of partition {key[0]} are {sorted(seqs)}: not contiguous from 1, or repeated")
    row = {x["record_id"]: x for x in o["rows"]}
    held = row.get(o["held"])
    if held is None:
        return p + ["the held write has no outbox row"]
    earlier = [row[q] for q in o["quick"] if q in row and row[q]["partition"] == held["partition"]]
    if not earlier:
        return p + ["no write of the held row's partition committed during the hold (rerun)"]
    late = [e["record_id"] for e in earlier if e["seq"] > held["seq"]]
    if late:
        p.append(f"the long transaction's row (position {held['seq']}) is numbered before rows that committed first: {late}")
    return p


def judge_retained(obs: dict) -> list[str]:
    o = obs["retained"]
    unpublished = [x["record_id"] for x in o["rows"] if not x["published"]]
    if unpublished:
        return [f"rows still unpublished after forwarding: {unpublished[:5]}"]
    return [] if o["rows"] else ["no published row is kept: the outbox does not hold rows for the redelivery retention"]


def judge_epoch(obs: dict) -> list[str]:
    o, p = obs["epoch"], []
    row = o["row"]
    if row is None:
        return ["the write after the generation advanced has no outbox row"]
    if row["epoch_generation"] != o["old_generation"] + 1 or row["seq"] != 1:
        p.append(f"the first row of the new generation is at ({row['epoch_generation']}, {row['seq']}), not "
                 f"({o['old_generation'] + 1}, 1)")
    kept = [x["seq"] for x in o["old_rows"] if x["partition"] == row["partition"] and x["seq"] is not None]
    fork = max(kept, default=0)
    match = [h for h in o["history"] if h["partition"] == row["partition"] and h["old_generation"] == o["old_generation"]
             and h["new_generation"] == o["old_generation"] + 1]
    if not match:
        p.append(f"no epoch history for partition {row['partition']}")
    elif match[0]["fork_seq"] != fork:
        p.append(f"the fork point is {match[0]['fork_seq']}, the old epoch's last position was {fork}")
    return p


def judge_duplicate(obs: dict) -> list[str]:
    o = obs["duplicate"]
    if o["stored"] != 2:
        return [f"the bus holds {o['stored']} copies of the duplicate, not 2: the check proves nothing (precondition)"]
    return [] if o["seen"] == 1 else [f"one event delivered twice took effect {o['seen']} times"]


def judge_hops(obs: dict) -> list[str]:
    o, p = obs["hops"], []
    if o["emitted"] or o["over"]:
        p.append(f"an event past the hop limit was emitted: {(o['emitted'] or o['over'])[:2]}")
    if o["seen"]:
        p.append("the handling that emitted past the limit committed")
    if not o["dead"]:
        p.append("the event whose handling failed every time is not dead-lettered")
    return p


def judge_claim(obs: dict) -> list[str]:
    o, p = obs["claim"], []
    if o["readable"] != 200:
        return [f"the claimed object answered {o['readable']} to the suite: the failure would not be the digest's "
                "(precondition)"]
    if o["seen"]:
        p.append("a claim check whose object does not match its digest was handled")
    if not o["dead"]:
        p.append("the event with the mismatched claim check is not dead-lettered")
    if not o["after"]:
        p.append("the subscription is stuck after the mismatched claim check")
    return p


def judge_bus_retention(obs: dict) -> list[str]:
    o = obs["bus_retention"]
    if o["kind"] != "jetstream":
        return []
    return [] if o.get("max_age_s") == RETENTION_S else [f"the stream keeps messages {o.get('max_age_s')} s, not the "
                                                         f"transport retention of {RETENTION_S} s"]


def judge_bus_start(obs: dict) -> list[str]:
    o, p = obs["bus_start"], []
    early = [code for t, code in o["late"] if t < 2.5]
    late = [code for t, code in o["late"] if t > 6.0]
    if any(code == 200 for code in early):
        p.append("ready before its bus listened")
    if not late or late[-1] != 200:
        p.append(f"not ready 3 s after its bus listened: {o['late'][-3:]}")
    if o["late_exit"] != 0:
        p.append(f"stopped after a late bus, it exited {o['late_exit']}")
    if not o["absent_alive"] or any(code == 200 for _, code in o["absent"]):
        p.append("with no bus it did not keep starting")
    if o["absent_exit"] != 0:
        p.append(f"stopped while waiting for its bus, it exited {o['absent_exit']}, not 0")
    if o["refused_exit"] != 78:
        p.append(f"a bus that refuses the settings ended it with {o['refused_exit']}, not 78")
    return p


def _late_held(o: dict) -> None:
    n = o["numbering"]
    held = next((x for x in n["rows"] if x["record_id"] == n["held"]), None)
    first = held and next((x for x in n["rows"] if x["record_id"] in n["quick"] and x["partition"] == held["partition"]),
                          None)
    if first is None:  # no row to swap: judge_numbering already reports why (the held row missing, or "rerun");
        return         # crashing here hid that as "the check failed to run" (1.0.73)
    held["seq"], first["seq"] = first["seq"], held["seq"]


EVENTS = [
    Check("CK-EV-01", "业务事务回滚时没有发件行，提交后才有", ("P6.2",), ("rollback",), judge_rollback,
          lambda o: mutated(o, lambda x: x["rollback"].update(after=(x["rollback"]["before"][0] + 1,
                                                                     x["rollback"]["before"][1])))),
    Check("CK-EV-02", "位置连续、不重号，长事务的行晚于先提交的行编号", ("P6.5", "P6.6"), ("numbering",),
          judge_numbering, lambda o: mutated(o, _late_held)),
    Check("CK-EV-03", "恢复代次推进后新纪元从 1 编号，纪元历史记下分叉点", ("P6.7", "P6.8"), ("epoch",), judge_epoch,
          lambda o: mutated(o, lambda x: x["epoch"].update(history=[]))),
    Check("CK-EV-05", "已发布的行在补发保留内仍在", ("P6.12",), ("retained",), judge_retained,
          lambda o: mutated(o, lambda x: x["retained"].update(rows=[]))),
    Check("CK-EV-06", "摘要不符的凭证检查按处理失败", ("P6.14",), ("claim",), judge_claim,
          lambda o: mutated(o, lambda x: x["claim"].update(seen=1))),
    Check("CK-EV-07", "跳数超过 10 的事件不发出", ("P6.15",), ("hops",), judge_hops,
          lambda o: mutated(o, lambda x: x["hops"].update(emitted=[{"hops": 11}]))),
    Check("CK-EV-08", "同一事件投递两次，业务效果一次", ("P6.25",), ("duplicate",), judge_duplicate,
          lambda o: mutated(o, lambda x: x["duplicate"].update(seen=2))),
    Check("CK-BUS-09", "运输按保留删除消息（JetStream 的 MaxAge）", ("P6.27",), ("bus_retention",), judge_bus_retention,
          lambda o: mutated(o, lambda x: x["bus_retention"].update(kind="jetstream", max_age_s=0))),
    Check("CK-BUS-10", "运输晚到时等它；一直没有时停止以 0 退出；被拒时 78", ("P6.34",), ("bus_start",), judge_bus_start,
          lambda o: mutated(o, lambda x: x["bus_start"].update(absent_exit=69))),
]
