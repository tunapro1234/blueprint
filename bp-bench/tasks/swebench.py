"""SWE-bench Lite task type.

Workflow per task:
  1. Download task list from HuggingFace (cached locally)
  2. Clone the target repo at the base_commit
  3. Agent reads the problem statement, explores the code, and writes a patch
  4. Apply the agent's patch and run the test command to score
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import urllib.request
from pathlib import Path

from .base import TaskType, TaskResult

DATASET_URL = (
    "https://huggingface.co/datasets/princeton-nlp/SWE-bench_Lite/resolve/main/"
    "data/test-00000-of-00001.parquet"
)
CACHE_DIR = Path.home() / ".cache" / "bp-bench" / "swebench"
PARQUET_CACHE = CACHE_DIR / "swebench_lite.parquet"
JSON_CACHE = CACHE_DIR / "swebench_lite.json"
REPOS_CACHE = CACHE_DIR / "repos"


def _download_dataset() -> list[dict]:
    """Download and cache the SWE-bench Lite dataset.

    Tries parquet first (requires pyarrow/pandas), falls back to
    the JSONL API endpoint.
    """
    if JSON_CACHE.exists():
        return json.loads(JSON_CACHE.read_text())

    CACHE_DIR.mkdir(parents=True, exist_ok=True)

    # Try the HuggingFace datasets API (JSONL rows endpoint) first
    # This avoids needing pyarrow
    api_url = (
        "https://datasets-server.huggingface.co/rows"
        "?dataset=princeton-nlp%2FSWE-bench_Lite&config=default&split=test&offset=0&length=300"
    )
    try:
        req = urllib.request.Request(api_url, headers={"User-Agent": "bp-bench/1.0"})
        with urllib.request.urlopen(req, timeout=60) as resp:
            data = json.loads(resp.read().decode())
        rows = [r["row"] for r in data.get("rows", [])]
        if rows:
            JSON_CACHE.write_text(json.dumps(rows, indent=2))
            return rows
    except Exception:
        pass

    # Fallback: download parquet and convert
    try:
        urllib.request.urlretrieve(DATASET_URL, str(PARQUET_CACHE))
        import pandas as pd
        df = pd.read_parquet(PARQUET_CACHE)
        rows = df.to_dict(orient="records")
        JSON_CACHE.write_text(json.dumps(rows, indent=2, default=str))
        return rows
    except ImportError:
        raise RuntimeError(
            "SWE-bench dataset download requires pandas+pyarrow. "
            "Install them or manually place the JSON at: " + str(JSON_CACHE)
        )


def _clone_repo(repo: str, base_commit: str) -> Path:
    """Clone (or reuse) a repo at a specific commit."""
    # repo format: "owner/name"
    repo_dir = REPOS_CACHE / repo.replace("/", "__")
    REPOS_CACHE.mkdir(parents=True, exist_ok=True)

    if not (repo_dir / ".git").is_dir():
        subprocess.run(
            ["git", "clone", f"https://github.com/{repo}.git", str(repo_dir)],
            check=True, capture_output=True, text=True,
        )

    # Checkout the base commit
    subprocess.run(
        ["git", "checkout", base_commit, "--force"],
        cwd=str(repo_dir),
        check=True, capture_output=True, text=True,
    )
    # Clean any leftover state
    subprocess.run(
        ["git", "clean", "-fdx"],
        cwd=str(repo_dir),
        capture_output=True, text=True,
    )

    return repo_dir


class SWEBenchTask(TaskType):
    """SWE-bench Lite tasks — agent patches a real repo, scored by test suite."""

    name = "swebench"

    def __init__(self, instance_ids: list[str] | None = None):
        """Optionally filter to specific instance IDs."""
        self._filter_ids = set(instance_ids) if instance_ids else None

    def load_tasks(self, limit: int | None = None) -> list[dict]:
        rows = _download_dataset()
        tasks = []
        for row in rows:
            instance_id = row.get("instance_id", "")
            if self._filter_ids and instance_id not in self._filter_ids:
                continue

            tasks.append({
                "task_id": f"swebench/{instance_id}",
                "instance_id": instance_id,
                "repo": row.get("repo", ""),
                "base_commit": row.get("base_commit", ""),
                "problem_statement": row.get("problem_statement", ""),
                "hints_text": row.get("hints_text", ""),
                "test_patch": row.get("test_patch", ""),
                "patch": row.get("patch", ""),  # gold patch for reference
                "test_cmd": row.get("test_cmd", ""),
                "FAIL_TO_PASS": row.get("FAIL_TO_PASS", ""),
                "PASS_TO_PASS": row.get("PASS_TO_PASS", ""),
            })
            if limit and len(tasks) >= limit:
                break
        return tasks

    def setup_workspace(self, task: dict, ws: Path) -> None:
        repo_dir = _clone_repo(task["repo"], task["base_commit"])

        # Copy the repo checkout into the workspace
        # (so the agent doesn't modify the cached copy)
        import shutil
        for item in repo_dir.iterdir():
            dest = ws / item.name
            if item.is_dir():
                shutil.copytree(item, dest, symlinks=True)
            else:
                shutil.copy2(item, dest)

        # Apply the test patch so the failing tests are present
        if task.get("test_patch"):
            patch_file = ws / "_test_patch.diff"
            patch_file.write_text(task["test_patch"])
            subprocess.run(
                ["git", "apply", "--allow-empty", str(patch_file)],
                cwd=str(ws),
                capture_output=True, text=True,
            )
            patch_file.unlink(missing_ok=True)

    def get_instruction(self, task: dict, ws: Path) -> str:
        problem = task["problem_statement"]
        hints = task.get("hints_text", "")
        fail_to_pass = task.get("FAIL_TO_PASS", "")

        instruction = f"""\
