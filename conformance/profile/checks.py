"""The core profile's checks (be-protocol chapters 01, 05, 06, 10). Each judge takes the observation and returns the
problems; each bad() turns a good observation into a known violation the judge must catch (the self-test)."""
from __future__ import annotations

import copy
import hashlib
import json
import re

from prometheus_client.parser import text_string_to_metric_families

from profile.model import Check

UUID7 = re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}")
UUID = re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}")
TIME_MS = re.compile(r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3,9}Z")
TRACE = re.compile(r"[0-9a-f]{32}")
BOUNDS = [0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, float("inf")]


def mutated(obs: dict, fn) -> dict:
    o = copy.deepcopy(obs)
    fn(o)
    return o


def lines(obs: dict) -> list[str]:
    return [line for line in obs["logs"].splitlines() if line.strip()]


def records(obs: dict) -> list[dict]:
    out = []
    for line in lines(obs):
        try:
            out.append(json.loads(line))
        except ValueError:
            pass
    return out


# ---------------------------------------------------------------------------------------------- lifecycle (01)

def judge_exits(obs: dict) -> list[str]:
    p = []
    if obs["exit_unknown_arg"] != 64:
        p.append(f"an unknown entry argument exited {obs['exit_unknown_arg']}, not 64")
    if obs["migrate_again"] != 0:
        p.append(f"migrate run again exited {obs['migrate_again']}, not 0")
    return p


def judge_bad_config(obs: dict) -> list[str]:
    b, p = obs["bad_config"], []
    if b["code"] != 78:
        p.append(f"a configuration without PG_HOST and BUS_URL exited {b['code']}, not 78")
    for key in ("PG_HOST", "BUS_URL"):
        if key not in b["output"]:
            p.append(f"{key} is not named: all problems are reported at once")
    return p


def judge_endpoints(obs: dict) -> list[str]:
    p, h = [], obs["health"]
    if h["get"][0] != 200 or h["get"][1] not in ("", "ok"):
        p.append(f"GET /healthz answered {h['get']}")
    if h["head"][0] != 200:
        p.append(f"HEAD /healthz answered {h['head'][0]}")
    if obs["ready"][0] != 200:
        p.append(f"/readyz answered {obs['ready'][0]} while ready")
    if not obs["grpc_listening"]:
        p.append("the grpc port is not listening while ready")
    return p


