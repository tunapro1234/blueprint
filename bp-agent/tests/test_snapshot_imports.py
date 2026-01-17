from pathlib import Path
import sys

import pytest

import agent


def test_snapshot_impl_paths_in_sys_path():
    root = Path(__file__).resolve().parents[1] / "src"
    state_dir = agent._detect_state_dir(root)
    state_path = state_dir / "state.yaml"

    if not state_path.exists():
        pytest.skip("state.yaml not found")

    pinned = agent._load_pinned_deps(state_path)
    if not pinned:
        pytest.skip("no pinned deps in state.yaml")

    missing = []
    for dep_path, snapshot in pinned.items():
        dep_dir = (root / dep_path).resolve()
        dep_state_dir = agent._detect_state_dir(dep_dir)
        impl_dir = dep_state_dir / "history" / snapshot / "impl"
        if not impl_dir.exists():
            missing.append(str(impl_dir))
            continue
        if str(impl_dir) not in sys.path:
            missing.append(str(impl_dir))

    assert not missing, f"Missing snapshot impl paths in sys.path: {missing}"
