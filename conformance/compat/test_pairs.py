"""Suite D (version compatibility) on a running cell of two probes that call each other and consume each other's
stream (BE_ACC_CELL; cell.yaml's chain names the two): V4 and V5 two SDK versions with the same contract, V2 an additive
contract and the version before it. The probes' contract is probes/CONTRACT.md; the expectations are the assembly
project's step 12 plan."""
from __future__ import annotations

import os
import pathlib
import sys
import time
import uuid

import httpx
import pytest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))

from cells import logs  # noqa: E402
from cells.spec import Cell  # noqa: E402

# the first version of each probe with the additive contract (Note, a write's label, test.probe.noted.v1)
ADDITIVE = {"go": (3, 2, 23), "py": (3, 2, 22)}
# the first version of each probe on an SDK with P3.10 (an unknown route answers CAPABILITY_UNSUPPORTED itself)
P3_10 = {"go": (3, 2, 25), "py": (3, 2, 24)}


@pytest.fixture(scope="module")
def cell() -> Cell:
    d = os.environ.get("BE_ACC_CELL", "")
    if not d:
        raise AssertionError("BE_ACC_CELL is not set: these tests run against a running cell")
    c = Cell.load(d)
    assert len(c.chain) == 2, "a pair: two probes that call each other"
    return c


def url(c: Cell, component: str, path: str) -> str:
    return f"http://127.0.0.1:{c.host_port(component)}{path}"


def post(c: Cell, component: str, path: str, body: dict, headers: dict | None = None) -> httpx.Response:
    return httpx.post(url(c, component, path), json=body, headers=headers or {}, timeout=30)


def seen(c: Cell, component: str) -> dict:
    return httpx.get(url(c, component, "/api/probe/seen"), timeout=30).json()


def at_least(c: Cell, component: str, first: dict) -> bool:
    lang = "py" if "-py" in component else "go"
    return tuple(int(x) for x in c.versions_of_probes[component].split(".")) >= first[lang]


def additive(c: Cell, component: str) -> bool:
    return at_least(c, component, ADDITIVE)


def eventually(fn, within: float = 30.0):
    end, last = time.monotonic() + within, None
    while time.monotonic() < end:
        try:
            return fn()
        except AssertionError as e:
            last = e
            time.sleep(0.2)
    raise last


def test_calls_go_both_ways_on_both_surfaces(cell):
    for caller in cell.chain:
        for surface in ("grpc", "http"):
            for body in ({"op": "echo", "bytes": 1024}, {"op": "echo", "bytes": 60 * 1024}, {"op": "work", "ms": 5}):
                r = post(cell, caller, "/api/probe/call", {**body, "surface": surface})
                assert r.status_code == 200, (caller, surface, body, r.text)


@pytest.mark.parametrize("surface", ["grpc", "http"])
def test_the_deadline_shrinks_hop_by_hop_and_its_errors_map_the_same(cell, surface):
    """P4.3-P4.5: 200 ms left for a 500 ms call times out after the attempt's limit (left - 50 ms), not after 500 ms;
    40 ms left is not sent at all. Both answers cross the other SDK with the same reason and domain. The callee goes on
    with its 500 ms of CPU after the caller gave up (the probe's work does not watch the deadline); a Python probe does
    it on its event loop, so the next call waits until it is done before it times anything."""
    for caller in cell.chain:
        other = cell.next_of(caller)
        time.sleep(0.7)
        t0 = time.monotonic()
        r = post(cell, caller, "/api/probe/call", {"op": "work", "ms": 500, "surface": surface}, {"Be-Timeout-Ms": "200"})
        took = time.monotonic() - t0
        body = r.json()
        assert (r.status_code, body["reason"], body["domain"]) == (504, "DEADLINE_EXCEEDED", "be"), (caller, r.text)
        assert body["metadata"]["dependency"] == other
        assert took < 0.4, f"{caller}: the deadline did not reach {other} ({took:.3f} s)"
        r = post(cell, caller, "/api/probe/call", {"op": "work", "ms": 5, "surface": surface}, {"Be-Timeout-Ms": "40"})
        assert (r.status_code, r.json()["reason"]) == (504, "DEADLINE_BUDGET_EXHAUSTED"), (caller, r.text)


