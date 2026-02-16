#!/usr/bin/env python3
"""Full benchmark runner: multiple models × multiple task types × N repetitions.

Loads .env from bp-agent, runs benchmarks, aggregates results with statistics.

All task types (including blueprint) go through the same unified runner.

Usage:
  python run_full_bench.py                                    # all models, blueprint, 3 runs each
  python run_full_bench.py --runs 1 --models flash            # quick test
  python run_full_bench.py --models flash pro                 # specific models
  python run_full_bench.py --task-types exercism --limit 5    # exercism tasks
  python run_full_bench.py --task-types blueprint exercism    # both task types
  python run_full_bench.py --modes all                        # all 4 bp×agent modes
  python run_full_bench.py --modes no_blueprint+single with_blueprint+single
"""

import argparse
import json
import os
import sys
import time
from pathlib import Path

# Load .env before importing bp_agent
ENV_FILE = Path(__file__).parent.parent / "bp-agent" / ".env"


def load_env(env_file: Path):
    """Load .env file into os.environ."""
    if not env_file.exists():
        print(f"WARNING: {env_file} not found")
        return
    for line in env_file.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if "=" in line:
            key, _, val = line.partition("=")
            os.environ[key.strip()] = val.strip()
    print(f"Loaded env from {env_file}")


load_env(ENV_FILE)

# Now import bench components
from run_bench import MODES, run_task
from tasks import get_task_type
from analysis import analyze_results, print_summary_report, print_cross_model_report

# ---------------------------------------------------------------------------
# Model configurations
# ---------------------------------------------------------------------------

MODEL_CONFIGS = {
    "flash": {
        "provider": "gemini",
        "model": "gemini-3-flash-preview",
        "label": "Gemini 3 Flash",
    },
    "pro": {
        "provider": "gemini",
        "model": "gemini-3-pro-preview",
        "label": "Gemini 3 Pro",
    },
    "codex": {
        "provider": "codex",
        "model": "gpt-5.2-codex",
        "label": "GPT-5.2 Codex",
    },
    "glm5": {
        "provider": "openrouter",
        "model": "z-ai/glm-5",
        "label": "GLM-5",
    },
    "kimi25": {
        "provider": "openrouter",
        "model": "moonshotai/kimi-k2.5",
        "label": "Kimi K2.5",
    },
}

# ---------------------------------------------------------------------------
# Unified runner — works for ALL task types including blueprint
# ---------------------------------------------------------------------------


def run_model_tasks(model_key: str, config: dict, task_type_obj,
                    tasks: list[dict], num_runs: int,
                    modes: list[tuple[str, str]], keep: bool) -> list[dict]:
    """Run tasks × modes × num_runs for a single model on a task type."""
    provider = config["provider"]
    model = config["model"]
    label = config["label"]
    all_results = []

    print(f"\n{'#'*70}")
    print(f"#  MODEL: {label} ({provider}/{model})")
    print(f"#  Task type: {task_type_obj.name}")
    print(f"#  Tasks: {len(tasks)}  |  Modes: {len(modes)}  |  Runs per combo: {num_runs}")
    print(f"{'#'*70}")

    for task in tasks:
        for bp_mode, ag_mode in modes:
            for run_idx in range(1, num_runs + 1):
                mode_label = f"{bp_mode}+{ag_mode}"
                print(f"\n>>> [{label}] {task['task_id']} {mode_label} — run {run_idx}/{num_runs}")

                try:
                    result = run_task(task_type_obj, task, ag_mode,
                                      provider, model, keep,
                                      bp_mode=bp_mode)
                    result["model_key"] = model_key
                    result["model_label"] = label
                    result["run_index"] = run_idx
                    all_results.append(result)
                except Exception as e:
                    print(f"  ERROR: {e}")
                    all_results.append({
                        "task_type": task_type_obj.name,
                        "task_id": task["task_id"],
                        "bp_mode": bp_mode,
                        "agent_mode": ag_mode,
                        "model_key": model_key,
                        "model_label": label,
                        "run_index": run_idx,
                        "success": False,
                        "elapsed": 0,
                        "score": {"total": 0, "passed": 0, "tests_total": 0, "details": {}},
                        "error": str(e),
                    })

    return all_results


# ---------------------------------------------------------------------------
# Mode parsing
# ---------------------------------------------------------------------------

