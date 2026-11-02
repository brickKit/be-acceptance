"""Suite E3's fault helpers with replicas (cells/faults.py): which container is a replica, when every replica is
healthy, and stopping one."""
import subprocess

import pytest

from cells import faults
from cells.spec import Cell


def cell(tmp, row="D"):
    (tmp / "cell.yaml").write_text(f"cell: E\nrow: {row}\nalone: [erp/sales@3.0.18, mdm/org@3.0.12]\nchain: []\n"
                                   "replicas:\n  erp/sales@3.0.18: 2\n")
    return Cell.load(tmp)


def test_the_replicas_are_the_numbered_containers(tmp_path):
    assert faults.replica(cell(tmp_path), "erp/sales", 2) == "brickkit-e-erp-sales-3-0-18-2"


def test_healthy_means_every_replica(tmp_path, monkeypatch):
    asked = []

    def inspect(cmd, **_):
        asked.append(cmd)
        return subprocess.CompletedProcess(cmd, 0, stdout="healthy\nstarting\n" if len(asked) == 1 else "healthy\nhealthy\n")
    monkeypatch.setattr(faults.subprocess, "run", inspect)
    c = cell(tmp_path)
    assert not faults.healthy(c, "erp/sales") and faults.healthy(c, "erp/sales")
    assert asked[0][-2:] == ["brickkit-e-erp-sales-3-0-18-1", "brickkit-e-erp-sales-3-0-18-2"]


def test_kubernetes_is_ready_with_every_replica(tmp_path, monkeypatch):
    monkeypatch.setattr(faults.subprocess, "run", lambda cmd, **_: subprocess.CompletedProcess(cmd, 0, stdout="1"))
    assert not faults.healthy(cell(tmp_path, "K"), "erp/sales"), "one of two Pods ready"


def test_stopping_a_replica_needs_two(tmp_path, monkeypatch):
    stopped = []
    monkeypatch.setattr(faults, "_run", lambda cmd: stopped.append(cmd) or "")
    c = cell(tmp_path)
    assert faults.stop_replica(c, "erp/sales") == "brickkit-e-erp-sales-3-0-18-2"
    assert stopped == [["docker", "stop", "brickkit-e-erp-sales-3-0-18-2"]]
    with pytest.raises(ValueError, match="one replica"):
        faults.stop_replica(c, "mdm/org")


def test_on_kubernetes_a_running_replica_pod_is_deleted_not_the_migration_job(tmp_path, monkeypatch):
    """The Deployment's pods share their name prefix with the migration Job's pod (erp-sales-3-0-18-migration-...):
    step 13's K13-E1 deleted that one, and no replica stopped."""
    ran = []

    def run(cmd):
        ran.append(cmd)
        if "get" in cmd:
            return ("erp-sales-3-0-18-7c569b777f-5ltcc=Running erp-sales-3-0-18-7c569b777f-bkbtb=Running "
                    "erp-sales-3-0-18-migration-mjlbr=Succeeded mdm-org-3-0-13-6b9c8d7f5d-x2x2x=Running")
        return ""
    monkeypatch.setattr(faults, "_run", run)
    gone = faults.stop_replica(cell(tmp_path, "K"), "erp/sales")
    assert gone == "pod/erp-sales-3-0-18-7c569b777f-bkbtb", gone
    assert ran[-1][-3:] == ["delete", "pod/erp-sales-3-0-18-7c569b777f-bkbtb", "--wait=false"]
