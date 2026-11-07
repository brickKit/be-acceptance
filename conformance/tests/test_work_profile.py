"""The background work profile (foundations item 1, T5; the Python probe since foundations item 6, T4) against both
probes: their jobs enqueued over the user surface, the probe stopped, killed and outdated while it runs them. Work
items do not touch the bus; one transport is enough. Every check's self-test catches its known violation first."""
import json
import platform

import pytest

from harness import Deployment, fresh_bus
from profile import observe, work
from profile.model import SUITE_VERSION, Report, Verdict, run


@pytest.mark.parametrize("kind", ["go", "py"])
async def test_the_probe_conforms_to_the_work_profile(kind, specs, world, tmp_path):
    spec = specs[kind]
    d = Deployment(world, await fresh_bus("jetstream", [spec.stream]), tmp_path)
    try:
        env = await d.env(spec, {"PROBE_STEPS_EVERY": "1s"})
        r = await d.start(spec, env)
        token = d.world.token("0190f3a1-0000-7000-8000-0000000000a1")
        described = (await observe.endpoints(r))["describe"]
        obs, r = await work.observe(d, r, spec, env, token)
    finally:
        await d.stop()
    report = Report(spec.id, observe.artifact_digest(spec), described.get("protocol", ""), SUITE_VERSION,
                    {"python": platform.python_version()}, "work", [run(c, obs) for c in work.WORK])
    (tmp_path / "observed-work.json").write_text(json.dumps(obs, ensure_ascii=False, indent=1, default=str))
    (tmp_path / "report-work.json").write_text(json.dumps(report.as_dict(), ensure_ascii=False, indent=2))
    print(report.summary())
    assert report.verdict == Verdict.CONFORMS, report.summary()
