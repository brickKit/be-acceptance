"""The Kubernetes row with the base resources in the cluster (step 10, B7): the cell reaches them by a Service name,
and the sampler reads their pods as base resources, apart from the cell's own pods."""
import pytest

from cells import units
from cells.spec import Cell


def cell(tmp_path, row: str, extra: str = "") -> Cell:
    (tmp_path / "cell.yaml").write_text(f"cell: T\nrow: {row}\nalone: [test/probe-go@3.2.21]\nchain: []\n{extra}")
    return Cell.load(tmp_path)


def test_the_base_resources_in_the_cluster_are_one_service_away(tmp_path):
    assert cell(tmp_path, "K").base_host == "host.minikube.internal"
    c = cell(tmp_path, "K", "base: cluster\n")
    assert c.base == "cluster" and c.base_host == "base.be-base.svc.cluster.local"


def test_only_the_kubernetes_row_has_its_base_resources_in_the_cluster(tmp_path):
    with pytest.raises(ValueError, match="base"):
        cell(tmp_path, "D", "base: cluster\n")


def pod(app: str, uid: str, phase: str = "Running") -> dict:
    return {"metadata": {"name": f"{app}-7d9f-abcde", "uid": uid, "labels": {"app": app}}, "status": {"phase": phase}}


def test_pods_become_units_under_a_prefix(tmp_path):
    node = tmp_path / "node"
    for uid in ("1111-aa", "2222-bb"):
        (node / "kubepods.slice" / "kubepods-burstable.slice" / f"kubepods-burstable-pod{uid.replace('-', '_')}.slice").mkdir(parents=True)
    got = units.pod_units(node, [pod("be-postgres", "1111-aa"), pod("be-nats", "2222-bb"), pod("gone", "3333-cc", "Pending")],
                          "base")
    assert sorted(got) == ["base:be-nats", "base:be-postgres"]
    assert got["base:be-postgres"].path.name == "kubepods-burstable-pod1111_aa.slice"


class Kube:
    """kubectl as nodeports calls it: get svc answers with these Services; apply records each manifest."""
    def __init__(self, services: list[dict]):
        self.services, self.applied = services, []

    def __call__(self, args, input=None, **_):
        import json
        import subprocess
        if "get" in args:
            return subprocess.CompletedProcess(args, 0, json.dumps({"items": self.services}), "")
        self.applied.append(json.loads(input)["metadata"]["name"])
        return subprocess.CompletedProcess(args, 0, "", "")


def service(name: str, port: int) -> dict:
    return {"metadata": {"name": name}, "spec": {"selector": {"app": name}, "ports": [{"port": port}]}}


def test_a_shell_whose_members_moved_out_gets_no_node_port(tmp_path, monkeypatch):
    # R5 on an H cell: be/go-core's members run alone, the shell has left the project; cell.yaml still names it
    (tmp_path / "cell.yaml").write_text("cell: T\nrow: K\nshells:\n  be/go-core@3.0.36: [erp/inventory, erp/credit]\nchain: []\n")
    c = Cell.load(tmp_path)
    kube = Kube([service("erp-inventory-3-0-8", units.PORTS["erp/inventory"][0]),
                 service("erp-credit-3-0-8", units.PORTS["erp/credit"][0])])
    monkeypatch.setattr(units.subprocess, "run", kube)
    units.nodeports(c)
    assert kube.applied == ["np-erp-inventory", "np-erp-credit"]


def test_a_component_without_a_service_still_fails(tmp_path, monkeypatch):
    (tmp_path / "cell.yaml").write_text("cell: T\nrow: K\nshells:\n  be/go-core@3.0.36: [erp/inventory, erp/credit]\nchain: []\n")
    monkeypatch.setattr(units.subprocess, "run", Kube([service("erp-inventory-3-0-8", units.PORTS["erp/inventory"][0])]))
    with pytest.raises(RuntimeError, match="erp/credit"):
        units.nodeports(Cell.load(tmp_path))
