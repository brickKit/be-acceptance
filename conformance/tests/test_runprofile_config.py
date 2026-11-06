"""An evidence writer's alert channel in a cell (foundations item 4, T11): a member whose version declares the run
profile's keys gets them (its intents go there when the evidence store does not take them, P12.16); one that does not
declare them gets none. T11's drill found the cells configured no run profile at all."""
import yaml

from cells import prepare
from cells.spec import Cell

ADMIN = "postgres://postgres:admin-pw@localhost:5432/postgres?sslmode=disable"


def test_a_member_that_declares_the_run_profile_is_given_it(tmp_path, monkeypatch):
    monkeypatch.setenv("BE_ACC_PG_ADMIN", ADMIN)
    (tmp_path / "config").mkdir()
    (tmp_path / ".secrets").mkdir()
    (tmp_path / "cell.yaml").write_text("cell: T\nalone: [infra/iam-casdoor@3.0.31, infra/authz@3.0.35, "
                                        "infra/audit@3.0.25, erp/credit@3.0.22]\nchain: []\n")
    c = Cell.load(tmp_path)
    monkeypatch.setattr(prepare, "declares", lambda _c, comp, key: key == "RUN_PROFILE_URL" and comp == "infra/authz")
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
