from cells import watch


def result(hop_p99=1.0, errors=None, throughput=50.0, settled=True):
    return {"before": {"settled": settled, "left": {"x:work": 3}},
            "driver": {"hop_ms": {"P50": 0.8, "P99": hop_p99}, "error_rate": 0.01 if errors else 0,
                       "errors": errors or {}, "throughput": throughput}}


def test_a_normal_measurement_is_quiet():
    assert watch.anomalies(result(), "50", [{"p50": 0.8, "p99": 1.2}]) == []


def test_what_needs_acting_on_is_named():
    assert "settled" in watch.anomalies(result(settled=False), "50", [])[0]
    assert "errors at a fixed rate" in watch.anomalies(result(errors={"503 X": 3}), "50", [])[0]
    assert "throughput 30" in watch.anomalies(result(throughput=30.0), "50", [])[0]
    assert "p99 90.0 against 1.2" in watch.anomalies(result(hop_p99=90.0), "50", [{"p50": 0.8, "p99": 1.2}])[0]


def test_a_ramp_far_below_earlier_runs_is_named():
    ramp = {"driver": {"steps": [], "sustained_rate": 300.0}}
    assert watch.anomalies(ramp, "ramp", [{"sustained": 1900.0}])
    assert watch.anomalies(ramp, "ramp", [{"sustained": 400.0}]) == []


def test_a_slow_path_at_a_fixed_rate_is_named_even_without_errors():
    slow_write = {"driver": {"latency_ms": {"P50": 46.3, "P99": 50.4}, "error_rate": 0, "throughput": 50.0},
                  "completion": {"received": {"n": 10, "p50_ms": 120.0, "p99_ms": 400.0}}}
    assert "above the plausible 30 ms for write" in watch.anomalies(slow_write, "50", [], "write")[0]
    slow_write["driver"]["latency_ms"]["P50"] = 6.0
    assert watch.anomalies(slow_write, "50", [], "write") == []


def test_a_relay_waiting_for_a_poll_is_named_although_its_events_arrive_fast():
    relay = {"driver": {"latency_ms": {"P50": 4.0, "P99": 6.0}, "error_rate": 0, "throughput": 50.0},
             "completion": {"received": {"n": 10, "p50_ms": 4.0, "p99_ms": 8.0},
                            "relayed": {"n": 10, "p50_ms": 250.0, "p99_ms": 500.0}}}
    assert "relays complete with p50 250 ms" in watch.anomalies(relay, "50", [], "relay")[0]
    relay["completion"]["relayed"] = {"n": 10, "p50_ms": 9.0, "p99_ms": 2500.0}
    assert "relays complete with p99 2500 ms" in watch.anomalies(relay, "50", [], "relay")[0]
    relay["completion"]["relayed"]["p99_ms"] = 20.0
    assert watch.anomalies(relay, "50", [], "relay") == []


def saturated(errors, cores):
    return {"driver": {"errors": errors}, "resources": {"containers": {
        "brickkit-d-s-test-probe-py-3-2-4-1": {"process": {"cores": cores}},
        "be-postgres": {"process": {"cores": 3.0}}}}}


def test_an_expected_limit_at_200_is_counted_not_reported():
    assert watch.expected_limit(saturated({"503 OUTBOUND_LIMIT": 900}, 1.02), "200", ["errors"], True) == "CPU saturated"
    # the infrastructure being busy is not the component's limit; nor are errors of another kind
    assert watch.expected_limit(saturated({"503 OUTBOUND_LIMIT": 900}, 0.4), "200", ["errors"], True) == ""
    assert watch.expected_limit(saturated({"500 INTERNAL": 3}, 1.02), "200", ["errors"], True) == ""
    slow_arrival = saturated({}, 0.5)
    assert watch.expected_limit(slow_arrival, "200", ["events arrive with p99 5000 ms"], True) == \
        "subscription one at a time"
    assert watch.expected_limit(slow_arrival, "200", ["events arrive with p99 5000 ms"], False) == "", \
        "slow at 50/s too: a defect, not a limit"


def test_nothing_at_50_is_an_expected_limit():
    assert watch.expected_limit(saturated({"503 OUTBOUND_LIMIT": 900}, 1.02), "50", ["errors"], True) == ""
