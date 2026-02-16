"""Base class for benchmark task types."""

from __future__ import annotations

import subprocess
import sys
from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from pathlib import Path


@dataclass
class TaskResult:
    """Result from scoring a single task."""
    task_id: str
    passed: int = 0
    total: int = 0
    score: float = 0.0
    details: dict = field(default_factory=dict)


class TaskType(ABC):
    """Abstract base for benchmark task sources.

    Subclasses provide:
      - A list of task descriptors via load_tasks()
      - Workspace setup (clone repos, copy test files, etc.) via setup_workspace()
      - An instruction string the agent receives via get_instruction()
      - Optionally, blueprint support via setup_blueprint() / get_blueprint_instruction()
      - Scoring via score()
    """

    name: str  # e.g. "exercism", "swebench", "blueprint"

    @abstractmethod
    def load_tasks(self, limit: int | None = None) -> list[dict]:
        """Return a list of task descriptors.

        Each descriptor is a dict with at least {"task_id": str}.
        Additional keys are task-type-specific.
        """

    @abstractmethod
    def setup_workspace(self, task: dict, ws: Path) -> None:
        """Prepare the workspace directory for a single task.

        Called before the agent runs.  Should copy/create any files
        the agent needs (test files, starter code, repo checkout, etc.).
        """

    @abstractmethod
    def get_instruction(self, task: dict, ws: Path) -> str:
        """Return the instruction string the agent receives (no-blueprint mode)."""

    def setup_blueprint(self, task: dict, ws: Path) -> None:
        """Write BLUEPRINT.yaml (or equivalent) into the workspace.

        Called *after* setup_workspace when bp_mode == 'with_blueprint'.
        Default: no-op.  Override in subclasses that support blueprint mode.
        """

    def get_blueprint_instruction(self, task: dict, ws: Path) -> str:
        """Return the instruction for with-blueprint mode.

        Default: falls back to get_instruction() (no blueprint effect).
        Override to provide a blueprint-aware instruction.
        """
        return self.get_instruction(task, ws)

    @abstractmethod
    def score(self, task: dict, ws: Path) -> TaskResult:
        """Score the agent's output.  Returns a TaskResult."""


def run_pytest(ws: Path, test_path: str | None = None, timeout: int = 120) -> tuple[int, int, str]:
    """Run pytest in *ws* and return (passed, total, raw_output).

    Parameters
    ----------
    ws : Path
        Working directory where pytest should run.
    test_path : str | None
        Optional relative path to a specific test file/dir.
    timeout : int
        Maximum seconds before killing the process.
    """
    cmd = [sys.executable, "-m", "pytest", "-x", "-q", "--tb=short", "--no-header"]
    if test_path:
        cmd.append(test_path)

    try:
        proc = subprocess.run(
            cmd,
            cwd=str(ws),
            capture_output=True,
            text=True,
            timeout=timeout,
        )
    except subprocess.TimeoutExpired:
        return 0, 1, "TIMEOUT"

    output = proc.stdout + proc.stderr

    # Parse pytest summary line like "5 passed, 2 failed"
    passed = 0
    total = 0
    for line in output.splitlines():
        line = line.strip()
        # Look for the summary line: "X passed" / "X failed" / "X error"
        if "passed" in line or "failed" in line or "error" in line:
            import re
            p = re.search(r"(\d+) passed", line)
            f = re.search(r"(\d+) failed", line)
            e = re.search(r"(\d+) error", line)
            if p:
                passed = int(p.group(1))
            failed = int(f.group(1)) if f else 0
            errors = int(e.group(1)) if e else 0
            total = passed + failed + errors
            break

    return passed, total, output
