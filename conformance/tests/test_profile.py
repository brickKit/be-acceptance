"""The core profile against both probes (step 3, T3.9): the same checks judge the Go and the Python probe from
outside; every check's self-test catches its known violation first, so a check that always passes is reported as
invalid, never as a pass."""
import asyncio
import json
import platform

from harness import Deployment, address, free_port, fresh_bus
from profile import checks, observe
from profile.model import SUITE_VERSION, Check, Report, Result, Verdict, run


async def observe_pair(d, specs):
    """Both probes, each the other's next and peer, observed while they work."""
    go, py = specs["go"], specs["py"]
    ports = {"go": free_port(), "py": free_port()}
    envs = {"go": await d.env(go, {"PROBE_NEXT": py.id, "PROBE_PEER_STREAM": py.stream, "STOP_DRAIN_DELAY": "1s",
                                   **address(py, ports["py"])}),
            "py": await d.env(py, {"PROBE_NEXT": go.id, "PROBE_PEER_STREAM": go.stream, "STOP_DRAIN_DELAY": "1s",
                                   **address(go, ports["go"])})}
    obs = {k: {"component": specs[k].id, "stream": specs[k].stream, "partitions": 4,
               "impl": envs[k]["IMPLEMENTATION_INSTANCE"], "owner": specs[k].schema + "_owner",
               "sdk_tables": observe.sdk_tables(), **await observe.exits(specs[k], envs[k])} for k in specs}
    caps = {k: await observe.capture(specs[k].stream) for k in specs}
    running = {k: await d.start(specs[k], envs[k], ports[k]) for k in ("go", "py")}
    token = d.world.token("0190f3a1-0000-7000-8000-0000000000a1")
    for k, r in running.items():
        obs[k].update(await observe.endpoints(r))
        await observe.traffic(d, r, "test/probe-py" if k == "go" else "test/probe-go", token)
        obs[k]["http_requests"] = 5
    await asyncio.sleep(3)  # forwarding of the writes (the big one through object storage)
    for k, r in running.items():
        nc, got = caps[k]
        await nc.close()
        await observe.claims(got)
        obs[k]["messages"] = got
        async with __import__("httpx").AsyncClient() as c:
            obs[k]["metrics"] = (await c.get(r.url + "/metrics")).text
        obs[k]["db"] = await observe.database(specs[k].schema, specs[k].schema + "_owner", specs[k].schema + "_rw")
    for k, r in running.items():
        obs[k].update(await observe.stopping(r))
        obs[k]["logs"] = r.logs()
        obs[k]["secrets"] = observe.secrets_of(envs[k], token)
    d.running.clear()
    return obs


async def test_both_probes_conform_to_the_core_profile(specs, world, tmp_path):
    d = Deployment(world, await fresh_bus("jetstream", [specs["go"].stream, specs["py"].stream]), tmp_path)
    obs = await observe_pair(d, specs)
    reports = []
    for k in ("go", "py"):
        o = obs[k]
        report = Report(o["component"], observe.artifact_digest(specs[k]), o["describe"].get("protocol", ""),
                        SUITE_VERSION, {"python": platform.python_version(), "bus": "jetstream"}, "core",
                        [run(c, o) for c in checks.CORE])
        (tmp_path / f"report-{k}.json").write_text(json.dumps(report.as_dict(), ensure_ascii=False, indent=2))
        print(report.summary())
        reports.append(report)
    assert [r.verdict for r in reports] == [Verdict.CONFORMS] * 2, "\n".join(r.summary() for r in reports)


def test_a_check_that_passes_its_known_violation_is_invalid():
    lazy = Check("CK-X", "always passes", (), ("logs",), lambda o: [], lambda o: o)
    assert run(lazy, {"logs": ""}).result == Result.INVALID


def test_the_verdict_never_counts_what_did_not_run():
    good = Check("CK-A", "a", (), ("a",), lambda o: [] if o["a"] else ["bad"], lambda o: {**o, "a": False})
    outcomes = [run(good, {"a": True}), run(good, {})]
    assert [o.result for o in outcomes] == [Result.PASS, Result.NOT_RUN]
    assert Report("c", "sha256:x", "3", SUITE_VERSION, {}, "core", outcomes).verdict == Verdict.PARTIAL
    failing = run(good, {"a": False})
    assert Report("c", "sha256:x", "3", SUITE_VERSION, {}, "core", outcomes + [failing]).verdict == Verdict.NONCONFORMING
    assert Report("c", "", "3", SUITE_VERSION, {}, "core", outcomes[:1]).verdict == Verdict.INVALID, "no artifact digest"
    assert Report("c", "sha256:x", "3", SUITE_VERSION, {}, "core", outcomes[:1]).verdict == Verdict.CONFORMS
