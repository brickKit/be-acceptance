"""A component's log in a running cell, read on the cell's own engine (suite D looks for a trace in a probe's access
log): the container's on Docker and Podman, the pod's on Kubernetes, and for a process on this machine (the L, LD and
LP rows) its lines in the mode: local session's log, whose path cells/localrun.py records when it starts the session."""
from __future__ import annotations

import os
import pathlib
import subprocess

from cells.spec import Cell


def session_record(c: Cell) -> pathlib.Path:
    """Where cells/localrun.py writes the path of the cell's session log (beside the forwarder's PID file)."""
    return pathlib.Path(os.environ.get("XDG_RUNTIME_DIR", "/tmp")) / f"be-cell-session-{c.name}.path"


def of(c: Cell, component: str) -> str:
    """Everything the component (or the shell that hosts it) wrote so far."""
    host = next((s for s, ms in c.shells.items() if component in ms), component)
    service = f"{host.replace('/', '-')}-{c.versions[host].replace('.', '-')}"
    if host in c.local:  # a session line is "<service>  | <the process's line>"
        log = pathlib.Path(session_record(c).read_text().strip())
        return "\n".join(line.split("|", 1)[1].lstrip() for line in log.read_text().splitlines()
                         if "|" in line and line.split("|", 1)[0].strip() == service)
    if c.target == "k8s":
        kube = ["kubectl", "--context", "bek", "-n", c.project]
        pods = subprocess.run([*kube, "get", "pods", "-o", "name"], capture_output=True, text=True).stdout.split()
        return "\n".join(subprocess.run([*kube, "logs", p, "--all-containers"], capture_output=True, text=True).stdout
                         for p in pods if p.startswith(f"pod/{service}-") and "-migration-" not in p)
    r = subprocess.run([c.target, "logs", c.container_of(component)], capture_output=True, text=True)
    return r.stdout + r.stderr
