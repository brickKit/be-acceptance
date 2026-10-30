"""Suite C: faults against a running, seeded cell under the background load (see README.md). The fixtures are the L3
scenarios' (scenarios/conftest.py): the cell, its test accounts and the seed data the orders start from."""
from __future__ import annotations

import importlib.util
import pathlib

_spec = importlib.util.spec_from_file_location(
    "l3_scenarios_conftest", pathlib.Path(__file__).resolve().parent.parent / "scenarios" / "conftest.py")
l3 = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(l3)

cell, login, bob, world, pytest_configure = l3.cell, l3.login, l3.bob, l3.world, l3.pytest_configure
