"""Suite C (the deployment experiment's faults): the fault injections, all from outside the components (no fault hooks
in product code). A process crashes: it is sent SIGABRT inside its container and aborts (the Go runtime dumps its
goroutines and exits 2); Docker and Podman restart it (`restart: unless-stopped`), on Kubernetes the kubelet does. Not
`docker kill`: Docker counts that as a manual stop and does not restart the container; and a SIGKILL from inside the
container never reaches its PID 1. A base resource stops and
starts. A step stops in the middle: a trigger that only the test's own order meets sleeps inside the coordinator's
transaction, before it commits (the participants have already committed their part); the test kills the process,
ends the sleeping backend (the process's connection is gone, its transaction rolls back) and drops the trigger."""
from __future__ import annotations

import asyncio
import subprocess
import time
from urllib.parse import urlsplit

import asyncpg

from cells.spec import Cell
from harness import need

KUBE = ["minikube", "-p", "bek"]
# where a step can be stopped: the table, the condition on the new row (with $ORDER for the test's order) and what the
# coordinator is about to commit
POINTS = {
    # RS06: every branch confirmed at the participants; sales records the answers and the confirmation
    "branch-committed": ("erp_sales.confirmation_branch",
                         "NEW.state = 'committed' AND (SELECT order_id FROM erp_sales.confirmation WHERE id = NEW.confirmation_id) = '$ORDER'"),
    # RS05: every branch held; sales moves the process to committing
    "process-committing": ("erp_sales.confirmation", "NEW.state = 'committing' AND NEW.order_id = '$ORDER'"),
    # BL04: billing issued the invoice and asks sales to formalize the claim
    "claim-formalized": ("erp_sales.billing_claim", "NEW.state = 'formalized' AND NEW.order_id = '$ORDER'"),
}


def host_of(c: Cell, component: str) -> str:
    """The process a component runs in: its shell's, or its own."""
    return next((s for s, ms in c.shells.items() if component in ms), component)


def engine(c: Cell) -> str:
    if c.has_local:
        raise ValueError(f"{c.name}: suite C does not run on the rows with processes on this machine")
    return {"docker": "docker", "podman": "podman"}.get(c.target, "k8s")


def _run(cmd: list[str]) -> str:
    return subprocess.run(cmd, check=True, capture_output=True, text=True).stdout.strip()


def deployment(c: Cell, host: str) -> str:
    return f"{host.replace('/', '-')}-{c.versions[host].replace('.', '-')}"


def starts(c: Cell, component: str) -> str:
    """How many times the process has started (Kubernetes: its pods' restarts), or when it last started (Docker,
    Podman): a crash is over once this changed and the process is healthy again."""
    host = host_of(c, component)
    e = engine(c)
    if e in ("docker", "podman"):
        return _run([e, "inspect", "-f", "{{.State.StartedAt}}", c.container_of(host)])
    return _run([*KUBE, "kubectl", "--", "-n", c.project, "get", "pods", "-o",
                 "jsonpath={range .items[*]}{.metadata.name}={.status.containerStatuses[0].restartCount} {end}"])


def kill(c: Cell, component: str) -> str:
    """Crash the process that runs the component (SIGABRT to its PID 1); returns what was crashed."""
    host = host_of(c, component)
    e = engine(c)
    if e in ("docker", "podman"):
        name = c.container_of(host)
        _run([e, "exec", name, "kill", "-ABRT", "1"])
        return name
    _run([*KUBE, "kubectl", "--", "-n", c.project, "exec", f"deploy/{deployment(c, host)}", "--", "kill", "-ABRT", "1"])
    return deployment(c, host)


def healthy(c: Cell, component: str) -> bool:
    """The process that runs the component passes its health check (Kubernetes: its Deployment is ready)."""
    host = host_of(c, component)
    e = engine(c)
    if e in ("docker", "podman"):
        out = subprocess.run([e, "inspect", "-f", "{{.State.Health.Status}}", c.container_of(host)],
                             capture_output=True, text=True).stdout.strip()
        return out == "healthy"
    out = subprocess.run([*KUBE, "kubectl", "--", "-n", c.project, "get", "deploy", deployment(c, host), "-o",
                          "jsonpath={.status.readyReplicas}"], capture_output=True, text=True).stdout.strip()
    return out == "1"


