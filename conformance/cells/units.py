"""What the sampler reads besides Docker and Podman containers (measure/series.py Window.units): the Kubernetes row's
pods and the minikube node as a whole (the node minus the cell's pods is the cluster's own cost: control plane,
kubelet, CNI, kube-proxy), and the local row's processes on this machine. Each unit's read() gives memory (bytes,
page cache included where the cgroup counts it), anon (without page cache) and cumulative CPU seconds, or None."""
from __future__ import annotations

import json
import pathlib
import re
import subprocess

from cells.spec import PORTS, SHELL_PORTS, Cell
from measure import readings

KUBE = ["minikube", "-p", "bek", "kubectl", "--"]


class CgroupUnit:
    def __init__(self, path: pathlib.Path):
        self.path = path

    def read(self) -> dict | None:
        c = readings.cgroup("", "", self.path)
        return {"memory": c.memory, "anon": c.anon, "cpu": c.cpu} if c else None


def node_scope() -> pathlib.Path | None:
    f = readings.find("bek")  # the minikube node's container (profile bek)
    return f[1] if f else None


def pod_units(node: pathlib.Path, pods: list[dict], prefix: str) -> dict:
    """One unit per running pod, named <prefix>:<its app label or Deployment>, read from its cgroup under the node."""
    out = {}
    for p in pods:
        if p["status"].get("phase") != "Running":
            continue
        uid = p["metadata"]["uid"].replace("-", "_")
        found = next(node.glob(f"**/kubepods*pod{uid}.slice"), None)
        if found is None:
            raise RuntimeError(f"no cgroup for pod {p['metadata']['name']} under {node}")
        owner = p["metadata"].get("labels", {}).get("app") or p["metadata"]["name"].rsplit("-", 2)[0]
        out[f"{prefix}:{owner}"] = CgroupUnit(found)
    return out


def pods_in(namespace: str) -> list[dict]:
    return json.loads(subprocess.run([*KUBE, "get", "pods", "-n", namespace, "-o", "json"], capture_output=True,
                                     text=True, check=True).stdout)["items"]


def kube_units(c: Cell) -> dict:
    """One unit per pod of the cell (pod:<name>), the base resources' pods when they run in the cluster (base:<name>,
    namespace be-base, step 10 B7), and the node."""
    node = node_scope()
    if node is None:
        raise RuntimeError("the minikube node (profile bek) is not running")
    units = {"minikube-node": CgroupUnit(node), **pod_units(node, pods_in(c.project), "pod")}
    if c.base == "cluster":
        units |= pod_units(node, pods_in("be-base"), "base")
    return units


def nodeports(c: Cell) -> list[str]:
    """For every component the cell runs, a NodePort Service beside BrickKit's own (same selector and port) on node
    port 30000 + (port - 8000); the minikube node publishes those on host port 10000 + port, where the driver, the
    seeds and the scenarios reach every row's components. A shell named in cell.yaml may not be running (R5 moved its
    members out and it left the project): its members have their own Services either way, so it is skipped."""
    svcs = json.loads(subprocess.run([*KUBE, "get", "svc", "-n", c.project, "-o", "json"], capture_output=True,
                                     text=True, check=True).stdout)["items"]
    made = []
    for comp in [*c.placed, *c.shells]:
        port = PORTS[comp][0] if comp in PORTS else SHELL_PORTS[comp]
        name = comp.replace("/", "-")
        svc = next((s for s in svcs if s["metadata"]["name"].startswith(name) and not s["metadata"]["name"].startswith("np-")
                    and any(p["port"] == port for p in s["spec"].get("ports", []))), None)
        if svc is None and comp in c.shells:
            continue
        if svc is None:
            raise RuntimeError(f"no Service of {comp} on port {port} in namespace {c.project}")
        manifest = {"apiVersion": "v1", "kind": "Service",
                    "metadata": {"name": f"np-{name}", "namespace": c.project},
                    "spec": {"type": "NodePort", "selector": svc["spec"]["selector"],
                             "ports": [{"port": port, "targetPort": port, "nodePort": 30000 + port - 8000}]}}
        subprocess.run([*KUBE, "apply", "-f", "-"], input=json.dumps(manifest), text=True, check=True,
                       capture_output=True)
        made.append(f"np-{name}:{30000 + port - 8000}")
    return made


def host_ports() -> str:
    """minikube start --ports: every component's and shell's host port to its node port."""
    ports = [p[0] for p in PORTS.values()] + list(SHELL_PORTS.values())
    return ",".join(f"{10000 + p}:{30000 + p - 8000}" for p in sorted(set(ports)) if 8000 <= p < 10768)


def local_units(c: Cell) -> dict:
    from cells.localrun import local_units as units
    return units(c)
