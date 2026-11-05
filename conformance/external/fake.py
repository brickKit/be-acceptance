"""A fake payment system for the external commands profile (be-protocol chapter 13; the assembly project's protocol
10): POST /payments pays by the business reference, GET /payments/<reference> answers what it holds, GET
/payments?from=&to= lists what it took in a time window (RFC 3339; an adapter's listing, P13.11). The suite scripts its
faults, one per payment, in turn (`faults`), and whether it deduplicates by the reference; every payment it took is
kept (`payments`), so a test sees a double payment for what it is. Standard library only: in the test's process, or
on its own for a drill (`python3 fake.py --port N`), scripted then by POST /control with {"faults": [...],
"dedupe": bool, "query_down": bool} and read by GET /control."""
from __future__ import annotations

import argparse
import json
import threading
import uuid
from dataclasses import dataclass, field
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

# Faults a payment may meet (each consumed by one POST /payments):
CUT_RECORDED = "cut-recorded"      # paid, then the connection cut before the answer: the caller cannot know
CUT_UNRECORDED = "cut-unrecorded"  # cut before it was taken: nothing paid, and the caller cannot know either
REJECT = "reject"                  # 422: the request is wrong for good
LIMIT = "limit"                    # 429: explicitly not taken now


@dataclass
class Fake:
    dedupe: bool = False
    query_down: bool = False
    faults: list[str] = field(default_factory=list)
    payments: list[dict] = field(default_factory=list)  # every payment taken, duplicates included
    lock: threading.Lock = field(default_factory=threading.Lock)

    def held(self, reference: str) -> dict | None:
        found = [p for p in self.payments if p["reference"] == reference]
        return found[0] if found else None

    def taken(self, reference: str) -> int:
        with self.lock:
            return sum(1 for p in self.payments if p["reference"] == reference)


def _now() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%fZ")


def _at(s: str) -> datetime:
    return datetime.fromisoformat(s.replace("Z", "+00:00"))


def serve(fake: Fake, host: str = "127.0.0.1", port: int = 0, control: bool = False) -> tuple[ThreadingHTTPServer, str]:
    """Starts the fake (on a free local port by default); the caller shuts the server down. With control, POST
    /control scripts it (a drill drives it from outside its process)."""

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):  # quiet
            pass

        def answer(self, status: int, body: dict) -> None:
            raw = json.dumps(body).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def cut(self) -> None:
            self.close_connection = True
            self.connection.close()

        def do_POST(self):
            body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))) or b"{}")
            if control and self.path == "/control":
                with fake.lock:
                    for k in ("faults", "dedupe", "query_down"):
                        if k in body:
                            setattr(fake, k, body[k])
                return self.answer(200, {"ok": True})
            if self.path != "/payments":
                return self.answer(404, {"error": "no such path"})
            ref = body.get("reference", "")
            with fake.lock:
                fault = fake.faults.pop(0) if fake.faults else ""
                if fault == CUT_UNRECORDED:
                    return self.cut()
                if fault == REJECT:
                    return self.answer(422, {"error": "the account is not valid"})
                if fault == LIMIT:
                    return self.answer(429, {"error": "too many requests"})
                existing = fake.held(ref)
                if existing and fake.dedupe:  # the original answer, nothing paid again
                    payment = existing
                else:
                    payment = {"reference": ref, "external_id": "pay-" + uuid.uuid4().hex[:12], "state": "succeeded",
                               "params": body.get("params"), "at": _now()}
                    fake.payments.append(payment)
            if fault == CUT_RECORDED:
                return self.cut()
            self.answer(200, {"state": payment["state"], "external_id": payment["external_id"]})

        def do_GET(self):
            url = urlparse(self.path)
            if control and url.path == "/control":
                with fake.lock:
                    return self.answer(200, {"payments": fake.payments, "faults": fake.faults})
            if url.path == "/payments":  # the listing: what was taken in [from, to]
                if fake.query_down:
                    return self.answer(503, {"error": "the query is down"})
                q = parse_qs(url.query)
                try:
                    lo, hi = _at(q["from"][0]), _at(q["to"][0])
                except (KeyError, ValueError):
                    return self.answer(400, {"error": "from and to are RFC 3339 times"})
                with fake.lock:
                    seen, out = set(), []
                    for p in fake.payments:
                        if lo <= _at(p["at"]) <= hi and p["reference"] not in seen:
                            seen.add(p["reference"])
                            out.append({"reference": p["reference"], "state": p["state"], "external_id": p["external_id"]})
                return self.answer(200, {"payments": out})
            if not url.path.startswith("/payments/"):
                return self.answer(404, {"error": "no such path"})
            if fake.query_down:
                return self.answer(503, {"error": "the query is down"})
            with fake.lock:
                p = fake.held(self.path[len("/payments/"):])
            if p is None:
                return self.answer(404, {"error": "no such payment"})
            self.answer(200, {"state": p["state"], "external_id": p["external_id"]})

    server = ThreadingHTTPServer((host, port), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server, f"http://{host}:{server.server_address[1]}"


if __name__ == "__main__":  # on its own, for a drill
    a = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    a.add_argument("--host", default="0.0.0.0")
    a.add_argument("--port", type=int, required=True)
    args = a.parse_args()
    srv, where = serve(Fake(), args.host, args.port, control=True)
    print(f"fake payments at {where}", flush=True)
    threading.Event().wait()