def wait_running(c: Cell, component: str, before: str | None = None, limit: float = 180) -> float:
    """Seconds until the process is healthy again; with `before` (starts() taken before the fault), also started
    again since."""
    t0 = time.monotonic()
    while time.monotonic() - t0 < limit:
        try:
            again = before is None or starts(c, component) != before
        except subprocess.CalledProcessError:
            again = False
        if again and healthy(c, component):
            return time.monotonic() - t0
        time.sleep(0.5)
    raise RuntimeError(f"{c.name}: {host_of(c, component)} not up again within {limit:.0f} s")


def stop(c: Cell, component: str, action: str = "stop") -> str:
    """Stop (or start again) the process that runs the component, cleanly: the container, or on Kubernetes the
    Deployment scaled to 0 (or 1)."""
    host = host_of(c, component)
    e = engine(c)
    if e in ("docker", "podman"):
        _run([e, action, c.container_of(host)])
        return c.container_of(host)
    deploy = deployment(c, host)
    _run([*KUBE, "kubectl", "--", "-n", c.project, "scale", "deploy", deploy, f"--replicas={0 if action == 'stop' else 1}"])
    return deploy


def base(action: str, name: str) -> None:
    """Stop or start a base resource (be-postgres, be-nats, ...): they run in Docker for every row."""
    _run(["docker", action, name])


class Point:
    """A trigger that stops the coordinator's transaction for one order (POINTS) until released."""

    def __init__(self, c: Cell, point: str, order_id: str):
        self.c, self.point, self.order = c, point, order_id
        self.table, cond = POINTS[point]
        self.cond = cond.replace("$ORDER", order_id)
        self.name = f"be_fault_{point.replace('-', '_')}"
        self.schema = self.table.split(".")[0]

    async def _conn(self) -> asyncpg.Connection:
        return await asyncpg.connect(urlsplit(need("BE_ACC_PG_ADMIN"))._replace(path="/" + self.c.database).geturl())

    async def arm(self, seconds: int = 900) -> None:
        conn = await self._conn()
        try:
            await conn.execute(f"""CREATE OR REPLACE FUNCTION {self.schema}.{self.name}() RETURNS trigger
                LANGUAGE plpgsql SECURITY DEFINER AS $$ BEGIN IF {self.cond} THEN PERFORM pg_sleep({seconds}); END IF;
                RETURN NEW; END $$""")
            await conn.execute(f"DROP TRIGGER IF EXISTS {self.name} ON {self.table}")
            await conn.execute(f"CREATE TRIGGER {self.name} BEFORE UPDATE ON {self.table} FOR EACH ROW "
                               f"EXECUTE FUNCTION {self.schema}.{self.name}()")
        finally:
            await conn.close()

    async def sleeping(self) -> list[int]:
        """The backends asleep in this trigger."""
        conn = await self._conn()
        try:
            return [r[0] for r in await conn.fetch(
                "SELECT pid FROM pg_stat_activity WHERE datname = $1 AND wait_event = 'PgSleep' AND query ILIKE $2",
                self.c.database, f"%{self.table.split('.')[1]}%")]
        finally:
            await conn.close()

    async def wait_sleeping(self, limit: float = 60) -> list[int]:
        t0 = time.monotonic()
        while time.monotonic() - t0 < limit:
            if pids := await self.sleeping():
                return pids
            await asyncio.sleep(0.2)
        raise RuntimeError(f"{self.c.name}: nothing reached {self.point} within {limit:.0f} s")

    async def release(self, limit: float = 60) -> int:
        """End the backends asleep in the trigger (their process is gone: their transactions roll back) and drop it.
        A sleeping transaction holds a lock on the table that DROP TRIGGER waits for, and a restarted process may fall
        asleep in it again meanwhile, so: end the sleepers, try the drop briefly, repeat."""
        conn = await self._conn()
        ended, t0 = 0, time.monotonic()
        try:
            await conn.execute("SET lock_timeout = '2s'")
            while True:
                for (p,) in await conn.fetch(
                        "SELECT pid FROM pg_stat_activity WHERE datname = $1 AND wait_event = 'PgSleep'", self.c.database):
                    ended += bool(await conn.fetchval("SELECT pg_terminate_backend($1)", p))
                try:
                    await conn.execute(f"DROP TRIGGER IF EXISTS {self.name} ON {self.table}")
                    await conn.execute(f"DROP FUNCTION IF EXISTS {self.schema}.{self.name}()")
                    return ended
                except asyncpg.exceptions.LockNotAvailableError:
                    if time.monotonic() - t0 > limit:
                        raise
        finally:
            await conn.close()
