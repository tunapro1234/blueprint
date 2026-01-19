"""API layer exports."""

from __future__ import annotations

import sys
from pathlib import Path
from typing import Optional


def _detect_state_dir(base_dir: Path) -> Path:
    for name in (".blueprint", "blueprint"):
        candidate = base_dir / name
        if candidate.exists():
            return candidate
    return base_dir / ".blueprint"


def _load_pinned_deps(state_path: Path) -> dict[str, str]:
    if not state_path.exists():
        return {}

    pinned: dict[str, str] = {}
    current_dep: Optional[str] = None
    in_deps = False

    for raw in state_path.read_text(encoding="utf-8").splitlines():
        line = raw.rstrip()
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue

        if not in_deps:
            if stripped == "deps:":
                in_deps = True
            continue

        if line and not line.startswith((" ", "\t")):
            break

        if line.startswith("  ") and stripped.endswith(":") and not stripped.startswith("pinned:"):
            current_dep = stripped[:-1].strip().strip('"').strip("'")
            continue

        if current_dep and stripped.startswith("pinned:"):
            value = stripped.split(":", 1)[1].strip().strip('"').strip("'")
            if value:
                pinned[current_dep] = value

    return pinned


def _inject_snapshot_deps():
    base_dir = Path(__file__).resolve().parent
    parent_dir = base_dir.parent
    if str(parent_dir) not in sys.path:
        sys.path.insert(0, str(parent_dir))

    state_dir = _detect_state_dir(base_dir)
    pinned = _load_pinned_deps(state_dir / "state.yaml")
    for dep_path, snapshot_id in pinned.items():
        dep_dir = (base_dir / dep_path).resolve()
        dep_state_dir = _detect_state_dir(dep_dir)
        impl_dir = dep_state_dir / "history" / snapshot_id / "impl"
        if impl_dir.exists():
            impl_path = str(impl_dir)
            if impl_path not in sys.path:
                sys.path.insert(0, impl_path)


_inject_snapshot_deps()

try:
    from agent import Agent, AgentConfig, AgentResult
    from tools import ToolRegistry, ToolSchema, ToolResult
    from task import TaskStore, Task, TaskStatus
    from llm import (
        LLMRouter,
        ProviderAdapter,
        CompletionRequest,
        LLMResponse,
        ProviderError,
        Message,
        GeminiAdapter,
        GeminiConfig,
        CodexAdapter,
        CodexConfig,
        CodexAuth,
        OpusAdapter,
        OpusConfig,
    )
except ImportError:
    from ..agent import Agent, AgentConfig, AgentResult
    from ..tools import ToolRegistry, ToolSchema, ToolResult
    from ..task import TaskStore, Task, TaskStatus
    from ..llm import (
        LLMRouter,
        ProviderAdapter,
        CompletionRequest,
        LLMResponse,
        ProviderError,
        Message,
        GeminiAdapter,
        GeminiConfig,
        CodexAdapter,
        CodexConfig,
        CodexAuth,
        OpusAdapter,
        OpusConfig,
    )

from .server import AgentServer

__all__ = [
    "Agent",
    "AgentConfig",
    "AgentResult",
    "ToolRegistry",
    "ToolSchema",
    "ToolResult",
    "TaskStore",
    "Task",
    "TaskStatus",
    "LLMRouter",
    "ProviderAdapter",
    "CompletionRequest",
    "LLMResponse",
    "ProviderError",
    "Message",
    "GeminiAdapter",
    "GeminiConfig",
    "CodexAdapter",
    "CodexConfig",
    "CodexAuth",
    "OpusAdapter",
    "OpusConfig",
    "AgentServer",
]
