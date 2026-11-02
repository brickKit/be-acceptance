"""Suite E3's fault helpers with replicas (cells/faults.py): which container is a replica, when every replica is
healthy, and stopping one (which returns only once it has taken effect)."""
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
        return subprocess.CompletedProcess(cmd, 0, stdout="true/healthy\ntrue/starting\n" if len(asked) == 1 else "true/healthy\ntrue/healthy\n")
    monkeypatch.setattr(faults.subprocess, "run", inspect)
    c = cell(tmp_path)
    assert not faults.healthy(c, "erp/sales") and faults.healthy(c, "erp/sales")
    assert asked[0][-2:] == ["brickkit-e-erp-sales-3-0-18-1", "brickkit-e-erp-sales-3-0-18-2"]


def test_kubernetes_is_ready_with_every_replica(tmp_path, monkeypatch):
    monkeypatch.setattr(faults.subprocess, "run", lambda cmd, **_: subprocess.CompletedProcess(cmd, 0, stdout="1"))
    assert not faults.healthy(cell(tmp_path, "K"), "erp/sales"), "one of two Pods ready"


def test_a_stopped_container_is_not_ready_whatever_its_last_health(tmp_path, monkeypatch):
    monkeypatch.setattr(faults.subprocess, "run",
                        lambda cmd, **_: subprocess.CompletedProcess(cmd, 0, stdout="true/healthy\nfalse/healthy\n"))
    c = cell(tmp_path)
    assert faults.ready(c, "erp/sales") == 1 and not faults.healthy(c, "erp/sales")


def ready_after(answers: list[str]):
    """A fake `docker inspect` or `kubectl get deploy` answering these in turn, then the last one for good."""
    def run(cmd, **_):
        return subprocess.CompletedProcess(cmd, 0, stdout=answers.pop(0) if len(answers) > 1 else answers[0])
    return run


def test_stopping_a_replica_needs_two(tmp_path, monkeypatch):
    stopped = []
    monkeypatch.setattr(faults, "_run", lambda cmd: stopped.append(cmd) or "")
    monkeypatch.setattr(faults.subprocess, "run", ready_after(["true/healthy\nfalse/healthy\n"]))
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
    monkeypatch.setattr(faults.subprocess, "run", ready_after(["2", "2", "1"]))
    gone = faults.stop_replica(cell(tmp_path, "K"), "erp/sales")
    assert gone == "pod/erp-sales-3-0-18-7c569b777f-bkbtb", gone
    assert ran[-1][-3:] == ["delete", "pod/erp-sales-3-0-18-7c569b777f-bkbtb", "--wait=false"]


def test_a_stop_that_stops_no_replica_fails(tmp_path, monkeypatch):
    """Step 13's K13-E1 again: the delete hits something that is not a replica (then the migration Job's pod), the
    Deployment keeps both replicas ready, and the stop must not return as if one had gone (E3 passed then without
    testing anything)."""
    monkeypatch.setattr(faults, "_run", lambda cmd: "erp-sales-3-0-18-7c569b777f-5ltcc=Running" if "get" in cmd else "")
    monkeypatch.setattr(faults.subprocess, "run", ready_after(["2"]))
    monkeypatch.setattr(faults.time, "sleep", lambda _: None)
    clock = iter(range(0, 1000, 5))
    monkeypatch.setattr(faults.time, "monotonic", lambda: next(clock))
    with pytest.raises(RuntimeError, match="still ready"):
        faults.stop_replica(cell(tmp_path, "K"), "erp/sales")
