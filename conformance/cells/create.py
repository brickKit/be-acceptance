"""Creates a cell's BrickKit project from its cell.yaml, from scratch every time: init, the two install sources, the
shells and lone probes at their exact versions, then the deploy file with each probe placed as the cell says and exposed on host port 10000 + its port (the
driver and the profile reach it there) (members
compiled into a shell but not hosted in this cell are removed from the project). Rebuilding rather than upgrading
gives every cell a clean project made only from cell.yaml."""
from __future__ import annotations

import pathlib
import shutil
import subprocess

import yaml

from cells.spec import HOST_BAND, PORTS, SHELL_PORTS

SOURCES = """  - name: acceptance        # the probes (one repository, tags test-probe-<variant>/<version>)
    type: git
    repo: https://github.com/brickKit/be-acceptance
  - name: brickkit-github    # one component per repository (the shells): be/go-core -> be-go-core
    type: git
    baseUrl: https://github.com/brickKit/
"""


def brickkit(d: pathlib.Path, *args: str, stdin: str = "") -> str:
    r = subprocess.run(["brickkit", *args], cwd=d, input=stdin, capture_output=True, text=True, timeout=600)
    if r.returncode != 0:
        raise RuntimeError(f"brickkit {' '.join(args)}: {r.stdout}{r.stderr}")
    return r.stdout


def remove_all(d: pathlib.Path, ids: list[str]) -> None:
    """Removes components brickkit.yaml lists but the cell does not run; brickkit refuses to remove one another still
    requires, so the ones refused go again after the rest, until a pass removes nothing (then the first error)."""
    # a component brickkit.yaml lists at several versions (two shells of different versions compile in different ones)
    # is removed version by version: brickkit asks which one
    declared = yaml.safe_load((d / "brickkit.yaml").read_text())["components"]
    left = []
    for c in ids:
        versions = [e["version"] for e in declared if e["id"] == c]
        left += [f"{c}@{v}" for v in versions] if len(versions) > 1 else [c]
    while left:
        refused = []
        for c in left:
            try:
                brickkit(d, "remove", c)
            except RuntimeError as e:
                if "is not in the project" not in str(e):  # gone with another version that kept it (requiredBy)
                    refused.append((c, e))
        if len(refused) == len(left):
            raise refused[0][1]
        left = [c for c, _ in refused]


def deploy_file(name: str, shells: dict[str, list[str]], alone: list[str], extra: list[str] = (),
                kept: list[str] = (), row: str = "D", local: frozenset[str] = frozenset(), also: list[str] = (),
                replicas: dict[str, int] | None = None, units: str = "") -> str:
    """The cell's deploy file; kept are the versions brickkit.yaml keeps for a dependent (id@version, requiredBy):
    each needs its own entry, run alone and not exposed. The row picks the target: Docker and Podman map each
    component's user surface to host port 10000 + its port; Kubernetes exposes nothing (the cell adds NodePort
    Services that the minikube node publishes on the same host ports, cells/kube.py); what runs on this machine (local:
    every shell and component in the L row, the named ones in LD and LP) listens on its own port, and
    cells/localrun.py forwards the host port to it. A shell or component with replicas (suite E) runs that many
    copies; on Docker and Podman it, and a replicated shell's members, map no host port (BrickKit refuses one for
    several replicas): cells/front.py puts a front door on that port. units: quadlet (the P row only) runs the
    containers as Podman Quadlet units instead of compose (podman.units; FRR-025)."""
    replicas = replicas or {}
    target = {"P": "podman", "LP": "podman", "K": "k8s"}.get(row, "docker")
    if units and (row != "P" or units != "quadlet"):
        raise ValueError(f"{name}: units {units!r}: only quadlet, and only in the P row (BrickKit refuses it with mode: local)")
    out = [f"# deploy.yaml — cell {name}; written by be-acceptance/conformance/cells/create.py from cell.yaml",
           f"target: {target}"]
    if units:
        out += ["podman:", f"  units: {units}"]
    if row == "K":
        out += ["k8s:", "  context: bek", f"  namespace: {name.lower()}", "  createNamespace: true"]
    out += ["", "components:"]

    def place(what: str, port: int, indent: str) -> list[str]:
        if what in replicas:
            return [f"{indent}replicas: {replicas[what]}"]
        if row == "K":
            return []
        if what in local:  # on its own port; cells/localrun.py forwards 10000 + port to it, as Docker's proxy does
            return [f"{indent}mode: local"]
        return [f"{indent}expose: true", f"{indent}exposePort: {HOST_BAND + port}"]
    for shell, members in shells.items():
        out += [f"  - id: {shell}", *place(shell, SHELL_PORTS[shell], "    ")]
        hosted = [*members, *(extra if shell == next(iter(shells)) else [])]
        if hosted:
            out.append("    members:")
            for m in hosted:
                out.append(f"      - id: {m}")
                # a member of a local shell is in its process; one of a replicated shell is reached by its front door
                if m in PORTS and row != "K" and shell not in local and shell not in replicas:
                    out += place(m, PORTS[m][0], "        ")
    for p in alone:
        out += [f"  - id: {p}", *place(p, PORTS[p][0], "    ")]
    for k in kept:
        out.append(f"  - id: {k}")
        if k in also and row != "K":  # suite D's side-by-side version: reachable on its own host port
            out += [f"    expose: true", f"    exposePort: {HOST_BAND + 10 + PORTS[k.split('@')[0]][0]}"]
    return "\n".join(out) + "\n"


