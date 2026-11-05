"""The fake payment system's own scripting (external/fake.py), as a drill drives it from outside its process: a
reference's own faults come first and touch no other payment; the faults in turn serve the rest."""
import json
import urllib.error
import urllib.request

from external.fake import LIMIT, Fake, serve


def call(method: str, url: str, body: dict | None = None) -> tuple[int, dict]:
    req = urllib.request.Request(url, method=method, data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=5) as r:
            return r.status, json.loads(r.read() or b"{}")
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read() or b"{}")


def test_a_references_faults_come_first_and_touch_no_other_payment():
    fake = Fake()
    server, url = serve(fake, control=True)
    try:
        assert call("POST", url + "/control", {"faults": [LIMIT], "faults_by_reference": {"r-stale": [LIMIT, LIMIT]}})[0] == 200
        assert call("POST", url + "/payments", {"reference": "r-stale"})[0] == 429
        assert call("POST", url + "/payments", {"reference": "r-other"})[0] == 429  # the one in turn
        assert call("POST", url + "/payments", {"reference": "r-other"})[0] == 200
        assert call("POST", url + "/payments", {"reference": "r-stale"})[0] == 429  # its own second
        assert call("POST", url + "/payments", {"reference": "r-stale"})[0] == 200  # its own spent
        status, got = call("GET", url + "/control")
        assert status == 200 and got["faults"] == [] and got["faults_by_reference"] == {"r-stale": []}
        assert fake.taken("r-stale") == 1 and fake.taken("r-other") == 1
    finally:
        server.shutdown()
