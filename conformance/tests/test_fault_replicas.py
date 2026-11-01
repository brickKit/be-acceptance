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
