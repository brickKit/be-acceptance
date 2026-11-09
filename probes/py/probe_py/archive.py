"""The tiering profile (Python since 3.2.50, Go 3.2.47; assembly protocol 11): archives of every column type the
canonical row knows, in three levels, closed (a terminal state) or open. The SDK seals, copies and releases them by its
own jobs; the route is the deployment's choice (TIERING_ROUTE_PROBE_ARCHIVE: the warm route by default, or the object
store). The retention is pending and nothing is erasable, so no hold or approval route: the profile needs no guard.
The same as the Go probe's probe/archive.go."""
from __future__ import annotations

import datetime as dt
import uuid

from fastapi.responses import JSONResponse

from besdk import canon, errs
from besdk.tiering import reads
from besdk.tiering import rows as canonical
from besdk.tiering.classes import COMPLETED, OBJECT, POSTGRES, Child, Class, Retention, Table
from besdk.tiering.rows import (BOOL, BYTEA, DATE, INT2, INT4, INT8, JSONB, NUMERIC, TEXT, TEXT_ARRAY, TIMESTAMPTZ,
                                UUID)

ARCHIVE_CLASS = "probe.archive"
MAX_LABEL = 64


def archive() -> Class:
    return Class(ARCHIVE_CLASS, COMPLETED,
                 Table("probe_archive", {"id": UUID, "batch": TEXT, "closed_at": TIMESTAMPTZ, "created_at": TIMESTAMPTZ,
                                         "t": TEXT, "i2": INT2, "i4": INT4, "i8": INT8, "n": NUMERIC, "b": BOOL,
                                         "d": DATE, "j": JSONB, "bin": BYTEA, "tags": TEXT_ARRAY}, pk="id"),
                 anchor="closed_at", created="created_at", terminal="r.closed_at IS NOT NULL", residence_days=365,
                 retention=Retention(pending=True), schema_version="1",
                 children=(Child(Table("probe_archive_part", {"id": UUID, "archive_id": UUID, "label": TEXT}, pk="id"),
                                 "archive_id"),
                           Child(Table("probe_archive_piece", {"part_id": UUID, "seq": INT4, "note": TEXT},
                                       key=("part_id", "seq")), "part_id", "probe_archive_part")),
                 group=("batch",), locate=("batch",), routes=(POSTGRES, OBJECT))


class Archives:
    def __init__(self, rt):
        self.rt = rt

    async def make(self, c):
        """One archive: a value of every type in its root (two of them null), its parts, two pieces each."""
        b = await c.json()
        if not isinstance(b, dict):
            raise errs.new("REQUEST_INVALID", "the body is not an object")
        batch, closed_days, parts = b.get("batch"), b.get("closed_days_ago", 0), b.get("parts", 0)
        if not isinstance(batch, str) or not batch or len(batch.encode()) > MAX_LABEL or \
                not isinstance(parts, int) or isinstance(parts, bool) or not 0 <= parts <= 50 or \
                not isinstance(closed_days, int) or isinstance(closed_days, bool):
            raise errs.new("REQUEST_INVALID", "a batch of at most 64 characters and 0 to 50 parts")
        aid = uuid.uuid7()
        closed = dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=closed_days) if closed_days >= 0 else None

        async def body(tx):
            await tx.execute("""INSERT INTO probe_archive (id, batch, closed_at, created_at, t, i2, i4, i8, n, b, d, j,
                bin, tags) VALUES ($1, $2, $3, now() - interval '500 days', 'café ünïcode', -32768, 2147483647,
                9007199254740993, 1250.5, true, NULL, '{"a": [1, 2]}', '\\x00ff10', ARRAY['B-2', 'A-1'])""",
                             aid, batch, closed)
            for i in range(parts):
                part = uuid.uuid7()
                await tx.execute("INSERT INTO probe_archive_part VALUES ($1, $2, $3)", part, aid, f"part {i}")
                await tx.execute("INSERT INTO probe_archive_piece VALUES ($1, 1, 'first'), ($1, 2, NULL)", part)

        await self.rt.db.tx(body)
        return JSONResponse({"id": str(aid)})

    async def row(self, c):
        """One archive as canonical rows, wherever it is: "online", or the route it was archived on."""
        aid = c.param("id")
        if self.rt.tiering is not None:
            a = await reads.get(self.rt.tiering, ARCHIVE_CLASS, aid)
            if a is not None:
                return JSONResponse({"where": a.route, "root": a.root, "children": _sorted(a.children)})
        try:
            key = uuid.UUID(aid)
        except ValueError:
            raise errs.new("NOT_FOUND", "no such archive") from None
        cl = archive()

        async def body(tx):
            roots = await _canonical_rows(tx, cl.root, "t.id = $1", key)
            if not roots:
                return None
            children = {}
            for ch in cl.children:
                where = "t.part_id IN (SELECT id FROM probe_archive_part WHERE archive_id = $1)" if ch.parent \
                    else "t.archive_id = $1"
                children[ch.name] = await _canonical_rows(tx, ch.table, where, key)
            return roots[0], children

        got = await self.rt.db.tx(body)
        if got is None:
            raise errs.new("NOT_FOUND", "no such archive")
        return JSONResponse({"where": "online", "root": got[0], "children": _sorted(got[1])})


async def _canonical_rows(tx, t: Table, where: str, *args) -> list[dict]:
    """Rows of a class table as canonical rows (P15.8): what a route answers for them once archived."""
    names = sorted(t.columns)
    exprs = [f"t.{n}::text AS {n}" if t.columns[n] in (NUMERIC, JSONB) else f"t.{n}" for n in names]
    return [canonical.row(t.columns, dict(r))
            for r in await tx.fetch(f"SELECT {', '.join(exprs)} FROM {t.name} t WHERE {where}", *args)]


def _sorted(children: dict[str, list[dict]]) -> dict[str, list[dict]]:
    """Each child table's rows by their canonical text, so an online read and an archived one compare equal."""
    return {k: sorted(rs, key=canon.encode) for k, rs in children.items()}
