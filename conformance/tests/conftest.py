import pathlib
import sys

import pytest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))

from besdk.testing import World  # noqa: E402
from harness import Spec, go_binary, manifest_version, py_probe  # noqa: E402


@pytest.fixture(scope="session")
def specs(tmp_path_factory):
    """Every probe component: the Go variants run one binary built once per session, the Python variants their locked
    environment; COMPONENT_ID picks the variant, as in the images."""
    go, py = go_binary(tmp_path_factory.mktemp("probe-go") / "probe-go"), py_probe()
    out = {}
    for kind, grpc in (("go", 9600), ("go2", 9602), ("go3", 9603), ("go4", 9604), ("py", 9601), ("py2", 9605)):
        out[kind] = Spec(f"test/probe-{kind}", f"test_probe_{kind}", f"test.probe_{kind}", grpc,
                         go if kind.startswith("go") else py, manifest_version(kind))
    return out


@pytest.fixture
async def world(tmp_path):
    w = await World(tmp_path).start()
    try:
        yield w
    finally:
        await w.stop()
