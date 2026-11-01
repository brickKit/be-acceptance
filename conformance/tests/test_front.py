"""Front doors for replicated services (cells/front.py, suite E)."""
import pytest

from cells import front
from cells.spec import Cell


def cell(tmp, row="D", replicas="  be/go-core@3.0.42: 2\n  erp/inventory@3.0.11: 2\n"):
    (tmp / "cell.yaml").write_text(f"cell: E\nrow: {row}\nshells:\n  be/go-core@3.0.42: [erp/sales, erp/credit]\n"
                                   f"alone: [erp/inventory@3.0.11, mdm/org@3.0.11]\nchain: []\nreplicas:\n{replicas}")
    (tmp / "brickkit.yaml").write_text("components:\n  - {id: be/go-core, version: 3.0.42}\n"
                                       "  - {id: erp/sales, version: 3.0.17}\n  - {id: erp/credit, version: 3.0.10}\n"
                                       "  - {id: erp/inventory, version: 3.0.11}\n  - {id: mdm/org, version: 3.0.11}\n")
    return Cell.load(tmp)


def test_a_replicated_shell_gets_a_door_for_itself_and_each_member(tmp_path):
    doors = {d[0]: d[1:] for d in front.doors(cell(tmp_path))}
    assert doors["front-e-be-go-core"] == ("be-go-core-3-0-42", 8090, 18090)
    assert doors["front-e-erp-sales"][0] == "erp-sales-3-0-17"  # a member: the shell's network alias
    assert doors["front-e-erp-credit"][0] == "erp-credit-3-0-10"
    assert doors["front-e-erp-inventory"][0] == "erp-inventory-3-0-11"
    assert "front-e-mdm-org" not in doors  # one replica: its own host port


def test_kubernetes_and_a_single_replica_need_no_door(tmp_path):
    assert front.doors(cell(tmp_path, row="K")) == []
    assert front.doors(cell(tmp_path, replicas="  erp/inventory@3.0.11: 1\n")) == []


def test_kubernetes_has_no_door_to_remove(tmp_path, monkeypatch):
    """`cells front down` runs for every row when a cell goes down; on Kubernetes there is no engine command to ask."""
    def refuse(*a, **k):
        raise AssertionError(f"ran {a[0]}")
    monkeypatch.setattr(front.subprocess, "run", refuse)
    front.down(cell(tmp_path, row="K"))


def test_the_config_balances_each_request_and_follows_the_dns():
    cfg = front.config("erp-sales-3-0-17", 8088)
    assert "mode http" in cfg and "balance roundrobin" in cfg and "parse-resolv-conf" in cfg
    assert "server-template r 1-8 erp-sales-3-0-17:8088" in cfg and "bind :8088" in cfg


def test_replicas_must_name_a_shell_or_a_component_alone(tmp_path):
    with pytest.raises(ValueError, match="replicas"):
        cell(tmp_path, replicas="  erp/sales@3.0.17: 2\n")  # a member: the shell is what has replicas
