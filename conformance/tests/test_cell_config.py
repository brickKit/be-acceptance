"""What a cell configures from what its members declare (foundations item 4, T11): an evidence writer's alert channel
(the run profile's keys: its intents go there when the evidence store does not take them, P12.16), a producer's object
store; and every key declared without a default is either set or unset on purpose. T11's drill found the cells had
configured no run profile at all since the members declared it."""
import yaml

from cells import prepare
from cells.spec import Cell

ADMIN = "postgres://postgres:admin-pw@localhost:5432/postgres?sslmode=disable"


def cell(tmp_path, monkeypatch, props: dict) -> Cell:
    monkeypatch.setenv("BE_ACC_PG_ADMIN", ADMIN)
    (tmp_path / "config").mkdir()
    (tmp_path / ".secrets").mkdir()
    (tmp_path / "cell.yaml").write_text("cell: T\nalone: [infra/iam-casdoor@3.0.31, infra/authz@3.0.35, "
                                        "infra/audit@3.0.25, erp/credit@3.0.22]\nchain: []\n")
    monkeypatch.setattr(prepare, "schema_props", lambda _c, comp: props.get(comp, {}))
    return Cell.load(tmp_path)


def test_a_member_that_declares_the_run_profile_is_given_it(tmp_path, monkeypatch):
    c = cell(tmp_path, monkeypatch, {"infra/authz": {"RUN_PROFILE_URL": {}}})
    prepare.configs(c)

    def conf(f):
        return yaml.safe_load((tmp_path / "config" / f"{f}.yaml").read_text())

    authz = conf("infra-authz")
    assert {k: authz[k] for k in ("RUN_PROFILE_URL", "RUN_PROFILE_BUCKET", "RUN_PROFILE_ACCESS_KEY",
                                  "RUN_PROFILE_SECRET_KEY")} == {
        "RUN_PROFILE_URL": "$var:RUN_PROFILE_URL", "RUN_PROFILE_BUCKET": "$var:RUN_PROFILE_BUCKET",
        "RUN_PROFILE_ACCESS_KEY": "${BE_ACC_S3_ACCESS_KEY}", "RUN_PROFILE_SECRET_KEY": "${BE_ACC_S3_SECRET_KEY}"}
    assert "RUN_PROFILE_URL" not in conf("erp-credit"), "a member that does not declare it gets none"
    v = conf("vars")
    assert v["RUN_PROFILE_URL"] == f"http://{c.base_host}:9000" and v["RUN_PROFILE_BUCKET"] == "be-run-profile"


def test_a_producer_that_declares_the_object_store_is_given_it(tmp_path, monkeypatch):
    c = cell(tmp_path, monkeypatch, {"erp/credit": {"OBJECT_STORE_URL": {}}})
    prepare.configs(c)
    credit = yaml.safe_load((tmp_path / "config" / "erp-credit.yaml").read_text())
    assert credit["OBJECT_STORE_URL"] == "$var:OBJECT_STORE_URL" and credit["OBJECT_STORE_PREFIX"] == "erp_credit/"
    assert credit["OBJECT_STORE_SECRET_KEY"] == "${BE_ACC_S3_SECRET_KEY}"


def test_a_key_declared_without_a_default_and_left_unset_is_named(tmp_path, monkeypatch):
    c = cell(tmp_path, monkeypatch, {"erp/credit": {
        "CREDIT_NEW_KEY": {"type": "string"},                                      # new, nobody set it: named
        "OTEL_URL": {"type": "string"},                                            # unset on purpose
        "PG_STATEMENT_TIMEOUT": {"type": "string", "description": "SDK default 5s"},  # has a default
        "PG_SCHEMA": {"type": "string"}}})                                         # set by the cell
    prepare.configs(c)
    assert prepare.unset_keys(c) == ["erp/credit: CREDIT_NEW_KEY"]
