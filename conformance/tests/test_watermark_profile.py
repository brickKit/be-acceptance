"""The watermark source profile (foundations item 1, T8) against the Go probe's watermark stream: the suite pulls it
as a declared puller and as a stranger, and tampers with the outbox the way an unadvanced restore would (SK17). Every
check's self-test catches its known violation first."""
import json
import platform

from harness import Deployment, fresh_bus
from profile import observe, watermark
from profile.model import SUITE_VERSION, Report, Verdict, run


async def test_the_go_probe_conforms_to_the_watermark_source_profile(specs, world, tmp_path):
    spec = specs["go"]
    d = Deployment(world, await fresh_bus("jetstream", [spec.stream, watermark.stream_of(spec)]), tmp_path)
    try:
        env = await d.env(spec, {})
        r = await d.start(spec, env)
        token = d.world.token("0190f3a1-0000-7000-8000-0000000000a1")
        described = (await observe.endpoints(r))["describe"]
        obs = await watermark.observe(d, r, spec, env, token)
    finally:
        await d.stop()
    report = Report(spec.id, observe.artifact_digest(spec), described.get("protocol", ""), SUITE_VERSION,
                    {"python": platform.python_version()}, "watermark-source", [run(c, obs) for c in watermark.WATERMARK])
    (tmp_path / "observed-watermark.json").write_text(json.dumps(obs, ensure_ascii=False, indent=1, default=str))
    (tmp_path / "report-watermark.json").write_text(json.dumps(report.as_dict(), ensure_ascii=False, indent=2))
    print(report.summary())
    assert report.verdict == Verdict.CONFORMS, report.summary()
