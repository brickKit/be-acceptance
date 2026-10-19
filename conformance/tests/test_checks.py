"""The profile's judges on hand-made observations (no infrastructure)."""
import json

from profile.checks import judge_shell_metrics

METRICS = """# TYPE be_http_server_duration_seconds histogram
be_http_server_duration_seconds_bucket{component="test/probe-go",method="POST",route="/api/probe/echo",status="200",le="+Inf"} 1
be_http_server_duration_seconds_sum{component="test/probe-go",method="POST",route="/api/probe/echo",status="200"} 0.001
be_http_server_duration_seconds_count{component="test/probe-go",method="POST",route="/api/probe/echo",status="200"} 1
"""


def access(component):
    return json.dumps({"time": "2026-10-19T00:00:00.000Z", "level": "INFO", "msg": "access", "component": component,
                       "version": "1.0.0", "route": "/x", "status": 200})


def test_a_member_nobody_called_needs_no_request_series():
    obs = {"shell_metrics": METRICS, "members": ["test/probe-go", "infra/audit"], "logs": access("test/probe-go")}
    assert judge_shell_metrics(obs) == []


def test_a_member_that_served_requests_needs_its_series():
    logs = "\n".join([access("test/probe-go"), access("infra/audit")])
    obs = {"shell_metrics": METRICS, "members": ["test/probe-go", "infra/audit"], "logs": logs}
    assert judge_shell_metrics(obs) == ["no request metrics of infra/audit"]


def test_without_the_output_every_member_needs_its_series():
    obs = {"shell_metrics": METRICS, "members": ["test/probe-go", "infra/audit"]}
    assert judge_shell_metrics(obs) == ["no request metrics of infra/audit"]


def test_a_series_without_the_member_label_is_refused():
    obs = {"shell_metrics": METRICS.replace("component=", "c="), "members": ["test/probe-go"],
           "logs": access("test/probe-go")}
    assert any("not a hosted member" in p for p in judge_shell_metrics(obs))
