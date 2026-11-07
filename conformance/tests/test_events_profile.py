"""The events profile (foundations item 1, T4; the Python probe since foundations item 6, T5) against both probes, on
both transports: the probe produces on its own stream and consumes one the suite injects into. Every check's
self-test catches its known violation first."""
import json
import platform

import pytest

from harness import Deployment, fresh_bus
from profile import events, observe
from profile.model import SUITE_VERSION, Report, Verdict, run


@pytest.mark.parametrize("kind", ["go", "py"])
@pytest.mark.parametrize("bus", ["jetstream", "pgqueue"])
async def test_the_probe_conforms_to_the_events_profile(bus, kind, specs, world, tmp_path):
    spec = specs[kind]
    d = Deployment(world, await fresh_bus(bus, [spec.stream, events.INJECTED]), tmp_path)
    try:
        await events.ensure_injected(d.bus)
        env = await d.env(spec, {"PROBE_PEER_STREAM": events.INJECTED})
        r = await d.start(spec, env)
        token = d.world.token("0190f3a1-0000-7000-8000-0000000000a1")
        described = (await observe.endpoints(r))["describe"]
        obs = await events.observe(d, r, spec, token)
        assert await r.stop() == 0
        d.running.remove(r)
        obs["bus_start"] = await events.bus_start(d, spec, env)
    finally:
        await d.stop()
    report = Report(spec.id, observe.artifact_digest(spec), described.get("protocol", ""), SUITE_VERSION,
                    {"python": platform.python_version(), "bus": bus}, "events", [run(c, obs) for c in events.EVENTS])
    (tmp_path / f"observed-events-{bus}.json").write_text(json.dumps(obs, ensure_ascii=False, indent=1, default=str))
    (tmp_path / f"report-events-{bus}.json").write_text(json.dumps(report.as_dict(), ensure_ascii=False, indent=2))
    print(report.summary())
    assert report.verdict == Verdict.CONFORMS, report.summary()
