"""A DingTalk simulator for the channel member integration/im-dingtalk (the assembly project's protocol 10; be-protocol
chapter 20), modelled on DingTalk's public documents as the member is (its docs/design.md open questions): the old
API's union id's user (POST /topapi/user/getbyunionid), the new API's application token (POST
/v1.0/oauth2/accessToken, the secret in the body; the old GET /gettoken too), interactive card (POST
/v1.0/im/interactiveCards/send), a user's OAuth token (POST /v1.0/oauth2/userAccessToken) and
the user it was given to (GET /v1.0/contact/users/me). It keeps every card it took (`cards`); the suite grants an OAuth
code for a DingTalk user (`grant`) and scripts the next sends' answers (`faults`). The callbacks' signature and
encryption (`sign`, `encrypt`, `decrypt`, `callback`) are DingTalk's event subscription as the member checks them, so
the suite can click a card's button as DingTalk would push it. Standard library plus `cryptography` (AES-CBC).

Run in the test's process (`Sim(...).start(port)`); the cell's member reaches it on the host (cells/prepare.py's
DINGTALK_SIM_PORT)."""
from __future__ import annotations

import base64
import hashlib
import json
import os
import secrets
import struct
import threading
import time
import uuid
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes

# Faults a send may meet (each consumed by one card):
REJECT = "reject"  # 400 with DingTalk's error code: refused for good
LIMIT = "limit"    # 429: not taken now, sent again later


def sign(token: str, timestamp: str, nonce: str, encrypt: str) -> str:
    """A callback's signature: SHA-1 of the four values sorted and joined."""
    return hashlib.sha1("".join(sorted([token, timestamp, nonce, encrypt])).encode()).hexdigest()


def _key(aes_key: str) -> bytes:
    k = base64.b64decode(aes_key + "=")
    assert len(k) == 32, "the AES key is not 43 base64 characters"
    return k


def encrypt(aes_key: str, app_key: str, msg: bytes) -> str:
    """16 random bytes, the length (4 bytes, big endian), the message and the application key, PKCS#7 to 32 bytes,
    AES-256-CBC with the key's first 16 bytes as the IV, base64."""
    k = _key(aes_key)
    plain = os.urandom(16) + struct.pack(">I", len(msg)) + msg + app_key.encode()
    pad = 32 - len(plain) % 32
    plain += bytes([pad]) * pad
    e = Cipher(algorithms.AES(k), modes.CBC(k[:16])).encryptor()
    return base64.b64encode(e.update(plain) + e.finalize()).decode()


def decrypt(aes_key: str, app_key: str, text: str) -> bytes:
    k = _key(aes_key)
    d = Cipher(algorithms.AES(k), modes.CBC(k[:16])).decryptor()
    plain = d.update(base64.b64decode(text)) + d.finalize()
    plain = plain[:-plain[-1]]
    n = struct.unpack(">I", plain[16:20])[0]
    assert plain[20 + n:].decode() == app_key, "the message is for another application"
    return plain[20:20 + n]


def callback(token: str, aes_key: str, app_key: str, event: dict, at_ms: int | None = None) -> tuple[dict, dict]:
    """A pushed callback as DingTalk sends it: the query (signature, timestamp, nonce) and the body ({"encrypt"})."""
    ts, nonce = str(at_ms if at_ms is not None else int(time.time() * 1000)), secrets.token_hex(4)
    enc = encrypt(aes_key, app_key, json.dumps(event).encode())
    return {"signature": sign(token, ts, nonce, enc), "timestamp": ts, "nonce": nonce}, {"encrypt": enc}


def click(card: dict, action: str, user_id: str | None = None) -> dict:
    """The card_callback event of a click on the card's button for the action (by the card's user unless another)."""
    value = next(b["Value"] for b in card["buttons"] if b.get("Action") == action)
    return {"eventId": str(uuid.uuid4()), "eventType": "card_callback", "outTrackId": card["outTrackId"],
            "userId": user_id or card["userId"], "value": value, "clickedAt": int(time.time() * 1000)}


