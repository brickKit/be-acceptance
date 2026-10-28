"""A cell with `pooler: pgbouncer` (step 10, B1): the running connections go through a transaction-mode PgBouncer,
the migrations straight to PostgreSQL, as the database foundations document asks."""
import yaml

from cells import prepare
from cells.spec import Cell

ADMIN = "postgres://postgres:admin-pw@localhost:5432/postgres?sslmode=disable"
SECURED = ["infra/iam-casdoor@3.0.9", "infra/authz@3.0.10", "infra/audit@3.0.7"]


def cell(tmp_path, extra: str, alone: list[str]) -> Cell:
    (tmp_path / "config").mkdir()
    (tmp_path / ".secrets").mkdir()
    (tmp_path / "cell.yaml").write_text(f"cell: T\nalone: [{', '.join(alone)}]\nchain: []\n{extra}")
    return Cell.load(tmp_path)


def test_without_a_pooler_nothing_changes(tmp_path, monkeypatch):
    monkeypatch.setenv("BE_ACC_PG_ADMIN", ADMIN)
    c = cell(tmp_path, "", ["test/probe-go@3.2.20"])
    assert c.pooler == ""
    prepare.configs(c)
    v = yaml.safe_load((tmp_path / "config" / "vars.yaml").read_text())
    assert v["PG_PORT"] == "5432" and "PG_MIGRATION_HOST" not in v
    assert "PG_MIGRATION_HOST" not in (tmp_path / "config" / "test-probe-go.yaml").read_text()
    assert not (tmp_path / ".secrets" / "pgbouncer").exists()


def test_running_connections_go_through_the_pooler_migrations_straight(tmp_path, monkeypatch):
    monkeypatch.setenv("BE_ACC_PG_ADMIN", ADMIN)
    monkeypatch.setenv("BE_ACC_IAM_CLIENT_SECRET", "x")
    c = cell(tmp_path, "pooler: pgbouncer\n", [*SECURED, "erp/sales@3.0.12", "test/probe-go@3.2.20"])
    prepare.configs(c)
    v = yaml.safe_load((tmp_path / "config" / "vars.yaml").read_text())
    assert v["PG_PORT"] == "6432" and v["PG_HOST"] == c.base_host
    assert v["PG_MIGRATION_HOST"] == c.base_host and v["PG_MIGRATION_PORT"] == "5432"
    for f in ("erp-sales", "infra-authz", "infra-iam-casdoor", "test-probe-go"):
        comp = yaml.safe_load((tmp_path / "config" / f"{f}.yaml").read_text())
        assert comp["PG_MIGRATION_HOST"] == "$var:PG_MIGRATION_HOST", f
        assert comp["PG_MIGRATION_PORT"] == "$var:PG_MIGRATION_PORT", f


def test_the_pooler_is_transaction_mode_with_protocol_prepared_statements(tmp_path, monkeypatch):
    monkeypatch.setenv("BE_ACC_PG_ADMIN", ADMIN)
    c = cell(tmp_path, "pooler: pgbouncer\n", ["test/probe-go@3.2.20"])
    prepare.configs(c)
    ini = (tmp_path / ".secrets" / "pgbouncer" / "pgbouncer.ini").read_text()
    for line in ("pool_mode = transaction", "listen_port = 6432", "max_prepared_statements = 200",
                 "default_pool_size = 5", "auth_user = postgres", "auth_type = scram-sha-256"):
        assert line in ini, line
    assert f"{c.database} = host=be-postgres port=5432 dbname={c.database}" in ini
    assert (tmp_path / ".secrets" / "pgbouncer" / "userlist.txt").read_text() == '"postgres" "admin-pw"\n'


def test_an_unknown_pooler_is_refused(tmp_path):
    import pytest
    with pytest.raises(ValueError, match="pooler"):
        cell(tmp_path, "pooler: pgcat\n", ["test/probe-go@3.2.20"])
