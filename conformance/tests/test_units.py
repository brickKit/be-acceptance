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
