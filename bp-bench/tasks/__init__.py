"""Task types for bp-bench.

Registry of available task types:
  - blueprint:    Original store/api/cli blueprint benchmark
  - exercism:     Exercism Python exercises (pytest-scored)
  - swebench:     SWE-bench Lite real-world bug fixes (test-scored)
  - translation:  Translation between language pairs (similarity-scored)
  - arc-agi:      ARC-AGI abstract reasoning (exact grid match)
"""

from .base import TaskType, TaskResult, run_pytest
from .blueprint import BlueprintTask
from .exercism import ExercismTask
from .swebench import SWEBenchTask
from .translation import TranslationTask
from .arc_agi import ArcAgiTask

TASK_REGISTRY: dict[str, type[TaskType]] = {
    "blueprint": BlueprintTask,
    "exercism": ExercismTask,
    "swebench": SWEBenchTask,
    "translation": TranslationTask,
    "arc-agi": ArcAgiTask,
}


def get_task_type(name: str, **kwargs) -> TaskType:
    """Instantiate a task type by name.

    Parameters
    ----------
    name : str
        One of the keys in TASK_REGISTRY.
    **kwargs
        Forwarded to the task type constructor.
    """
    cls = TASK_REGISTRY.get(name)
    if cls is None:
        available = ", ".join(sorted(TASK_REGISTRY.keys()))
        raise ValueError(f"Unknown task type {name!r}. Available: {available}")
    return cls(**kwargs)


__all__ = [
    "TaskType",
    "TaskResult",
    "run_pytest",
    "BlueprintTask",
    "ExercismTask",
    "SWEBenchTask",
    "TranslationTask",
    "ArcAgiTask",
    "TASK_REGISTRY",
    "get_task_type",
]
