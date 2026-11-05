"""The authorization profile's resource (since 3.2.38; the suite's conformance/profile/authz.py), the same as the Go
probe's probe/entries.go: entries with one dimension of each kind, an ordinary, a restricted and a secret field, a
list, a single object, commands whose candidate state is checked, a high-risk approval that can hold its transaction
open, and an export with its own fields. Only with PROBE_GUARD: an entry is always protected."""
from __future__ import annotations

import asyncio
import uuid

from fastapi.responses import JSONResponse, Response

from besdk import authz, errs, lists
from besdk.authz import AMOUNT, NULL, ORG_TREE, PRINCIPAL, VALUE_SET, Dimension, Fact, Field, Operation, Resource

READ, CREATE, MOVE, APPROVE, EXPORT = "read", "create", "move", "approve", "export"
MAX_HOLD_MS = 2000
COLUMNS = ("legal_entity", "dept_path", "owner_id", "amount", "currency")
SELECT = ("id::text AS id, number, legal_entity, dept_path, owner_id::text AS owner_id, amount::text AS amount, currency, "
          "cost::text AS cost, note, approved")


def resource(p: str) -> Resource:
    """legal_entity (value set), dept (org tree), owner (principal), amount (amount)."""
    o = Field("ordinary")
    return Resource(f"{p}.entry", f"{p}.entry.{READ}",
                    {"legal_entity": Dimension(VALUE_SET, "legal_entity"), "dept": Dimension(ORG_TREE, "dept_path"),
                     "owner": Dimension(PRINCIPAL, "owner_id", "owner"), "amount": Dimension(AMOUNT, "amount", "", "currency")},
                    {"id": o, "number": o, "legal_entity": o, "dept_path": o, "owner_id": o, "amount": o, "currency": o,
                     "approved": o, "cost": Field("restricted", f"{p}.entry.cost"), "note": Field("secret")})


def operations(p: str) -> list[Operation]:
    t = f"{p}.entry"
    return [Operation(f"{t}.{READ}", t, "read", "low"),
            Operation(f"{t}.{CREATE}", t, "create", "low", ["business_write"]),
            Operation(f"{t}.{MOVE}", t, "action", "low", ["business_write"]),
            Operation(f"{t}.{APPROVE}", t, "action", "high", ["business_write"]),
            Operation(f"{t}.{EXPORT}", t, "export", "low")]


def _fact(v) -> Fact:
    return NULL if v is None else Fact.of(v)


def facts(e: dict) -> dict:
    amount = NULL if e["amount"] is None or e["currency"] is None else Fact.money(e["amount"], e["currency"])
    return {"legal_entity": _fact(e["legal_entity"]), "dept": _fact(e["dept_path"]), "owner": _fact(e["owner_id"]),
            "amount": amount}


def candidate(op: str, e: dict) -> None:
    """A state an entry would take (security 01: a create or a change is judged on the new state as well): anything
    short of allowed is PERMISSION_DENIED (P8.18); unknown stays AUTHORIZATION_UNPROVABLE."""
    try:
        authz.subject().check(op, facts(e))
    except errs.Error as err:
        if err.reason in ("NOT_FOUND", "PERMISSION_DENIED"):
            raise errs.new("PERMISSION_DENIED", "the target state is outside the caller's authorization") from None
        raise


SPEC_COLUMNS = [lists.Column("number", "number", lists.TEXT, True, True),
                lists.Column("legal_entity", "legal_entity", lists.TEXT, False, True),
                lists.Column("dept_path", "dept_path", lists.TEXT, False, True),
                lists.Column("owner_id", "owner_id", lists.UUID, False, True),
                lists.Column("amount", "amount", lists.DECIMAL, False, True),
                lists.Column("currency", "currency", lists.TEXT, False, True),
                lists.Column("cost", "cost", lists.DECIMAL, True, True)]


