"""The third-party reader of the cold tier (tierread.py; foundations item 5, T11b) against be-protocol's vector
tier-digest (vectors/tier-digest.json, copied from be-protocol 3.0.0-alpha.112; P15.8, P15.9): the canonical rows, the
refusals, the canonical keys, a table's hash and the unit's digest, from the protocol's text and not from the SDK."""
import json
import pathlib

import pytest

import tierread

VECTOR = json.loads((pathlib.Path(__file__).parent.parent / "vectors" / "tier-digest.json").read_text())


def source(columns: dict[str, str], row: dict) -> dict:
    """The vector's input row as a reader gets it from Parquet: bytea as bytes (the vector writes it in hex)."""
    return {k: bytes.fromhex(v) if columns.get(k) == "bytea" and v is not None else v for k, v in row.items()}


@pytest.mark.parametrize("case", VECTOR["cases"], ids=lambda c: c["id"])
def test_the_reader_answers_the_vector(case):
    i, want = case["input"], case["expected"]
    if "refused" in want:
        with pytest.raises(ValueError):
            tierread.canonical_row(i["columns"], source(i["columns"], i["row"]))
    elif "canonical_row" in want:
        assert tierread.canonical_row(i["columns"], source(i["columns"], i["row"])).decode() == want["canonical_row"]
    elif "canonical_pk" in want:
        assert tierread.canonical_pk(i["columns"], i["pk"], source(i["columns"], i["row"])).decode() == want["canonical_pk"]
    elif "sha256" in want:
        assert tierread.table_sum(i["columns"], i["pk"], [source(i["columns"], r) for r in i["rows"]])[1] == want["sha256"]
    else:
        sums = []
        for t in i["tables"]:
            n, h = tierread.table_sum(t["columns"], t["pk"], [source(t["columns"], r) for r in t["rows"]])
            sums.append((t["name"], n, h))
        assert tierread.unit_digest(i["class"], i["schema_version"], sums) == want["digest"]


def test_every_case_was_answered():
    """Every case is one of the kinds above (a new kind in the vector fails here, not silently)."""
    kinds = {k for c in VECTOR["cases"] for k in c["expected"]}
    assert kinds <= {"refused", "canonical_row", "canonical_pk", "sha256", "digest"}, kinds
