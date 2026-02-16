"""Statistical analysis for bp-bench results.

Stdlib-only (math, statistics) — no scipy/numpy dependency.

Provides:
  - compute_stats: descriptive stats with 95% CI (t-distribution)
  - compute_effect: Cohen's d effect size with CI
  - analyze_results: group and aggregate benchmark results
  - print_summary_report: unified mode comparison table
  - print_cross_model_report: cross-model comparison with effect sizes
  - normalize_result: backwards-compat normalizer for old result formats
"""

from __future__ import annotations

import math
import statistics
from collections import defaultdict

# ---------------------------------------------------------------------------
# Hardcoded t-distribution critical values for 95% CI (two-tailed, α=0.05)
# t_table[df] = t_critical; for df > 120, use 1.96 (z-approx).
# ---------------------------------------------------------------------------

_T_TABLE = {
    1: 12.706, 2: 4.303, 3: 3.182, 4: 2.776, 5: 2.571,
    6: 2.447, 7: 2.365, 8: 2.306, 9: 2.262, 10: 2.228,
    11: 2.201, 12: 2.179, 13: 2.160, 14: 2.145, 15: 2.131,
    16: 2.120, 17: 2.110, 18: 2.101, 19: 2.093, 20: 2.086,
    25: 2.060, 30: 2.042, 40: 2.021, 50: 2.009, 60: 2.000,
    80: 1.990, 100: 1.984, 120: 1.980,
}


def _t_critical(df: int) -> float:
    """Look up t-critical for given degrees of freedom."""
    if df <= 0:
        return float("inf")
    if df in _T_TABLE:
        return _T_TABLE[df]
    # Interpolate between known values
    keys = sorted(_T_TABLE.keys())
    if df < keys[0]:
        return _T_TABLE[keys[0]]
    if df > keys[-1]:
        return 1.96  # z-approximation
    # Find bracketing keys
    for i in range(len(keys) - 1):
        if keys[i] <= df <= keys[i + 1]:
            lo, hi = keys[i], keys[i + 1]
            frac = (df - lo) / (hi - lo)
            return _T_TABLE[lo] + frac * (_T_TABLE[hi] - _T_TABLE[lo])
    return 1.96


# ---------------------------------------------------------------------------
# Core stats functions
# ---------------------------------------------------------------------------

def compute_stats(values: list[float]) -> dict:
    """Compute descriptive statistics with 95% confidence interval.

    Returns dict with keys: mean, std, ci_lower, ci_upper, min, max, n.
    """
    if not values:
        return {"mean": 0.0, "std": 0.0, "ci_lower": 0.0, "ci_upper": 0.0,
                "min": 0.0, "max": 0.0, "n": 0}

    n = len(values)
    mean = statistics.mean(values)
    std = statistics.stdev(values) if n > 1 else 0.0

    if n > 1:
        se = std / math.sqrt(n)
        t = _t_critical(n - 1)
        margin = t * se
        ci_lower = mean - margin
        ci_upper = mean + margin
    else:
        ci_lower = mean
        ci_upper = mean

    return {
        "mean": round(mean, 4),
        "std": round(std, 4),
        "ci_lower": round(ci_lower, 4),
        "ci_upper": round(ci_upper, 4),
        "min": round(min(values), 4),
        "max": round(max(values), 4),
        "n": n,
    }


