"""Suite E: a cell's `databases:` places components' schemas on another database or instance (cells/homes.py)."""
import pytest
import yaml

from cells import homes, prepare
from cells.spec import Cell

ADMIN = "postgres://postgres:admin-pw@localhost:5432/postgres?sslmode=disable"
SECURED = ["infra/iam-casdoor@3.0.13", "infra/authz@3.0.14", "infra/audit@3.0.11"]


def cell(tmp_path, extra: str, alone: list[str]) -> Cell:
    (tmp_path / "config").mkdir(exist_ok=True)
    (tmp_path / ".secrets").mkdir(exist_ok=True)
    (tmp_path / "cell.yaml").write_text(f"cell: T\nalone: [{', '.join(alone)}]\nchain: []\n{extra}")
    return Cell.load(tmp_path)


def test_a_component_lives_in_the_cells_database_unless_moved(tmp_path):
    c = cell(tmp_path, "databases:\n  infra/audit@3.0.11: be_experiment_audit\n  erp/sales: second/be_experiment\n",
             [*SECURED, "erp/sales@3.0.18"])
    assert c.home_of("infra/authz") == ("main", "be_experiment")
    assert c.home_of("infra/audit") == ("main", "be_experiment_audit")
    assert c.home_of("erp/sales") == ("second", "be_experiment")
    assert c.homes == [("main", "be_experiment"), ("main", "be_experiment_audit"), ("second", "be_experiment")]


@pytest.mark.parametrize("extra", [
    "databases:\n  erp/billing: x\n",  # not run by the cell
    "databases:\n  erp/sales: third/x\n",  # no such instance
    "databases:\n  erp/sales: second/\n",  # no database
    "pooler: pgbouncer\ndatabases:\n  erp/sales: x\n",  # the pooler serves the cell's database only
])
def test_a_home_the_cell_cannot_have_is_refused(tmp_path, extra):
    with pytest.raises(ValueError, match="databases"):
        cell(tmp_path, extra, ["erp/sales@3.0.18"])


def test_a_moved_component_is_configured_with_its_own_home(tmp_path, monkeypatch):
    monkeypatch.setenv("BE_ACC_PG_ADMIN", ADMIN)
    c = cell(tmp_path, "databases:\n  infra/audit: be_experiment_audit\n  erp/sales: second/be_experiment\n",
             [*SECURED, "erp/sales@3.0.18", "erp/credit@3.0.11"])
    prepare.configs(c)

    def conf(f):
        return yaml.safe_load((tmp_path / "config" / f"{f}.yaml").read_text())

    assert {k: conf("erp-sales")[k] for k in ("PG_HOST", "PG_PORT", "PG_DATABASE")} == {
        "PG_HOST": c.base_host, "PG_PORT": "5433", "PG_DATABASE": "be_experiment"}
    assert {k: conf("infra-audit")[k] for k in ("PG_HOST", "PG_PORT", "PG_DATABASE")} == {
        "PG_HOST": c.base_host, "PG_PORT": "5432", "PG_DATABASE": "be_experiment_audit"}
    assert conf("erp-credit")["PG_DATABASE"] == "$var:PG_DATABASE" and conf("erp-credit")["PG_HOST"] == "$var:PG_HOST"
    assert conf("erp-sales")["PG_SCHEMA"] == "erp_sales", "the schema keeps its name wherever it lives"


def test_the_checks_see_moved_schemas_as_foreign_tables_of_the_same_name(tmp_path):
    c = cell(tmp_path, "databases:\n  infra/audit: be_experiment_audit\n  erp/sales: second/be_experiment\n",
             [*SECURED, "erp/sales@3.0.18"])
    sql = homes.mapping_sql(c, "postgres", "pw")
    assert "CREATE SERVER cell_second_be_experiment FOREIGN DATA WRAPPER postgres_fdw OPTIONS " \
           "(host 'be-postgres-2', port '5432', dbname 'be_experiment')" in sql
    assert "CREATE SERVER cell_main_be_experiment_audit FOREIGN DATA WRAPPER postgres_fdw OPTIONS " \
           "(host 'localhost', port '5432', dbname 'be_experiment_audit')" in sql
    assert "IMPORT FOREIGN SCHEMA erp_sales FROM SERVER cell_second_be_experiment INTO erp_sales" in sql
    assert "IMPORT FOREIGN SCHEMA infra_audit FROM SERVER cell_main_be_experiment_audit INTO infra_audit" in sql
    assert not [s for s in sql if "infra_authz" in s], "a schema at home is not touched"
    assert sql.index("DROP SCHEMA IF EXISTS erp_sales CASCADE") < sql.index(
        "IMPORT FOREIGN SCHEMA erp_sales FROM SERVER cell_second_be_experiment INTO erp_sales"), "imported afresh"
    assert homes.mapping_sql(cell(tmp_path, "", SECURED), "postgres", "pw") == [], "nothing moved, nothing mapped"


def test_an_instance_is_reached_on_its_own_port_with_the_same_credentials(monkeypatch):
    monkeypatch.setenv("BE_ACC_PG_ADMIN", ADMIN)
    assert homes.admin("second", "be_experiment") == \
        "postgres://postgres:admin-pw@localhost:5433/be_experiment?sslmode=disable"
    assert homes.admin("main") == ADMIN
