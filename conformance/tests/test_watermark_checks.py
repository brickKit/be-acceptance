"""The watermark source profile's checks on a recorded observation (the Go probe, 2026-11-03; no base resources):
each passes it, each one's known violation fails it, and a pull that found nothing proves nothing."""
import json
import pathlib

import pytest

from profile import watermark
from profile.model import Result, run

OBSERVED = json.loads((pathlib.Path(__file__).parent / "data" / "watermark-observed.json").read_text())


def copy() -> dict:
    return json.loads(json.dumps(OBSERVED))


@pytest.mark.parametrize("check", watermark.WATERMARK, ids=lambda c: c.id)
def test_each_check_passes_the_recording_and_catches_its_violation(check):
    assert run(check, OBSERVED).result == Result.PASS, check.judge(OBSERVED)
    assert check.judge(check.bad(OBSERVED)), "the known violation passed"


def test_an_unchecked_position_with_nothing_kept_proves_nothing():
    o = copy()
    for pg in o["check"]["kept"]["pages"]:
        for pr in pg["partitions"]:
            pr["events"] = []
    assert any("precondition" in p for p in watermark.judge_check(o))


def test_writes_that_were_never_numbered_prove_nothing():
    o = copy()
    o["echo"]["held_at"] = None
    assert "precondition" in watermark.judge_echo(o)[0]
