"""A cell's spec (cell.yaml beside its brickkit.yaml): which probes form the call chain, the bus, the database, and the
placement (which shell hosts which probes, which run alone, at exact versions); create.py writes the BrickKit project
from it."""
from __future__ import annotations

import pathlib
from dataclasses import dataclass, field

import yaml

PROBE_PORTS = {"test/probe-go": (8600, 9600), "test/probe-go2": (8602, 9602), "test/probe-go3": (8603, 9603),
               "test/probe-go4": (8604, 9604), "test/probe-py": (8601, 9601), "test/probe-py2": (8605, 9605)}
# the security members a cell may run (alone or in a shell): their user port and system port (0: none)
INFRA_PORTS = {"infra/iam-casdoor": (8200, 0), "infra/authz": (8223, 9223), "infra/audit": (8206, 9206)}
# the master data components (step 6), with the security members always
MDM_PORTS = {"mdm/org": (8083, 9093), "mdm/currency": (8226, 9226), "mdm/partner": (8225, 9225), "mdm/product": (8082, 9092)}
# the business components of step 7 (the trading chain) and the print component
ERP_PORTS = {"erp/inventory": (8086, 9096), "erp/credit": (8088, 9088), "erp/sales": (8084, 9094),
             "erp/billing": (8089, 9089), "infra/print": (8400, 9400)}
PORTS = {**PROBE_PORTS, **INFRA_PORTS, **MDM_PORTS, **ERP_PORTS}
SHELL_ROLES = {"be/go-core": "shell_go_core", "be/go-infra": "shell_go_infra", "be/py-render": "shell_py_render"}
SHELL_PORTS = {"be/go-core": 8090, "be/go-infra": 8224, "be/py-render": 8402}
# cells expose every port at 10000 + port (18090-18605): below Linux's ephemeral range (32768-60999), where any
# outgoing connection or test listener may take a port at random (a cell once failed to start on 38090, 2026-10-18),
# and clear of the registry's ports
HOST_BAND = 10000


def schema_of(component_id: str) -> str:
    """test/probe-go2 -> test_probe_go2 (registry/schemas.tsv)."""
    return component_id.replace("/", "_").replace("-", "_")


def stream_of(component_id: str) -> str:
    return component_id.replace("/", ".").replace("-", "_")


def file_of(component_id: str) -> str:
    """BrickKit's config file name: test/probe-go2 -> test-probe-go2."""
    return component_id.replace("/", "-")


