"""Base Agent implementation."""

from __future__ import annotations

import os
import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import Optional, Callable, Any


def _detect_state_dir(base_dir: Path) -> Path:
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


# Snapshot imports are handled by snapshot-aware entrypoints (e.g. run_api_server.py).

from bp_agent.llm import (
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
from bp_agent.tools import ToolRegistry, ToolSchema, register_builtins, GiveResultSignal, build_schema
from bp_agent.task import TaskStore


@dataclass
class AgentConfig:
    provider: str = "gemini"
    model: str = "gemini-3-flash-preview"
    reasoning_effort: Optional[str] = None
    max_iterations: int = 10
    temperature: float = 0.3
    enable_task_store: bool = True
    enable_builtin_tools: bool = True
    enable_subagents: bool = False
    codex_auth_file: Optional[str] = None
    # Subagent worker config (used when this agent spawns workers)
    worker_model: Optional[str] = None  # defaults to same model
    worker_provider: Optional[str] = None  # defaults to same provider
    worker_max_iterations: int = 10
    worker_tools: Optional[list[str]] = None  # None = all builtins


@dataclass
class AgentResult:
    success: bool
    output: str
    task_id: Optional[str] = None
    trace: Optional[dict[str, Any]] = None


DEFAULT_SYSTEM_PROMPT = """You are a task execution soldier. Execute orders precisely. No chatter.

Tools: bash, read_file, write_file, list_dir, give_result

PROTOCOL:
1. Execute task using tools
2. Call give_result with CLEAN output only
3. Never repeat same tool call

OUTPUT RULES:
- NO explanations, NO fluff, NO "here is...", NO markdown unless data requires it
- List requested → return list (one item per line)
- File requested → return file content directly
- Number requested → return just the number
- Data requested → return just the data

Example: "list files" → give_result("src\nbin\nREADME.md")
Example: "count .py files" → give_result("26")
Example: "read config.json" → give_result('{"key": "value"}')"""

CHAT_SYSTEM_PROMPT = """You are a helpful assistant with access to tools.

Use tools when you need to interact with the filesystem or run commands.
Respond naturally in conversation. Use markdown when helpful.

When you have a specific task that can be delegated, use spawn_worker to create
a worker agent. Workers run independently with their own context - give them
clear, self-contained instructions. For multiple independent tasks, use
spawn_workers to run them in parallel."""


class Agent:
    def __init__(self, name: str, config: AgentConfig | None = None, system_prompt: str | None = None):
        self.name = name
        self.config = config or AgentConfig()
        self.system_prompt = system_prompt or DEFAULT_SYSTEM_PROMPT

        self.llm = _build_llm_router(self.config)
        self.tools = ToolRegistry()
        if self.config.enable_builtin_tools:
            register_builtins(self.tools)
        if self.config.enable_subagents:
            self._register_subagent_tools()
        self.tasks = TaskStore() if self.config.enable_task_store else None
        self._trace_enabled = False
        self._last_trace: Optional[dict[str, Any]] = None
        self._chat_messages: list[Message] = []
        self._workers: dict[str, AgentResult] = {}  # worker_id -> result
        self._worker_counter = 0

    def add_tool(self, name: str, handler: Callable, schema: ToolSchema):
        self.tools.register(name, handler, schema)

    # --- Subagent / Worker spawning ---

    def _register_subagent_tools(self):
        """Register spawn_worker and check_worker tools."""
        parent = self  # closure reference

        def _spawn_worker(instruction: str, context: str = "", system_prompt: str = "") -> str:
            return parent._spawn_worker(instruction, context, system_prompt)

        def _spawn_workers(tasks: str) -> str:
            return parent._spawn_workers_parallel(tasks)

        self.tools.register("spawn_worker", _spawn_worker, build_schema(
            "spawn_worker",
            "Spawn a worker agent to execute a specific task. The worker has its own isolated context, "
            "runs the task, and returns the result. Use this for tasks that can be delegated.",
            instruction={"type": "string", "description": "Clear, specific instruction for the worker", "required": True},
            context={"type": "string", "description": "Relevant context from your conversation to pass to the worker"},
            system_prompt={"type": "string", "description": "Custom system prompt for the worker (optional)"},
        ))

        self.tools.register("spawn_workers", _spawn_workers, build_schema(
            "spawn_workers",
            "Spawn multiple workers in parallel. Each task runs independently. "
            "Pass a JSON array of objects with 'instruction' and optional 'context' fields.",
            tasks={"type": "string", "description": 'JSON array: [{"instruction": "...", "context": "..."}, ...]', "required": True},
        ))

    def _make_worker(self, system_prompt: str | None = None) -> "Agent":
        """Create a disposable worker agent that shares this agent's LLM router."""
        worker_config = AgentConfig(
            provider=self.config.worker_provider or self.config.provider,
            model=self.config.worker_model or self.config.model,
            max_iterations=self.config.worker_max_iterations,
            temperature=self.config.temperature,
            enable_task_store=False,
            enable_builtin_tools=True,
            enable_subagents=False,  # Workers cannot spawn subagents
        )
        self._worker_counter += 1
        worker = Agent(
            name=f"{self.name}/worker-{self._worker_counter}",
            config=worker_config,
            system_prompt=system_prompt or DEFAULT_SYSTEM_PROMPT,
        )
        # Share LLM router (API keys, rotation state)
        worker.llm = self.llm
        return worker

    def _spawn_worker(self, instruction: str, context: str = "", system_prompt: str = "") -> str:
        """Spawn a single worker, execute, return result."""
        worker = self._make_worker(system_prompt or None)

        full_instruction = instruction
        if context:
            full_instruction = f"Context: {context}\n\nTask: {instruction}"

        result = worker.execute(full_instruction)
        worker_id = worker.name
        self._workers[worker_id] = result

        if result.success:
            return result.output
        return f"[worker failed] {result.output}"

    def _spawn_workers_parallel(self, tasks_json: str) -> str:
        """Spawn multiple workers in parallel."""
        import concurrent.futures

        try:
            tasks = json.loads(tasks_json)
        except json.JSONDecodeError as e:
            return f"[error] Invalid JSON: {e}"

        if not isinstance(tasks, list):
            return "[error] Expected JSON array"

        results = {}

        with concurrent.futures.ThreadPoolExecutor(max_workers=len(tasks)) as executor:
            futures = {}
            for i, task in enumerate(tasks):
                instr = task.get("instruction", "") if isinstance(task, dict) else str(task)
                ctx = task.get("context", "") if isinstance(task, dict) else ""
                sp = task.get("system_prompt", "") if isinstance(task, dict) else ""
                futures[executor.submit(self._spawn_worker, instr, ctx, sp)] = i

            for future in concurrent.futures.as_completed(futures):
                idx = futures[future]
                try:
                    results[idx] = future.result()
                except Exception as e:
                    results[idx] = f"[error] {e}"

        # Return results in order
        lines = []
        for i in range(len(tasks)):
            instr = tasks[i].get("instruction", str(tasks[i])) if isinstance(tasks[i], dict) else str(tasks[i])
            lines.append(f"[worker {i+1}] {instr}")
            lines.append(results.get(i, "[no result]"))
            lines.append("")
        return "\n".join(lines).strip()

    def chat(self, message: str, system_prompt: str | None = None) -> str:
        """Multi-turn chat. Maintains conversation history. Tools work, give_result not required."""
        if not self._chat_messages:
            self._chat_messages = [
                Message(role="system", content=system_prompt or self.system_prompt),
            ]

        self._chat_messages.append(Message(role="user", content=message))

        tool_schemas = self.tools.get_schemas() if self.tools.count() > 0 else None

        for _ in range(self.config.max_iterations):
            request = CompletionRequest(
                messages=self._chat_messages,
                tools=tool_schemas,
                temperature=self.config.temperature,
                model=self.config.model,
                provider=self.config.provider,
            )
            response = self.llm.complete(request)

            if not response.tool_calls:
                self._chat_messages.append(Message(role="assistant", content=response.content))
                return response.content

            self._chat_messages.append(Message(role="assistant", content=response.content))

            for tool_call in response.tool_calls:
                try:
                    result = self.tools.execute(tool_call.name, tool_call.args)
                except GiveResultSignal as sig:
                    self._chat_messages.append(
                        Message(role="user", content=f"[tool:{tool_call.name}] {sig.result}")
                    )
                    self._chat_messages.append(Message(role="assistant", content=sig.result))
                    return sig.result

                self._chat_messages.append(
                    Message(role="user", content=f"[tool:{tool_call.name}] {result.output}")
                )

        return "(max iterations reached)"

    def reset_chat(self):
        """Clear chat history."""
        self._chat_messages = []

    @property
    def chat_history(self) -> list[Message]:
        """Get current chat messages (read-only view)."""
        return list(self._chat_messages)

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

        # Track tool calls to detect duplicates
        previous_calls: dict[str, str] = {}  # "name:args" -> result
        duplicate_count = 0
        last_tool_result: Optional[str] = None

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
                # Check for duplicate tool calls
                import json
                call_key = f"{tool_call.name}:{json.dumps(tool_call.args, sort_keys=True)}"
                if call_key in previous_calls:
                    duplicate_count += 1
                    # After 2 duplicates, auto-return last result as failsafe
                    if duplicate_count >= 2 and last_tool_result:
                        if self.tasks and task:
                            self.tasks.update(task.id, status="completed", output=last_tool_result)
                        return AgentResult(
                            success=True,
                            output=last_tool_result,
                            task_id=task.id if task else None,
                            trace=trace,
                        )
                    # Duplicate detected - don't execute, warn strongly
                    messages.append(
                        Message(role="user", content=f"ERROR: You already called {tool_call.name} with these exact arguments. Result was: {previous_calls[call_key]}\n\nYou MUST call give_result now with your answer. Do not repeat tool calls.")
                    )
                    continue

                try:
                    result = self.tools.execute(tool_call.name, tool_call.args)
                except GiveResultSignal as sig:
                    # give_result was called - return the result
                    if trace is not None:
                        trace["tool_results"].append(
                            {"name": "give_result", "output": sig.result, "error": None}
                        )
                        self._last_trace = trace
                    if self.tasks and task:
                        self.tasks.update(task.id, status="completed", output=sig.result)
                    return AgentResult(
                        success=True,
                        output=sig.result,
                        task_id=task.id if task else None,
                        trace=trace,
                    )
                # Store result for duplicate detection and failsafe
                previous_calls[call_key] = result.output
                last_tool_result = result.output

                if trace is not None:
                    trace["tool_results"].append(
                        {"name": tool_call.name, "output": result.output, "error": result.error}
                    )
                messages.append(
                    Message(role="user", content=f"Tool {tool_call.name} returned: {result.output}\n\nIf this answers the question, call give_result now.")
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
