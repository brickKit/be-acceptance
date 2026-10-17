"""The Python probe: the acceptance suite's communication-only component (be-acceptance probes/CONTRACT.md), the same
contract as the Go probe. No business logic, so what it measures comes from the language, the topology and the engines
only."""
from importlib.resources import files

from besdk.authz import Definitions, Operation, Resource
from besdk.component import Definition
from besdk.config import Key, Type
from besdk.events.core import Stream

ID, VERSION, GRPC_PORT = "test/probe-py", "3.0.0", 9601
STREAM, WRITTEN_TYPE = "test.probe_py", "test.probe.written.v1"
CALLERS = ["test/probe-go", "test/probe-py"]
RELAY_BYTES, MAX_WORK_MS, MAX_BYTES = 1024, 1000, 512 << 10


def authz() -> Definitions:
    """The probe's one resource and its operations: they protect the user routes when PROBE_GUARD is on and the system
    methods always."""
    ops = [Operation("test.probe.read", "test.probe", "read", "low")]
    ops += [Operation(f"test.probe.{op}", "test.probe", "action", "low") for op in ("echo", "work", "write", "call")]
    return Definitions(ID, [Resource("test.probe", "test.probe.read", {}, {})], ops)


def definition() -> Definition:
    from probe_py.module import Probe

    return Definition(ID, VERSION, Probe, keys=[
        Key("PROBE_NEXT", Type.STRING),  # the probe that call and relay go to; empty: none
        Key("PROBE_PEER_STREAM", Type.STRING),  # the peer's event stream to consume; empty: none
        Key("PROBE_GUARD", Type.BOOLEAN, default=False)],
        migrations=files("probe_py") / "migrations", grpc_port=GRPC_PORT, streams=[Stream(STREAM, 4)],
        subscribes=True, authz=authz())
