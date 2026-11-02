"""The events profile's checks on a recorded observation (the Go probe on JetStream, 2026-11-02; no base resources):
each passes it, each one's known violation fails it, and without the consumer and bus observations the report is
partial, never conforming (SK02)."""
import json
import pathlib

import pytest

from profile import events
from profile.model import Report, Result, Verdict, run

OBSERVED = json.loads((pathlib.Path(__file__).parent / "data" / "events-observed-jetstream.json").read_text())


@pytest.mark.parametrize("check", events.EVENTS, ids=lambda c: c.id)
def test_each_check_passes_the_recording_and_catches_its_violation(check):
    assert run(check, OBSERVED).result == Result.PASS, check.judge(OBSERVED)
    assert check.judge(check.bad(OBSERVED)), "the known violation passed"


def test_without_a_bus_the_profile_is_partial():
    missing = {k: v for k, v in OBSERVED.items() if k not in ("duplicate", "hops", "claim", "bus_retention", "bus_start")}
    report = Report("test/probe-go", "sha256:x", "3.0.0-alpha.45", "0.1.0", {}, "events",
                    [run(c, missing) for c in events.EVENTS])
    assert report.verdict == Verdict.PARTIAL
    assert {o.check for o in report.outcomes if o.result == Result.NOT_RUN} == \
        {"CK-EV-06", "CK-EV-07", "CK-EV-08", "CK-BUS-09", "CK-BUS-10"}


def test_a_duplicate_stored_once_proves_nothing():
    o = json.loads(json.dumps(OBSERVED))
    o["duplicate"]["stored"] = 1
    assert "precondition" in events.judge_duplicate(o)[0]
