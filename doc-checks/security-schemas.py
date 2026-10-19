#!/usr/bin/env python3
"""Generate the JSON Schemas of the security records from their field tables (security 09a: "at freeze the
protobuf and JSON Schema are generated from these tables"), or check that a directory holds exactly them.

Sources, relative to the assembly project root: the security 09a page and the component protocol's chapter 08 (the
audit record). A field table is a Markdown table with the columns 字段 | 类型 | 必填 | 约束 right after a bold line that
names the structure in backticks (**授权视图 `be.security.view`**); the common references are one table with a
leading 结构 column; a union is a table whose first column is `kind`. Structures written inline in a constraint
(`SubjectEntry = {segments: Set<Segment>, …}`) are picked up too. One schema file is written for every record type
(a name starting with "be."), self-contained: every structure it uses is in its $defs.

The schemas describe the bytes on the wire (D76): a record without its own digest, which travels next to it in the
envelope. They check structure and types; the conditional rules in the 约束 column stay in code.

Usage: security-schemas.py [--project DIR] (--out DIR | --check DIR)
"""
import argparse
import json
import pathlib
import re
import sys

SOURCES = ["docs/zh/06-protocols/security/09a-encoding-and-records.md", "tools/be-protocol/spec/08-identity-authz-audit.md"]
SCHEMA_ID = "https://github.com/brickKit/be-protocol/schemas/security/"

# Record types that carry the record envelope (09a, 记录封装): definitions, the view and the security records.
ENVELOPED = {"be.security.resource_definition", "be.security.operation_definition", "be.security.view",
             "be.security.security_disposition", "be.security.account_status"}
ENVELOPE_ON_WIRE_WITHOUT = {"digest"}  # the digest travels in the envelope, computed over these bytes
ALIASES = {"Segment": "be.security.segment"}
HEADERS = {"记录封装": "Envelope", "通用部分": "SecurityRecordCommon"}