def judge_nodelay(obs: dict) -> list[str]:
    took = sorted(obs["kept_alive_ms"])
    median = took[len(took) // 2]
    # Nagle with a delayed ACK costs about 40 ms per response; without it these answers take a few milliseconds
    return [] if median < 15 else [f"requests on a kept-alive connection take {median} ms (median of {len(took)}): "
                                   "the second write of each response waits for the delayed ACK (TCP_NODELAY off?)"]


def judge_describe(obs: dict) -> list[str]:
    d, p = obs["describe"], []
    if d.get("component_id") != obs["component"]:
        p.append(f"component_id is {d.get('component_id')!r}")
    for k in ("version", "protocol", "sdk"):
        if not isinstance(d.get(k), str) or not d.get(k):
            p.append(f"{k} is missing")
    if not isinstance(d.get("capabilities"), list):
        p.append("capabilities is not a list")
    return p


def judge_ready_line(obs: dict) -> list[str]:
    ready = [r for r in records(obs) if r.get("event") == "ready"]
    if len(ready) != 1:
        return [f"{len(ready)} lines with event ready, want 1"]
    r, p = ready[0], []
    for k in ("component", "version", "protocol", "startup_ms"):
        if k not in r:
            p.append(f"the ready line has no {k}")
    return p


def judge_stop(obs: dict) -> list[str]:
    p, seen = [], obs["stopping_ready"]
    if not seen or seen[0] != 503:
        p.append(f"/readyz did not answer 503 during the drain: {seen}")
    else:
        body = json.loads(seen[1])
        if body.get("reason") != "NOT_READY" or (body.get("metadata") or {}).get("state") != "stopping":
            p.append(f"the 503 says {body.get('reason')}, state {(body.get('metadata') or {}).get('state')}")
    if obs["stop_exit"] != 0:
        p.append(f"a graceful stop exited {obs['stop_exit']}")
    return p


# ---------------------------------------------------------------------------------------------- database (05)

def judge_database(obs: dict) -> list[str]:
    db, p = obs["db"], []
    names = {t["name"] for t in db["tables"]}
    for t in ("schema_migrations", "be_sdk_migrations", *obs["sdk_tables"]):
        if t not in names:
            p.append(f"table {t} is not in the component schema")
    shape = {(c["table_name"], c["column_name"], c["data_type"], c["is_nullable"]) for c in db["state_columns"]}
    for table in ("schema_migrations", "be_sdk_migrations"):
        if {(table, "version", "bigint", "NO"), (table, "dirty", "boolean", "NO")} - shape:
            p.append(f"{table} is not (version bigint primary key, dirty boolean not null)")
    if db["state_elsewhere"]:
        p.append(f"state tables outside the component schema: {db['state_elsewhere']}")
    p += [f"{t['name']} belongs to {t['owner']}, not the owner role" for t in db["tables"] if t["owner"] != obs["owner"]]
    p += [f"the runtime role cannot read and write {t['name']}" for t in db["tables"] if t["kind"] == "r" and not t["dml"]]
    if not db["runtime_usage"]:
        p.append("the runtime role cannot use the schema")
    if db["runtime_create"]:
        p.append("the runtime role can create objects in the schema")
    if db["public_exec"]:
        p.append(f"functions executable by PUBLIC: {db['public_exec']}")
    if db["in_public"]:
        p.append(f"objects of the component's roles in public: {db['in_public']}")
    return p


# ---------------------------------------------------------------------------------------------- envelope (06)

def fnv1a64(data: bytes) -> int:
    h = 0xCBF29CE484222325
    for b in data:
        h = ((h ^ b) * 0x100000001B3) & 0xFFFFFFFFFFFFFFFF
    return h


def row_digest(m: dict) -> str:
    """P6.4 over the payload the row holds: for a claim check that is the object fetched, not the reference (P6.14)."""
    if m["headers"].get("ce-beclaim") == "true":
        return (m.get("fetched") or {}).get("row_digest", "")
    return hashlib.sha256(m["headers"].get("ce-id", "").encode() + b"\0" + m["data"]).hexdigest()


REQUIRED = ("specversion", "id", "source", "type", "time", "subject", "datacontenttype", "bestream", "bepartition",
            "beimpl", "beepoch", "beseq", "beversion", "beprojection", "bedigest", "behops")


def judge_envelope(obs: dict) -> list[str]:
    msgs, p = obs["messages"], []
    if len(msgs) < 2 or not any(m["headers"].get("ce-beclaim") == "true" for m in msgs):
        return [f"{len(msgs)} messages captured; want the small and the big write"]
    for m in msgs:
        h, mid = m["headers"], m["headers"].get("ce-id", "?")
        missing = [a for a in REQUIRED if not h.get("ce-" + a)]
        if missing:
            p.append(f"{mid}: no {', '.join('ce-' + a for a in missing)}")
            continue
        part = fnv1a64(h["ce-subject"].lower().encode()) % obs["partitions"]
        checks = [(h["ce-specversion"] == "1.0", "specversion"), (bool(UUID7.fullmatch(h["ce-id"])), "id is not a UUIDv7"),
                  (h["ce-source"] == "urn:be:" + obs["component"], "source"),
                  (bool(TIME_MS.fullmatch(h["ce-time"])), "time is not RFC 3339 UTC"),
                  (h["ce-datacontenttype"] == "application/json", "datacontenttype"),
                  (h["ce-bestream"] == obs["stream"], "bestream"), (h["ce-bepartition"] == str(part), "bepartition"),
                  (h["ce-beimpl"] == obs["impl"], "beimpl is not the configured instance"),
                  (bool(re.fullmatch(r"\d+\.\d+", h["ce-beepoch"])), "beepoch"),
                  (h["ce-beseq"].isdigit() and int(h["ce-beseq"]) >= 1, "beseq"),
                  (h["ce-beversion"].isdigit(), "beversion"), (h["ce-beprojection"] in ("full", "partial", "delete"),
                                                                "beprojection"),
                  (h["ce-behops"].isdigit(), "behops"),
                  (m["subject"] == f"be.{obs['stream']}.{part}.{h['ce-type']}", "subject"),
                  (h["ce-bedigest"] == row_digest(m), "bedigest")]
        p += [f"{mid}: {what}" for ok, what in checks if not ok]
        if h.get("ce-beclaim") == "true":
            ref, got = json.loads(m["data"]).get("claim", {}), m.get("fetched") or {}
            if (got.get("size"), got.get("sha256")) != (ref.get("size"), ref.get("sha256")):
                p.append(f"{mid}: the claimed object does not match its reference")
    return p


# ---------------------------------------------------------------------------------------------- observability (10)

def judge_log_lines(obs: dict) -> list[str]:
    p = []
    for i, line in enumerate(lines(obs)):
        try:
            r = json.loads(line)
        except ValueError:
            p.append(f"line {i + 1} is not JSON: {line[:80]}")
            continue
        if not isinstance(r, dict):
            p.append(f"line {i + 1} is not an object")
            continue
        if not TIME_MS.fullmatch(str(r.get("time", ""))):
            p.append(f"line {i + 1}: time {r.get('time')!r} is not RFC 3339 UTC to the millisecond")
        if r.get("level") not in ("DEBUG", "INFO", "WARN", "ERROR"):
            p.append(f"line {i + 1}: level {r.get('level')!r}")
        allowed = {obs["component"], *obs.get("members", [])}  # a shell's output carries its members' lines too
        if not r.get("msg") or r.get("component") not in allowed or not r.get("version"):
            p.append(f"line {i + 1}: msg, component or version missing")
        if "request_id" in r and not TRACE.fullmatch(str(r.get("trace_id", ""))):
            p.append(f"line {i + 1}: a request line without a valid trace_id")
    return p[:20]


def judge_access(obs: dict) -> list[str]:
    p, access = [], [r for r in records(obs) if r.get("msg") == "access"]
    http = [r for r in access if "route" in r]
    grpc = [r for r in access if "code" in r]
    for r in http:
        if not all(k in r for k in ("method", "route", "status", "duration_ms", "request_id", "trace_id")):
            p.append(f"an HTTP access line lacks fields: {sorted(r)}")
        if any(seg.startswith((":", "*")) for seg in str(r.get("route", "")).split("/")):
            p.append(f"route {r['route']} is not in the OpenAPI form ({{id}})")
        if r.get("route", "").startswith(("/healthz", "/readyz", "/metrics", "/.well-known")) and r.get("level") == "INFO":
            p.append(f"an INFO access line for the platform endpoint {r['route']}")
    for r in grpc:
        if not all(k in r for k in ("method", "code", "duration_ms")) or not str(r.get("method", "")).startswith("/"):
            p.append(f"a gRPC access line lacks fields: {sorted(r)}")
    if len([r for r in http if r.get("route", "").startswith("/api/")]) < obs["http_requests"]:
        p.append("fewer HTTP access lines than requests")
    if not grpc:
        p.append("no gRPC access line")
    return p


def judge_secrets(obs: dict) -> list[str]:
    return [f"a credential or secret appears in the output ({s[:6]}…)" for s in obs["secrets"] if s in obs["logs"]]


def judge_metrics(obs: dict) -> list[str]:
    try:
        families = {f.name: f for f in text_string_to_metric_families(obs["metrics"])}
    except Exception as e:  # noqa: BLE001
        return [f"/metrics is not Prometheus text: {e}"]
    p = []
    want = {"be_http_server_duration_seconds": ("histogram", {"method", "route", "status"}),
            "be_grpc_server_duration_seconds": ("histogram", {"method", "code"}),
            "be_outbound_attempts_total": ("counter", {"dependency", "surface", "class"}),
            "be_outbound_breaker_open": ("gauge", {"dependency", "surface"})}
    for name, (kind, labels) in want.items():
        fam = families.get(name.removesuffix("_total")) or families.get(name)
        if fam is None or fam.type != kind:
            p.append(f"{name}: missing or not a {kind}")
            continue
        for s in fam.samples:
            have = set(s.labels) - {"le"}
            if have != labels:
                p.append(f"{name}: labels {sorted(have)}, want {sorted(labels)}")
                break
        if kind == "histogram":
            bounds = sorted({float(s.labels["le"]) for s in fam.samples if s.name.endswith("_bucket")})
            if bounds != BOUNDS:
                p.append(f"{name}: bucket bounds {bounds}")
    return p


def _break_log(o: dict) -> None:
    o["logs"] += "\nplain text, not JSON\n"


CORE = [
    Check("CK-LC-01/02", "入口参数与重复迁移", ("P1.1", "P1.2"), ("exit_unknown_arg", "migrate_again"), judge_exits,
          lambda o: mutated(o, lambda x: x.update(exit_unknown_arg=0))),
    Check("CK-CF-03", "配置错误一次报全并以 78 退出", ("P1.9",), ("bad_config",), judge_bad_config,
          lambda o: mutated(o, lambda x: x["bad_config"].update(code=1))),
    Check("CK-LC-04/05/06", "健康、就绪与端口", ("P1.4", "P1.5", "P1.6"), ("health", "ready", "grpc_listening"),
          judge_endpoints, lambda o: mutated(o, lambda x: x.update(grpc_listening=False))),
    Check("CK-LC-14", "保持连接上的回答不被扣住", ("P1.19",), ("kept_alive_ms",), judge_nodelay,
          lambda o: mutated(o, lambda x: x.update(kept_alive_ms=[41.0] * 20))),
    Check("CK-LC-08", "自描述", ("P1.8",), ("describe",), judge_describe,
          lambda o: mutated(o, lambda x: x["describe"].pop("protocol", None))),
    Check("CK-LC-10", "就绪日志", ("P1.12",), ("logs",), judge_ready_line,
          lambda o: mutated(o, lambda x: x.update(logs=x["logs"].replace('"event":"ready"', '"event":"x"')))),
    Check("CK-LC-11/12", "体面停止", ("P1.14", "P1.15"), ("stopping_ready", "stop_exit"), judge_stop,
          lambda o: mutated(o, lambda x: x.update(stop_exit=1))),
    Check("CK-DB-09", "库内的表与角色", ("P5.11", "P5.12", "P5.23"), ("db", "sdk_tables"), judge_database,
          lambda o: mutated(o, lambda x: x["db"].update(runtime_create=True))),
    Check("CK-EV-09", "发布的信封", ("P6.4", "P6.13", "P6.14"), ("messages",), judge_envelope,
          lambda o: mutated(o, lambda x: x["messages"][0]["headers"].update({"ce-bedigest": "0" * 64}))),
    Check("CK-OBS-01", "日志行的结构", ("P10.1", "P10.2"), ("logs",), judge_log_lines,
          lambda o: mutated(o, _break_log)),
    Check("CK-OBS-02", "访问日志", ("P10.3", "P1.7"), ("logs", "http_requests"), judge_access,
          lambda o: mutated(o, lambda x: x.update(logs=x["logs"].replace('"msg":"access"', '"msg":"acc"')))),
    Check("CK-OBS-03", "日志不含凭据与密钥", ("P10.4", "P8.3"), ("logs", "secrets"), judge_secrets,
          lambda o: mutated(o, lambda x: x.update(logs=x["logs"] + x["secrets"][0]))),
    Check("CK-OBS-04", "指标", ("P10.5", "P10.6"), ("metrics",), judge_metrics,
          lambda o: mutated(o, lambda x: x.update(metrics=x["metrics"].replace("be_outbound_breaker_open",
                                                                               "be_outbound_breaker_state")))),
]


def judge_shell_metrics(obs: dict) -> list[str]:
    try:
        families = list(text_string_to_metric_families(obs["shell_metrics"]))
    except Exception as e:  # noqa: BLE001
        return [f"the shell's /metrics is not Prometheus text: {e}"]
    p, members = [], set(obs["members"])
    for fam in families:
        for s in fam.samples:
            if s.labels.get("component") not in members:
                p.append(f"{s.name} has component {s.labels.get('component')!r}, not a hosted member")
                break
    seen = {s.labels.get("component") for f in families if f.name == "be_http_server_duration_seconds" for s in f.samples}
    p += [f"no request metrics of {m}" for m in sorted(served(obs, members) - seen)]
    return p[:20]


def served(obs: dict, members: set[str]) -> set[str]:
    """The hosted members that served a user-surface request during the observation: those with an HTTP access line
    (one with a route; a system-surface line has a gRPC method instead) in the shell's output, read after the metrics.
    A member whose user surface nobody called has no request series yet (the audit member, called only on its system
    surface). Without the output, every member."""
    lines = obs.get("logs")
    if not lines:
        return members
    out = set()
    for line in (lines.splitlines() if isinstance(lines, str) else lines):
        try:
            d = json.loads(line)
        except (ValueError, TypeError):
            continue
        if isinstance(d, dict) and d.get("msg") == "access" and d.get("route") and d.get("component") in members:
            out.add(d["component"])
    return out


def judge_shell_endpoints(obs: dict) -> list[str]:
    p, h = [], obs["health"]
    if h["get"][0] != 200 or h["head"][0] != 200:
        p.append(f"/healthz answered {h['get'][0]}/{h['head'][0]}")
    if obs["ready"][0] != 200:
        p.append(f"/readyz answered {obs['ready'][0]} with every member ready")
    d = obs["describe"]
    if d.get("component_id") != obs["component"]:
        p.append(f"component_id is {d.get('component_id')!r}")
    hosted = sorted(m.get("component_id") for m in d.get("members") or [])
    if hosted != sorted(obs["members"]):
        p.append(f"the description lists members {hosted}")
    return p


def judge_shell_stop(obs: dict) -> list[str]:
    return [] if obs["stop_exit"] == 0 else [f"a graceful stop exited {obs['stop_exit']}"]


CORE.append(Check("CK-OBS-05", "外壳的指标带成员标签", ("P10.7",), ("shell_metrics",), judge_shell_metrics,
                  lambda o: mutated(o, lambda x: x.update(shell_metrics=x["shell_metrics"].replace("component=", "c="))),
                  applies=lambda o: "" if o.get("shell") else "只对外壳（见外壳的报告）"))

SHELL = [
    Check("CK-LC-05/06/08", "外壳的健康、就绪与自描述", ("P1.5", "P1.6", "P1.8"), ("health", "ready", "describe"),
          judge_shell_endpoints, lambda o: mutated(o, lambda x: x["describe"].update(members=[]))),
    Check("CK-LC-12", "外壳体面停止", ("P1.15", "P1.17"), ("stop_exit",), judge_shell_stop,
          lambda o: mutated(o, lambda x: x.update(stop_exit=1))),
    Check("CK-OBS-01", "外壳输出的日志行结构", ("P10.1", "P10.2"), ("logs",), judge_log_lines,
          lambda o: mutated(o, _break_log)),
    CORE[3],
    CORE[-1],
]
