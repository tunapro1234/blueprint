"""Exercism Python exercise task type.

Workflow per task:
  1. Clone (or reuse cached) exercism/python repo
  2. Copy exercise directory into workspace
  3. Agent writes the solution file
  4. Score by running the exercise's test file with pytest
"""

from __future__ import annotations

import json
import shutil
import subprocess
from pathlib import Path

from .base import TaskType, TaskResult, run_pytest

# Exercises selected for a good spread of difficulty and concepts.
# Each maps to a directory under exercises/practice/ in the exercism/python repo.
DEFAULT_EXERCISES = [
    "hello-world",
    "two-fer",
    "resistor-color",
    "leap",
    "isogram",
    "pangram",
    "bob",
    "difference-of-squares",
    "grains",
    "acronym",
    "triangle",
    "etl",
    "scrabble-score",
    "hamming",
    "rna-transcription",
    "nucleotide-count",
    "word-count",
    "phone-number",
    "series",
    "flatten-array",
]

REPO_URL = "https://github.com/exercism/python.git"
CACHE_DIR = Path.home() / ".cache" / "bp-bench" / "exercism-python"


def _ensure_repo(shallow: bool = True) -> Path:
    """Clone the exercism/python repo if not already cached."""
    if (CACHE_DIR / ".git").is_dir():
        return CACHE_DIR
    CACHE_DIR.parent.mkdir(parents=True, exist_ok=True)
    cmd = ["git", "clone"]
    if shallow:
        cmd += ["--depth", "1"]
    cmd += [REPO_URL, str(CACHE_DIR)]
    subprocess.run(cmd, check=True, capture_output=True, text=True)
    return CACHE_DIR


def _exercise_dir(repo: Path, slug: str) -> Path:
    return repo / "exercises" / "practice" / slug


class ExercismTask(TaskType):
    """Exercism Python exercises — agent writes a solution, pytest scores it."""

    name = "exercism"

    def __init__(self, exercises: list[str] | None = None):
        self.exercises = exercises or DEFAULT_EXERCISES

    def load_tasks(self, limit: int | None = None) -> list[dict]:
        repo = _ensure_repo()
        tasks = []
        for slug in self.exercises:
            edir = _exercise_dir(repo, slug)
            if not edir.is_dir():
                continue

            # Read .meta/config.json for the solution filename
            meta = edir / ".meta" / "config.json"
            solution_file = f"{slug.replace('-', '_')}.py"
            if meta.exists():
                try:
                    cfg = json.loads(meta.read_text())
                    files = cfg.get("files", {})
                    sols = files.get("solution", [])
                    if sols:
                        solution_file = sols[0]
                except (json.JSONDecodeError, KeyError):
                    pass

            # Find the test file
            test_file = f"{slug.replace('-', '_')}_test.py"
            if not (edir / test_file).exists():
                # try alternate naming
                for candidate in edir.glob("*_test.py"):
                    test_file = candidate.name
                    break

            tasks.append({
                "task_id": f"exercism/{slug}",
                "slug": slug,
                "exercise_dir": str(edir),
                "solution_file": solution_file,
                "test_file": test_file,
            })
            if limit and len(tasks) >= limit:
                break
        return tasks

    def setup_workspace(self, task: dict, ws: Path) -> None:
        edir = Path(task["exercise_dir"])
        # Copy test file
        test_src = edir / task["test_file"]
        if test_src.exists():
            shutil.copy2(test_src, ws / task["test_file"])

        # Copy any helper/support files (but not the solution or .meta)
        for f in edir.iterdir():
            if f.name.startswith("."):
                continue
            if f.name == task["solution_file"]:
                continue
            if f.is_file() and f.name != task["test_file"]:
                shutil.copy2(f, ws / f.name)

        # Write an empty solution stub so the agent knows what file to create
        stub = ws / task["solution_file"]
        if not stub.exists():
            stub.write_text(f"# Implement your solution for {task['slug']} here\n")

    def get_instruction(self, task: dict, ws: Path) -> str:
        slug = task["slug"]
        solution_file = task["solution_file"]
        test_file = task["test_file"]

        # Try to read the exercise description
        edir = Path(task["exercise_dir"])
        description = ""
        for desc_path in [edir / ".docs" / "instructions.md", edir / "README.md"]:
            if desc_path.exists():
                description = desc_path.read_text()
                break

        return f"""\
You are solving the Exercism Python exercise "{slug}".

{description}

Your task:
- Read the test file `{test_file}` to understand what is expected.
- Implement the solution in `{solution_file}`.
- All tests in `{test_file}` must pass.
- Use only Python stdlib.
- Write the solution using write_file, then call give_result("done").
"""

    def setup_blueprint(self, task: dict, ws: Path) -> None:
        slug = task["slug"]
        solution_file = task["solution_file"]
        test_file = task["test_file"]

        # Extract function/class signatures from the test file
        signatures = []
        test_path = ws / test_file
        if test_path.exists():
            for line in test_path.read_text().splitlines():
                stripped = line.strip()
                # Look for imports from the solution module (reveals expected API)
                if stripped.startswith("from ") and slug.replace("-", "_") in stripped:
                    signatures.append(stripped)
                elif stripped.startswith("import ") and slug.replace("-", "_") in stripped:
                    signatures.append(stripped)

        # Read exercise description
        edir = Path(task["exercise_dir"])
        description = ""
        for desc_path in [edir / ".docs" / "instructions.md", edir / "README.md"]:
            if desc_path.exists():
                description = desc_path.read_text()
                # Truncate long descriptions
                if len(description) > 1500:
                    description = description[:1500] + "\n..."
                break

        api_section = ""
        if signatures:
            sigs = "\n".join(f'  - "{s}"' for s in signatures)
            api_section = f"api:\n{sigs}\n"

        blueprint = f"""\
_meta:
  version: "1"
intent: Implement the Exercism "{slug}" exercise in Python
{api_section}workflow:
  - step: read_tests
    action: Read {test_file} to understand expected behavior and API
  - step: plan
    action: Identify functions/classes to implement based on test expectations
  - step: implement
    action: Write the solution in {solution_file}
    constraints:
      - Use only Python stdlib
      - Match the exact function/class names expected by tests
      - Handle edge cases shown in tests
  - step: verify
    action: Review solution against test expectations before finalizing
"""
        if description:
            blueprint += f"context:\n  description: |\n"
            for line in description.splitlines()[:20]:
                blueprint += f"    {line}\n"

        (ws / "BLUEPRINT.yaml").write_text(blueprint, encoding="utf-8")

    def get_blueprint_instruction(self, task: dict, ws: Path) -> str:
        slug = task["slug"]
        solution_file = task["solution_file"]
        test_file = task["test_file"]
        return f"""\
You are solving the Exercism Python exercise "{slug}".

There is a BLUEPRINT.yaml in the workspace — read it first.
It describes the workflow and API expectations.

Follow the workflow steps:
1. Read {test_file} to understand what is expected
2. Plan your implementation based on test expectations
3. Implement the solution in {solution_file}
4. Review against test expectations

Use only Python stdlib. Write the solution using write_file, then call give_result("done").
"""

    def score(self, task: dict, ws: Path) -> TaskResult:
        test_file = task["test_file"]
        passed, total, output = run_pytest(ws, test_file)

        score = passed / total if total > 0 else 0.0
        return TaskResult(
            task_id=task["task_id"],
            passed=passed,
            total=total,
            score=round(score, 3),
            details={
                "test_file": test_file,
                "output": output[-2000:],  # truncate to last 2000 chars
            },
        )
