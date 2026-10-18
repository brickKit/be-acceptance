"""A cell's spec (cell.yaml beside its brickkit.yaml): which probes form the call chain, the bus, the database, and the
placement (which shell hosts which probes, which run alone, at exact versions); create.py writes the BrickKit project
from it."""
from __future__ import annotations

import pathlib
from dataclasses import dataclass, field

import yaml

PROBE_PORTS = {"test/probe-go": (8600, 9600), "test/probe-go2": (8602, 9602), "test/probe-go3": (8603, 9603),
               "test/probe-go4": (8604, 9604), "test/probe-py": (8601, 9601), "test/probe-py2": (8605, 9605)}
SHELL_ROLES = {"be/go-core": "shell_go_core", "be/go-infra": "shell_go_infra", "be/py-render": "shell_py_render"}
SHELL_PORTS = {"be/go-core": 8090, "be/go-infra": 8224, "be/py-render": 8402}
HOST_BAND = 30000  # cells expose every port at 30000 + port: a band of their own, clear of the registry's 2xxxx


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
    chain: list[str]  # each probe calls the next and consumes the previous one's stream; the last calls the first
    database: str = "be_experiment"
    bus: str = "jetstream"  # or pgqueue
    deployment: str = "0190f3a1-0000-7000-8000-00000000e001"
    shells: dict[str, list[str]] = field(default_factory=dict)  # shell -> hosted members
    alone: list[str] = field(default_factory=list)  # probes running alone

    @staticmethod
    def load(directory: str | pathlib.Path) -> Cell:
        d = pathlib.Path(directory).resolve()
        spec = yaml.safe_load((d / "cell.yaml").read_text())
        shells = {k.split("@")[0]: list(v) for k, v in (spec.get("shells") or {}).items()}
        alone = [a.split("@")[0] for a in spec.get("alone") or []]
        c = Cell(d, spec["cell"], spec["chain"], spec.get("database", "be_experiment"), spec.get("bus", "jetstream"),
                 shells=shells, alone=alone)
        unknown = [p for p in c.chain if p not in c.probes]
        if unknown:
            raise ValueError(f"{c.name}: the chain names probes the cell does not run: {unknown}")
        return c

    @property
    def probes(self) -> list[str]:
        return self.alone + [m for ms in self.shells.values() for m in ms]

    def next_of(self, probe: str) -> str | None:
        if probe not in self.chain or len(self.chain) < 2:
            return None
        return self.chain[(self.chain.index(probe) + 1) % len(self.chain)]

    def previous_of(self, probe: str) -> str | None:
        if probe not in self.chain or len(self.chain) < 2:
            return None
        return self.chain[self.chain.index(probe) - 1]

    def host_port(self, component: str) -> int:
        """The host port a probe's user surface or a shell's own port is exposed on."""
        return HOST_BAND + (PROBE_PORTS[component][0] if component in PROBE_PORTS else SHELL_PORTS[component])

    def container_of(self, component: str) -> str:
        """The container a probe runs in (its shell's, or its own) or a shell's."""
        host = next((s for s, ms in self.shells.items() if component in ms), component)
        version = self.versions[host]
        return f"brickkit-{self.project}-{host.replace('/', '-')}-{version.replace('.', '-')}-1"

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