@dataclass
class Sim:
    app_key: str
    app_secret: str
    cards: list[dict] = field(default_factory=list)       # every card taken, in order
    faults: list[str] = field(default_factory=list)       # the next sends' answers, each consumed once
    codes: dict[str, str] = field(default_factory=dict)   # an OAuth code -> the union id it was given to
    tokens: dict[str, str] = field(default_factory=dict)  # a user's access token -> union id
    users: dict[str, str] = field(default_factory=dict)   # union id -> the organisation's user id
    app_token: str = ""  # derived from the secret: a member's cached token stays valid across the simulator's restarts,
    # as DingTalk's does for its two hours
    lock: threading.Lock = field(default_factory=threading.Lock)
    server: ThreadingHTTPServer | None = None

    def __post_init__(self) -> None:
        self.app_token = self.app_token or hashlib.sha256(f"{self.app_key}:{self.app_secret}".encode()).hexdigest()[:32]

    def grant(self, user_id: str) -> str:
        """An OAuth code DingTalk would give the user at the consent page."""
        code, union = secrets.token_hex(8), f"union-{user_id}"
        with self.lock:
            self.codes[code], self.users[union] = union, user_id
        return code

    def cards_of(self, user_id: str) -> list[dict]:
        with self.lock:
            return [c for c in self.cards if c["userId"] == user_id]

    def start(self, port: int, host: str = "0.0.0.0") -> "Sim":
        sim = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def answer(self, status: int, body: dict) -> None:
                raw = json.dumps(body).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def body(self) -> dict:
                n = int(self.headers.get("Content-Length") or 0)
                return json.loads(self.rfile.read(n) or b"{}")

            def do_GET(self):
                u = urlparse(self.path)
                q = {k: v[0] for k, v in parse_qs(u.query).items()}
                if u.path == "/gettoken":
                    if q.get("appkey") != sim.app_key or q.get("appsecret") != sim.app_secret:
                        return self.answer(200, {"errcode": 40089, "errmsg": "invalid appkey or appsecret"})
                    return self.answer(200, {"errcode": 0, "access_token": sim.app_token, "expires_in": 7200})
                if u.path == "/v1.0/contact/users/me":
                    with sim.lock:
                        union = sim.tokens.get(self.headers.get("x-acs-dingtalk-access-token", ""))
                    if not union:
                        return self.answer(401, {"code": "InvalidAuthentication"})
                    return self.answer(200, {"unionId": union})
                self.answer(404, {"code": "NotFound"})

            def do_POST(self):
                u = urlparse(self.path)
                q = {k: v[0] for k, v in parse_qs(u.query).items()}
                b = self.body()
                if u.path == "/v1.0/oauth2/accessToken":
                    if b.get("appKey") != sim.app_key or b.get("appSecret") != sim.app_secret:
                        return self.answer(400, {"code": "invalidClientIdOrSecret"})
                    return self.answer(200, {"accessToken": sim.app_token, "expireIn": 7200})
                if u.path == "/v1.0/oauth2/userAccessToken":
                    with sim.lock:
                        union = sim.codes.pop(b.get("code", ""), None)
                        ok = union and b.get("clientId") == sim.app_key and b.get("clientSecret") == sim.app_secret
                        if ok:
                            token = secrets.token_hex(16)
                            sim.tokens[token] = union
                    if not ok:
                        return self.answer(400, {"code": "invalidAuthCode"})
                    return self.answer(200, {"accessToken": token, "expireIn": 7200})
                if u.path == "/topapi/user/getbyunionid":
                    if q.get("access_token") != sim.app_token:
                        return self.answer(200, {"errcode": 40014, "errmsg": "invalid access_token"})
                    with sim.lock:
                        user = sim.users.get(b.get("unionid", ""))
                    if not user:
                        return self.answer(200, {"errcode": 60121, "errmsg": "not in the organisation"})
                    return self.answer(200, {"errcode": 0, "result": {"userid": user}})
                if u.path == "/v1.0/im/interactiveCards/send":
                    if self.headers.get("x-acs-dingtalk-access-token") != sim.app_token:
                        return self.answer(401, {"code": "InvalidAuthentication"})
                    with sim.lock:
                        fault = sim.faults.pop(0) if sim.faults else ""
                    if fault == LIMIT:
                        return self.answer(429, {"code": "Throttling"})
                    if fault == REJECT:
                        return self.answer(400, {"code": "invalidParameter"})
                    params = b["cardData"]["cardParamMap"]
                    card = {"outTrackId": b["outTrackId"], "userId": b["receiverUserIdList"][0],
                            "template": b["cardTemplateId"], "title": params["title"], "body": params["body"],
                            "buttons": json.loads(params["buttons"])}
                    with sim.lock:
                        sim.cards.append(card)
                    return self.answer(200, {"success": True, "result": {"processQueryKey": secrets.token_hex(8)}})
                self.answer(404, {"code": "NotFound"})

        self.server = ThreadingHTTPServer((host, port), Handler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        return self

    def stop(self) -> None:
        if self.server:
            self.server.shutdown()
            self.server.server_close()
