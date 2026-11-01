"""The cell's deploy file (cells/create.py)."""
import yaml

from cells.create import deploy_file


def test_a_version_kept_for_a_dependent_has_its_own_entry():
    # brickkit.yaml keeps mdm/org@3.0.1 for mdm/partner (requiredBy): the deploy file needs an entry for it as well,
    # or brickkit build refuses the project (DEPLOY_INCONSISTENT); it runs alone and is not exposed
    out = yaml.safe_load(deploy_file("X", {}, ["mdm/org", "mdm/partner"], kept=["mdm/org@3.0.1"]))
    ids = [c["id"] for c in out["components"]]
    assert ids == ["mdm/org", "mdm/partner", "mdm/org@3.0.1"]
    assert "expose" not in out["components"][-1]


def test_no_kept_version_no_extra_entry():
    out = yaml.safe_load(deploy_file("X", {}, ["mdm/org"]))
    assert [c["id"] for c in out["components"]] == ["mdm/org"]


def test_ld_and_lp_run_the_named_components_on_this_machine():
    """LD and LP: the named shell or components run as processes on this machine (mode: local, their own port), the
    rest in the row's engine with its host port; a local shell's members are in its process (step 9, LD and LP)."""
    out = yaml.safe_load(deploy_file("X", {}, ["mdm/org", "erp/sales"], row="LD", local=frozenset({"erp/sales"})))
    assert out["target"] == "docker"
    by_id = {c["id"]: c for c in out["components"]}
    assert by_id["erp/sales"] == {"id": "erp/sales", "mode": "local"}
    assert by_id["mdm/org"]["expose"] is True and by_id["mdm/org"]["exposePort"] > 10000
    out = yaml.safe_load(deploy_file("X", {"be/go-core": ["erp/sales"]}, ["mdm/org"], row="LP",
                                     local=frozenset({"be/go-core"})))
    assert out["target"] == "podman"
    shell = next(c for c in out["components"] if c["id"] == "be/go-core")
    assert shell["mode"] == "local" and shell["members"] == [{"id": "erp/sales"}]


def test_replicas_map_no_host_port_on_docker_and_keep_the_node_port_on_kubernetes():
    """Suite E: on Docker and Podman a replicated shell or component, and a replicated shell's members, map no host
    port (BrickKit refuses one for several replicas; cells/front.py takes it); on Kubernetes only the count is written."""
    out = yaml.safe_load(deploy_file("X", {"be/go-core": ["erp/sales"]}, ["mdm/org", "erp/inventory"],
                                     replicas={"be/go-core": 2, "erp/inventory": 2}))
    by_id = {c["id"]: c for c in out["components"]}
    assert by_id["erp/inventory"] == {"id": "erp/inventory", "replicas": 2}
    assert by_id["be/go-core"] == {"id": "be/go-core", "replicas": 2, "members": [{"id": "erp/sales"}]}
    assert by_id["mdm/org"]["expose"] is True
    k = yaml.safe_load(deploy_file("X", {}, ["mdm/org", "erp/inventory"], row="K", replicas={"erp/inventory": 2}))
    assert {c["id"]: c for c in k["components"]}["erp/inventory"] == {"id": "erp/inventory", "replicas": 2}