def compute_effect(baseline: list[float], treatment: list[float]) -> dict:
    """Compute effect size (Cohen's d) between baseline and treatment groups.

    Returns dict with keys: delta, cohens_d, magnitude, ci_lower, ci_upper.
    Magnitude: negligible (<0.2), small (0.2-0.5), medium (0.5-0.8), large (≥0.8).
    """
    if not baseline or not treatment:
        return {"delta": 0.0, "cohens_d": 0.0, "magnitude": "n/a",
                "ci_lower": 0.0, "ci_upper": 0.0}

    m1 = statistics.mean(baseline)
    m2 = statistics.mean(treatment)
    delta = m2 - m1

    n1, n2 = len(baseline), len(treatment)

    # Pooled standard deviation
    if n1 > 1 and n2 > 1:
        s1 = statistics.stdev(baseline)
        s2 = statistics.stdev(treatment)
        pooled = math.sqrt(((n1 - 1) * s1**2 + (n2 - 1) * s2**2) / (n1 + n2 - 2))
    elif n1 > 1:
        pooled = statistics.stdev(baseline)
    elif n2 > 1:
        pooled = statistics.stdev(treatment)
    else:
        pooled = 0.0

    cohens_d = delta / pooled if pooled > 0 else 0.0

    # Magnitude classification
    abs_d = abs(cohens_d)
    if abs_d < 0.2:
        magnitude = "negligible"
    elif abs_d < 0.5:
        magnitude = "small"
    elif abs_d < 0.8:
        magnitude = "medium"
    else:
        magnitude = "large"

    # Approximate 95% CI for the difference of means
    if n1 > 1 and n2 > 1:
        se = pooled * math.sqrt(1 / n1 + 1 / n2)
        df = n1 + n2 - 2
        t = _t_critical(df)
        ci_lower = delta - t * se
        ci_upper = delta + t * se
    else:
        ci_lower = delta
        ci_upper = delta

    return {
        "delta": round(delta, 4),
        "cohens_d": round(cohens_d, 4),
        "magnitude": magnitude,
        "ci_lower": round(ci_lower, 4),
        "ci_upper": round(ci_upper, 4),
    }


# ---------------------------------------------------------------------------
# Result analysis
# ---------------------------------------------------------------------------

def analyze_results(results: list[dict]) -> dict:
    """Group and analyze benchmark results.

    Groups by (task_type, model_key, bp_mode, agent_mode).
    Computes per-group score and elapsed stats.
    Computes blueprint and multi-agent effects.

    Returns a dict with keys:
      - groups: {group_key: {score_stats, elapsed_stats, results}}
      - blueprint_effects: {(task_type, model_key, agent_mode): effect_dict}
      - agent_effects: {(task_type, model_key, bp_mode): effect_dict}
      - models: sorted list of model_keys
      - task_types: sorted list of task_types
    """
    # Normalize all results first
    normed = [normalize_result(r) for r in results]

    # Group by (task_type, model_key, bp_mode, agent_mode)
    groups: dict[tuple, list[dict]] = defaultdict(list)
    for r in normed:
        key = (r["task_type"], r.get("model_key", "default"),
               r["bp_mode"], r["agent_mode"])
        groups[key].append(r)

    group_stats = {}
    for key, group_results in groups.items():
        scores = [r["score"]["total"] for r in group_results]
        elapsed = [r["elapsed"] for r in group_results if r.get("elapsed")]
        group_stats[key] = {
            "score_stats": compute_stats(scores),
            "elapsed_stats": compute_stats(elapsed),
            "n": len(group_results),
        }

    # Blueprint effects: compare no_blueprint vs with_blueprint
    # for each (task_type, model_key, agent_mode)
    bp_effects = {}
    triplets = set()
    for (tt, mk, bp, ag) in groups:
        triplets.add((tt, mk, ag))
    for tt, mk, ag in triplets:
        baseline = [r["score"]["total"] for r in groups.get((tt, mk, "no_blueprint", ag), [])]
        treatment = [r["score"]["total"] for r in groups.get((tt, mk, "with_blueprint", ag), [])]
        if baseline and treatment:
            bp_effects[(tt, mk, ag)] = compute_effect(baseline, treatment)

    # Multi-agent effects: compare single vs multi
    # for each (task_type, model_key, bp_mode)
    agent_effects = {}
    triplets2 = set()
    for (tt, mk, bp, ag) in groups:
        triplets2.add((tt, mk, bp))
    for tt, mk, bp in triplets2:
        baseline = [r["score"]["total"] for r in groups.get((tt, mk, bp, "single"), [])]
        treatment = [r["score"]["total"] for r in groups.get((tt, mk, bp, "multi"), [])]
        if baseline and treatment:
            agent_effects[(tt, mk, bp)] = compute_effect(baseline, treatment)

    models = sorted(set(r.get("model_key", "default") for r in normed))
    task_types = sorted(set(r["task_type"] for r in normed))

    return {
        "groups": group_stats,
        "blueprint_effects": bp_effects,
        "agent_effects": agent_effects,
        "models": models,
        "task_types": task_types,
    }


