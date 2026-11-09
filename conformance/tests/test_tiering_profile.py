"""The tiering profile (foundations item 5, T10e; be-protocol chapter 15; the assembly project's protocol 11) against
either probe's archive: the SDK's own jobs seal an archive closed past its residence, copy it to the class's route and
publish it; read by its key it answers what it answered online, every level and every column type; an open archive
stays online. go picks the object store (TIERING_ROUTE_PROBE_ARCHIVE), go2 keeps the warm route. A third-party reader
(tierread.py: pyarrow and the protocol's text, not the SDK; since 1.0.63) computes the same digest the catalog sealed
from the manifest and the Parquet objects, or the warm rows, and reads the same rows. The release waits for a backup
record in the run profile, which this profile does not keep: the online rows stay (the cells release). The Python
probe's archive the same, since 1.0.73 (py writes Parquet with pyarrow, the reader reads it with pyarrow too: the
reader's independence from the writer is the Go probe's half)."""
import asyncio
import json
from urllib.parse import urlsplit

import aioboto3
import asyncpg

import tierread
from harness import DATABASE, Deployment, ensure_bucket, fresh_bus, need

BUCKET = "be-tiering"


async def archive(p, closed_days_ago: int) -> str:
    r = await p.post("/api/probe/archive", {"batch": "b1", "closed_days_ago": closed_days_ago, "parts": 3})
    assert r.status_code == 200, r.text
    return r.json()["id"]


async def read(p, id_: str, want: str | None = None, within: float = 240.0) -> dict:
    got = {}
    for _ in range(int(within / 1.0)):
        r = await p.get(f"/api/probe/archive/{id_}")
        assert r.status_code == 200, r.text
        got = r.json()
        if want is None or got["where"] == want:
            return got
        await asyncio.sleep(1.0)
    raise AssertionError(f"{p.spec.id} {id_}: still {got.get('where')}, not {want}")


async def third_party(schema: str, root: str) -> tuple[str, str, dict[str, list[dict]]]:
    """The catalog's digest of the unit holding root, and the third-party reader's digest and rows of its copy."""
    admin = urlsplit(need("BE_ACC_PG_ADMIN"))._replace(path="/" + DATABASE).geturl()
    c = await asyncpg.connect(admin, server_settings={"search_path": schema})
    try:
        unit, digest, version = await c.fetchrow("SELECT u.id, u.digest, u.manifest_version FROM be_tier_member m "
                                                 "JOIN be_tier_unit u ON u.id = m.unit WHERE m.root_id = $1", root)
        manifest = json.loads(await c.fetchval("SELECT manifest FROM be_tier_manifest WHERE unit = $1 AND version = $2",
                                               unit, version))
        if manifest["route"] == "postgres":
            rows: dict[str, list[bytes]] = {}
            for r in await c.fetch("SELECT tbl, row FROM be_tier_row WHERE unit = $1 AND version = $2", unit, version):
                rows.setdefault(r["tbl"], []).append(bytes(r["row"]))
            return digest, tierread.digest_from_warm(manifest, rows), {t: [json.loads(b) for b in rs] for t, rs in rows.items()}
    finally:
        await c.close()
    objects = {}
    async with aioboto3.Session().client("s3", endpoint_url=need("BE_ACC_S3_URL"), region_name="us-east-1",
                                         aws_access_key_id=need("BE_ACC_S3_ACCESS_KEY"),
                                         aws_secret_access_key=need("BE_ACC_S3_SECRET_KEY")) as s3:
        for t in manifest["tables"]:
            for o in t.get("objects") or []:
                got = await s3.get_object(Bucket=BUCKET, Key=o["key"])
                objects[o["key"]] = await got["Body"].read()
    third, tables = tierread.digest_from_objects(manifest, objects.__getitem__)
    types = {t["name"]: {col["name"]: col["type"] for col in t["columns"]} for t in manifest["tables"]}
    canonical = {name: [json.loads(tierread.canonical_row(types[name], r)) for r in rs] for name, rs in tables.items()}
    return digest, third, canonical


def by_text(rows: list[dict]) -> list[str]:
    return sorted(json.dumps(r, sort_keys=True) for r in rows)


async def test_the_go_probe_tiers_its_archive(specs, world, tmp_path):
    await tiers(specs["go"], specs["go2"], world, tmp_path)


async def test_the_python_probe_tiers_its_archive(specs, world, tmp_path):  # since 1.0.73 (be-sdk-python a60)
    await tiers(specs["py"], specs["py2"], world, tmp_path)


async def tiers(go, go2, world, tmp_path):
    """go picks the object store, go2 keeps the warm route (either language's pair of variants)."""
    d = Deployment(world, await fresh_bus("jetstream", [go.stream, go2.stream]), tmp_path)
    try:
        await ensure_bucket(BUCKET)
        due = {"TIERING_LIST_DAYS": "1", "TIERING_RESIDENCE_PROBE_ARCHIVE": "1", "TIERING_WINDOW": "* * * * *",
               "TIERING_GRACE": "0s"}
        store = {"TIERING_ROUTE_PROBE_ARCHIVE": "object-parquet", "TIERING_STORE_URL": need("BE_ACC_S3_URL"),
                 "TIERING_BUCKET": BUCKET}
        for role in ("WRITE", "READ", "DELETE"):
            store[f"TIERING_{role}_ACCESS_KEY"] = need("BE_ACC_S3_ACCESS_KEY")
            store[f"TIERING_{role}_SECRET_KEY"] = need("BE_ACC_S3_SECRET_KEY")
        cold = await d.start(go, await d.env(go, {**due, **store}))
        warm = await d.start(go2, await d.env(go2, due))
        for p, route in ((cold, "object-parquet"), (warm, "postgres")):
            closed, still_open = await archive(p, 30), await archive(p, -1)
            before = await read(p, closed)
            assert before["where"] == "online"
            assert len(before["children"]["probe_archive_part"]) == 3
            assert len(before["children"]["probe_archive_piece"]) == 6
            after = await read(p, closed, route)
            assert after["root"] == before["root"], "every column type as it was"
            assert after["children"] == before["children"], "every level as it was"
            sealed, third, rows = await third_party(p.spec.schema, closed)
            assert third == sealed, f"{route}: the third-party reader's digest is the sealed one (G3)"
            assert by_text(rows["probe_archive"]) == by_text([before["root"]]), f"{route}: the same root (G4)"
            for name, kids in before["children"].items():
                assert by_text(rows.get(name, [])) == by_text(kids), f"{route}: the same {name} (G4)"
            assert (await read(p, still_open))["where"] == "online", "an open archive stays online"
    finally:
        await d.stop()
