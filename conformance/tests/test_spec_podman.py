"""How a P-row cell runs on Podman (cells/spec.py): rootful or rootless, compose or Quadlet units."""
from cells.spec import Cell


def cell(tmp, extra: str):
    (tmp / "cell.yaml").write_text(f"cell: Q\nrow: P\nalone: [mdm/org@3.0.10]\nchain: []\n{extra}")
    return Cell.load(tmp)


def test_only_root_runs_up_for_a_rootful_cell_with_quadlet_units(tmp_path):
    """FRR-025 retest: Quadlet units for rootful Podman go into the system's systemd, which only root may write, so the
    runner can't run `brickkit up` itself (as the user it would install a second, rootless copy of the project)."""
    assert cell(tmp_path, "podman: rootful\nunits: quadlet\n").root_runs_up
    assert not cell(tmp_path, "units: quadlet\n").root_runs_up
    assert not cell(tmp_path, "podman: rootful\n").root_runs_up  # compose: through root's socket
    assert not cell(tmp_path, "").root_runs_up