# ---------------------------------------------------------------------------
# Reporting
# ---------------------------------------------------------------------------

def print_summary_report(analysis: dict):
    """Print a unified summary table with mode comparison and effects."""
    groups = analysis["groups"]
    bp_effects = analysis["blueprint_effects"]
    agent_effects = analysis["agent_effects"]

    print(f"\n{'='*80}")
    print("  RESULTS SUMMARY")
    print(f"{'='*80}")

    # Table: Task Type | Model | Mode | Mean | 95% CI | Std | N
    print(f"  {'Task Type':14s} {'Model':10s} {'Mode':28s} "
          f"{'Mean':>7s} {'95% CI':>15s} {'Std':>6s} {'N':>4s}")
    print(f"  {'-'*14} {'-'*10} {'-'*28} {'-'*7} {'-'*15} {'-'*6} {'-'*4}")

    for key in sorted(groups.keys()):
        tt, mk, bp, ag = key
        s = groups[key]["score_stats"]
        ci = f"[{s['ci_lower']:.3f}, {s['ci_upper']:.3f}]"
        mode = f"{bp}+{ag}"
        print(f"  {tt:14s} {mk:10s} {mode:28s} "
              f"{s['mean']:7.3f} {ci:>15s} {s['std']:6.3f} {s['n']:4d}")

    # Blueprint effects
    if bp_effects:
        print(f"\n  BLUEPRINT EFFECT (treatment - baseline):")
        print(f"  {'Task Type':14s} {'Model':10s} {'Agent':8s} "
              f"{'Delta':>7s} {'Cohen d':>8s} {'Magnitude':>12s} {'95% CI':>18s}")
        print(f"  {'-'*14} {'-'*10} {'-'*8} {'-'*7} {'-'*8} {'-'*12} {'-'*18}")
        for (tt, mk, ag), eff in sorted(bp_effects.items()):
            ci = f"[{eff['ci_lower']:.3f}, {eff['ci_upper']:.3f}]"
            print(f"  {tt:14s} {mk:10s} {ag:8s} "
                  f"{eff['delta']:+7.3f} {eff['cohens_d']:8.3f} "
                  f"{eff['magnitude']:>12s} {ci:>18s}")

    # Multi-agent effects
    if agent_effects:
        print(f"\n  MULTI-AGENT EFFECT (multi - single):")
        print(f"  {'Task Type':14s} {'Model':10s} {'BP Mode':14s} "
              f"{'Delta':>7s} {'Cohen d':>8s} {'Magnitude':>12s} {'95% CI':>18s}")
        print(f"  {'-'*14} {'-'*10} {'-'*14} {'-'*7} {'-'*8} {'-'*12} {'-'*18}")
        for (tt, mk, bp), eff in sorted(agent_effects.items()):
            ci = f"[{eff['ci_lower']:.3f}, {eff['ci_upper']:.3f}]"
            print(f"  {tt:14s} {mk:10s} {bp:14s} "
                  f"{eff['delta']:+7.3f} {eff['cohens_d']:8.3f} "
                  f"{eff['magnitude']:>12s} {ci:>18s}")

    print()


