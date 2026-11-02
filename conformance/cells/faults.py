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
import threading
import time

import asyncpg

from cells import homes
from cells.spec import Cell, schema_of

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


def replica(c: Cell, host: str, n: int) -> str:
    """The container of a host's n-th replica (Docker and Podman number them from 1)."""
    return c.container_of(host).rsplit("-", 1)[0] + f"-{n}"


def ready(c: Cell, component: str) -> int:
    """How many replicas of the process that runs the component are running and pass their health check (Kubernetes:
    its Deployment's ready Pods; a Pod being deleted no longer counts)."""
    host = host_of(c, component)
    e = engine(c)
    if e in ("docker", "podman"):
        n = c.replicas.get(host, 1)
        out = subprocess.run([e, "inspect", "-f", "{{.State.Running}}/{{if .State.Health}}{{.State.Health.Status}}{{end}}",
                              *(replica(c, host, i) for i in range(1, n + 1))], capture_output=True, text=True).stdout
        return out.split().count("true/healthy")
    out = subprocess.run([*KUBE, "kubectl", "--", "-n", c.project, "get", "deploy", deployment(c, host), "-o",
                          "jsonpath={.status.readyReplicas}"], capture_output=True, text=True).stdout.strip()
    return int(out or 0)


def healthy(c: Cell, component: str) -> bool:
    """Every replica of the process that runs the component is ready."""
    return ready(c, component) == c.replicas.get(host_of(c, component), 1)


def wait_down(c: Cell, component: str, limit: float = 30) -> float:
    """Seconds until fewer replicas of the process are ready than it has: the stop has taken effect. Raises when it has
    not: the fault hit something else, and a test that only checks what follows would pass without it (step 13's
    K13-E1 deleted the migration Job's pod, no replica stopped, and E3 passed)."""
    t0 = time.monotonic()
    while time.monotonic() - t0 < limit:
        if not healthy(c, component):
            return time.monotonic() - t0
        time.sleep(0.2)
    raise RuntimeError(f"{c.name}: every replica of {host_of(c, component)} still ready {limit:.0f} s after the stop")


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
    Deployment scaled to 0 (or 1). A stop returns once it has taken effect (wait_down)."""
    host = host_of(c, component)
    e = engine(c)
    if e in ("docker", "podman"):
        _run([e, action, c.container_of(host)])
        stopped = c.container_of(host)
    else:
        stopped = deployment(c, host)
        _run([*KUBE, "kubectl", "--", "-n", c.project, "scale", "deploy", stopped,
              f"--replicas={0 if action == 'stop' else c.replicas.get(host, 1)}"])
    if action == "stop":
        wait_down(c, component)
    return stopped


def stop_replica(c: Cell, component: str) -> str:
    """Suite E3: stop one replica of the process that runs the component, cleanly (SIGTERM, its grace period): the
    last container on Docker and Podman (started again by start_replica), one Pod on Kubernetes (its Deployment
    makes another). Returns once fewer replicas are ready (wait_down)."""
    host = host_of(c, component)
    e = engine(c)
    if c.replicas.get(host, 1) < 2:
        raise ValueError(f"{c.name}: {host} has one replica")
    if e in ("docker", "podman"):
        name = replica(c, host, c.replicas[host])
        _run([e, "stop", name])
        wait_down(c, component)
        return name
    # the Deployment's running pods: the migration Job's pod shares the name prefix (<deployment>-migration-...)
    listed = _run([*KUBE, "kubectl", "--", "-n", c.project, "get", "pods", "-o",
                   "jsonpath={range .items[*]}{.metadata.name}={.status.phase} {end}"]).split()
    pods = [f"pod/{n}" for n, phase in (p.split("=", 1) for p in listed)
            if n.startswith(f"{deployment(c, host)}-") and "-migration-" not in n and phase == "Running"]
    _run([*KUBE, "kubectl", "--", "-n", c.project, "delete", pods[-1], "--wait=false"])
    wait_down(c, component)
    return pods[-1]


def start_replica(c: Cell, name: str) -> None:
    """Start again what stop_replica stopped (Kubernetes needs nothing: the Deployment has made another Pod)."""
    e = engine(c)
    if e in ("docker", "podman"):
        _run([e, "start", name])


async def expired_leases(c: Cell, component: str) -> int:
    """The component's work items still running on a lease that has run out: no one has taken them over yet."""
    s = schema_of(component)
    conn, _ = await homes.home(c, s)
    try:
        return await conn.fetchval(f"SELECT count(*) FROM {s}.be_work_item WHERE state = 'running' AND lease_until < now()")
    finally:
        await conn.close()


