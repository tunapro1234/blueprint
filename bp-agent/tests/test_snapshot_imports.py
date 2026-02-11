from pathlib import Path

import pytest

import bp_agent.agent as agent


def test_snapshot_history_flat_layout():
    root = Path(__file__).resolve().parents[1] / "src"
    state_dir = agent._detect_state_dir(root)
    state_path = state_dir / "state.yaml"

    if not state_path.exists():
        pytest.skip("state.yaml not found")

    pinned = agent._load_pinned_deps(state_path)
    if not pinned:
        pytest.skip("no pinned deps in state.yaml")

    missing = []
    impl_dirs = []
    for dep_path, snapshot in pinned.items():
        dep_dir = (root / dep_path).resolve()
        dep_state_dir = agent._detect_state_dir(dep_dir)
        snapshot_dir = dep_state_dir / "history" / snapshot
        if not snapshot_dir.exists():
            missing.append(str(snapshot_dir))
            continue
        if (snapshot_dir / "impl").exists():
            impl_dirs.append(str(snapshot_dir / "impl"))

    assert not missing, f"Missing snapshot directories: {missing}"
    assert not impl_dirs, f"Legacy impl directories found in snapshots: {impl_dirs}"