def create(directory: str | pathlib.Path) -> None:
    d = pathlib.Path(directory).resolve()
    spec = yaml.safe_load((d / "cell.yaml").read_text())
    shells = {k.split("@")[0]: v for k, v in (spec.get("shells") or {}).items()}
    alone = spec.get("alone") or []
    for name in ("brickkit.yaml", "deploy.yaml", "config", ".brickkit", "AGENTS.md", "CLAUDE.md", ".claude",
                 "components", "shell", ".gitignore"):
        p = d / name
        if p.is_dir():
            shutil.rmtree(p)
        elif p.exists():
            p.unlink()
    brickkit(d, "init", "--yes", "--no-skills")  # the directory already holds cell.yaml
    bk = d / "brickkit.yaml"
    bk.write_text(bk.read_text().replace("  # More sources: uncomment and fill in the address", SOURCES +
                                         "  # More sources: uncomment and fill in the address"))
    for ref in [*(spec.get("shells") or {}), *alone]:
        brickkit(d, "add", ref, "--yes")
    also = list(spec.get("also") or [])
    if also:  # older versions run beside the default one: compatibility versions for one of the cell's own components
        doc = yaml.safe_load(bk.read_text())
        for ref in also:
            cid, version = ref.split("@")
            other = next(a.split("@")[0] for a in alone if a.split("@")[0] != cid)
            doc["components"].append({"id": cid, "version": version, "requiredBy": [other]})
        bk.write_text(bk.read_text().split("components:")[0] + yaml.safe_dump({"components": doc["components"]},
                                                                               sort_keys=False))
    declared = yaml.safe_load(bk.read_text())["components"]
    added = [c["id"] for c in declared]
    kept = [f"{c['id']}@{c['version']}" for c in declared if c.get("requiredBy")]
    placed = {m for ms in shells.values() for m in ms} | {a.split("@")[0] for a in alone} | set(shells)
    # two shells of different versions may bring two versions of one compiled-in member: one entry per ID
    unhosted = list(dict.fromkeys(c for c in added if c not in placed))
    # a project lists exactly what its deploy file runs: compiled-in members this cell does not host are removed
    row = spec.get("row", "D")
    ids = [a.split("@")[0] for a in alone]
    local = frozenset([*shells, *ids] if row == "L" else [x.split("@")[0] for x in spec.get("local") or []])
    replicas = {k.split("@")[0]: int(v) for k, v in (spec.get("replicas") or {}).items()}
    units = spec.get("units", "")
    (d / "deploy.yaml").write_text(deploy_file(spec["cell"], shells, ids, unhosted, kept, row, local, also, replicas, units))
    remove_all(d, unhosted)
    # removing a member also removes the versions kept only for it (requiredBy): read what is kept again
    kept = [f"{c['id']}@{c['version']}" for c in yaml.safe_load(bk.read_text())["components"] if c.get("requiredBy")]
    (d / "deploy.yaml").write_text(deploy_file(spec["cell"], shells, ids, kept=kept, row=row, local=local, also=also, replicas=replicas, units=units))
    if local:  # mode: local runs from source: clone each local shell and component at its version into components/
        for comp in yaml.safe_load(bk.read_text())["components"]:
            if comp["id"] in local:
                brickkit(d, "add", f"{comp['id']}@{comp['version']}", "--yes", "--repo")
        # adding a shell's source brings its compiled-in members back: the ones this cell does not host go again
        back = [c["id"] for c in yaml.safe_load(bk.read_text())["components"] if c["id"] in unhosted]
        if back:
            (d / "deploy.yaml").write_text(deploy_file(spec["cell"], shells, ids, back, kept, row, local, also, replicas, units))
            remove_all(d, back)
            kept = [f"{c['id']}@{c['version']}" for c in yaml.safe_load(bk.read_text())["components"]
                    if c.get("requiredBy")]
            (d / "deploy.yaml").write_text(deploy_file(spec["cell"], shells, ids, kept=kept, row=row, local=local, also=also, replicas=replicas, units=units))
    gi = d / ".gitignore"
    gi.write_text(gi.read_text() + "\n# Generated by cells/create.py from cell.yaml: only cell.yaml is the cell's source\n"
                  "AGENTS.md\nCLAUDE.md\nshell/\n")
