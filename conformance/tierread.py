"""A third-party reader of the cold tier (be-protocol P15.8, P15.9, P15.16; foundations item 5, T11b): a unit's
canonical rows and digest from its manifest and Parquet objects (pyarrow), or from its warm rows, written from the
protocol's text and not from be-sdk-go, so that anyone can check an archive copy without the SDK. The canonical
encoding and the domain-separated digest are the Python SDK's (besdk.canon, security 09a)."""
from __future__ import annotations

import base64
import datetime as dt
import hashlib
import io
import json
import struct
import uuid
from collections.abc import Callable, Iterable
from typing import Any

import pyarrow.parquet as pq
from besdk import canon


def value(typ: str, v: Any) -> Any:
    """One column's canonical value (P15.8) from what Parquet or the warm row gives."""
    match typ:
        case "text":
            return canon.text(v)
        case "int2" | "int4" | "int8":
            return canon.decimal(str(int(v)))
        case "numeric":
            return canon.decimal(str(v))
        case "bool":
            if not isinstance(v, bool):
                raise ValueError(f"bool {v!r}")
            return v
        case "uuid":
            if isinstance(v, (bytes, bytearray)):
                return str(uuid.UUID(bytes=bytes(v)))
            return str(uuid.UUID(str(v)))
        case "timestamptz":
            if isinstance(v, str):
                return canon.instant(v)
            if v.tzinfo is None:  # Parquet's UTC-adjusted timestamp read without its zone
                v = v.replace(tzinfo=dt.timezone.utc)
            return canon.time(v)
        case "date":
            return v if isinstance(v, str) else v.isoformat()
        case "jsonb":
            return v if isinstance(v, str) else json.dumps(v)
        case "bytea":
            if isinstance(v, str):  # the warm row's canonical form already
                return v
            return base64.b64encode(bytes(v)).decode()
        case "text[]":
            items = json.loads(v) if isinstance(v, str) else v
            if any(i is None for i in items):
                raise ValueError("a text array with a null element has no canonical form")
            return [canon.text(i) for i in items]
    raise ValueError(f"no canonical form for type {typ!r}")


def canonical_row(types: dict[str, str], values: dict[str, Any]) -> bytes:
    """The canonical row's bytes: the columns not null, each by its type."""
    return canon.encode({k: value(types[k], v) for k, v in values.items() if v is not None})


def canonical_pk(types: dict[str, str], key: list[str], values: dict[str, Any]) -> bytes:
    return canon.encode([value(types[k], values[k]) for k in key])


def table_sum(types: dict[str, str], key: list[str], rows: Iterable[dict[str, Any]]) -> tuple[int, str]:
    """The rows and the table's hash (P15.9): sorted by the canonical key's bytes, each row's 8-byte big-endian length
    then its bytes, SHA-256 in lower-case hex."""
    keyed = sorted((canonical_pk(types, key, r), canonical_row(types, r)) for r in rows)
    h = hashlib.sha256()
    for _, row in keyed:
        h.update(struct.pack(">Q", len(row)))
        h.update(row)
    return len(keyed), h.hexdigest()


def unit_digest(cls: str, schema_version: str, sums: list[tuple[str, int, str]]) -> str:
    tables = [{"name": n, "rows": str(r), "sha256": s} for n, r, s in sorted(sums)]
    return canon.digest("be.tier.unit", 1, {"class": cls, "schema_version": schema_version, "tables": tables})


def _types(table: dict) -> tuple[dict[str, str], list[str]]:
    return {c["name"]: c["type"] for c in table["columns"]}, list(table["pk"])


def parquet_rows(data: bytes) -> list[dict[str, Any]]:
    return pq.read_table(io.BytesIO(data)).to_pylist()


def digest_from_objects(manifest: dict, fetch: Callable[[str], bytes]) -> tuple[str, dict[str, list[dict]]]:
    """An object-parquet unit's digest from its manifest and objects (fetch reads one by its key); every object's size
    and SHA-256 checked against the manifest first. Answers the digest and the rows read, by table."""
    sums, tables = [], {}
    for t in manifest["tables"]:
        types, key = _types(t)
        rows: list[dict] = []
        for o in t.get("objects") or []:
            data = fetch(o["key"])
            if len(data) != int(o["bytes"]) or hashlib.sha256(data).hexdigest() != o["sha256"]:
                raise ValueError(f"object {o['key']}: not the bytes the manifest names")
            part = parquet_rows(data)
            if len(part) != int(o["rows"]):
                raise ValueError(f"object {o['key']}: {len(part)} rows, the manifest says {o['rows']}")
            rows += part
        n, h = table_sum(types, key, rows)
        if n != int(t["rows"]):
            raise ValueError(f"table {t['name']}: {n} rows, the manifest says {t['rows']}")
        sums.append((t["name"], n, h))
        tables[t["name"]] = rows
    return unit_digest(manifest["class"], manifest["schema_version"], sums), tables


def digest_from_warm(manifest: dict, rows: dict[str, list[bytes]]) -> str:
    """A postgres unit's digest from its manifest and its warm rows (be_tier_row's canonical row bytes, by table)."""
    sums = []
    for t in manifest["tables"]:
        types, key = _types(t)
        parsed = [json.loads(r) for r in rows.get(t["name"], [])]
        n, h = table_sum(types, key, parsed)
        sums.append((t["name"], n, h))
    return unit_digest(manifest["class"], manifest["schema_version"], sums)
