from __future__ import annotations

import os
import subprocess
import sys
from pathlib import Path


def _find_repo_root() -> Path:
    # src-py/bp_cli.py -> parents[1] is repo root
    path = Path(__file__).resolve()
    if len(path.parents) >= 2:
        return path.parents[1]
    return Path.cwd()


def _find_bp_binary() -> Path:
    env_path = os.environ.get("BP_GO_BINARY")
    if env_path:
        return Path(env_path).expanduser().resolve()
    root = _find_repo_root()
    return root / "bp"


def _build_bp_binary(bp_path: Path) -> None:
    root = _find_repo_root()
    src_go = root / "src-go"
    if not src_go.exists():
        raise FileNotFoundError(f"src-go not found at {src_go}")
    cmd = ["go", "build", "-o", str(bp_path), "./cmd/bp"]
    subprocess.run(cmd, cwd=str(src_go), check=True)


def _ensure_bp_binary() -> Path:
    bp_path = _find_bp_binary()
    if bp_path.exists():
        return bp_path
    _build_bp_binary(bp_path)
    return bp_path


def main(argv: list[str] | None = None) -> int:
    if argv is None:
        argv = sys.argv[1:]
    bp_path = _ensure_bp_binary()
    cmd = [str(bp_path), *argv]
    try:
        result = subprocess.run(cmd)
        return result.returncode
    except FileNotFoundError as exc:
        sys.stderr.write(f"bp backend not found: {exc}\n")
        return 1
    except subprocess.CalledProcessError as exc:
        return exc.returncode


if __name__ == "__main__":
    raise SystemExit(main())
