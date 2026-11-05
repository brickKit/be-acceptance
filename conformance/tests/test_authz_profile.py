"""The authorization profile (foundations item 3, T8d) against both probes with their guard on: the suite issues the
views and answers the verifications (wrong nonces, a stop, an older recovery generation), seeds the probe's entries
and reads and moves them as a user whose grant constrains one dimension at a time. Every check's self-test catches its
known violation first. About 90 seconds a probe: CK-AZ-03 waits out W_read (30 s)."""
import json
import platform

import pytest

from harness import Deployment, fresh_bus
from profile import authz, observe
from profile.model import SUITE_VERSION, Report, Result, Verdict, run


@pytest.mark.parametrize("kind", ["go", "py"])
async def test_the_probes_conform_to_the_authorization_profile(specs, world, tmp_path, kind):
    spec = specs[kind]
    d = Deployment(world, await fresh_bus("jetstream", [spec.stream]), tmp_path)
    try:
        env = await d.env(spec, {})
        obs = await authz.observe(d, spec, env)
        described = (await observe.endpoints(d.running[0]))["describe"]
    finally:
        await d.stop()
    report = Report(spec.id, observe.artifact_digest(spec), described.get("protocol", ""), SUITE_VERSION,
                    {"python": platform.python_version()}, "authz", [run(c, obs) for c in authz.AUTHZ])
    (tmp_path / f"observed-authz-{kind}.json").write_text(json.dumps(obs, ensure_ascii=False, indent=1, default=str))
    (tmp_path / f"report-authz-{kind}.json").write_text(json.dumps(report.as_dict(), ensure_ascii=False, indent=2))
    print(report.summary())
    assert report.verdict == Verdict.CONFORMS, report.summary()
    assert all(o.result == Result.PASS for o in report.outcomes), report.summary()