@dataclass
class Cell:
    dir: pathlib.Path
    name: str
    chain: list[str]  # each probe calls the next and consumes the previous one's stream; the last calls the first; empty: none
    database: str = "be_experiment"
    bus: str = "jetstream"  # or pgqueue
    deployment: str = "0190f3a1-0000-7000-8000-00000000e001"
    shells: dict[str, list[str]] = field(default_factory=dict)  # shell -> hosted members
    alone: list[str] = field(default_factory=list)  # probes running alone
    concurrency: int = 1  # messages each probe's subscription handles at once (BUS_SUBSCRIPTION_CONCURRENCY, P6.31)
    # the run form (experiment plan, suite A rows): D Docker, P Podman (rootless), K Kubernetes (minikube), L processes
    # on this machine (mode: local), LD and LP some on this machine and the rest on Docker or Podman; the base
    # resources stay in Docker for every row (step 9 plan)
    row: str = "D"
    # what runs as a process on this machine: shells and components running alone (every one in the L row)
    local: list[str] = field(default_factory=list)
    # a connection pooler in front of PostgreSQL (step 10, B1): "" none, or "pgbouncer" (transaction mode); the running
    # connections go through it, the migrations straight to the database
    pooler: str = ""
    baseline_scale: int = 1  # the baseline data times this (step 10, B5: 2)
    # where the base resources run: "" in Docker on this machine (every row), "cluster" in the K row's own cluster,
    # namespace be-base, behind one Service (step 10, B7)
    base: str = ""

    @property
    def base_host(self) -> str:
        """How the components reach the base resources' host ports."""
        # a mode: local process gets host.docker.internal rewritten to localhost by BrickKit
        if self.base == "cluster":
            return "base.be-base.svc.cluster.local"
        return "host.minikube.internal" if self.row == "K" else "host.docker.internal"

    @property
    def target(self) -> str:
        return {"P": "podman", "LP": "podman", "K": "k8s"}.get(self.row, "docker")

    @property
    def has_local(self) -> bool:
        """Some of the cell runs as processes on this machine (the L, LD and LP rows)."""
        return bool(self.local)

    @staticmethod
    def load(directory: str | pathlib.Path) -> Cell:
        d = pathlib.Path(directory).resolve()
        spec = yaml.safe_load((d / "cell.yaml").read_text())
        shells = {k.split("@")[0]: list(v) for k, v in (spec.get("shells") or {}).items()}
        alone = [a.split("@")[0] for a in spec.get("alone") or []]
        row = spec.get("row", "D")
        local = [*shells, *alone] if row == "L" else [x.split("@")[0] for x in spec.get("local") or []]
        c = Cell(d, spec["cell"], spec.get("chain") or [], spec.get("database", "be_experiment"), spec.get("bus", "jetstream"),
                 shells=shells, alone=alone, concurrency=int(spec.get("subscription_concurrency", 1)),
                 row=row, local=local, pooler=spec.get("pooler", ""),
                 baseline_scale=int(spec.get("baseline_scale", 1)), base=spec.get("base", ""))
        if c.base not in ("", "cluster") or (c.base == "cluster" and c.row != "K"):
            raise ValueError(f"{c.name}: base {c.base!r}: only the K row runs its base resources in the cluster")
        if c.pooler not in ("", "pgbouncer"):
            raise ValueError(f"{c.name}: unknown pooler {c.pooler!r} (pgbouncer or none)")
        stray = [x for x in c.local if x not in c.shells and x not in c.alone]
        if stray:
            raise ValueError(f"{c.name}: local names what is neither a shell nor a component running alone: {stray}")
        unknown = [p for p in c.chain if p not in c.probes]
        if unknown:
            raise ValueError(f"{c.name}: the chain names probes the cell does not run: {unknown}")
        return c

    @property
    def placed(self) -> list[str]:
        """Every component the cell runs: alone or as a shell's member."""
        return self.alone + [m for ms in self.shells.values() for m in ms]

    @property
    def probes(self) -> list[str]:
        return [p for p in self.placed if p in PROBE_PORTS]

    @property
    def infra(self) -> list[str]:
        """The security members the cell runs; with them the probes' routes need a token (PROBE_GUARD)."""
        return [p for p in self.placed if p in INFRA_PORTS]

    @property
    def mdm(self) -> list[str]:
        """The master data components the cell runs (they need the security members)."""
        return [p for p in self.placed if p in MDM_PORTS]

    @property
    def erp(self) -> list[str]:
        """The business components and the print component the cell runs (they need the security members)."""
        return [p for p in self.placed if p in ERP_PORTS]

    @property
    def secured(self) -> bool:
        return set(self.infra) == set(INFRA_PORTS)

    def next_of(self, probe: str) -> str | None:
        if probe not in self.chain or len(self.chain) < 2:
            return None
        return self.chain[(self.chain.index(probe) + 1) % len(self.chain)]

    def previous_of(self, probe: str) -> str | None:
        if probe not in self.chain or len(self.chain) < 2:
            return None
        return self.chain[self.chain.index(probe) - 1]

    def host_port(self, component: str) -> int:
        """The host port a component's user surface or a shell's own port is exposed on."""
        return HOST_BAND + (PORTS[component][0] if component in PORTS else SHELL_PORTS[component])

    def container_of(self, component: str) -> str:
        """The container a probe runs in (its shell's, or its own) or a shell's."""
        host = next((s for s, ms in self.shells.items() if component in ms), component)
        version = self.versions[host]
        return f"brickkit-{self.project}-{host.replace('/', '-')}-{version.replace('.', '-')}-1"

    def unit_of(self, component: str) -> str:
        """The sampler's reading that holds a component (in its shell's process when it is a member): the container on
        Docker and Podman, the pod on Kubernetes, the process on this machine (cells/units.py, cells/localrun.py)."""
        host = next((s for s, ms in self.shells.items() if component in ms), component)
        if host in self.local:
            return f"proc:{host}"
        if self.row == "K":
            return f"pod:{host.replace('/', '-')}-{self.versions[host].replace('.', '-')}"
        return self.container_of(host)

    @property
    def project(self) -> str:
        return self.name.lower()

    @property
    def versions_of_probes(self) -> dict[str, str]:
        """Each probe's version: its own when alone, its shell's member version when hosted."""
        out = {p: v for p, v in self.versions.items() if p in PROBE_PORTS}
        bk = yaml.safe_load((self.dir / "brickkit.yaml").read_text())
        out.update({e["id"]: e["version"] for e in bk.get("components") or [] if e["id"] in PROBE_PORTS})
        return out

    @property
    def versions(self) -> dict[str, str]:
        spec = yaml.safe_load((self.dir / "cell.yaml").read_text())
        return {ref.split("@")[0]: ref.split("@")[1] for ref in [*(spec.get("shells") or {}), *(spec.get("alone") or [])]}
