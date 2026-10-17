"""Runs the Python probe alone (serve, migrate); the Python shell lists probe_py.definition() instead."""
from besdk import launcher
from probe_py import definition

launcher.main(definition())