class Entries:
    def __init__(self, rt, p: str):
        self.rt, self.p, self.res = rt, p, resource(p)

    def op(self, name: str) -> str:
        return f"{self.p}.entry.{name}"

    def routes(self, add) -> None:
        add("GET", "entries", f"entry.{READ}", self.list)
        add("POST", "entries", f"entry.{CREATE}", self.create)
        add("GET", "entries/{id}", f"entry.{READ}", self.read)
        add("POST", "entries/{id}/move", f"entry.{MOVE}", self.move)
        add("POST", "entries/{id}/approve", f"entry.{APPROVE}", self.approve)
        add("GET", "entries/{id}/export", f"entry.{EXPORT}", self.export)

    async def load(self, raw: str) -> dict:
        try:
            rid = uuid.UUID(raw)
        except ValueError:
            raise errs.new("NOT_FOUND", "not found") from None
        row = await self.rt.db.tx(lambda tx: tx.fetchrow(f"SELECT {SELECT} FROM probe_entries WHERE id = $1", rid))
        if row is None:
            raise errs.new("NOT_FOUND", "not found")  # the same answer as an invisible one (P8.16)
        return dict(row)

    async def one(self, c, op: str):
        """A stored entry for the operation, shaped by the operation's own fields (an export outputs the export
        segments' fields, P8.15) and masked (P8.31)."""
        e = await self.load(c.param("id"))
        d = authz.subject().check(self.op(op), facts(e))
        return JSONResponse(authz.mask(self.res, e, d.fields))

    async def read(self, c):
        return await self.one(c, READ)

    async def export(self, c):
        return await self.one(c, EXPORT)

    async def create(self, c):
        body = await c.json()
        e = {"id": str(uuid.uuid7()), "number": str(body.get("number", "")), "cost": str(body.get("cost") or "0"),
             "note": str(body.get("note", "")), **{k: body.get(k) for k in COLUMNS}}
        candidate(self.op(CREATE), e)

        async def fn(tx):
            await tx.execute("INSERT INTO probe_entries (id, number, legal_entity, dept_path, owner_id, amount, currency, "
                             "cost, note) VALUES ($1::text::uuid, $2, $3, $4, $5::text::uuid, $6::text::numeric, $7, "
                             "$8::text::numeric, $9)", e["id"], e["number"], e["legal_entity"], e["dept_path"],
                             e["owner_id"], e["amount"], e["currency"], e["cost"], e["note"])
        await self.rt.db.tx(fn)
        return JSONResponse({"id": e["id"]}, status_code=201)

    async def move(self, c):
        """The stored state must allow the move, and so must the new one."""
        body = await c.json()
        e = await self.load(c.param("id"))
        authz.subject().check(self.op(MOVE), facts(e))
        e.update({k: body[k] for k in COLUMNS if body.get(k) is not None})
        candidate(self.op(MOVE), e)

        async def fn(tx):
            await tx.execute("UPDATE probe_entries SET legal_entity = $2, dept_path = $3, owner_id = $4::text::uuid, "
                             "amount = $5::text::numeric, currency = $6 WHERE id = $1::text::uuid", e["id"],
                             e["legal_entity"], e["dept_path"], e["owner_id"], e["amount"], e["currency"])
        await self.rt.db.tx(fn)
        return Response(status_code=204)

    async def approve(self, c):
        """High risk: the write commits only while the view that allowed it is valid (P8.12); hold_ms keeps the
        transaction open after the write (CK-AZ-04)."""
        body = await c.json()
        hold = body.get("hold_ms", 0)
        if isinstance(hold, bool) or not isinstance(hold, int) or not 0 <= hold <= MAX_HOLD_MS:
            raise errs.new("REQUEST_INVALID", f"hold_ms must be 0..{MAX_HOLD_MS}")
        e = await self.load(c.param("id"))
        authz.subject().check(self.op(APPROVE), facts(e))

        async def fn(tx):
            await tx.execute("UPDATE probe_entries SET approved = true WHERE id = $1::text::uuid", e["id"])
            await asyncio.sleep(hold / 1000)
        await self.rt.db.tx(fn)
        return Response(status_code=204)

    async def list(self, c):
        """?<column>=<value> filters (eq), sort=<field> or -<field>, readable_only=<field>,..., limit, cursor."""
        s = authz.subject()
        req = lists.Request(cursor=c.query("cursor"), limit=lists.parse_limit(c.query("limit")))
        for col in SPEC_COLUMNS:
            if v := c.query(col.field):
                req.filters.append(lists.Filter(col.field, "eq", [v]))
        if v := c.query("sort"):
            req.sort = [lists.Order(v.removeprefix("-"), v.startswith("-"))]
        if v := c.query("readable_only"):
            req.readable_only = v.split(",")
        q = lists.build(s, lists.Spec(self.op(READ), "e", "id", lists.UUID, SPEC_COLUMNS), req)
        cols = ", ".join("e." + x for x in SELECT.split(", "))
        if q.hits:
            cols += ", " + ", ".join(f"{h} AS hit{i}" for i, h in enumerate(q.hits))

        def scan(r):
            row = dict(r)
            return {k: row[k] for k in ("id", "number", "cost", "note", "approved", *COLUMNS)}, \
                [bool(row[f"hit{i}"]) for i in range(len(q.hits))]
        rows, nxt = await lists.page(self.rt.db, q, "probe_entries", cols, scan,
                                     lambda x: ([str(x[0][f]) for f in q.sort_fields()], x[0]["id"]))
        out = {"items": [authz.mask(self.res, e, q.fields(hits)) for e, hits in rows],
               "next": nxt}
        if q.readable_only:
            out["readable_only"] = q.readable_only
        return JSONResponse(out)
