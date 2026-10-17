"""Results, checks and the report (assembly project protocol 18, "一致性套件")."""
from __future__ import annotations

import enum
from dataclasses import dataclass, field
from typing import Any, Callable

SUITE_VERSION = "0.1.0"


class Result(enum.StrEnum):
    PASS = "通过"
    FAIL = "失败"
    NOT_RUN = "未运行"  # the environment was missing or a prerequisite failed: never a pass
    NOT_APPLICABLE = "不适用"  # the component does not enable the capability, with the reason
    INVALID = "检查失效"  # the self-test did not fail on its known violation: the result cannot be trusted


class Verdict(enum.StrEnum):
    CONFORMS = "符合"
    PARTIAL = "部分"
    NONCONFORMING = "不符合"
    INVALID = "无效"


Judge = Callable[[dict], list[str]]  # an observation -> the problems found (empty: none)


@dataclass(frozen=True)
class Check:
    """One check: the rules it holds, what it needs observed, how it judges, and a known violation (bad) that its
    judge must fail on before its result counts. applies returns a reason when the check does not apply."""

    id: str
    title: str
    rules: tuple[str, ...]
    needs: tuple[str, ...]  # observation keys; missing ones make the check NOT_RUN
    judge: Judge
    bad: Callable[[dict], dict]  # turns a good observation into a known violation
    applies: Callable[[dict], str] = lambda obs: ""


@dataclass
class Outcome:
    check: str
    result: Result
    problems: list[str] = field(default_factory=list)
    reason: str = ""


def run(check: Check, obs: dict) -> Outcome:
    """Self-test first, then the check itself."""
    if reason := check.applies(obs):
        return Outcome(check.id, Result.NOT_APPLICABLE, reason=reason)
    missing = [k for k in check.needs if obs.get(k) is None]
    if missing:
        return Outcome(check.id, Result.NOT_RUN, reason="not observed: " + ", ".join(missing))
    try:
        if not check.judge(check.bad(obs)):
            return Outcome(check.id, Result.INVALID, reason="the self-test passed a known violation")
        problems = check.judge(obs)
    except Exception as e:  # noqa: BLE001 - a check that crashes is not a pass
        return Outcome(check.id, Result.INVALID, reason=f"the check failed to run: {e!r}")
    return Outcome(check.id, Result.FAIL if problems else Result.PASS, problems)


@dataclass
class Report:
    component: str
    artifact: str  # the component artifact's digest (image or binary)
    protocol: str
    suite: str
    environment: dict[str, Any]
    profile: str
    outcomes: list[Outcome]

    @property
    def verdict(self) -> Verdict:
        """The strictest of: invalid > nonconforming > partial > conforms (protocol 18)."""
        results = {o.result for o in self.outcomes}
        if Result.INVALID in results or not (self.artifact and self.protocol and self.suite):
            return Verdict.INVALID
        if Result.FAIL in results:
            return Verdict.NONCONFORMING
        if Result.NOT_RUN in results:
            return Verdict.PARTIAL
        return Verdict.CONFORMS

    def summary(self) -> str:
        lines = [f"{self.component} {self.profile}: {self.verdict}"]
        for o in self.outcomes:
            detail = "; ".join(o.problems) or o.reason
            lines.append(f"  {o.check} {o.result}" + (f" — {detail}" if detail else ""))
        return "\n".join(lines)

    def as_dict(self) -> dict:
        return {"component": self.component, "artifact": self.artifact, "protocol": self.protocol, "suite": self.suite,
                "environment": self.environment, "profile": self.profile, "verdict": str(self.verdict),
                "outcomes": [{"check": o.check, "result": str(o.result), "problems": o.problems, "reason": o.reason}
                             for o in self.outcomes]}
