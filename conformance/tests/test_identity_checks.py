"""The identity profile's checks on recorded observations (the Go and Python probes, 2026-11-03, Python on a46; no base resources):
each passes its recording, each one's known violation fails it, and a precondition that did not hold proves nothing.
The tokens in the recordings were signed by a test deployment's throwaway keys."""
import json
import pathlib

import pytest

from profile import identity
from profile.model import Result, run

DATA = pathlib.Path(__file__).parent / "data"
GO = json.loads((DATA / "identity-observed-go.json").read_text())
PY = json.loads((DATA / "identity-observed-py.json").read_text())


def copy(o: dict) -> dict:
    return json.loads(json.dumps(o))


@pytest.mark.parametrize("check", identity.IDENTITY, ids=lambda c: c.id)
def test_each_check_passes_the_go_recording_and_catches_its_violation(check):
    assert run(check, GO).result == Result.PASS, check.judge(GO)
    assert check.judge(check.bad(GO)), "the known violation passed"


def test_the_python_recording_passes_every_check():
    results = {c.id: run(c, PY).result for c in identity.IDENTITY}
    assert all(r == Result.PASS for r in results.values()), results


def test_an_sdk_without_staleness_is_not_applicable():
    o = copy(PY)
    o["sdk"] = "an-sdk-without-p8.25"
    results = {c.id: run(c, o).result for c in identity.IDENTITY}
    assert results["CK-ID-05"] == results["CK-ID-06"] == Result.NOT_APPLICABLE


def test_a_probe_that_never_answers_stale_fails_rather_than_not_running():
    o = copy(GO)
    o["stale_wait"] = [200, ""]
    assert run(next(c for c in identity.IDENTITY if c.id == "CK-ID-05"), o).result == Result.FAIL


def test_a_peer_that_accepts_the_token_proves_nothing():
    o = copy(GO)
    o["downstream"]["peer_direct"], o["downstream"]["peer_reason"] = 200, ""
    assert any("precondition" in p for p in identity.judge_downstream(o))


def test_a_refused_permitted_caller_proves_nothing():
    o = copy(GO)
    o["certificates"]["permitted"] = "UNAVAILABLE"
    assert any("precondition" in p for p in identity.judge_certificates(o))
