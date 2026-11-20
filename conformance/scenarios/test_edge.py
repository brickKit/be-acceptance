"""L3, the edge in front of the cell (C12's P3; protocol 20's login and session, be-protocol chapter 11): a browser's
login through the edge (the identity member's redirect, Casdoor's login form, the callback) gives a session cookie and
nothing else; with it the edge injects the session's token into every component's route, refuses a state-changing
request without BE-Request, serves the features document to live sessions only, and answers the caller's own
operation keys (infra/authz's read, A177) and the mobile aggregation the same way. After a logout the cookie the
browser kept gets SESSION_ENDED and is cleared, and it does not stop a new login (P11.12, P11.13; UI32). The calls go
to http://localhost:8200, the origin Casdoor's callback is registered under. A cell without `edge: true` has no edge;
its scenarios are skipped, saying so."""
from __future__ import annotations

import json
import urllib.parse
from contextlib import closing

import httpx
import pytest

from cells.bffqueries import hash_of
from harness import need

EDGE = "http://localhost:8200"
CASDOOR = "http://localhost:8000"


@pytest.fixture(autouse=True)
def _needs_edge(cell):
    if not cell.edge:
        pytest.skip("the cell has no edge (cell.yaml edge: true)")


def browser_login(user: str, return_to: str = "/home", cookies: dict | None = None) -> tuple[httpx.Client, str]:
    """What a browser does through the edge; the client keeps the cookies it was given (the edge's origin)."""
    c = httpx.Client(timeout=20, follow_redirects=False, cookies=cookies or {})
    r = c.get(EDGE + "/api/iam/login", params={"return_to": return_to})
    assert r.status_code == 302, f"the login through the edge: {r.status_code} {r.text[:200]}"
    q = urllib.parse.parse_qs(urllib.parse.urlsplit(r.headers["location"]).query)
    login = {"clientId": q["client_id"][0], "responseType": "code", "redirectUri": q["redirect_uri"][0], "type": "code",
             "scope": q["scope"][0], "state": q["state"][0], "code_challenge_method": "S256",
             "code_challenge": q["code_challenge"][0]}
    a = httpx.post(CASDOOR + "/api/login?" + urllib.parse.urlencode(login), timeout=20, json={
        "application": "be-iam", "organization": "brickkit", "username": user,
        "password": need("BE_ACC_IAM_TEST_PASSWORD"), "type": "code"})
    code = a.json()["data"]
    assert q["redirect_uri"][0] == EDGE + "/api/iam/callback", "Casdoor sends the browser back to the edge's origin"
    r = c.get(EDGE + "/api/iam/callback", params={"code": code, "state": q["state"][0]})
    assert r.status_code == 303 and r.headers["location"] == return_to, f"the callback: {r.status_code} {r.text[:200]}"
    cookie = r.cookies.get("be_session")  # the callback's own Set-Cookie (the client may still hold an older one)
    assert cookie, "a session cookie"
    c.cookies = {"be_session": cookie}
    return c, cookie


def reason(r: httpx.Response) -> str:
    try:
        return r.json().get("reason", "")
    except json.JSONDecodeError:
        return ""


def test_a_session_through_the_edge(cell):
    c, cookie = browser_login("test.alice")
    with closing(c):
        # features: live sessions only (P11.8, UI25)
        r = c.get(EDGE + "/api/features")
        assert r.status_code == 200, r.text[:200]
        assert {x["id"] for x in r.json()["components"]} >= {"infra/authz", "infra/iam-casdoor"}
        assert httpx.get(EDGE + "/api/features").status_code == 401, "no session, no features"
        # the token is injected: a component's read with nothing but the cookie
        r = c.get(EDGE + "/api/authz/me/operations")
        assert r.status_code == 200, r.text[:300]
        ops = r.json()["operations"]
        assert "infra.authz.security_record.own_operations" in ops and "infra.authz.security_record.change" in ops, \
            "the administrator's own keys"
        # a browser's own Authorization never reaches a component (P11.1): a forged one changes nothing
        r = c.get(EDGE + "/api/authz/me/operations", headers={"Authorization": "Bearer forged"})
        assert r.status_code == 200 and r.json()["operations"] == ops
        # a state change without BE-Request: refused at the edge (P11.2, UI06)
        r = c.post(EDGE + "/api/authz/changes", json={})
        assert (r.status_code, reason(r)) == (403, "CSRF_HEADER_REQUIRED"), r.text[:200]
        # with it the request reaches the component, which judges it (an empty change is the component's 400)
        r = c.post(EDGE + "/api/authz/changes", json={}, headers={"BE-Request": "1", "Idempotency-Key": "l3:edge:empty"})
        assert r.status_code in (400, 409, 422) and reason(r) != "CSRF_HEADER_REQUIRED", r.text[:200]
        # a path outside every public prefix is not routed; a prefix is a whole segment (P11.11)
        assert c.get(EDGE + "/api/authzx/me/operations").status_code == 404


def test_the_mobile_aggregation_through_the_edge(cell):
    if "infra/bff-mobile" not in cell.placed:
        pytest.skip("the cell has no infra/bff-mobile")
    c, _ = browser_login("test.buyer.lead")  # holds the mobile entry (the seed's grant); the administrator does not
    with closing(c):
        body = {"extensions": {"persistedQuery": {"version": 1, "sha256Hash": hash_of("home")}}, "variables": {"limit": 5}}
        r = c.post(EDGE + "/api/bff-mobile/graphql", json=body, headers={"BE-Request": "1"})
        assert r.status_code == 200, r.text[:300]
        assert not r.json().get("errors"), r.json().get("errors")
        r = c.post(EDGE + "/api/bff-mobile/graphql", json=body)
        assert (r.status_code, reason(r)) == (403, "CSRF_HEADER_REQUIRED")


def test_an_ended_cookie_does_not_stop_a_new_login(cell):
    c, cookie = browser_login("test.bob")
    with closing(c):
        r = c.post(EDGE + "/api/iam/logout", headers={"BE-Request": "1"})
        assert r.status_code in (200, 204), r.text[:200]
    # the browser still holds the old cookie (say the logout's answer was lost): the session is over
    old = httpx.Client(timeout=20, follow_redirects=False, cookies={"be_session": cookie})
    with old:
        r = old.get(EDGE + "/api/authz/me/operations")
        assert (r.status_code, reason(r)) == (401, "SESSION_ENDED"), r.text[:200]
        assert "be_session=;" in r.headers.get("set-cookie", "") or 'be_session="";' in r.headers.get("set-cookie", ""), \
            f"the ended cookie is cleared (P11.12): {r.headers.get('set-cookie')}"
    # and a login with it still goes through, ending with a new session (P11.13, UI32)
    c2, fresh = browser_login("test.bob", cookies={"be_session": cookie})
    with closing(c2):
        assert fresh != cookie
        assert c2.get(EDGE + "/api/authz/me/operations").status_code == 200
