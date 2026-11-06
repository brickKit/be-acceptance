"""The tiering profile (foundations item 5, T10e; be-protocol chapter 15; the assembly project's protocol 11) against
the Go probe's archive: the SDK's own jobs seal an archive closed past its residence, copy it to the class's route and
publish it; read by its key it answers what it answered online, every level and every column type; an open archive
stays online. go picks the object store (TIERING_ROUTE_PROBE_ARCHIVE), go2 keeps the warm route. The release waits for
a backup record in the run profile, which this profile does not keep: the online rows stay (the cells release). The
Python probe's archive comes with be-sdk-python's chapter 15 (foundations item 6)."""
import asyncio

from harness import Deployment, ensure_bucket, fresh_bus, need

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


async def test_the_go_probe_tiers_its_archive(specs, world, tmp_path):
    go, go2 = specs["go"], specs["go2"]
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
            assert (await read(p, still_open))["where"] == "online", "an open archive stays online"
    finally:
        await d.stop()