def base(action: str, name: str) -> None:
    """Stop or start a base resource (be-postgres, be-nats, ...): they run in Docker for every row."""
    _run(["docker", action, name])


def backlog_summary(samples: list[tuple[float, int]], stopped: float, started: float) -> dict:
    """Samples (unix time, unpublished outbox rows) around a bus stop: the most in flight in the 10 s before the stop
    (the load always has a few rows on their way out), the peak after it and when (seconds from the stop), and how
    long after the start the backlog was back to that (None: never)."""
    before = max((n for t, n in samples if stopped - 10 <= t < stopped), default=0)
    after = [(t, n) for t, n in samples if t >= stopped]
    peak_t, peak = max(after, key=lambda x: x[1], default=(stopped, 0))
    drained = next((t for t, n in samples if t >= started and n <= before), None)
    return {"before": before, "peak": peak, "peak_at": round(peak_t - stopped, 1),
            "drained_after": None if drained is None else round(drained - started, 1)}


async def _conn(c: Cell) -> asyncpg.Connection:
    return await homes.checks(c)


async def outbox_backlog(c: Cell) -> int:
    """Outbox rows not yet published, over every producer's schema."""
    conn = await _conn(c)
    try:
        schemas = [s for (s,) in await conn.fetch(
            "SELECT table_schema FROM information_schema.tables WHERE table_name = 'be_outbox'")]
        return sum([await conn.fetchval(f'SELECT count(*) FROM "{s}".be_outbox WHERE published_at IS NULL') for s in schemas])
    finally:
        await conn.close()


async def projected_version(c: Cell, projection: str, record_id: str) -> int | None:
    """The version of a record in erp/sales' projection, once complete (None: not there or not complete)."""
    conn = await _conn(c)
    try:
        return await conn.fetchval("SELECT version FROM erp_sales.be_projection WHERE projection = $1 AND record_id = $2 "
                                   "AND state = 'complete'", projection, record_id)
    finally:
        await conn.close()


class Backlog:
    """Samples the outbox backlog once a second in a thread of its own, until stopped."""

    def __init__(self, c: Cell):
        self.c, self.samples, self._stop = c, [], threading.Event()
        self._thread = threading.Thread(target=lambda: asyncio.run(self._run()), daemon=True)

    async def _run(self) -> None:
        while not self._stop.is_set():
            t = time.time()
            try:
                self.samples.append((t, await outbox_backlog(self.c)))
            except (OSError, asyncpg.PostgresError):
                pass
            await asyncio.sleep(max(0.0, 1 - (time.time() - t)))

    def __enter__(self) -> "Backlog":
        self._thread.start()
        return self

    def __exit__(self, *_) -> None:
        self._stop.set()
        self._thread.join()


class Point:
    """A trigger that stops the coordinator's transaction for one order (POINTS) until released."""

    def __init__(self, c: Cell, point: str, order_id: str):
        self.c, self.point, self.order = c, point, order_id
        self.table, cond = POINTS[point]
        self.cond = cond.replace("$ORDER", order_id)
        self.name = f"be_fault_{point.replace('-', '_')}"
        self.schema = self.table.split(".")[0]

    async def _conn(self) -> asyncpg.Connection:
        """The trigger goes on the table itself: the database that holds its schema (cells/homes.py)."""
        conn, self.database = await homes.home(self.c, self.schema)
        return conn

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
                self.database, f"%{self.table.split('.')[1]}%")]
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
                        "SELECT pid FROM pg_stat_activity WHERE datname = $1 AND wait_event = 'PgSleep'", self.database):
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
