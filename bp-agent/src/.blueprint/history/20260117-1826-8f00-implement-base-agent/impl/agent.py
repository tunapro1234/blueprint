"""Base Agent implementation."""

from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path
from typing import Optional, Callable


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
    root = Path(__file__).resolve().parent
    state_dir = _detect_state_dir(root)
    pinned = _load_pinned_deps(state_dir / "state.yaml")
    for dep_path, snapshot_id in pinned.items():
        dep_dir = (root / dep_path).resolve()
        dep_state_dir = _detect_state_dir(dep_dir)
        impl_dir = dep_state_dir / "history" / snapshot_id / "impl"
        if impl_dir.exists():
            impl_path = str(impl_dir)
            if impl_path not in os.sys.path:
                os.sys.path.insert(0, impl_path)


_inject_snapshot_deps()

from llm import LLMClient, Message
from tools import ToolRegistry, ToolSchema
from task import TaskStore


@dataclass
class AgentConfig:
    model: str = "gemini-3-flash-preview"
    max_iterations: int = 10
    temperature: float = 0.3
    enable_task_store: bool = True


@dataclass
class AgentResult:
    success: bool
    output: str
    task_id: Optional[str] = None


class Agent:
    def __init__(self, name: str, config: AgentConfig | None = None, system_prompt: str | None = None):
        self.name = name
        self.config = config or AgentConfig()
        self.system_prompt = system_prompt or "You are a helpful assistant."

        self.llm = LLMClient(api_keys=load_api_keys(), model=self.config.model)
        self.tools = ToolRegistry()
        self.tasks = TaskStore() if self.config.enable_task_store else None

    def add_tool(self, name: str, handler: Callable, schema: ToolSchema):
        self.tools.register(name, handler, schema)

    def execute(self, instruction: str) -> AgentResult:
        task = self.tasks.create(instruction) if self.tasks else None

        messages = [
            Message(role="system", content=self.system_prompt),
            Message(role="user", content=instruction),
        ]

        tool_schemas = self.tools.get_schemas() if self.tools.count() > 0 else None

        for _ in range(self.config.max_iterations):
            response = self.llm.complete(messages, tools=tool_schemas, temperature=self.config.temperature)

            if not response.tool_calls:
                if self.tasks and task:
                    self.tasks.update(task.id, status="completed", output=response.content)
                return AgentResult(success=True, output=response.content, task_id=task.id if task else None)

            messages.append(Message(role="assistant", content=response.content))

            for tool_call in response.tool_calls:
                result = self.tools.execute(tool_call.name, tool_call.args)
                messages.append(
                    Message(role="user", content=f"Tool {tool_call.name} result: {result.output}")
                )

        if self.tasks and task:
            self.tasks.update(task.id, status="failed", error="Max iterations reached")

        return AgentResult(success=False, output="", task_id=task.id if task else None)


def load_api_keys() -> list[str]:
    """Load API keys from environment variables."""
    keys: list[str] = []

    primary = os.getenv("GEMINI_API_KEY")
    if primary:
        keys.append(primary)

    for i in range(2, 10):
        key = os.getenv(f"GEMINI_API_KEY_{i}")
        if key:
            keys.append(key)

    if not keys:
        raise ValueError("No API keys found")

    return keys
