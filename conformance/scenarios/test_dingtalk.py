"""L3, the DingTalk channel (C10; the assembly project's protocol 10, be-protocol chapter 20): the purchasing lead binds
their own DingTalk user through OAuth against the suite's DingTalk simulator (external/dingtalk.py); a submitted
requisition's approval then reaches them as an interactive card besides the in-app message, with an approve button
and no reject (purchase rejections need a comment, which a button cannot give: P17.19); clicking approve in DingTalk
(a callback signed and encrypted as DingTalk pushes it) approves the task in erp/purchase; the same click pushed again
and clicked again make no second decision and no note; a card whose task was decided in the worklist meanwhile is
refused with an in-app note (another action on a used grant, GRANT_USED, needs a second button: the SDK's vectors and
integration tests cover it); a callback DingTalk did
not sign is dropped; a card forwarded to another DingTalk user is quarantined; unbound, the lead gets the in-app
message only. Each test makes its own documents on top of the seed data; the tests run in this file's order."""
from __future__ import annotations

import sys
import pathlib
import time
from urllib.parse import parse_qs, urlparse

import httpx
import pytest

from conftest import eventually
from test_notification import message_of, submitted_requisition
from test_workflow import decide, worklist_entry

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from cells.prepare import DINGTALK_APP_KEY, DINGTALK_SIM_PORT  # noqa: E402
from external import dingtalk  # noqa: E402

DT = "integration/im-dingtalk"
LEAD_DT = "dt-buyer-lead"  # the lead's DingTalk user in the simulated organisation
TASK = "erp.purchase.requisition.approval"


@pytest.fixture(autouse=True)
def _needs_dingtalk(cell):
    for c in (DT, "infra/notification", "infra/workflow", "erp/purchase", "mdm/employee"):
        if c not in cell.placed:
            pytest.skip(f"the cell has no {c}")


@pytest.fixture(scope="module")
def sim(cell):
    secret = (cell.dir / ".secrets" / "dingtalk" / "app-secret").read_text()
    s = dingtalk.Sim(DINGTALK_APP_KEY, secret).start(DINGTALK_SIM_PORT)
    yield s
    s.stop()


@pytest.fixture(scope="module")
def keys(cell) -> tuple[str, str]:
    d = cell.dir / ".secrets" / "dingtalk"
    return (d / "callback-token").read_text(), (d / "callback-aes-key").read_text()


def push(user, keys, event: dict, sign: bool = True) -> httpx.Response:
    """DingTalk pushes a callback to the member's public route."""
    query, body = dingtalk.callback(keys[0], keys[1], DINGTALK_APP_KEY, event)
    if not sign:
        query["signature"] = "0" * 40
    return user.http.post(user.url(DT, "/api/im-dingtalk/callback"), params=query, json=body)


def bind(user, sim, dt_user: str) -> str:
    """The user's own binding: start, DingTalk's consent (the simulator's code), the redirect back; the outcome."""
    consent = urlparse(user.post(DT, "/api/im-dingtalk/bindings/start")["url"])
    q = parse_qs(consent.query)
    assert consent.path == "/oauth2/auth" and q["client_id"] == [DINGTALK_APP_KEY], consent
    r = user.http.get(user.url(DT, "/api/im-dingtalk/bindings/callback"),  # in the user's signed-in browser
                      params={"authCode": sim.grant(dt_user), "state": q["state"][0]},
                      headers={"Authorization": f"Bearer {user.token}"})
    assert r.status_code == 302, r.text
    return parse_qs(urlparse(r.headers["location"]).query)["dingtalk"][0]


def card_of(sim, entry: dict) -> dict:
    """The lead's card linking to the worklist entry's task."""
    link = f"/workflow/tasks/{entry['task']['id']}"

    def find():
        for c in sim.cards_of(LEAD_DT):
            if any((b.get("URL") or "").endswith(link) for b in c["buttons"]):
                return c
        return None
    return eventually(find, seconds=90)


def requisition(bob, id_: str) -> dict:
    return bob.get("erp/purchase", f"/api/purchase/requisitions/{id_}")


def note_of(lead, entry: dict) -> dict | None:
    """The in-app note that a button's action was not carried out (P20.8), for the task."""
    for m in lead.get("infra/notification", "/api/notification/messages?limit=200")["messages"]:
        if m["link"] == f"/workflow/tasks/{entry['task']['id']}" and m["title"].startswith("按钮的操作没有执行"):
            return m
    return None


