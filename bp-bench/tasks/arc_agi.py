"""ARC-AGI reasoning task type — tests abstract pattern recognition.

Clones the ARC-AGI repo (shallow), uses training tasks with known outputs.
Agent sees train examples + test inputs, must predict test outputs.
Scoring is exact grid match per test case.
"""

from __future__ import annotations

import json
import subprocess
from pathlib import Path

from .base import TaskType, TaskResult

REPO_URL = "https://github.com/fchollet/ARC-AGI.git"
CACHE_DIR = Path.home() / ".cache" / "bp-bench" / "arc-agi"

# Grid visualization: 0 → '.', other digits → themselves
SYMBOL_MAP = {0: ".", 1: "1", 2: "2", 3: "3", 4: "4",
              5: "5", 6: "6", 7: "7", 8: "8", 9: "9"}


def _ensure_repo(shallow: bool = True) -> Path:
    """Clone the ARC-AGI repo if not already cached."""
    if (CACHE_DIR / ".git").is_dir():
        return CACHE_DIR
    CACHE_DIR.parent.mkdir(parents=True, exist_ok=True)
    cmd = ["git", "clone"]
    if shallow:
        cmd += ["--depth", "1"]
    cmd += [REPO_URL, str(CACHE_DIR)]
    subprocess.run(cmd, check=True, capture_output=True, text=True)
    return CACHE_DIR


def render_grid(grid: list[list[int]]) -> str:
    """Render a 2D grid as human-readable text."""
    lines = []
    for row in grid:
        lines.append(" ".join(SYMBOL_MAP.get(c, str(c)) for c in row))
    return "\n".join(lines)


def _grid_dims(grid: list[list[int]]) -> str:
    """Return 'RxC' dimension string, safe for empty grids."""
    rows = len(grid)
    cols = len(grid[0]) if grid else 0
    return f"{rows}x{cols}"


def render_pair(pair: dict, label: str) -> str:
    """Render an input/output pair as text."""
    parts = [f"  {label} Input ({_grid_dims(pair['input'])}):",
             _indent(render_grid(pair["input"]), 4)]
    if "output" in pair:
        parts.append(f"  {label} Output ({_grid_dims(pair['output'])}):")
        parts.append(_indent(render_grid(pair["output"]), 4))
    return "\n".join(parts)


def _indent(text: str, n: int) -> str:
    prefix = " " * n
    return "\n".join(prefix + line for line in text.splitlines())


