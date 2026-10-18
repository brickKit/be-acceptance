"""The Python probe: the acceptance suite's communication-only component (be-acceptance probes/CONTRACT.md), the same
contract as the Go probe. No business logic, so what it measures comes from the language, the topology and the engines
only."""
from dataclasses import dataclass
from importlib.resources import files

from besdk.authz import Definitions, Operation, Resource
from besdk.component import Definition
from besdk.config import Key, Type
from besdk.events.core import Stream

VERSION, WRITTEN_TYPE = "3.2.3", "test.probe.written.v1"
RELAY_BYTES, MAX_WORK_MS, MAX_BYTES = 1024, 1000, 512 << 10


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
    """A variant's one resource and its operations: they protect the user routes when PROBE_GUARD is on and the system
    methods always."""
    p = prefix(component_id)
    ops = [Operation(f"{p}.read", p, "read", "low")]
    ops += [Operation(f"{p}.{op}", p, "action", "low") for op in ("echo", "work", "write", "call")]
    return Definitions(component_id, [Resource(p, f"{p}.read", {}, {})], ops)


def definition(v: Variant = VARIANTS[0]) -> Definition:
    from probe_py.module import Probe

    return Definition(v.id, VERSION, lambda rt: Probe(rt, v), keys=[
        Key("PROBE_NEXT", Type.STRING),  # the probe that call and relay go to; empty: none
        Key("PROBE_NEXT_URL", Type.STRING),  # its user surface ($endpoint:<id>), protocol P4.26
        Key("PROBE_NEXT_GRPC_URL", Type.STRING),  # its system surface ($endpoint:<id>:grpc)
        Key("PROBE_PEER_STREAM", Type.STRING),  # the peer's event stream to consume; empty: none
        Key("PROBE_GUARD", Type.BOOLEAN, default=False)],
        migrations=files("probe_py") / "migrations", grpc_port=v.grpc_port, streams=[Stream(v.stream, 4)],
        subscribes=True, authz=authz(v.id))


def definitions() -> list[Definition]:
    """Every Python probe variant, for the Python shell."""
    return [definition(v) for v in VARIANTS]


def by_id(component_id: str) -> Variant | None:
    return next((v for v in VARIANTS if v.id == component_id), None)
