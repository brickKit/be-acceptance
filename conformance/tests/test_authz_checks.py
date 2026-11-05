"""The authorization profile's checks on recorded observations (the Go and Python probes, 2026-11-05, be-sdk-go
alpha.72 and be-sdk-python a50; no base resources): each passes its recording, each one's known violation fails it,
and a precondition that did not hold proves nothing."""
import json
import pathlib

import pytest

from profile import authz
from profile.checks import mutated
from profile.model import Result, run

DATA = pathlib.Path(__file__).parent / "data"
GO = json.loads((DATA / "authz-observed-go.json").read_text())
PY = json.loads((DATA / "authz-observed-py.json").read_text())


@pytest.mark.parametrize("check", authz.AUTHZ, ids=lambda c: c.id)
def test_each_check_passes_the_go_recording_and_catches_its_violation(check):
    assert run(check, GO).result == Result.PASS, check.judge(GO)
    assert check.judge(check.bad(GO)), "the known violation passed"


def test_the_python_recording_passes_every_check():
    results = {c.id: run(c, PY).result for c in authz.AUTHZ}
    assert all(r == Result.PASS for r in results.values()), results


def by_id(cid):
    return next(c for c in authz.AUTHZ if c.id == cid)


@pytest.mark.parametrize("name,change,check", [
    # a write that started too late or too early proves nothing about the commit-time check
    ("held write started late", lambda x: x["validity"]["held_write"].update(started_after=6.0), "CK-AZ-04"),
    # the approval never went through: an expired view proves nothing
    ("no baseline", lambda x: x["validity"].update(baseline=False), "CK-AZ-02"),
    # a refused move must leave the entry as it was
    ("refused move changed the entry", lambda x: x["dimensions"]["amount"]["move_out"]["e1_after"].update(amount="150"),
     "CK-AZ-05"),
    # the move inside the grant failed: the refusal outside may be for another reason
    ("no move inside", lambda x: x["dimensions"]["org_tree"].update(move_in=403), "CK-AZ-05"),
    # a refused create left an entry
    ("refused create made one", lambda x: x["dimensions"]["principal"]["create_out"].update(entries_after=6), "CK-AZ-05"),
    # a refusal naming a hidden entry's owner (EN20)
    ("a refusal leaks", lambda x: x["dimensions"]["value_set"]["refusals"].append('{"owner_id":"' + authz.BOB + '"}'),
     "CK-AZ-05"),
    # a masked cost shown, or the secret note
    ("cost shown", lambda x: next(iter(x["dimensions"]["value_set"]["rows"].values())).update(cost="7", _masked=None),
     "CK-AZ-05"),
    ("note shown", lambda x: next(iter(x["dimensions"]["value_set"]["rows"].values())).update(note="secret"), "CK-AZ-05"),
    # a filter outside the grant errs instead of narrowing
    ("filter errs", lambda x: x["dimensions"]["org_tree"]["filter_out"].update(status=403), "CK-AZ-05"),
    # the export outputs the page's fields
    ("export borrows the page's fields", lambda x: x["overlap"]["export"]["body"].update(cost="7", _masked=[]),
     "CK-AZ-05b"),
    # a page that fails is a problem, not a crash
    ("a page fails", lambda x: x["overlap"]["pages"].append({"_status": 500}), "CK-AZ-05b"),
    # a started undeclared route, and a start that failed for another reason
    ("failed for another reason", lambda x: x["undeclared"].update(log="no database"), "CK-AZ-01"),
])
def test_each_rule_of_a_judge_fails_its_violation(name, change, check):
    c = by_id(check)
    assert c.judge(GO) == [], "precondition: the recording passes"
    assert c.judge(mutated(GO, change)), name
