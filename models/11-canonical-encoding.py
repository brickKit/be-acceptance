#!/usr/bin/env python3
"""Reference check for the 9th security candidate: canonical encoding and digests.

Encoding rule under test:
  1. normalise the value: sets sorted and de-duplicated, decimals in canonical text form,
     instants as UTC text with fixed microsecond precision, absent optional fields omitted,
     explicit presence states tagged ({"state": "null"} etc.), numbers only as integers
  2. serialise with RFC 8785 (JSON Canonicalization Scheme); with integer-only numbers and
     ASCII object keys this is: sorted keys, no insignificant whitespace, minimal escaping
  3. digest = SHA-256( "be." + type + ".v" + major + "\\n" + canonical bytes )

Checks:
  SAME       semantically equal values (field order, set order, decimal spelling, timezone
             spelling of the same instant) give the same digest
  DISTINCT   randomly generated distinct values never share canonical bytes, including strings
             with separators, quotes, backslashes, control and non-BMP characters, and
             null vs "" vs absent
  TYPED      the same JSON under two record types gives different digests
  F04        the two counterexamples of the old cold-format digest (unescaped separator,
             null marker) are distinguished
  FINGERPRINT plain concatenation of path + query + body collides; a canonical array does not

Bad variants (each must make at least one check fail, showing the checks are not vacuous):
  unsorted_sets    sets keep their input order (de-duplicated only)
  raw_decimal      decimals keep their input spelling (100.00 vs 1E2)
  keep_offset      instants keep their original UTC offset
  no_type_prefix   the digest omits the "be.<type>.v<major>" prefix
  unsorted_keys    object keys are serialised in insertion order
"""
import hashlib
import json
import random
import sys
from datetime import datetime, timezone, timedelta
from decimal import Decimal

SEED = 20261013
BAD = set()
BAD_VARIANTS = ["unsorted_sets", "raw_decimal", "keep_offset", "no_type_prefix", "unsorted_keys"]


def canon_decimal(x):
    if "raw_decimal" in BAD:
        return str(x)
    d = Decimal(str(x))
    if d == 0:
        return "0"
    t = format(d.normalize(), "f")
    if "." in t:
        t = t.rstrip("0").rstrip(".")
    return t


def canon_instant(dt):
    if "keep_offset" in BAD:
        return dt.isoformat()
    return dt.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%fZ")


def canonical_bytes(value):
    # json.dumps with sort_keys and compact separators equals RFC 8785 output for values made of
    # strings, booleans, None-free objects/arrays, integers within 2^53 and ASCII keys.
    def check(v):
        if isinstance(v, bool) or v is None:
            assert v is not None, "use tagged presence states, not JSON null"
        elif isinstance(v, int):
            assert abs(v) < 2 ** 53
        elif isinstance(v, float):
            raise AssertionError("floats are not allowed")
        elif isinstance(v, dict):
            for k, x in v.items():
                assert k.isascii(); check(x)
        elif isinstance(v, list):
            for x in v: check(x)
    check(value)
    return json.dumps(value, sort_keys="unsorted_keys" not in BAD, separators=(",", ":"), ensure_ascii=False).encode("utf-8")


def digest(rtype, major, value):
    if "no_type_prefix" in BAD:
        return hashlib.sha256(canonical_bytes(value)).hexdigest()
    return hashlib.sha256(f"be.{rtype}.v{major}\n".encode() + canonical_bytes(value)).hexdigest()


def norm_segment(seg):
    """A grant segment: sets sorted, decimals canonical, optional fields omitted when absent."""
    out = {"operation": seg["operation"], "resource_type": seg["resource_type"], "atoms": {}}
    for dim, atom in seg["atoms"].items():
        if atom == "ALL":
            out["atoms"][dim] = {"kind": "all"}
        elif atom[0] == "in":
            vals = list(dict.fromkeys(atom[1])) if "unsorted_sets" in BAD else sorted(set(atom[1]))
            out["atoms"][dim] = {"kind": "in", "values": vals}
        elif atom[0] == "interval":
            _, cur, lo, lc, hi, hc = atom
            a = {"kind": "interval", "currency": cur}
            if lo is not None:
                a["lo"] = canon_decimal(lo); a["lo_closed"] = lc
            if hi is not None:
                a["hi"] = canon_decimal(hi); a["hi_closed"] = hc
            out["atoms"][dim] = a
    rf = seg["read_fields"]
    out["read_fields"] = list(dict.fromkeys(rf)) if "unsorted_sets" in BAD else sorted(set(rf))
    out["valid_from"] = canon_instant(seg["valid_from"])
    if seg.get("valid_until") is not None:
        out["valid_until"] = canon_instant(seg["valid_until"])
    return out


TRICKY = ["", "a", "a|b", "b|c", "|", "\\N", "\\", "\"", "a\"b", "\n", "\u0000", "é", "𝄞", "a,b", "{}", "[]",
          "null", " ", "a b", "\t", "/", "//", "a/b"]


