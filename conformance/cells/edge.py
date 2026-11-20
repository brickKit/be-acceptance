"""The edge in front of a cell (C12, frontend/pc's P3): Traefik with the be-edge plugin (be-protocol chapter 11), on the
cell's network, on host port 8200, the origin the identity member's callback is registered under in Casdoor
(IAM_CALLBACK_URL http://localhost:8200/api/iam/callback). Each placed component's user surface is routed by its
public prefix (P11.11: /api/<name>, or the one its definition declares), whole path segments only; the plugin
exchanges the session cookie for a token with the identity member's system surface over mTLS as infra/edge (the
certificate cells/prepare.py issues when the cell has `edge: true`) and serves the features document be-ops made from
the cell's brickkit.yaml. In a real deployment the assembly tool generates these routes (docs/zh/04-foundations/
09-edge.md); in a cell the suite does. cell-chain.sh starts it after `brickkit up` and removes it before `brickkit down`
(a container attached to the project's network stops the network's removal).
python -m cells edge <up|down> <cell directory>"""
from __future__ import annotations

import os
import pathlib
import subprocess
import time
import urllib.error
import urllib.request

import yaml

from cells.spec import PORTS, Cell, file_of

TRAEFIK = "traefik:v3.0.4@sha256:a208c74fd80a566d4ea376053bff73d31616d7af3f1465a7747b8b89ee34d97e"
PORT = 8200  # the edge's origin: http://localhost:8200
IAM = "infra/iam-casdoor"
IAM_SYSTEM_PORT = 9200  # its system surface (be.session.v1)
# public prefixes that are not /api/<name> (the components' definitions, P11.11)
PREFIXES = {"infra/iam-casdoor": "/api/iam"}
# the plugin's source: the assembly project's tools/be-edge (BE_EDGE_DIR overrides, e.g. for a snapshot)
EDGE_DIR = pathlib.Path(os.environ.get("BE_EDGE_DIR") or pathlib.Path(__file__).resolve().parents[4] / "tools" / "be-edge")


def name_of(c: Cell) -> str:
    return f"edge-{c.project}"


def prefix_of(component: str) -> str:
    return PREFIXES.get(component, "/api/" + component.split("/", 1)[1])


def routes(c: Cell) -> list[tuple[str, str, str]]:
    """(component, public prefix, its address on the cell's network) for every placed component with a user surface:
    the network alias BrickKit gives a member or a component, id and version with '-' (the generated compose.yaml)."""
    versions = {e["id"]: e["version"] for e in yaml.safe_load((c.dir / "brickkit.yaml").read_text())["components"]}
    out = []
    for comp in sorted(c.placed):
        if comp not in PORTS:
            continue
        alias = f"{comp.replace('/', '-')}-{versions[comp].replace('.', '-')}"
        out.append((comp, prefix_of(comp), f"http://{alias}:{PORTS[comp][0]}"))
    return out


def dynamic(c: Cell, features: pathlib.Path, tls: pathlib.Path) -> str:
    rs = routes(c)
    iam = next(addr for comp, _, addr in rs if comp == IAM).removeprefix("http://").rsplit(":", 1)[0]
    lines = ["http:", "  routers:"]
    for comp, prefix, _ in rs:
        r = file_of(comp)
        # a whole path segment: /api/sales and /api/sales/..., never /api/sales-x (P11.11)
        lines += [f"    {r}:", f"      rule: Path(`{prefix}`) || PathPrefix(`{prefix}/`)", "      entryPoints: [web]",
                  "      middlewares: [session]", f"      service: {r}"]
    # the features document is the plugin's own (P11.8): any route through the middleware serves it
    lines += ["    features:", "      rule: Path(`/api/features`)", "      entryPoints: [web]", "      middlewares: [session]",
              f"      service: {file_of(IAM)}"]
    lines += ["  middlewares:", "    session:", "      plugin:", "        beedge:",
              f"          identityAddress: \"{iam}:{IAM_SYSTEM_PORT}\"", f"          identityComponent: {IAM}",
              f"          deployment: \"{c.deployment}\"",
              f"          certFile: {tls / 'infra-edge.cert.pem'}", f"          keyFile: {tls / 'infra-edge.key.pem'}",
              f"          caFile: {tls / 'ca.pem'}", f"          featuresFile: {features}", "  services:"]
    for comp, _, addr in rs:
        lines += [f"    {file_of(comp)}:", "      loadBalancer:", "        servers:", f"          - url: {addr}"]
    return "\n".join(lines) + "\n"


def up(c: Cell) -> list[str]:
    if c.target != "docker" and c.target != "podman":
        raise RuntimeError(f"{c.name}: the edge runs beside Docker and Podman cells only")
    if IAM not in c.placed:
        raise RuntimeError(f"{c.name}: the edge needs the identity member")
    down(c)
    folder = c.dir / ".secrets" / "edge"
    folder.mkdir(parents=True, exist_ok=True)
    tls = c.dir / ".secrets" / "tls"
    features = folder / "features.json"
    subprocess.run([os.environ["BE_OPS"], "features", "--root", str(c.dir), "--out", str(features)], check=True,
                   capture_output=True, text=True)
    features.chmod(0o644)
    (folder / "traefik.yml").write_text(f"""entryPoints:
  web:
    address: ":{PORT}"
providers:
  file:
    filename: {folder}/dynamic.yml
log:
  level: INFO
experimental:
  localPlugins:
    beedge:
      moduleName: github.com/brickKit/be-edge/v3
""")
    (folder / "dynamic.yml").write_text(dynamic(c, features, tls))
    for f in folder.iterdir():
        f.chmod(0o644)
    subprocess.run([c.target, "run", "-d", "--name", name_of(c), "--network", f"brickkit-{c.project}-net",
                    "-p", f"{PORT}:{PORT}", "-v", f"{folder}:{folder}:ro", "-v", f"{tls}:{tls}:ro",
                    "-v", f"{EDGE_DIR}:/plugins-local/src/github.com/brickKit/be-edge/v3:ro",
                    TRAEFIK, f"--configFile={folder}/traefik.yml"], check=True, capture_output=True, text=True)
    wait_ready()
    return [f"{name_of(c)}: http://localhost:{PORT}", *(f"{p} -> {a}" for _, p, a in routes(c))]


def wait_ready(timeout: float = 90) -> None:
    """Until the plugin answers: /api/features without a session is the plugin's own 401 (Traefik compiles the plugin
    with Yaegi at start, which takes seconds); a 404 is Traefik before the routes are loaded."""
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        try:
            urllib.request.urlopen(f"http://127.0.0.1:{PORT}/api/features", timeout=2)
        except urllib.error.HTTPError as e:
            if e.code == 401:
                return
        except OSError:
            pass
        time.sleep(0.5)
    raise RuntimeError(f"the edge did not answer on {PORT} within {timeout:.0f} s")


def down(c: Cell) -> None:
    subprocess.run([c.target, "rm", "-f", name_of(c)], capture_output=True)
