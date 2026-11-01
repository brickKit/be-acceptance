"""Suite D V1 on a running cell where two versions of one probe run side by side on one schema and one stream
(BE_ACC_CELL; cell.yaml's also names the older one): the rolling upgrade's window. V1_PHASE=expand: the default
version made only an expand migration, so both write and the newer reads the older one's rows. V1_PHASE=contract: the
default version has since been replaced by one with a contract migration while the older one still runs."""
from __future__ import annotations

import os
import pathlib
import subprocess
import sys
import time

import httpx
import pytest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))

from cells.spec import Cell  # noqa: E402

PHASE = os.environ.get("V1_PHASE", "expand")


@pytest.fixture(scope="module")
def cell() -> Cell:
    d = os.environ.get("BE_ACC_CELL", "")
    if not d:
        raise AssertionError("BE_ACC_CELL is not set: these tests run against a running cell")
    c = Cell.load(d)
    if len(c.also) != 1:
        pytest.skip("V1 needs one older version beside the default one (cell.yaml: also)")
    return c


def ports(c: Cell) -> tuple[int, int, str]:
    """(default version's port, older version's port, the consumer of their stream)."""
    ref = c.also[0]
    cid = ref.split("@")[0]
    return c.host_port(cid), c.also_port(ref), c.next_of(cid)


def write(port: int, body: dict) -> httpx.Response:
    return httpx.post(f"http://127.0.0.1:{port}/api/probe/write", json=body, timeout=30)


def read(port: int, rid: str) -> httpx.Response:
    return httpx.get(f"http://127.0.0.1:{port}/api/probe/written/{rid}", timeout=30)


def start_migration_again(c: Cell, ref: str) -> subprocess.CompletedProcess:
    """Runs the migration container of an older version (id@version) again, as a restart does: the D row's Docker, the P
    row's Podman (the same container names). The K row runs migrations as Jobs, which a second brickkit up creates
    again; the runner checks those (v1row.sh), so the test is skipped there."""
    if c.target == "k8s":
        pytest.skip("K row: brickkit up runs every version's migration Job again; v1row.sh checks they completed")
    cid, version = ref.split("@")
    name = f"brickkit-{c.project}-{cid.replace('/', '-')}-{version.replace('.', '-')}-migration-1"
    return subprocess.run([c.target, "start", "-a", name], capture_output=True, text=True, timeout=120)


def seen(c: Cell, component: str) -> int:
    return httpx.get(f"http://127.0.0.1:{c.host_port(component)}/api/probe/seen", timeout=30).json()["count"]


@pytest.mark.skipif(PHASE != "expand", reason="V1_PHASE=expand")
def test_v1_both_versions_write_and_the_newer_reads_the_older_rows(cell):
    new, old, consumer = ports(cell)
    before = seen(cell, consumer)
    a = write(old, {"bytes": 5})
    assert a.status_code == 201, a.text
    b = write(new, {"bytes": 6, "label": "new"})
    assert b.status_code == 201, b.text
    c = write(old, {"bytes": 7})
    assert c.status_code == 201, "the older version still writes after the newer one used its column: " + c.text
    assert read(new, a.json()["id"]).json() == {"id": a.json()["id"], "bytes": 5, "label": None}
    assert read(new, b.json()["id"]).json() == {"id": b.json()["id"], "bytes": 6, "label": "new"}
    end = time.monotonic() + 30
    while seen(cell, consumer) - before < 3 and time.monotonic() < end:
        time.sleep(0.2)
    assert seen(cell, consumer) - before == 3, "the consumer took both versions' events"


@pytest.mark.skipif(PHASE != "contract", reason="V1_PHASE=contract")
def test_v1_after_a_contract_migration_the_older_writer_is_refused(cell):
    """ST10: after a breaking migration the older writer is refused. The step 12 plan (F1) expects this to fail today:
    nothing refuses the older version; its insert fails on the renamed column instead."""
    new, old, _ = ports(cell)
    n = write(new, {"bytes": 8, "label": "after"})
    assert n.status_code == 201, n.text
    assert read(new, n.json()["id"]).json()["bytes"] == 8
    o = write(old, {"bytes": 9})
    print("the older version's write after the contract migration:", o.status_code, o.text)
    assert o.status_code != 201, "the older version wrote into a schema it does not know"
    assert o.json()["reason"] not in ("INTERNAL",), "refused as a version too old for the schema, not as a crash"


@pytest.mark.skipif(PHASE != "again", reason="V1_PHASE=again")
def test_v1_the_older_version_migrates_again_on_the_newer_schema(cell):
    """P5.32: on an SDK that has it, the older version's migration, run again (as every deployment run and a restart
    do), takes the schema the newer version migrated as done."""
    r = start_migration_again(cell, cell.also[0])
    print("the older version's migration, run again:", r.returncode, (r.stdout + r.stderr)[-1500:])
    assert r.returncode == 0, "the older version's migration refused a schema a newer version migrated"


@pytest.mark.skipif(PHASE != "contract", reason="V1_PHASE=contract")
def test_v1_the_older_version_does_not_start_again_on_the_newer_schema(cell):
    """Its migration, run again (as a restart would), meets a database migrated past the files it knows."""
    r = start_migration_again(cell, cell.also[0])
    print("the older version's migration, run again:", r.returncode, (r.stdout + r.stderr)[-1500:])
    assert r.returncode != 0, "the older version's migration accepted a schema migrated past it"