BASIC = {
    "Id": {"type": "string", "pattern": "^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"},
    "Key": {"type": "string", "pattern": "^[a-z][a-z0-9_]*(\\.[a-z][a-z0-9_]*)*$", "maxLength": 128},
    "ComponentId": {"type": "string", "pattern": "^[a-z0-9]([a-z0-9-]*[a-z0-9])?/[a-z0-9]([a-z0-9-]*[a-z0-9])?$"},
    "Text": {"type": "string"},
    "Decimal": {"type": "string", "pattern": "^(0|-?[1-9][0-9]*|-?(0|[1-9][0-9]*)\\.[0-9]*[1-9])$"},  # RE2: no lookahead
    "Currency": {"type": "string", "pattern": "^[A-Z]{3}$"},
    "Instant": {"type": "string", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\\.[0-9]{6}Z$"},
    "Date": {"type": "string", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},
    "DurationMs": {"type": "integer", "minimum": 0},
    "Int": {"type": "integer", "minimum": -(2 ** 53 - 1), "maximum": 2 ** 53 - 1},
    "Bool": {"type": "boolean"},
    "Digest": {"type": "string", "pattern": "^sha256:[0-9a-f]{64}$"},
}
ENUM_VALUE = re.compile(r"^[a-z][a-z0-9_]*$")
BOLD = re.compile(r"^\*\*([^*`]*)(?:`([^`]+)`)?[^*]*\*\*")
INLINE_DEF = re.compile(r"`([A-Z][A-Za-z]+) = (\{[^`]*\})`")


class Fail(Exception):
    pass


def cells(line):
    return [c.strip() for c in line.strip().strip("|").split("|")]


def unquote(s):
    return s.strip().strip("`").strip()


# ---- the type grammar: Set<T>, List<T>, Map<K,T>, Presence<T>, Enum(a, b), {a: T, b: U（否）}, names, …

def split_top(s, sep=","):
    out, depth, cur = [], 0, ""
    for ch in s:
        if ch in "<({":
            depth += 1
        elif ch in ">)}":
            depth -= 1
        if ch in (sep, "，") and depth == 0:
            out.append(cur)
            cur = ""
        else:
            cur += ch
    if cur.strip():
        out.append(cur)
    return [x.strip() for x in out]


def requirement(s):
    """Split a trailing （否）/（条件）/（是） off a type; return the type and whether it is required."""
    m = re.match(r"^(.*?)（(否|条件|是)[^）]*）\s*$", s)
    if m:
        return m.group(1).strip(), m.group(2) == "是"
    return s.strip(), True


def object_fields(body):
    """Fields of an inline {a: T, b: U（否）}."""
    fields = []
    for part in split_top(body):
        name, _, typ = part.partition(":")
        typ, req = requirement(typ)
        fields.append((name.strip(), typ, req))
    return fields


def schema_of(typ, enum_hint, used, where):
    typ = typ.strip().strip("`")
    if typ in ("…", "对象"):
        return {}
    if typ.startswith("{") and typ.endswith("}"):
        return struct_schema(object_fields(typ[1:-1]), used, where)
    m = re.match(r"^(Set|List|Presence)<(.*)>$", typ)
    if m:
        inner = schema_of(m.group(2), enum_hint, used, where)
        if m.group(1) == "Presence":
            return {"oneOf": [
                {"type": "object", "properties": {"state": {"const": "value"}, "value": inner}, "required": ["state", "value"],
                 "additionalProperties": False},
                {"type": "object", "properties": {"state": {"enum": ["null", "unobtained", "unsupported"]}},
                 "required": ["state"], "additionalProperties": False}]}
        s = {"type": "array", "items": inner}
        if m.group(1) == "Set":
            s["uniqueItems"] = True
        return s
    m = re.match(r"^Map<([^,]+),(.*)>$", typ)
    if m:
        return {"type": "object", "propertyNames": schema_of(m.group(1), None, used, where),
                "additionalProperties": schema_of(m.group(2), enum_hint, used, where)}
    m = re.match(r"^Enum\((.*)\)$", typ)
    if m:
        return {"type": "string", "enum": [v.strip() for v in m.group(1).split(",")]}
    if typ == "Enum":
        if not enum_hint:
            raise Fail(f"{where}: Enum without values")
        return {"type": "string", "enum": enum_hint}
    if typ in BASIC:
        used.add(typ)
        return {"$ref": f"#/$defs/{typ}"}
    name = ALIASES.get(typ, typ)
    if name in STRUCTS:
        used.add(name)
        return {"$ref": f"#/$defs/{name}"}
    raise Fail(f"{where}: unknown type {typ!r}")


def struct_schema(fields, used, where):
    props, required = {}, []
    for name, typ, req, *rest in fields:
        hint = rest[0] if rest else None
        props[name] = schema_of(typ, hint, used, f"{where}.{name}")
        if req:
            required.append(name)
    s = {"type": "object", "properties": props, "additionalProperties": False}
    if required:
        s["required"] = required
    return s


def enum_values(constraint):
    head = constraint.split("；")[0]
    vals = [v for v in re.findall(r"`([^`]+)`", head) if ENUM_VALUE.match(v)]
    return vals or None


# ---- reading the tables

STRUCTS = {}  # name -> ("struct", [(field, type, required, enum)]) or ("union", [(kind, [(field, type, required)])])


def read(text, source):
    lines = text.splitlines()
    i, pending = 0, None
    while i < len(lines):
        line = lines[i]
        m = BOLD.match(line)
        if m:
            label, name = m.group(1).strip(), m.group(2)
            pending = name if name else HEADERS.get(label)
        if line.startswith("|") and i + 1 < len(lines) and re.match(r"^\|\s*-", lines[i + 1]):
            head = [unquote(c) for c in cells(line)]
            rows = []
            j = i + 2
            while j < len(lines) and lines[j].startswith("|"):
                rows.append(cells(lines[j]))
                j += 1
            take_table(head, rows, pending, source)
            pending = None
            i = j
            continue
        i += 1
    for name, body in INLINE_DEF.findall(text):
        STRUCTS.setdefault(name, ("struct", [(f, t, r, None) for f, t, r in object_fields(body[1:-1])]))


def take_table(head, rows, name, source):
    if head == ["结构", "字段", "类型", "必填", "约束"]:
        current = None
        for r in rows:
            if unquote(r[0]):
                current = unquote(r[0])
                STRUCTS[current] = ("struct", [])
            STRUCTS[current][1].append(field_row(r[1:]))
        return
    if not name:
        return
    if head == ["字段", "类型", "必填", "约束"]:
        STRUCTS[ALIASES.get(name, name)] = ("struct", [field_row(r) for r in rows])
    elif head[0] == "kind":
        variants = []
        for r in rows:
            fields = []
            for fm in re.finditer(r"`([a-z_]+): ([^`]+)`(（(否|条件)）)?", r[1]):
                fields.append((fm.group(1), fm.group(2), fm.group(4) is None))
            variants.append((unquote(r[0]), fields))
        STRUCTS[name] = ("union", variants)


def field_row(r):
    name, typ, req, constraint = unquote(r[0]), unquote(r[1]), r[2].strip(), r[3]
    if req not in ("是", "否", "条件"):
        raise Fail(f"field {name}: 必填 is {req!r}")
    hint = enum_values(constraint) if "Enum" in typ and "Enum(" not in typ else None
    return (name, typ, req == "是", hint)


def build(name):
    used = set()
    root = schema_for(name, used, top=True)
    defs, done = {}, set()
    while used - done:
        n = sorted(used - done)[0]
        done.add(n)
        defs[n] = dict(BASIC[n]) if n in BASIC else schema_for(n, used)
    doc = {"$schema": "https://json-schema.org/draft/2020-12/schema", "$id": f"{SCHEMA_ID}{name}.schema.json",
           "title": name}
    doc.update(root)
    if defs:
        doc["$defs"] = dict(sorted(defs.items()))
    return doc


def schema_for(name, used, top=False):
    kind, body = STRUCTS[name]
    if kind == "union":
        variants = []
        for k, fields in body:
            s = struct_schema([("kind", "Enum(" + k + ")", True)] + fields, used, f"{name}[{k}]")
            variants.append(s)
        return {"oneOf": variants}
    fields = list(body)
    if top and name in ENVELOPED:
        env = [f for f in STRUCTS["Envelope"][1] if f[0] not in ENVELOPE_ON_WIRE_WITHOUT]
        fields = env + [f for f in fields if f[0] not in {e[0] for e in env}]
    return struct_schema(fields, used, name)


def generate(project):
    STRUCTS.clear()
    for src in SOURCES:
        read((project / src).read_text(), src)
    out = {}
    for name in sorted(n for n in STRUCTS if n.startswith("be.")):
        out[f"{name}.schema.json"] = json.dumps(build(name), ensure_ascii=False, indent=2) + "\n"
    return out


def main():
    a = argparse.ArgumentParser()
    a.add_argument("--project", default=str(pathlib.Path(__file__).resolve().parents[3]))
    g = a.add_mutually_exclusive_group(required=True)
    g.add_argument("--out")
    g.add_argument("--check")
    o = a.parse_args()
    try:
        files = generate(pathlib.Path(o.project))
    except Fail as e:
        print(f"security-schemas: {e}", file=sys.stderr)
        return 1
    if o.out:
        d = pathlib.Path(o.out)
        d.mkdir(parents=True, exist_ok=True)
        for old in d.glob("*.schema.json"):
            if old.name not in files:
                old.unlink()
        for n, body in files.items():
            (d / n).write_text(body)
        print("\n".join(sorted(files)))
        return 0
    d, bad = pathlib.Path(o.check), []
    have = {p.name for p in d.glob("*.schema.json")} if d.is_dir() else set()
    for n, body in files.items():
        if n not in have or (d / n).read_text() != body:
            bad.append(f"{n}: missing or out of date")
    bad += [f"{n}: not generated from any table" for n in sorted(have - set(files))]
    if bad:
        print("security-schemas: " + "; ".join(bad) + f" (regenerate: security-schemas.py --out {o.check})", file=sys.stderr)
        return 1
    print(f"security-schemas: {len(files)} schemas up to date")
    return 0


if __name__ == "__main__":
    sys.exit(main())
