#!/usr/bin/env python3
"""bp-bench: benchmark runner supporting multiple task types.

All task types (including blueprint) go through the same unified path.

Task types:
  - blueprint:    Original store/api/cli benchmark (deterministic scoring)
  - exercism:     Exercism Python exercises (pytest-scored)
  - swebench:     SWE-bench Lite real-world bug fixes (test-scored)
  - translation:  Translation between language pairs (similarity-scored)
  - arc-agi:      ARC-AGI abstract reasoning (exact grid match)

Usage:
  python run_bench.py                                          # blueprint, all 4 modes
  python run_bench.py --mode with_blueprint+single
  python run_bench.py --task-type exercism --limit 5           # 5 exercism exercises
  python run_bench.py --task-type exercism --bp-mode with_blueprint --limit 3
  python run_bench.py --task-type swebench --limit 1           # 1 SWE-bench task
  python run_bench.py --provider gemini --model gemini-3-flash-preview
  python run_bench.py --keep                                   # keep workspace after
"""

import argparse
import json
import os
import shutil
import sys
import tempfile
import time
from pathlib import Path

from tasks import get_task_type
from analysis import analyze_results, print_summary_report

# ---------------------------------------------------------------------------
# Mode definitions
# ---------------------------------------------------------------------------

MODES = [
    ("no_blueprint", "single"),
    ("no_blueprint", "multi"),
    ("with_blueprint", "single"),
    ("with_blueprint", "multi"),
]

SYSTEM_PROMPT = """\
You are a task execution agent. Follow the instructions precisely.

Tools: bash, read_file, write_file, list_dir, give_result

WORKFLOW:
1. If there are BLUEPRINT.yaml or similar guide files in the workspace, read them first
2. Plan your approach based on the instructions
3. Execute the task step by step
4. After completing the task, call give_result("done")

RULES:
- Write complete, working code
- Use only Python stdlib unless told otherwise
- Do not stop early — complete ALL required work before calling give_result
- Each write_file call creates one file — make as many calls as needed"""


# ---------------------------------------------------------------------------
# Runner
# ---------------------------------------------------------------------------

def run_agent(ws: Path, bp_mode: str, agent_mode: str, provider: str, model: str,
              instruction: str):
    """Run bp-agent and return AgentResult."""
    from bp_agent.agent import Agent, AgentConfig

    multi = agent_mode == "multi"

    config = AgentConfig(
        provider=provider,
        model=model,
        max_iterations=30,
        enable_subagents=multi,
        worker_max_iterations=15,
    )

    agent = Agent("bench", config=config, system_prompt=SYSTEM_PROMPT)

    orig_cwd = os.getcwd()
    os.chdir(str(ws))
    try:
        result = agent.execute(instruction)
    finally:
        os.chdir(orig_cwd)

    return result


def run_task(task_type_obj, task: dict, agent_mode: str,
             provider: str, model: str, keep: bool,
             bp_mode: str = "no_blueprint") -> dict:
    """Run a single task from any TaskType and return a result dict."""
    task_id = task["task_id"]
    ws = Path(tempfile.mkdtemp(prefix=f"bp-bench-{task_type_obj.name}-"))
    print(f"\n{'='*60}")
    print(f"  Task: {task_id}")
    print(f"  Type: {task_type_obj.name}")
    print(f"  Mode: {bp_mode} + {agent_mode}")
    print(f"  Workspace: {ws}")
    print(f"{'='*60}")

    try:
        task_type_obj.setup_workspace(task, ws)

        if bp_mode == "with_blueprint":
            task_type_obj.setup_blueprint(task, ws)
            instruction = task_type_obj.get_blueprint_instruction(task, ws)
        else:
            instruction = task_type_obj.get_instruction(task, ws)

        t0 = time.time()
        result = run_agent(ws, bp_mode, agent_mode, provider, model,
                           instruction=instruction)
        elapsed = time.time() - t0

        score_result = task_type_obj.score(task, ws)

        print(f"  Agent success: {result.success}")
        print(f"  Time: {elapsed:.1f}s")
        print(f"  Score: {score_result.score} ({score_result.passed}/{score_result.total} passed)")

        return {
            "task_type": task_type_obj.name,
            "task_id": task_id,
            "bp_mode": bp_mode,
            "agent_mode": agent_mode,
            "success": result.success,
            "elapsed": round(elapsed, 1),
            "score": {
                "total": score_result.score,
                "passed": score_result.passed,
                "tests_total": score_result.total,
                "details": score_result.details,
            },
        }
    finally:
        if not keep:
            shutil.rmtree(ws, ignore_errors=True)


