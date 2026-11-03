"""The background work profile's checks on a recorded observation (the Go probe, 2026-11-03; no base resources):
each passes it, each one's known violation fails it, and a fault that missed its target fails the check rather than
passing it."""
import json
import pathlib

import pytest

from profile import work
from profile.model import Result, run

OBSERVED = json.loads((pathlib.Path(__file__).parent / "data" / "work-observed.json").read_text())


def copy() -> dict:
    return json.loads(json.dumps(OBSERVED))


@pytest.mark.parametrize("check", work.WORK, ids=lambda c: c.id)
def test_each_check_passes_the_recording_and_catches_its_violation(check):
    assert run(check, OBSERVED).result == Result.PASS, check.judge(OBSERVED)
    assert check.judge(check.bad(OBSERVED)), "the known violation passed"


@pytest.mark.parametrize("fault, judge", [("kill", work.judge_takeover), ("stop", work.judge_stop),
                                          ("epoch", work.judge_outdated)])
def test_a_fault_on_an_item_nobody_ran_proves_nothing(fault, judge):
    o = copy()
    before = o[fault]["before"]["local"] if fault == "kill" else o[fault]["before"]
    before["attempts"][-1]["ended"] = "done"  # the attempt had already ended when the fault came
    assert "no attempt was running" in judge(o)[0]


def test_a_kill_that_did_not_kill_proves_nothing():
    o = copy()
    o["kill"]["exit"] = 0
    assert any("not killed" in p for p in work.judge_takeover(o))


def test_a_call_not_made_before_the_kill_proves_nothing():
    o = copy()
    o["kill"]["before"]["non_idempotent"]["calls"] = []
    assert "not made and marked" in work.judge_unknown(o)[0]