You are fixing a bug in a real open-source Python repository.

## Problem Statement
{problem}
"""
        if hints:
            instruction += f"""
## Hints
{hints}
"""
        if fail_to_pass:
            instruction += f"""
## Tests That Should Pass After Your Fix
{fail_to_pass}
"""
        instruction += """
## Instructions
- Explore the repository to understand the codebase.
- Identify the root cause of the bug.
- Write a minimal fix — modify only the files necessary.
- Do NOT modify test files.
- After making your changes, call give_result("done").
"""
        return instruction

    def setup_blueprint(self, task: dict, ws: Path) -> None:
        problem = task["problem_statement"]
        repo = task.get("repo", "")
        instance_id = task.get("instance_id", "")
        fail_to_pass = task.get("FAIL_TO_PASS", "")

        # Truncate long problem statements
        if len(problem) > 2000:
            problem = problem[:2000] + "\n..."

        blueprint = f"""\
_meta:
  version: "1"
intent: Fix bug reported in {instance_id}
context:
  repo: {repo}
  problem: |
"""
        for line in problem.splitlines():
            blueprint += f"    {line}\n"

        if fail_to_pass:
            blueprint += f"  failing_tests: {fail_to_pass}\n"

        blueprint += """\
workflow:
  - step: explore
    action: Understand the repository structure using list_dir and read_file
  - step: understand
    action: Read the problem statement and identify what behavior is broken
  - step: locate
    action: Find the relevant source files that need to be modified
  - step: diagnose
    action: Read the source code and understand the root cause of the bug
  - step: plan
    action: Plan a minimal fix that addresses the root cause
    constraints:
      - Modify only the files necessary to fix the bug
      - Do NOT modify test files
      - Keep changes minimal and focused
  - step: implement
    action: Apply the fix using write_file
  - step: verify
    action: Review your changes to ensure correctness
"""
        (ws / "BLUEPRINT.yaml").write_text(blueprint, encoding="utf-8")

    def get_blueprint_instruction(self, task: dict, ws: Path) -> str:
        problem = task["problem_statement"]
        hints = task.get("hints_text", "")
        fail_to_pass = task.get("FAIL_TO_PASS", "")

        instruction = f"""\
You are fixing a bug in a real open-source Python repository.

There is a BLUEPRINT.yaml in the workspace — read it first.
It describes a structured workflow for diagnosing and fixing the bug.

## Problem Statement
{problem}
"""
        if hints:
            instruction += f"""
## Hints
{hints}
"""
        if fail_to_pass:
            instruction += f"""
## Tests That Should Pass After Your Fix
{fail_to_pass}
"""
        instruction += """
Follow the workflow in BLUEPRINT.yaml step by step.
After making your changes, call give_result("done").
"""
        return instruction

    def score(self, task: dict, ws: Path) -> TaskResult:
        # Determine what tests need to pass
        fail_to_pass_raw = task.get("FAIL_TO_PASS", "")
        try:
            fail_to_pass = json.loads(fail_to_pass_raw) if isinstance(fail_to_pass_raw, str) else fail_to_pass_raw
        except (json.JSONDecodeError, TypeError):
            fail_to_pass = []
        if not isinstance(fail_to_pass, list):
            fail_to_pass = []

        # Run the test command if provided, otherwise try pytest
        test_cmd = task.get("test_cmd", "").strip()
        if not test_cmd:
            test_cmd = f"{sys.executable} -m pytest -x -q --tb=short"

        try:
            proc = subprocess.run(
                test_cmd,
                shell=True,
                cwd=str(ws),
                capture_output=True,
                text=True,
                timeout=300,
                env={**os.environ, "PYTHONPATH": str(ws)},
            )
            output = proc.stdout + proc.stderr
            returncode = proc.returncode
        except subprocess.TimeoutExpired:
            output = "TIMEOUT"
            returncode = 1

        # Check if the fail_to_pass tests now pass
        # Simple heuristic: if the test command exits 0, all tests pass
        if returncode == 0:
            passed = len(fail_to_pass) if fail_to_pass else 1
            total = passed
        else:
            # Parse output for pass/fail counts
            import re
            passed = 0
            total = 0
            for line in output.splitlines():
                p = re.search(r"(\d+) passed", line)
                f = re.search(r"(\d+) failed", line)
                e = re.search(r"(\d+) error", line)
                if p or f or e:
                    passed = int(p.group(1)) if p else 0
                    failed = int(f.group(1)) if f else 0
                    errors = int(e.group(1)) if e else 0
                    total = passed + failed + errors
                    break
            if total == 0:
                total = max(len(fail_to_pass), 1)

        score = passed / total if total > 0 else 0.0
        return TaskResult(
            task_id=task["task_id"],
            passed=passed,
            total=total,
            score=round(score, 3),
            details={
                "returncode": returncode,
                "output": output[-2000:],
            },
        )
