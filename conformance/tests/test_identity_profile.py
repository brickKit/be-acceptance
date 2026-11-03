"""The identity profile (foundations item 2, T6) against both probes with their guard on: the suite signs the tokens
and serves the key set, issues the views, calls the system surface with good and bad certificates. CK-ID-05 and 06
apply to the Go probe only (be-sdk-python implements P8.25 and P8.26 with foundations item 6). Every check's
self-test catches its known violation first."""
import json
import platform

import pytest

from harness import Deployment, fresh_bus
from profile import identity, observe
from profile.model import SUITE_VERSION, Report, Result, Verdict, run


@pytest.mark.parametrize("kind,peer", [("go", "go2"), ("py", None)])
async def test_the_probes_conform_to_the_identity_profile(specs, world, tmp_path, kind, peer):
    spec, peer_spec = specs[kind], specs[peer] if peer else None
    d = Deployment(world, await fresh_bus("jetstream", [spec.stream] + ([peer_spec.stream] if peer_spec else [])),
                   tmp_path)
    try:
        env = await d.env(spec, {})
        obs = await identity.observe(d, spec, env, peer_spec)
        described = (await observe.endpoints(d.running[0]))["describe"]
    finally:
        await d.stop()
    report = Report(spec.id, observe.artifact_digest(spec), described.get("protocol", ""), SUITE_VERSION,
                    {"python": platform.python_version()}, "identity", [run(c, obs) for c in identity.IDENTITY])
    (tmp_path / f"observed-identity-{kind}.json").write_text(json.dumps(obs, ensure_ascii=False, indent=1, default=str))
    (tmp_path / f"report-identity-{kind}.json").write_text(json.dumps(report.as_dict(), ensure_ascii=False, indent=2))
    print(report.summary())
    assert report.verdict == Verdict.CONFORMS, report.summary()
    applies = {o.check: o.result for o in report.outcomes}
    if kind == "py":
        assert applies["CK-ID-05"] == applies["CK-ID-06"] == Result.NOT_APPLICABLE
    else:
        assert all(r == Result.PASS for r in applies.values()), applies