@pytest.mark.parametrize("surface", ["grpc", "http"])
def test_the_trace_and_the_request_id_cross(cell, surface):
    """P4.2: the caller's trace and request ID arrive in the other SDK's access log."""
    for caller in cell.chain:
        trace = uuid.uuid4().hex
        r = post(cell, caller, "/api/probe/call", {"op": "echo", "bytes": 8, "surface": surface},
                 {"traceparent": f"00-{trace}-00f067aa0ba902b7-01", "X-Request-Id": f"compat-{trace[:12]}"})
        assert r.status_code == 200, r.text
        other = cell.next_of(caller)

        def logged():
            lines = [line for line in logs.of(cell, other).splitlines() if trace in line]
            assert lines, f"{other}'s log has no line of trace {trace}"
            assert any(f"compat-{trace[:12]}" in line for line in lines), lines
        eventually(logged, 10)


def test_events_go_both_ways_and_are_relayed(cell):
    a, b = cell.chain
    before = {p: seen(cell, p) for p in (a, b)}
    for producer in (a, b):
        for i in range(3):
            r = post(cell, producer, "/api/probe/write", {"bytes": 100, "relay": i == 0})
            assert r.status_code == 201, r.text
        assert post(cell, producer, "/api/probe/write", {"bytes": 100 * 1024}).status_code == 201
    for consumer in (a, b):
        def grew(p=consumer):
            now = seen(cell, p)
            got = {k: now[k] - before[p][k] for k in ("count", "big", "relayed")}
            assert got == {"count": 4, "big": 1, "relayed": 1}, (p, got)
        eventually(grew)


def pair_by_contract(c: Cell) -> tuple[str, str]:
    new = [p for p in c.chain if additive(c, p)]
    old = [p for p in c.chain if not additive(c, p)]
    if len(new) != 1 or len(old) != 1:
        pytest.skip("V2 needs one probe with the additive contract and one before it")
    return new[0], old[0]


@pytest.mark.parametrize("surface", ["grpc", "http"])
def test_v2_a_new_caller_gets_capability_unsupported_from_an_old_callee(cell, surface, request):
    """V2: an operation the callee's version does not have is refused explicitly, never a silent success. Over the
    user surface a callee before P3.10 answers 404 NOT_FOUND, which a caller cannot tell from a record out of sight, so
    it is not mapped (P4.29): expected to fail until the callee runs an SDK with P3.10."""
    new, old = pair_by_contract(cell)
    if surface == "http" and not at_least(cell, old, P3_10):
        request.applymarker(pytest.mark.xfail(strict=True, reason="P4.29: an old callee's 404 NOT_FOUND is not mapped"))
    r = post(cell, new, "/api/probe/call", {"op": "note", "surface": surface})
    assert r.status_code != 200, r.text
    assert r.json()["reason"] == "CAPABILITY_UNSUPPORTED", r.text


def test_v2_an_old_callee_ignores_a_field_it_does_not_know(cell):
    new, old = pair_by_contract(cell)
    r = post(cell, old, "/api/probe/write", {"bytes": 5, "label": "from-a-newer-caller"})
    assert r.status_code == 201, r.text


def test_v2_an_old_consumer_takes_the_new_fields_and_never_sees_the_new_type(cell):
    """P6.33: a payload field it does not know is ignored; a type it does not subscribe to is filtered by the
    transport, and the subscription goes on."""
    new, old = pair_by_contract(cell)
    before = seen(cell, old)
    assert post(cell, new, "/api/probe/write", {"bytes": 7, "label": "x"}).status_code == 201
    assert post(cell, new, "/api/probe/write", {"bytes": 7, "event": "noted"}).status_code == 201
    assert post(cell, new, "/api/probe/write", {"bytes": 7}).status_code == 201

    def took_two():
        assert seen(cell, old)["count"] - before["count"] == 2
    eventually(took_two)
    time.sleep(2)
    assert seen(cell, old)["count"] - before["count"] == 2, "the noted event never arrives"
    assert "dead letter" not in logs.of(cell, old)