def test_dingtalk_the_lead_binds_their_own_dingtalk(login, sim):
    """ID10, P20.9: started by the lead, DingTalk's code for the lead's DingTalk user binds it in the lead's own
    signed-in browser; the lead sees their own binding."""
    lead = login("test.buyer.lead")
    r = lead.http.delete(lead.url(DT, "/api/im-dingtalk/bindings/me"), headers={"Authorization": f"Bearer {lead.token}"})
    assert r.status_code in (200, 204, 404), r.text  # a rerun on the same cell starts unbound
    assert bind(lead, sim, LEAD_DT) == "bound"
    mine = lead.get(DT, "/api/im-dingtalk/bindings/me")
    assert mine["user_id"] == LEAD_DT, mine


def test_dingtalk_approve_from_the_card(bob, login, sim, keys, world):
    """P17.19, P17.20, P20.6, P20.8: the requisition's approval is a card with an approve button (no reject: a purchase
    rejection needs a comment) and the link; clicking approve approves the task; the same push again and a second click
    of approve make no second decision and tell the lead nothing (a replay of the same action)."""
    lead = login("test.buyer.lead")
    req = submitted_requisition(bob, world)
    entry = worklist_entry(lead, TASK, req["id"])
    card = card_of(sim, entry)
    actions = {b.get("Action") for b in card["buttons"]}
    assert "approve" in actions and "reject" not in actions, card
    assert "PR\\-CN01" in card["title"], "the title as plain text: the card's Markdown characters escaped (ID02)"
    assert all(b["Value"].count(".") == 2 for b in card["buttons"] if b.get("Action")), "delivery.action.one-time value"

    click = dingtalk.click(card, "approve")
    r = push(lead, keys, click)
    assert r.status_code == 200, r.text
    assert dingtalk.decrypt(keys[1], DINGTALK_APP_KEY, r.json()["encrypt"]) == b"success"
    eventually(lambda: requisition(bob, req["id"])["state"] == "approved", seconds=90)

    assert push(lead, keys, click).status_code == 200, "the same push again: answered, processed once"
    assert push(lead, keys, dingtalk.click(card, "approve")).status_code == 200, "a second click of approve"
    time.sleep(5)  # both were taken by now (the first took seconds)
    assert note_of(lead, entry) is None, "a replay of the same action is no refusal"
    assert requisition(bob, req["id"])["state"] == "approved", "one decision: the first"


def test_dingtalk_a_card_whose_task_was_decided_meanwhile(bob, login, sim, keys, world):
    """P20.8: decided in the worklist first, the card's approve is refused and the lead is told in-app."""
    lead = login("test.buyer.lead")
    req = submitted_requisition(bob, world)
    entry = worklist_entry(lead, TASK, req["id"])
    card = card_of(sim, entry)
    assert decide(lead, entry, "approve").status_code == 200
    assert push(lead, keys, dingtalk.click(card, "approve")).status_code == 200
    note = eventually(lambda: note_of(lead, entry), seconds=90)
    assert "按钮的操作没有执行" in note["title"], note


def test_dingtalk_callbacks_dingtalk_did_not_send(bob, login, sim, keys, world):
    """ID06, ID08: an unsigned callback is dropped (nothing written, the task still pending); a card forwarded to
    another DingTalk user is quarantined and the maintainer lists it."""
    lead = login("test.buyer.lead")
    req = submitted_requisition(bob, world)
    entry = worklist_entry(lead, TASK, req["id"])
    card = card_of(sim, entry)
    r = push(lead, keys, dingtalk.click(card, "approve"), sign=False)
    assert r.status_code in (401, 403), r.text
    forwarded = dingtalk.click(card, "approve", user_id="dt-someone-else")
    assert push(lead, keys, forwarded).status_code == 200

    def quarantined():
        return any(c["event_id"] == forwarded["eventId"]
                   for c in bob.get(DT, "/api/im-dingtalk/callbacks?limit=200")["callbacks"])
    eventually(quarantined, seconds=60)
    time.sleep(3)  # neither reached erp/purchase: give a wrong action the time it would have taken
    assert requisition(bob, req["id"])["state"] == "submitted"
    assert decide(lead, entry, "approve").status_code == 200  # leave nothing pending for later scenarios


def test_dingtalk_unbound_in_app_only(bob, login, sim, world):
    """P20.1: the lead ends their binding; the next approval reaches them in-app and no card is sent."""
    lead = login("test.buyer.lead")
    r = lead.http.delete(lead.url(DT, "/api/im-dingtalk/bindings/me"), headers={"Authorization": f"Bearer {lead.token}"})
    assert r.status_code in (200, 204), r.text
    before = len(sim.cards_of(LEAD_DT))
    req = submitted_requisition(bob, world)
    entry = worklist_entry(lead, TASK, req["id"])
    eventually(lambda: message_of(lead, f"/workflow/tasks/{entry['task']['id']}"), seconds=60)
    time.sleep(5)  # the external delivery answers unmapped at once; a card would have come by now
    assert len(sim.cards_of(LEAD_DT)) == before, "unbound: no card"
    assert decide(lead, entry, "approve").status_code == 200
