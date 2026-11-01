"""Front doors (suite E). On Docker and Podman a shell or component with several replicas maps no host port (BrickKit
refuses one), yet the load driver, the scenarios and the checks reach every component on host port 10000 + its port.
A front door is one HAProxy container per replicated service, on the project's network: it takes that host port and
spreads the requests over the replicas, one request at a time, resolving the service name through the engine's DNS
so that a replica restarting on another address is followed. A replicated shell gets one for itself and one for each
member (the members are network aliases of the shell's containers). Kubernetes needs none: the NodePort Service
selects every Pod. cell-chain.sh starts them after `brickkit up` and removes them before `brickkit down`, whose
network removal fails while a front door is still attached.
python -m cells front <up|down> <cell directory>"""
from __future__ import annotations

import pathlib
import subprocess
import sys

import yaml

from cells.spec import HOST_BAND, PORTS, SHELL_PORTS, Cell
from cells.units import wait_answering

IMAGE = "docker.io/library/haproxy@sha256:5893840576071e6217cd52ebdcc14f454c6f7a631056fa6f4e0f40d3cac4215b"  # 3.2.26


def doors(c: Cell) -> list[tuple[str, str, int, int]]:
    """(container name, service name on the network, port, host port) for every replicated shell, its members and
    every replicated component running alone."""
    if c.target == "k8s":
        return []
    versions = {e["id"]: e["version"] for e in yaml.safe_load((c.dir / "brickkit.yaml").read_text())["components"]}
    out = []
    for what, n in c.replicas.items():
        if n < 2:
            continue
        for t in [what, *c.shells.get(what, [])]:
            port = SHELL_PORTS[t] if t in SHELL_PORTS else PORTS[t][0]
            service = f"{t.replace('/', '-')}-{versions[t].replace('.', '-')}"
            out.append((f"front-{c.project}-{t.replace('/', '-')}", service, port, HOST_BAND + port))
    return out


def config(service: str, port: int) -> str:
    """HTTP mode, so every request is balanced on its own even over a kept-alive connection; the engine's DNS (from the
    container's resolv.conf: Docker's 127.0.0.11, Podman's network gateway) re-read every second. A replica that stops
    is out after two failed checks, and a request whose connection to it fails goes to another (redispatch, 2 retries,
    1 s to connect), as an edge does: E3 measures the components' stop, not the door's. Only connection failures are
    retried (the default retry-on), never a request that reached a replica, so no write runs twice."""
    return f"""global
    log stdout format raw local0 notice
defaults
    mode http
    log global
    retries 2
    option redispatch
    timeout connect 1s
    timeout client 120s
    timeout server 120s
resolvers engine
    parse-resolv-conf
    hold valid 1s
    hold obsolete 5s
frontend in
    bind :{port}
    default_backend replicas
backend replicas
    balance roundrobin
    option httpchk GET /healthz
    http-check expect status 200
    server-template r 1-8 {service}:{port} check inter 1s fall 2 rise 1 resolvers engine init-addr none resolve-prefer ipv4
"""


def up(c: Cell) -> list[str]:
    down(c)
    made = []
    folder = c.dir / ".front"
    folder.mkdir(exist_ok=True)
    for name, service, port, host_port in doors(c):
        cfg = folder / f"{name}.cfg"
        cfg.write_text(config(service, port))
        subprocess.run([c.target, "run", "-d", "--name", name, "--network", f"brickkit-{c.project}-net",
                        "-p", f"{host_port}:{port}", "-v", f"{cfg}:/usr/local/etc/haproxy/haproxy.cfg:ro", IMAGE],
                       check=True, capture_output=True, text=True)
        made.append(f"{name}:{host_port}->{service}:{port}")
    wait_answering([d[3] for d in doors(c)])
    return made


def down(c: Cell) -> None:
    if c.target == "k8s":  # no doors there (doors())
        return
    names = subprocess.run([c.target, "ps", "-a", "--format", "{{.Names}}"], capture_output=True,
                           text=True).stdout.split()
    stale = [n for n in names if n.startswith(f"front-{c.project}-")]
    if stale:
        subprocess.run([c.target, "rm", "-f", *stale], check=True, capture_output=True)


if __name__ == "__main__":
    action, cell = sys.argv[1], Cell.load(pathlib.Path(sys.argv[2]))
    if action == "up":
        print("\n".join(up(cell)) or "no front doors")
    else:
        down(cell)
