"""The Python probe: the acceptance suite's communication-only component (be-acceptance probes/CONTRACT.md), the same
contract as the Go probe. No business logic, so what it measures comes from the language, the topology and the engines
only."""
from dataclasses import dataclass
from datetime import timedelta
from importlib import metadata
from importlib.resources import files

from besdk.authz import Definitions, Operation, Resource
from besdk.component import Definition
from besdk.config import Key, Type
from besdk.events.core import Stream

VERSION, WRITTEN_TYPE = metadata.version("be-test-probe-py"), "test.probe.written.v1"  # one source: pyproject.toml
NOTED_TYPE = "test.probe.noted.v1"  # since 3.2.22: a write asked as "noted" (suite D, V3)
RELAY_BYTES, MAX_WORK_MS, MAX_BYTES, MAX_LABEL = 1024, 1000, 512 << 10, 64


@dataclass(frozen=True)
class Variant:
    """One probe component: the same code under its own component ID, ports and event stream, so a shell can host
    several and two probes can call each other inside one process."""

    id: str
    stream: str
    http_port: int  # the port its image listens on alone (deployment.port)
    grpc_port: int


VARIANTS = [Variant("test/probe-py", "test.probe_py", 8601, 9601), Variant("test/probe-py2", "test.probe_py2", 8605, 9605)]
# every probe of either language may call the probes' system methods
CALLERS = ["test/probe-go", "test/probe-go2", "test/probe-go3", "test/probe-go4", "test/probe-py", "test/probe-py2"]


def prefix(component_id: str) -> str:
    """A variant's resource type and operation-key prefix: test/probe-py2 -> test.probe_py2 (no two components own the
    same keys)."""
    return component_id.replace("/", ".").replace("-", "_")


def authz(component_id: str) -> Definitions:
    """A variant's resources and their operations: the probe's own (it protects the user routes when PROBE_GUARD is on
    and the system methods always) and, since 3.2.38, the entry of the authorization profile."""
    from probe_py import entries

    p = prefix(component_id)
    ops = [Operation(f"{p}.read", p, "read", "low")]
    ops += [Operation(f"{p}.{op}", p, "action", "low") for op in ("echo", "work", "write", "call", "note")]
    ops += entries.operations(p)  # since 3.2.38: the authorization profile's entries
    return Definitions(component_id, [Resource(p, f"{p}.read", {}, {}), entries.resource(p)], ops)


def definition(v: Variant = VARIANTS[0]) -> Definition:
    from probe_py.module import Probe

    return Definition(v.id, VERSION, lambda rt: Probe(rt, v), keys=[
        Key("PROBE_NEXT", Type.STRING),  # the probe that call and relay go to; empty: none
        Key("PROBE_NEXT_URL", Type.STRING),  # its user surface ($endpoint:<id>), protocol P4.26
        Key("PROBE_NEXT_GRPC_URL", Type.STRING),  # its system surface ($endpoint:<id>:grpc)
        Key("PROBE_PEER_STREAM", Type.STRING),  # the peer's event stream to consume; empty: none
        Key("PROBE_GUARD", Type.BOOLEAN, default=False),
        # the common contract's key since 3.2.32; the Python probe has no jobs yet (foundations item 6) and reads it not
        Key("PROBE_STEPS_EVERY", Type.DURATION, default=timedelta(0)),
        # since 3.2.33, the same: the Go probe's cron job for the background work profile; not read here yet
        Key("PROBE_CRON", Type.BOOLEAN, default=False),
        # since 3.2.38: a guarded route naming an operation the probe does not declare, so the start fails (CK-AZ-01)
        Key("PROBE_UNDECLARED_ROUTE", Type.BOOLEAN, default=False),
        # since 3.2.41, the common contract's keys of the Go probe's payment (an external command, be-protocol chapter
        # 13): be-sdk-python implements external commands with foundations item 6; until then they are not read here,
        # and EXTERNAL_COMMANDS_QUERY_ONLY (the Go SDK's own key) is declared here for the manifests to agree (A144)
        Key("PROBE_EXTERNAL_URL", Type.STRING),
        Key("PROBE_EXTERNAL_DEDUPE_WINDOW", Type.DURATION, default=timedelta(0)),
        Key("PROBE_EXTERNAL_LAST_USE", Type.BOOLEAN, default=False),
        Key("PROBE_EXTERNAL_MAX_PROCESSING", Type.DURATION, default=timedelta(0)),
        Key("PROBE_EXTERNAL_IRREVOCABLE", Type.BOOLEAN, default=True),
        Key("EXTERNAL_COMMANDS_QUERY_ONLY", Type.BOOLEAN, default=False),
        # the Go probe's archive (since Go 3.2.47, the tiering profile: be-protocol chapter 15): the keys the Go SDK reads
        # for its data class, the run profile's among them, declared here for the manifests to agree (A144); the
        # Python probe has no archive until the Python SDK's tiering (foundations item 6)
        *[Key(n, Type.STRING) for n in ("ALERT_WEBHOOK_URL", "RUN_PROFILE_ACCESS_KEY", "RUN_PROFILE_BUCKET",
                                         "RUN_PROFILE_URL", "TIERING_STORE_URL", "TIERING_RESIDENCE_PROBE_ARCHIVE",
                                         "TIERING_RETENTION_PROBE_ARCHIVE", "TIERING_ROUTE_PROBE_ARCHIVE")],
        Key("RUN_PROFILE_REGION", Type.STRING, default="us-east-1"),
        Key("RUN_PROFILE_SECRET_KEY", Type.STRING, secret=True),
        Key("TIERING_BUCKET", Type.STRING, default="be-tiering"),
        Key("TIERING_COLD_CONCURRENCY", Type.INTEGER, default=4, min=1),
        Key("TIERING_COLD_SYNC_LIMIT", Type.DURATION, default=timedelta(seconds=10)),
        Key("TIERING_GRACE", Type.DURATION, default=timedelta(days=7)),
        Key("TIERING_LIST_DAYS", Type.INTEGER, default=90, min=1),
        Key("TIERING_WINDOW", Type.STRING, default="0 2 * * *"),
        *[Key(f"TIERING_{r}_ACCESS_KEY", Type.STRING) for r in ("WRITE", "READ", "DELETE")],
        *[Key(f"TIERING_{r}_SECRET_KEY", Type.STRING, secret=True) for r in ("WRITE", "READ", "DELETE")]],
        migrations=files("probe_py") / "migrations", grpc_port=v.grpc_port, streams=[Stream(v.stream, 4)],
        subscribes=True, authz=authz(v.id))


def definitions() -> list[Definition]:
    """Every Python probe variant, for the Python shell."""
    return [definition(v) for v in VARIANTS]


def by_id(component_id: str) -> Variant | None:
    return next((v for v in VARIANTS if v.id == component_id), None)