class ArcAgiTask(TaskType):
    """ARC-AGI abstract reasoning — agent predicts grid transformations."""

    name = "arc-agi"

    def load_tasks(self, limit: int | None = None) -> list[dict]:
        repo = _ensure_repo()
        training_dir = repo / "data" / "training"
        if not training_dir.is_dir():
            raise FileNotFoundError(
                f"ARC-AGI training dir not found at {training_dir}. "
                "Check that the repo cloned correctly."
            )

        tasks = []
        for json_file in sorted(training_dir.glob("*.json")):
            try:
                data = json.loads(json_file.read_text())
            except (json.JSONDecodeError, ValueError) as e:
                print(f"Warning: skipping invalid JSON {json_file.name}: {e}")
                continue
            task_id = json_file.stem  # e.g. "007bbfb7"
            tasks.append({
                "task_id": f"arc-agi/{task_id}",
                "arc_id": task_id,
                "train": data["train"],
                "test": data["test"],
            })
            if limit and len(tasks) >= limit:
                break
        return tasks

    def setup_workspace(self, task: dict, ws: Path) -> None:
        # Write task.json with train examples + test inputs (no test outputs)
        task_data = {
            "train": task["train"],
            "test": [{"input": t["input"]} for t in task["test"]],
        }
        (ws / "task.json").write_text(json.dumps(task_data, indent=2))

        # Write human-readable visualization
        lines = [f"ARC-AGI Task: {task['arc_id']}", ""]
        lines.append("=== Training Examples ===")
        for i, pair in enumerate(task["train"]):
            lines.append(f"\n--- Example {i + 1} ---")
            lines.append(render_pair(pair, f"Example {i + 1}"))

        lines.append("\n=== Test Input(s) ===")
        for i, test_case in enumerate(task["test"]):
            lines.append(f"\n--- Test {i + 1} ---")
            lines.append(f"  Test {i + 1} Input ({_grid_dims(test_case['input'])}):")
            lines.append(_indent(render_grid(test_case["input"]), 4))
            lines.append(f"  (Predict the output grid for this input)")

        (ws / "examples.txt").write_text("\n".join(lines))

    def setup_blueprint(self, task: dict, ws: Path) -> None:
        num_train = len(task["train"])
        num_tests = len(task["test"])
        blueprint = f"""\
_meta:
  version: "1"
intent: Solve an ARC-AGI abstract reasoning task
context:
  training_examples: {num_train}
  test_cases: {num_tests}
  grid_values: integers 0-9 (0 = background)
workflow:
  - step: observe
    action: Read examples.txt and study each training input→output pair carefully
  - step: compare
    action: Compare inputs and outputs side by side — note size changes, color mappings, spatial transforms
  - step: hypothesize
    action: Formulate a specific, testable transformation rule
    hints:
      - Look for symmetry operations (rotate, reflect, transpose)
      - Check for color substitution or filtering
      - Look for pattern repetition, scaling, or cropping
      - Check if objects are moved, sorted, or counted
      - Consider conditional rules (e.g. different treatment by color)
  - step: verify
    action: Mentally apply your rule to EACH training input and confirm it produces the exact training output
    constraint: If verification fails for ANY example, revise the hypothesis
  - step: apply
    action: Apply the verified rule to each test input to produce predicted output grids
  - step: write
    action: Write predictions to output.json as a JSON array
output_format:
  file: output.json
  schema: '[{{"output": [[int, ...], ...]}}, ...]'
"""
        (ws / "BLUEPRINT.yaml").write_text(blueprint)

    def get_instruction(self, task: dict, ws: Path) -> str:
        num_tests = len(task["test"])
        return f"""\
You are solving an ARC-AGI abstract reasoning task.

The workspace contains:
- `task.json`: structured data with training examples and test inputs
- `examples.txt`: human-readable visualization of the grids

Each training example shows an input grid transformed into an output grid.
Your job:
1. Read the files to understand the training examples.
2. Figure out the transformation rule/pattern that maps each input to its output.
3. Apply that rule to the test input(s) to predict the output grid(s).
4. Write your predictions to `output.json` in this format:

```json
[
  {{"output": [[row1], [row2], ...]}},
  ...
]
```

The file must contain a JSON array with {num_tests} element(s), one per test case.
Each element must have an "output" key with a 2D array of integers.

Grid values are integers (typically 0-9). 0 usually represents the background.
Focus on finding the exact pattern — the output must match exactly.

Write output.json using write_file, then call give_result("done").
"""

    def get_blueprint_instruction(self, task: dict, ws: Path) -> str:
        num_tests = len(task["test"])
        return f"""\
You are solving an ARC-AGI abstract reasoning task.

The workspace contains:
- `BLUEPRINT.yaml`: a structured workflow for solving the task — read it first
- `task.json`: structured data with training examples and test inputs
- `examples.txt`: human-readable visualization of the grids

Follow the workflow defined in BLUEPRINT.yaml step by step.

Write your predictions to `output.json` as a JSON array with {num_tests} element(s).
Each element must have an "output" key with a 2D array of integers.

Write output.json using write_file, then call give_result("done").
"""

    def score(self, task: dict, ws: Path) -> TaskResult:
        output_file = ws / "output.json"
        if not output_file.exists():
            return TaskResult(
                task_id=task["task_id"],
                passed=0,
                total=len(task["test"]),
                score=0.0,
                details={"error": "output.json not found"},
            )

        try:
            predictions = json.loads(output_file.read_text())
        except (json.JSONDecodeError, ValueError) as e:
            return TaskResult(
                task_id=task["task_id"],
                passed=0,
                total=len(task["test"]),
                score=0.0,
                details={"error": f"Invalid JSON: {e}"},
            )

        if not isinstance(predictions, list):
            return TaskResult(
                task_id=task["task_id"],
                passed=0,
                total=len(task["test"]),
                score=0.0,
                details={"error": "output.json must be a JSON array"},
            )

        total = len(task["test"])
        passed = 0
        case_details = []

        for i, test_case in enumerate(task["test"]):
            expected = test_case["output"]

            if i >= len(predictions):
                case_details.append({"test_index": i, "match": False,
                                     "reason": "missing prediction"})
                continue

            pred = predictions[i]
            pred_grid = pred.get("output") if isinstance(pred, dict) else None

            if pred_grid is None:
                case_details.append({"test_index": i, "match": False,
                                     "reason": "no 'output' key in prediction"})
                continue

            if pred_grid == expected:
                passed += 1
                case_details.append({"test_index": i, "match": True})
            else:
                case_details.append({
                    "test_index": i,
                    "match": False,
                    "expected_shape": f"{len(expected)}x{len(expected[0]) if expected else 0}",
                    "predicted_shape": f"{len(pred_grid)}x{len(pred_grid[0]) if pred_grid else 0}"
                        if isinstance(pred_grid, list) else "invalid",
                })

        score = passed / total if total > 0 else 0.0
        return TaskResult(
            task_id=task["task_id"],
            passed=passed,
            total=total,
            score=round(score, 3),
            details={"cases": case_details},
        )