def print_cross_model_report(analysis: dict):
    """Print cross-model comparison with effect sizes per task type."""
    groups = analysis["groups"]
    models = analysis["models"]
    task_types = analysis["task_types"]
    bp_effects = analysis["blueprint_effects"]

    if len(models) < 2:
        return

    print(f"\n{'='*80}")
    print("  CROSS-MODEL COMPARISON")
    print(f"{'='*80}")

    for tt in task_types:
        print(f"\n  --- {tt} ---")

        # Mode × model matrix
        header = f"  {'Mode':28s}"
        for mk in models:
            header += f" {mk:>12s}"
        print(header)
        print(f"  {'-'*28}" + f" {'-'*12}" * len(models))

        modes = sorted(set(
            (bp, ag) for (t, m, bp, ag) in groups if t == tt
        ))
        for bp, ag in modes:
            row = f"  {bp + '+' + ag:28s}"
            for mk in models:
                key = (tt, mk, bp, ag)
                if key in groups:
                    s = groups[key]["score_stats"]
                    row += f" {s['mean']:12.3f}"
                else:
                    row += f" {'N/A':>12s}"
            print(row)

        # Overall per model
        print()
        row = f"  {'OVERALL':28s}"
        for mk in models:
            vals = []
            for (t, m, bp, ag), gs in groups.items():
                if t == tt and m == mk:
                    vals.append(gs["score_stats"]["mean"])
            if vals:
                row += f" {statistics.mean(vals):12.3f}"
            else:
                row += f" {'N/A':>12s}"
        print(row)

        # Blueprint effect per model (with Cohen's d)
        bp_effs = {mk: eff for (t, mk_, ag), eff in bp_effects.items()
                   if t == tt for mk in [mk_]}
        if bp_effs:
            print(f"\n  Blueprint effect (Cohen's d):")
            for mk in models:
                # Find effects for this model (any agent_mode)
                effs = [(ag, eff) for (t, m, ag), eff in bp_effects.items()
                        if t == tt and m == mk]
                for ag, eff in effs:
                    print(f"    {mk:12s} ({ag:6s}): "
                          f"delta={eff['delta']:+.3f}  "
                          f"d={eff['cohens_d']:.3f} ({eff['magnitude']})")

    print()


# ---------------------------------------------------------------------------
# Result normalization (backward compatibility)
# ---------------------------------------------------------------------------

def normalize_result(r: dict) -> dict:
    """Normalize a result record to the standard schema.

    Handles old blueprint format where task_type is missing and score
    has structure/functional/spec/integration keys instead of total/passed/tests_total.
    """
    result = dict(r)

    # Infer task_type if missing
    if "task_type" not in result:
        result["task_type"] = "blueprint"

    # Infer task_id if missing
    if "task_id" not in result:
        if result["task_type"] == "blueprint":
            result["task_id"] = "blueprint/store-api-cli"
        else:
            result["task_id"] = f"{result['task_type']}/unknown"

    # Normalize score to standard schema
    score = result.get("score", {})
    if "structure" in score:
        # Old blueprint format — has structure/functional/spec/integration keys
        total = score.get("total")
        if total is None:
            total = (
                score.get("structure", 0) * 0.3
                + score.get("functional", 0) * 0.3
                + score.get("spec", 0) * 0.2
                + score.get("integration", 0) * 0.2
            )
        result["score"] = {
            "total": round(total, 4),
            "passed": round(total * 21),
            "tests_total": 21,
            "details": {
                "structure": score.get("structure", 0),
                "functional": score.get("functional", 0),
                "spec": score.get("spec", 0),
                "integration": score.get("integration", 0),
                "checks": score.get("details", {}),
            },
        }
    elif "total" in score:
        # New standard format — ensure other fields exist
        if "passed" not in score:
            score["passed"] = 0
        if "tests_total" not in score:
            score["tests_total"] = 0
        if "details" not in score:
            score["details"] = {}

    # Default fields
    result.setdefault("bp_mode", "no_blueprint")
    result.setdefault("agent_mode", "single")
    result.setdefault("model_key", "default")
    result.setdefault("model_label", "")
    result.setdefault("run_index", 0)
    result.setdefault("success", True)
    result.setdefault("elapsed", 0.0)

    return result
