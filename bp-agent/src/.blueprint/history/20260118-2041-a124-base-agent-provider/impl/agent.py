"""Base Agent implementation."""

from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path
from typing import Optional, Callable, Any


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

from llm import (
    LLMRouter,
    CompletionRequest,
    Message,
    GeminiAdapter,
    GeminiConfig,
    CodexAdapter,
    CodexConfig,
    OpusAdapter,
    OpusConfig,
)
from tools import ToolRegistry, ToolSchema
from task import TaskStore


@dataclass
class AgentConfig:
    provider: str = "gemini"
    model: str = "gemini-3-flash-preview"
    reasoning_effort: Optional[str] = None
    max_iterations: int = 10
    temperature: float = 0.3
    enable_task_store: bool = True
    codex_auth_file: Optional[str] = None


@dataclass
class AgentResult:
    success: bool
    output: str
    task_id: Optional[str] = None
    trace: Optional[dict[str, Any]] = None


class Agent:
    def __init__(self, name: str, config: AgentConfig | None = None, system_prompt: str | None = None):
        self.name = name
        self.config = config or AgentConfig()
        self.system_prompt = system_prompt or "You are a helpful assistant."

        self.llm = _build_llm_router(self.config)
        self.tools = ToolRegistry()
        self.tasks = TaskStore() if self.config.enable_task_store else None
        self._trace_enabled = False
        self._last_trace: Optional[dict[str, Any]] = None

    def add_tool(self, name: str, handler: Callable, schema: ToolSchema):
        self.tools.register(name, handler, schema)

    def execute(self, instruction: str) -> AgentResult:
        task = self.tasks.create(instruction) if self.tasks else None

        messages = [
            Message(role="system", content=self.system_prompt),
            Message(role="user", content=instruction),
        ]

        tool_schemas = self.tools.get_schemas() if self.tools.count() > 0 else None
        trace: Optional[dict[str, Any]] = None
        if self._trace_enabled:
            trace = {
                "provider": self.config.provider,
                "model": self.config.model,
                "tool_calls": [],
                "tool_results": [],
                "raw": None,
            }

        for _ in range(self.config.max_iterations):
            request = CompletionRequest(
                messages=messages,
                tools=tool_schemas,
                temperature=self.config.temperature,
                model=self.config.model,
                provider=self.config.provider,
            )
            response = self.llm.complete(request)
            if trace is not None:
                trace["raw"] = response.raw
                if response.tool_calls:
                    trace["tool_calls"].extend(
                        {"name": tc.name, "args": tc.args} for tc in response.tool_calls
                    )

            if not response.tool_calls:
                if self.tasks and task:
                    self.tasks.update(task.id, status="completed", output=response.content)
                if trace is not None:
                    self._last_trace = trace
                return AgentResult(
                    success=True,
                    output=response.content,
                    task_id=task.id if task else None,
                    trace=trace,
                )

            messages.append(Message(role="assistant", content=response.content))

            for tool_call in response.tool_calls:
                result = self.tools.execute(tool_call.name, tool_call.args)
                if trace is not None:
                    trace["tool_results"].append(
                        {"name": tool_call.name, "output": result.output, "error": result.error}
                    )
                messages.append(
                    Message(role="user", content=f"Tool {tool_call.name} result: {result.output}")
                )

        if self.tasks and task:
            self.tasks.update(task.id, status="failed", error="Max iterations reached")

        if trace is not None:
            self._last_trace = trace

        return AgentResult(
            success=False,
            output="",
            task_id=task.id if task else None,
            trace=trace,
        )


def load_gemini_keys() -> list[str]:
    """Load Gemini API keys from environment variables."""
    keys: list[str] = []

    raw = os.getenv("GEMINI_API_KEY") or os.getenv("GEMINI_API_KEYS")
    if raw:
        for item in raw.split(","):
            if item.strip():
                keys.append(item.strip())

    for i in range(2, 10):
        key = os.getenv(f"GEMINI_API_KEY_{i}")
        if key:
            keys.append(key)

    if not keys:
        raise ValueError("No API keys found")

    return keys


def load_api_keys() -> list[str]:
    """Backward-compatible alias for load_gemini_keys."""
    return load_gemini_keys()


def load_codex_keys() -> list[str]:
    keys: list[str] = []

    raw = os.getenv("CODEX_API_KEY") or os.getenv("CODEX_API_KEYS")
    if raw:
        for item in raw.split(","):
            if item.strip():
                keys.append(item.strip())

    for i in range(2, 10):
        key = os.getenv(f"CODEX_API_KEY_{i}")
        if key:
            keys.append(key)

    return keys


def load_opus_keys() -> list[str]:
    keys: list[str] = []

    raw = os.getenv("OPUS_API_KEY") or os.getenv("OPUS_API_KEYS")
    if raw:
        for item in raw.split(","):
            if item.strip():
                keys.append(item.strip())

    for i in range(2, 10):
        key = os.getenv(f"OPUS_API_KEY_{i}")
        if key:
            keys.append(key)

    return keys


def _build_llm_router(config: AgentConfig) -> LLMRouter:
    router = LLMRouter(default_provider=config.provider or "gemini")

    try:
        gemini_keys = load_gemini_keys()
    except ValueError:
        if config.provider == "gemini":
            raise
    else:
        gemini_model = (
            config.model if config.provider == "gemini" else GeminiConfig().model
        )
        gemini_temperature = (
            config.temperature if config.provider == "gemini" else GeminiConfig().temperature
        )
        router.register_provider(
            "gemini",
            GeminiAdapter(
                GeminiConfig(
                    api_keys=gemini_keys,
                    model=gemini_model,
                    temperature=gemini_temperature,
                )
            ),
        )

    codex_keys = load_codex_keys()
    auth_files: list[str | None] = []
    if config.codex_auth_file is not None:
        auth_files = [config.codex_auth_file]
    elif config.provider == "codex":
        auth_files = [None]

    if codex_keys or auth_files:
        codex_model = config.model if config.provider == "codex" else CodexConfig().model
        reasoning = config.reasoning_effort or CodexConfig().reasoning_effort
        router.register_provider(
            "codex",
            CodexAdapter(
                CodexConfig(
                    api_keys=codex_keys or None,
                    auth_files=auth_files or None,
                    model=codex_model,
                    reasoning_effort=reasoning,
                )
            ),
        )
    elif config.provider == "codex":
        raise ValueError("Codex provider selected but no credentials found")

    opus_keys = load_opus_keys()
    opus_base_url = os.getenv("OPUS_BASE_URL")
    opus_endpoint = os.getenv("OPUS_ENDPOINT", "/responses")
    if opus_keys and opus_base_url:
        opus_model = config.model if config.provider == "opus" else None
        opus_temperature = config.temperature if config.provider == "opus" else 0.3
        router.register_provider(
            "opus",
            OpusAdapter(
                OpusConfig(
                    api_keys=opus_keys,
                    base_url=opus_base_url,
                    endpoint=opus_endpoint,
                    model=opus_model,
                    temperature=opus_temperature,
                )
            ),
        )
    elif config.provider == "opus":
        if not opus_keys:
            raise ValueError("Opus provider selected but no OPUS_API_KEY found")
        raise ValueError("Opus provider selected but OPUS_BASE_URL not set")

    return router
