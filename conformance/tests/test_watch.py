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
