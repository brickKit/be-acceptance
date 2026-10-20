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
