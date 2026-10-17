import pathlib
import sys

import pytest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))

from besdk.testing import World  # noqa: E402
from harness import ROOT, Spec, go_binary, PY  # noqa: E402


@pytest.fixture(scope="session")
def specs(tmp_path_factory):
    """The two probes: the Go one built once per session, the Python one run from its locked environment."""
    go = go_binary(tmp_path_factory.mktemp("probe-go") / "probe-go")
    return {"go": Spec("test/probe-go", "test_probe_go", "test.probe_go", 9600, go),
            "py": Spec("test/probe-py", "test_probe_py", "test.probe_py", 9601, PY)}


@pytest.fixture
async def world(tmp_path):
    w = await World(tmp_path).start()
    try:
        yield w
    finally:
        await w.stop()