def print_summary(results: list[dict]):
    """Print per-task results and averages."""
    print(f"\n{'='*60}")
    print("  TASK RESULTS SUMMARY")
    print(f"{'='*60}")

    total_score = 0.0
    for r in results:
        s = r["score"]["total"]
        total_score += s
        passed = r["score"].get("passed", "?")
        total_tests = r["score"].get("tests_total", "?")
        mode = f"{r['bp_mode']}+{r['agent_mode']}"
        status = "PASS" if s == 1.0 else ("PARTIAL" if s > 0 else "FAIL")
        print(f"  [{status:7s}] {r['task_id']:40s}  {mode:28s}  "
              f"{passed}/{total_tests}  score={s:.3f}")

    n = len(results)
    avg = total_score / n if n else 0
    print(f"\n  Tasks: {n}  |  Average score: {avg:.3f}")
    full_pass = sum(1 for r in results if r["score"]["total"] == 1.0)
    print(f"  Full pass: {full_pass}/{n}")
    print()


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="bp-bench: unified benchmark runner")
    parser.add_argument("--task-type", default="blueprint",
                        help="Task type: blueprint, exercism, swebench, translation, arc-agi")
    parser.add_argument("--mode",
                        help="Specific mode, e.g. with_blueprint+single (default: all 4 if blueprint, else bp-mode+agent-mode)")
    parser.add_argument("--provider", default="gemini")
    parser.add_argument("--model", default="gemini-3-flash-preview")
    parser.add_argument("--keep", action="store_true", help="keep workspace after run")
    parser.add_argument("--output", help="save results JSON to file")
    parser.add_argument("--limit", type=int, default=None,
                        help="max number of tasks to run")
    parser.add_argument("--agent-mode", default="single",
                        help="Agent mode: single or multi (default: single)")
    parser.add_argument("--bp-mode", default="no_blueprint",
                        choices=["no_blueprint", "with_blueprint"],
                        help="Blueprint mode (default: no_blueprint)")
    args = parser.parse_args()

    # Resolve task type (all types go through the registry now)
    task_type_obj = get_task_type(args.task_type)

    # Determine modes to run
    if args.mode:
        parts = args.mode.split("+")
        if len(parts) != 2:
            print("--mode format: with_blueprint+single or no_blueprint+multi")
            sys.exit(1)
        modes = [(parts[0], parts[1])]
    elif args.task_type == "blueprint" and not any(
        a in sys.argv for a in ["--bp-mode", "--agent-mode"]
    ):
        # Default for blueprint: all 4 modes (backward compat)
        modes = MODES
    else:
        modes = [(args.bp_mode, args.agent_mode)]

    # Load tasks
    tasks = task_type_obj.load_tasks(limit=args.limit)
    if not tasks:
        print(f"No tasks found for task type: {args.task_type}")
        sys.exit(1)

    print(f"Loaded {len(tasks)} {args.task_type} task(s)")
    print(f"Modes: {', '.join(bp + '+' + ag for bp, ag in modes)}")
    print()

    # Run
    results = []
    for task in tasks:
        for bp_mode, agent_mode in modes:
            r = run_task(task_type_obj, task, agent_mode,
                         args.provider, args.model, args.keep,
                         bp_mode=bp_mode)
            results.append(r)

    # Print results
    print_summary(results)

    # If multiple modes, show statistical analysis
    if len(modes) > 1 or len(results) > 1:
        analysis = analyze_results(results)
        print_summary_report(analysis)

    if args.output:
        Path(args.output).parent.mkdir(parents=True, exist_ok=True)
        Path(args.output).write_text(json.dumps(results, indent=2))
        print(f"Results saved to {args.output}")


if __name__ == "__main__":
    main()
