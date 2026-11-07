"""The watermark source stream (3.2.47; the Go probe since 3.2.31; CONTRACT.md "水位来源"): a write with
"watermark": true goes to <stream>_wm with the subject probe:<id>; the stream answers pulls (be.source.v1.
WatermarkService) and its record reads (be.source.v1.RecordService, P9.30): a write is never deleted, an ID the
probe never made is purged."""
from __future__ import annotations

from besdk.events.core import Stream
from besdk.events.sources import EXISTS, PURGED, ReadResult, Record, Records, Watermark

PARTITIONS = 2
SUITE = "test/acceptance"


def stream(name: str, pfx: str, callers: list[str]) -> Stream:
    """The watermark stream of a variant whose event stream is name."""
    who = [*callers, SUITE]
    return Stream(name + "_wm", PARTITIONS, watermark=Watermark(pfx + ".pull", who),
                  records=Records(pfx + ".read", who, read, page))


def data(rid: str, size: int, label: str | None, relay: bool) -> dict:
    """What the written event carries: the record's content."""
    return {"id": rid, "relay": relay, **({"blob": "w" * size} if size else {}), **({"label": label} if label else {})}


async def read(tx, ids: list[str]) -> list[ReadResult]:
    out = []
    for rid in ids:
        r = await tx.fetchrow("SELECT bytes, label, relay FROM probe_writes WHERE id::text = $1 AND watermark", rid)
        out.append(ReadResult(rid, PURGED) if r is None else
                   ReadResult(rid, EXISTS, 1, data(rid, r["bytes"], r["label"], r["relay"])))
    return out


async def page(tx, after: str, limit: int) -> list[Record]:
    rows = await tx.fetch("""SELECT id::text AS id, bytes, label, relay FROM probe_writes WHERE watermark
        AND id::text COLLATE "C" > $1 ORDER BY id::text COLLATE "C" LIMIT $2""", after, limit)
    return [Record(r["id"], 1, data(r["id"], r["bytes"], r["label"], r["relay"])) for r in rows]
