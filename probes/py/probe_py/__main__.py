"""Runs one Python probe variant alone (serve, migrate), the one the platform names in COMPONENT_ID; the Python shell
lists probe_py.definitions() instead. Choosing among the definitions is the entry point's job, never module code's:
this file is not loaded in a shell."""
import os

from besdk import launcher
from probe_py import VARIANTS, by_id, definition

launcher.main(definition(by_id(os.environ.get("COMPONENT_ID", "")) or VARIANTS[0]))