def rand_value(rng, depth=0):
    r = rng.random()
    if depth > 2 or r < 0.35:
        return rng.choice(TRICKY)
    if r < 0.45:
        return rng.randint(-5, 5)
    if r < 0.5:
        return rng.choice([True, False])
    if r < 0.6:
        return {"state": rng.choice(["null", "unobtained", "unsupported"])}
    if r < 0.8:
        return [rand_value(rng, depth + 1) for _ in range(rng.randint(0, 3))]
    keys = rng.sample(["a", "b", "c", "id", "x"], rng.randint(0, 3))
    return {k: rand_value(rng, depth + 1) for k in keys}


def run_checks():
    rng = random.Random(SEED)
    fails = []

    # SAME
    base = dict(operation="sales.order.approve", resource_type="sales.order",
                atoms={"legal": ("in", ["L2", "L1", "L1"]), "amount": ("interval", "CNY", None, True, "100.00", True),
                       "org": "ALL"},
                read_fields=["price", "name", "price"],
                valid_from=datetime(2026, 10, 13, 8, 0, tzinfo=timezone(timedelta(hours=8))))
    variant = dict(resource_type="sales.order", operation="sales.order.approve",
                   atoms={"org": "ALL", "amount": ("interval", "CNY", None, True, Decimal("1E2"), True),
                          "legal": ("in", ["L1", "L2"])},
                   read_fields=["name", "price"],
                   valid_from=datetime(2026, 10, 13, 0, 0, tzinfo=timezone.utc))
    if digest("grant_segment", 1, norm_segment(base)) != digest("grant_segment", 1, norm_segment(variant)):
        fails.append("SAME")

    # DISTINCT
    seen = {}
    n = 0
    for _ in range(60000):
        v = rand_value(rng)
        b = canonical_bytes(v)
        key = json.dumps(v, sort_keys=True)  # structural identity of the generated value
        if b in seen and seen[b] != key:
            fails.append(("DISTINCT", seen[b], key)); break
        seen[b] = key
        n += 1

    # TYPED
    v = {"id": "x"}
    if digest("grant", 1, v) == digest("share", 1, v) or digest("grant", 1, v) == digest("grant", 2, v):
        fails.append("TYPED")

    # F04: old rule joined fields with "|" and wrote null as "\N"
    old = lambda fields: "|".join("\\N" if f is None else f for f in fields).encode()
    pairs = [(["a|b", "c"], ["a", "b|c"]), ([None], ["\\N"])]
    old_collides = all(old(x) == old(y) for x, y in pairs)
    to_canon = lambda fs: [{"state": "null"} if f is None else f for f in fs]
    new_distinct = all(canonical_bytes(to_canon(x)) != canonical_bytes(to_canon(y)) for x, y in pairs)
    if not (old_collides and new_distinct):
        fails.append("F04")

    # FINGERPRINT: path + query + body concatenation vs canonical array
    concat = lambda p, q, b: (p + q + b).encode()
    a, b_ = ("/a", "b", "{}"), ("/ab", "", "{}")
    fp_collides = concat(*a) == concat(*b_)
    fp_fixed = canonical_bytes(list(a)) != canonical_bytes(list(b_))
    if not (fp_collides and fp_fixed):
        fails.append("FINGERPRINT")

    return fails, n, len(seen)


def main():
    fails, n, distinct = run_checks()
    print(f"SAME: {'FAIL' if 'SAME' in fails else 'ok'} (field order, set order, decimal 100.00 vs 1E2, +08:00 vs Z)")
    print(f"DISTINCT: {'FAIL' if any(isinstance(f, tuple) for f in fails) else 'ok'} ({n} random values, {distinct} distinct encodings)")
    print(f"TYPED: {'FAIL' if 'TYPED' in fails else 'ok'}")
    print(f"F04: old rule collides on both pairs; canonical encoding distinguishes them: {'ok' if 'F04' not in fails else 'FAIL'}")
    print(f"FINGERPRINT: '/a'+'b'+'{{}}' == '/ab'+''+'{{}}' under concatenation; canonical array differs: {'ok' if 'FINGERPRINT' not in fails else 'FAIL'}")
    if fails:
        print("FAIL", fails[:3], file=sys.stderr)
        sys.exit(1)
    caught = []
    for bad in BAD_VARIANTS:
        BAD.clear(); BAD.add(bad)
        try:
            bad_fails = run_checks()[0]
        except AssertionError as e:
            bad_fails = [f"assertion: {e}"]
        names = sorted({f if isinstance(f, str) else f[0] for f in bad_fails})
        print(f"bad variant {bad}: {'detected by ' + ', '.join(names) if bad_fails else 'NOT DETECTED'}")
        if bad_fails:
            caught.append(bad)
    BAD.clear()
    if len(caught) != len(BAD_VARIANTS):
        print("FAIL: some bad variants were not detected", file=sys.stderr)
        sys.exit(1)
    print(f"all {len(BAD_VARIANTS)} bad variants detected")


if __name__ == "__main__":
    main()
