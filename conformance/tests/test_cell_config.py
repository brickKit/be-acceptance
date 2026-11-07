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
        "PROBE_EXTERNAL_URL": {"type": "string"},                                  # unset on purpose
        "PG_STATEMENT_TIMEOUT": {"type": "string", "description": "SDK default 5s"},  # has a default
        "PG_SCHEMA": {"type": "string"}}})                                         # set by the cell
    prepare.configs(c)
    assert prepare.unset_keys(c) == ["erp/credit: CREDIT_NEW_KEY"]


def test_a_member_with_data_classes_is_given_the_cold_store(tmp_path, monkeypatch):
    """Foundations item 5, T10e: a member that declares the cold tier's store gets the cell's bucket and the store's
    three identities; the keys named after its classes are its own (residence, retention, route), unset on purpose."""
    c = cell(tmp_path, monkeypatch, {"infra/audit": {
        "TIERING_STORE_URL": {"type": "string"}, "TIERING_BUCKET": {"type": "string"},
        "TIERING_WRITE_SECRET_KEY": {"type": "string"}, "TIERING_RETENTION_INFRA_AUDIT_RECORDS": {"type": "string"},
        "TIERING_RESIDENCE_INFRA_AUDIT_RECORDS": {"type": "string"},
        "TIERING_ROUTE_INFRA_AUDIT_RECORDS": {"type": "string"}}})
    prepare.configs(c)
    audit = yaml.safe_load((tmp_path / "config" / "infra-audit.yaml").read_text())
    assert audit["TIERING_STORE_URL"] == "$var:OBJECT_STORE_URL" and audit["TIERING_BUCKET"] == "$var:TIERING_BUCKET"
    assert {audit[f"TIERING_{r}_SECRET_KEY"] for r in ("WRITE", "READ", "DELETE")} == {"${BE_ACC_S3_SECRET_KEY}"}
    assert yaml.safe_load((tmp_path / "config" / "vars.yaml").read_text())["TIERING_BUCKET"] == "be-tiering"
    assert prepare.unset_keys(c) == []


def test_the_cells_settings_reach_any_member(tmp_path, monkeypatch):
    """A cell's settings (cell.yaml) reach a component as they reach a probe: T10e makes the tiering due within
    minutes (TIERING_WINDOW, TIERING_GRACE)."""
    c = cell(tmp_path, monkeypatch, {})
    c.settings = {"infra/audit": {"TIERING_WINDOW": "* * * * *", "TIERING_GRACE": "0s"}}
    prepare.configs(c)
    audit = yaml.safe_load((tmp_path / "config" / "infra-audit.yaml").read_text())
    assert audit["TIERING_WINDOW"] == "* * * * *" and audit["TIERING_GRACE"] == "0s"
    assert "TIERING_WINDOW" not in yaml.safe_load((tmp_path / "config" / "erp-credit.yaml").read_text())


def test_a_secured_cells_certificates_come_from_its_issuer(tmp_path, monkeypatch):
    """T13d (A148): a secured cell's CA and credentials come from the deployment's issuer (be-ops trust), so its run
    profile holds the trust material a recovery judges records against; a new CA per preparation."""
    c = cell(tmp_path, monkeypatch, {})
    calls = []
    monkeypatch.setattr(prepare, "issuer", lambda _c, *args: calls.append(args))
    monkeypatch.setattr(prepare.testing, "World", lambda *_a: (_ for _ in ()).throw(AssertionError("the test world")))
    (tmp_path / ".secrets" / "trust").mkdir()
    (tmp_path / ".secrets" / "trust" / "ca.key.pem").write_text("a previous preparation's CA")
    prepare.certificates(c)
    assert not (tmp_path / ".secrets" / "trust").exists(), "a new CA per preparation"
    assert calls[0] == ("init",)
    issued = {a[2]: a[4] for a in calls[1:]}
    assert issued == {comp: comp.replace("/", "-") for comp in [*c.placed, "ops/recover"]}
    assert all(a[0] == "issue" and a[5:] == ("--out", str(tmp_path / ".secrets" / "tls")) for a in calls[1:])


def test_an_unsecured_cells_certificates_come_from_the_test_world(tmp_path, monkeypatch):
    monkeypatch.setenv("BE_ACC_PG_ADMIN", ADMIN)
    (tmp_path / "cell.yaml").write_text("cell: U\nalone: [erp/credit@3.0.22]\nchain: []\n")
    c = Cell.load(tmp_path)
    assert not c.secured
    monkeypatch.setattr(prepare, "issuer", lambda *_a: (_ for _ in ()).throw(AssertionError("the issuer")))
    prepare.certificates(c)
    assert (tmp_path / ".secrets" / "tls" / "erp-credit.cert.pem").exists()
    assert (tmp_path / ".secrets" / "tls" / "ops-recover.key.pem").exists()
