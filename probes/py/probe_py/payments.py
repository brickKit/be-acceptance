"""The external commands profile's payment (3.2.48; the Go probe's since 3.2.41; CONTRACT.md "付款"; be-protocol
chapter 13): a payment is an external command to the suite's fake payment system (conformance/external) at
PROBE_EXTERNAL_URL; the capabilities it declares are configured, so the suite checks each against what the fake system
really does. The business reference is the external idempotency key and the payment's identity there."""
from __future__ import annotations

from datetime import datetime, timezone
from urllib.parse import quote, urlencode

from fastapi.responses import JSONResponse

from besdk import errs
from besdk.calls.client import not_sent, status_of
from besdk.extcmd.commands import Answer, Command, Result
from besdk.extcmd.reconcile import Record
from besdk.extcmd.state import MAYBE_REACHED, NOT_REACHED, REJECTED, Capabilities

OP_PAY = "pay"


def capabilities(config) -> dict[str, Capabilities]:
    return {OP_PAY: Capabilities(query=True, dedupe_window=config.duration("PROBE_EXTERNAL_DEDUPE_WINDOW"),
                                 last_use=config.bool("PROBE_EXTERNAL_LAST_USE"),
                                 max_processing=config.duration("PROBE_EXTERNAL_MAX_PROCESSING"),
                                 irrevocable=config.bool("PROBE_EXTERNAL_IRREVOCABLE"))}


def classify(e: BaseException) -> str:
    """A failed send (P13.5): nothing sent (no connection, held back by the SDK, an explicit rate limit), refused for
    good (the request is wrong), or possibly processed (a timeout, a gateway error, a cut answer)."""
    status = status_of(e)
    if not_sent(e) or status == 429:
        return NOT_REACHED
    if status in (400, 422):
        return REJECTED
    return MAYBE_REACHED


def _iso(t: datetime) -> str:
    return t.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


class Payments:
    """The adapter (besdk.extcmd.commands.Adapter) with the listing of a time window (P13.11, Go since 3.2.43)."""

    def __init__(self, rt):
        self.rt = rt

    def _client(self):
        url = self.rt.config.string("PROBE_EXTERNAL_URL")
        if not url:
            raise RuntimeError("PROBE_EXTERNAL_URL is not set")
        return self.rt.http_at("external", url)

    async def send(self, c: Command) -> Result:
        try:
            h = self._client()
        except RuntimeError as e:
            return Result(NOT_REACHED, response=str(e))
        body = {"reference": c.reference, "params": c.params, **({"expires_at": _iso(c.expires_at)} if c.expires_at else {})}
        try:
            out = await h.do("POST", "/payments", body, idempotency_key=c.reference)
        except Exception as e:  # noqa: BLE001 - classified (P13.5)
            return Result(classify(e), response=str(e))
        return Result(out["state"], external_id=out.get("external_id", ""), response=out["state"])

    async def query(self, c: Command) -> Answer:
        try:
            out = await self._client().do("GET", "/payments/" + quote(c.reference, safe=""))
        except errs.Error as e:
            if status_of(e) == 404:
                return Answer(False)
            raise
        return Answer(True, out["state"], out.get("external_id", ""), response=out["state"])

    async def list(self, start: datetime, end: datetime) -> list[Record]:
        out = await self._client().do("GET", "/payments?" + urlencode({"from": _iso(start), "to": _iso(end)}))
        return [Record(p["reference"], p["state"], p.get("external_id", ""), response=p["state"])
                for p in out.get("payments", [])]


async def pay(rt, component: str, c):
    """POST /api/probe/payment: the command, written in this request's transaction."""
    body = await c.json()
    ref, params = body.get("reference"), body.get("params")
    if not ref or not isinstance(params, dict) or not params:
        raise errs.new("REQUEST_INVALID", "reference and params are required")
    expires = body.get("expires_at")
    try:
        expires_at = datetime.fromisoformat(expires.replace("Z", "+00:00")) if expires else None
    except (AttributeError, ValueError):
        raise errs.new("REQUEST_INVALID", "expires_at is not an RFC 3339 time") from None
    cmd, state = await rt.db.tx(lambda tx: rt.external.request(tx, component, ref, OP_PAY, params, expires_at))
    return JSONResponse({"command": cmd.id, "state": state})


async def payment(rt, component: str, c):
    """GET /api/probe/payment/{reference}: the command and its attempts."""
    ref = c.param("reference")

    async def read(tx):
        cmd = await tx.fetchrow("""SELECT id, state, coalesce(reason, '') AS reason, external_id, resend_deadline
            FROM be_external_command WHERE requester = $1 AND reference = $2""", component, ref)
        if cmd is None:
            return None
        attempts = await tx.fetch("""SELECT kind, coalesce(result, '') AS result, reached FROM be_external_attempt
            WHERE command = $1 ORDER BY n""", cmd["id"])
        return cmd, attempts

    got = await rt.db.tx(read)
    if got is None:
        raise errs.new("NOT_FOUND", "no such payment")
    cmd, attempts = got
    return JSONResponse({"command": str(cmd["id"]), "state": cmd["state"], "reason": cmd["reason"],
                         "external_id": cmd["external_id"],
                         "resend_deadline": _iso(cmd["resend_deadline"]) if cmd["resend_deadline"] else None,
                         "attempts": [{"kind": a["kind"], "result": a["result"], "reached": a["reached"]}
                                      for a in attempts]})