def parse_modes(mode_strs: list[str]) -> list[tuple[str, str]]:
    """Parse mode strings like 'no_blueprint+single' or 'all' into mode tuples."""
    if mode_strs == ["all"]:
        return list(MODES)

    modes = []
    for ms in mode_strs:
        parts = ms.split("+")
        if len(parts) != 2:
            print(f"Invalid mode format: {ms!r}. Expected 'bp_mode+agent_mode'.")
            sys.exit(1)
        bp_mode, agent_mode = parts
        if bp_mode not in ("no_blueprint", "with_blueprint"):
            print(f"Invalid bp_mode: {bp_mode!r}")
            sys.exit(1)
        if agent_mode not in ("single", "multi"):
            print(f"Invalid agent_mode: {agent_mode!r}")
            sys.exit(1)
        modes.append((bp_mode, agent_mode))
    return modes


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="bp-bench: full multi-model benchmark")
    parser.add_argument("--models", nargs="+", default=list(MODEL_CONFIGS.keys()),
                        choices=list(MODEL_CONFIGS.keys()),
                        help="Which models to test (default: all)")
    parser.add_argument("--task-types", nargs="+", default=["blueprint"],
                        help="Task types to run (default: blueprint)")
    parser.add_argument("--runs", type=int, default=3,
                        help="Runs per mode/task per model (default: 3)")
    parser.add_argument("--keep", action="store_true", help="Keep workspaces after runs")
    parser.add_argument("--output", default="results/full_bench.json",
                        help="Output JSON path")
    parser.add_argument("--limit", type=int, default=None,
                        help="Max tasks to load per task type")
    parser.add_argument("--modes", nargs="+", default=["all"],
                        help="Modes to run: 'all' or specific like 'no_blueprint+single with_blueprint+single'")
    args = parser.parse_args()

    modes = parse_modes(args.modes)

    # Preload all task types and their tasks (fail fast)
    task_type_objs = {}
    loaded_tasks = {}
    for tt_name in args.task_types:
        tt_obj = get_task_type(tt_name)
        task_type_objs[tt_name] = tt_obj
        loaded_tasks[tt_name] = tt_obj.load_tasks(limit=args.limit)

    # Count total runs
    total_runs = sum(
        len(args.models) * len(tasks) * len(modes) * args.runs
        for tasks in loaded_tasks.values()
    )

    print(f"bp-bench full run: {len(args.models)} models × {len(args.task_types)} task type(s)")
    print(f"  Modes: {', '.join(bp + '+' + ag for bp, ag in modes)}")
    print(f"  Runs per combo: {args.runs}")
    print(f"  Total runs: {total_runs}")
    print(f"  Models: {', '.join(args.models)}")
    print()

    all_results = []
    t_start = time.time()

    for model_key in args.models:
        config = MODEL_CONFIGS[model_key]

        for tt_name, tt_obj in task_type_objs.items():
            tasks = loaded_tasks[tt_name]
            results = run_model_tasks(model_key, config, tt_obj, tasks,
                                      args.runs, modes, args.keep)
            all_results.extend(results)

            # Per-model per-tasktype summary
            if results:
                analysis = analyze_results(results)
                print_summary_report(analysis)

        # Save intermediate results after each model
        out_path = Path(args.output)
        out_path.parent.mkdir(parents=True, exist_ok=True)
        out_path.write_text(json.dumps(all_results, indent=2))
        print(f"\n  [Intermediate results saved to {out_path}]")

    elapsed_total = time.time() - t_start

    # Final cross-model comparison
    if len(args.models) > 1 and all_results:
        analysis = analyze_results(all_results)
        print_cross_model_report(analysis)

    # Save final results
    out_path = Path(args.output)
    out_path.parent.mkdir(parents=True, exist_ok=True)

    final_output = {
        "metadata": {
            "timestamp": time.strftime("%Y-%m-%d %H:%M:%S"),
            "total_runs": total_runs,
            "runs_per_mode": args.runs,
            "models": args.models,
            "task_types": args.task_types,
            "modes": [f"{bp}+{ag}" for bp, ag in modes],
            "total_elapsed_seconds": round(elapsed_total, 1),
        },
        "results": all_results,
    }
    out_path.write_text(json.dumps(final_output, indent=2))
    print(f"\nTotal time: {elapsed_total/60:.1f} minutes")
    print(f"Results saved to {out_path}")


if __name__ == "__main__":
    main()
