"""Prints the configSchema block of a probe's component.yaml from the SDK's key list (serve and migrate), so the
manifest declares exactly what the SDK reads: `uv run python scripts/config_schema.py`. The conformance tests check
that both probes' manifests carry this block."""
from __future__ import annotations

from besdk import db, host
from besdk.config import Key, Type

import probe_py

FILE_KEYS = {"SERVICE_TLS_CERT_FILE", "SERVICE_TLS_KEY_FILE", "SERVICE_TLS_CA_FILE"}
TYPES = {Type.STRING: "string", Type.INTEGER: "integer", Type.BOOLEAN: "boolean", Type.DURATION: "string",
         Type.ENUM: "string"}


OWN = {"PROBE_NEXT": "the component ID of the probe that call and relay go to; unset: none",
       "PROBE_PEER_STREAM": "the peer probe's event stream to consume (test.probe_go, test.probe_py); unset: none",
       "PROBE_NEXT_URL": "the next probe's user surface: $endpoint:<its ID> (protocol P4.26); unset: no HTTP calls",
       "PROBE_NEXT_GRPC_URL": "the next probe's system surface: $endpoint:<its ID>:grpc (protocol P4.26); unset: no gRPC calls",
       "PROBE_GUARD": "true: the user routes need a credential and their operation instead of being public"}


def describe(k: Key) -> str:
    parts = [OWN[k.name]] if k.name in OWN else []
    if k.type == Type.DURATION:
        parts.append("a duration (e.g. 2s, 500ms)")
    if k.default is not None:
        d = k.default
        if hasattr(d, "total_seconds"):
            d = f"{d.total_seconds():g}s"
        parts.append(f"SDK default {str(d).lower() if isinstance(d, bool) else d}")
    if k.process:
        parts.append("process-level: the same for every member of a shell")
    if k.name in FILE_KEYS:
        parts.append("a file path, re-read on rotation")
    return "; ".join(parts)


def keys() -> list[Key]:
    seen, out = set(), []
    for k in [*host.keys(probe_py.definition()), *db.migrate_keys()]:
        if k.name not in seen:
            seen.add(k.name)
            out.append(k)
    return out


def block() -> str:
    lines = ["configSchema:", "  type: object"]
    required = [k.name for k in keys() if k.required]
    if required:
        lines.append("  required: [" + ", ".join(required) + "]")
    lines.append("  properties:")
    for k in keys():
        lines.append(f"    {k.name}:")
        lines.append(f"      type: {TYPES[k.type]}")
        if k.type == Type.ENUM:
            lines.append("      enum: [" + ", ".join(k.values) + "]")
        if k.secret or k.name in FILE_KEYS:
            lines.append("      secret: true")
        if k.name in FILE_KEYS:
            lines.append("      mount: file")
        if text := describe(k):
            lines.append(f"      description: \"{text}\"")
    return "\n".join(lines) + "\n"


if __name__ == "__main__":
    print(block(), end="")
