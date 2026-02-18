"""Base Agent implementation."""

from __future__ import annotations

import os
import json
import threading
from dataclasses import dataclass, field
from pathlib import Path
from typing import Iterator, Optional, Callable, Any


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
    OpenAIAdapter,
    OpenAIConfig,
    OpusAdapter,
    OpusConfig,
)
from bp_agent.llm.types import accumulate_stream
from bp_agent.tools import ToolRegistry, ToolSchema, register_builtins, GiveResultSignal, build_schema
from bp_agent.task import TaskStore
from bp_agent.store import AgentStore
try:
    from bp_tunnel.hub import TunnelHub
except ImportError:
    TunnelHub = None  # bp-tunnel not installed


class MessageInbox:
    """Thread-safe message inbox for continuous chat."""

    def __init__(self):
        self._messages: list[str] = []
        self._lock = threading.Lock()
        self._event = threading.Event()

    def push(self, message: str) -> None:
        with self._lock:
            self._messages.append(message)
            self._event.set()

    def pull_all(self) -> list[str]:
        with self._lock:
            msgs = self._messages.copy()
            self._messages.clear()
            self._event.clear()
            return msgs

    def wait(self, timeout: float | None = None) -> bool:
        """Block until a message arrives. Returns True if message available."""
        return self._event.wait(timeout=timeout)

    @property
    def has_messages(self) -> bool:
        return self._event.is_set()


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
    store_dir: Optional[str] = None  # base dir for agent storage (None = no persistence)
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
        self.store = AgentStore(name, self.config.store_dir) if self.config.store_dir else None
        self._trace_enabled = False
        self._last_trace: Optional[dict[str, Any]] = None
        self._chat_messages: list[Message] = []
        self._session_id: Optional[str] = None
        self._cancel = threading.Event()
        self._streaming = False
        self._inbox = MessageInbox()
        self._workers: dict[str, AgentResult] = {}  # worker_id -> result
        self._worker_counter = 0
        self._hub: Optional[TunnelHub] = None
        self._hub_alias: Optional[str] = None

    def add_tool(self, name: str, handler: Callable, schema: ToolSchema):
        self.tools.register(name, handler, schema)

    # --- Tunnel / messaging ---

    def connect_hub(self, hub: TunnelHub, alias: str | None = None):
        """Connect this agent to a TunnelHub for inter-agent messaging."""
        self._hub = hub
        self._hub_alias = alias or self.name
        self._register_tunnel_tools()

    def _register_tunnel_tools(self):
        """Register send_message / check_messages / send_to_channel / read_channel tools."""
        hub = self._hub
        me = self._hub_alias or self.name

        def _send_message(to: str, message: str) -> str:
            hub.send(f"agent:{to}", message, sender=me)
            return f"[sent to {to}]"

        def _check_messages() -> str:
            msgs = hub.receive_all(f"agent:{me}")
            if not msgs:
                return "[no messages]"
            return "\n".join(str(m) for m in msgs)

        def _send_to_channel(channel: str, message: str) -> str:
            hub.send(channel, message, sender=me)
            return f"[sent to channel:{channel}]"

        def _read_channel(channel: str) -> str:
            msgs = hub.receive_all(channel)
            if not msgs:
                return "[empty]"
            return "\n".join(str(m) for m in msgs)

        self.tools.register("send_message", _send_message, build_schema(
            "send_message",
            "Send a message to another agent by name",
            to={"type": "string", "description": "Target agent name", "required": True},
            message={"type": "string", "description": "Message content", "required": True},
        ))
        self.tools.register("check_messages", _check_messages, build_schema(
            "check_messages",
            "Check your inbox for messages from other agents",
        ))
        self.tools.register("send_to_channel", _send_to_channel, build_schema(
            "send_to_channel",
            "Send a message to a named channel (any agent can read it)",
            channel={"type": "string", "description": "Channel name", "required": True},
            message={"type": "string", "description": "Message content", "required": True},
        ))
        self.tools.register("read_channel", _read_channel, build_schema(
            "read_channel",
            "Read all messages from a named channel",
            channel={"type": "string", "description": "Channel name", "required": True},
        ))

    def attach(self, tunnel) -> None:
        """Attach to a bp_tunnel.Tunnel for messaging and task assignment."""
        from bp_tunnel.hub import TunnelHub as _TunnelHub

        hub = _TunnelHub(tunnel)
        self.connect_hub(hub, alias=tunnel.agent_name)
        self._tunnel = tunnel

    def run(self, poll_interval: float = 1.0) -> None:
        """Listen for tasks on attached tunnel and auto-execute them.

        Polls for type='task' messages, runs execute(), sends results back.
        """
        import time as _time

        if not hasattr(self, "_tunnel") or self._tunnel is None:
            raise RuntimeError("No tunnel attached. Call agent.attach(tunnel) first.")

        while True:
            msg = self._tunnel.receive(type="task")
            if msg:
                result = self.execute(msg.payload)
                self._tunnel.send(
                    result.output if result.success else f"ERROR: {result.output}",
                    to=msg.from_,
                    type="result",
                )
            else:
                _time.sleep(poll_interval)

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
                self._persist_session()
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
                    self._persist_session()
                    return sig.result

                self._chat_messages.append(
                    Message(role="user", content=f"[tool:{tool_call.name}] {result.output}")
                )

        self._persist_session()
        return "(max iterations reached)"

    def cancel(self) -> None:
        """Cancel current stream. Safe to call from another thread."""
        self._cancel.set()

    @property
    def is_streaming(self) -> bool:
        return self._streaming

    def chat_stream(self, message: str | None = None, system_prompt: str | None = None) -> Iterator[str]:
        """Multi-turn streaming chat. Yields text deltas, handles tool calls internally.

        Supports cancellation via cancel() or push_message(). When cancelled:
        - Partial response is saved to history as [interrupted]
        - Session is NOT persisted
        - Next chat_stream call will have the partial context

        Args:
            message: User message. If None, only inbox messages (from push_message) are processed.
            system_prompt: System prompt for first turn.
        """
        self._cancel.clear()
        self._streaming = True

        if not self._chat_messages:
            self._chat_messages = [
                Message(role="system", content=system_prompt or self.system_prompt),
            ]

        if message:
            self._chat_messages.append(Message(role="user", content=message))

        # Pull any queued messages from push_message()
        inbox_msgs = [m for m in self._inbox.pull_all() if m]
        for msg in inbox_msgs:
            self._chat_messages.append(Message(role="user", content=msg))

        # Nothing to process — no explicit message and no inbox messages
        if not message and not inbox_msgs:
            self._streaming = False
            return

        tool_schemas = self.tools.get_schemas() if self.tools.count() > 0 else None

        try:
            for _ in range(self.config.max_iterations):
                if self._cancel.is_set():
                    return

                request = CompletionRequest(
                    messages=self._chat_messages,
                    tools=tool_schemas,
                    temperature=self.config.temperature,
                    model=self.config.model,
                    provider=self.config.provider,
                )

                # Collect chunks, yield text deltas, check cancel between chunks
                text_parts: list[str] = []
                all_chunks: list = []
                interrupted = False
                for chunk in self.llm.complete_stream(request):
                    if self._cancel.is_set():
                        interrupted = True
                        break
                    all_chunks.append(chunk)
                    if chunk.delta:
                        text_parts.append(chunk.delta)
                        yield chunk.delta

                if interrupted:
                    partial = "".join(text_parts)
                    if partial:
                        self._chat_messages.append(
                            Message(role="assistant", content=partial + "\n[interrupted]")
                        )
                    # Don't persist — next call will have context
                    return

                response = accumulate_stream(iter(all_chunks))

                if not response.tool_calls:
                    self._chat_messages.append(Message(role="assistant", content=response.content))
                    self._persist_session()
                    return

                self._chat_messages.append(Message(role="assistant", content=response.content))

                for tool_call in response.tool_calls:
                    if self._cancel.is_set():
                        return

                    try:
                        result = self.tools.execute(tool_call.name, tool_call.args)
                    except GiveResultSignal as sig:
                        self._chat_messages.append(
                            Message(role="user", content=f"[tool:{tool_call.name}] {sig.result}")
                        )
                        self._chat_messages.append(Message(role="assistant", content=sig.result))
                        self._persist_session()
                        yield sig.result
                        return

                    self._chat_messages.append(
                        Message(role="user", content=f"[tool:{tool_call.name}] {result.output}")
                    )

            self._persist_session()
            yield "(max iterations reached)"
        finally:
            self._streaming = False
            self._cancel.clear()

    def reset_chat(self):
        """Clear chat history and start a new session."""
        self._chat_messages = []
        self._session_id = None

    @property
    def chat_history(self) -> list[Message]:
        """Get current chat messages (read-only view)."""
        return list(self._chat_messages)

    @property
    def session_id(self) -> Optional[str]:
        return self._session_id

    def load_session(self, session_id: str) -> bool:
        """Load a previous session's chat history. Returns True if found."""
        if not self.store:
            return False
        messages = self.store.load_session(session_id)
        if messages is None:
            return False
        self._chat_messages = [Message(**m) for m in messages]
        self._session_id = session_id
        return True

    def _persist_session(self) -> None:
        """Save current chat to store if persistence is enabled."""
        if not self.store or not self._chat_messages:
            return
        if not self._session_id:
            self._session_id = self.store.new_session_id()
        self.store.save_session(
            self._session_id,
            [{"role": m.role, "content": m.content} for m in self._chat_messages],
        )

    def push_message(self, message: str) -> None:
        """Push a message to the inbox. If agent is streaming, cancels current stream.

        Use with chat_stream: push a message from another thread, then call
        chat_stream() (no args) to process queued messages.
        """
        self._inbox.push(message)
        if self._streaming:
            self._cancel.set()

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
                    Message(role="user", content=f"Tool {tool_call.name} returned: {result.output}")
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
            config.model if config.provider == "gemini" else "gemini-3-flash-preview"
        )
        gemini_temperature = (
            config.temperature if config.provider == "gemini" else 0.3
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

    # --- OpenRouter / OpenAI-compatible ---
    openrouter_key = os.getenv("OPENROUTER_API_KEY")
    openrouter_base = os.getenv("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1")
    if openrouter_key:
        or_model = config.model if config.provider == "openrouter" else "openai/gpt-4o"
        or_temp = config.temperature if config.provider == "openrouter" else 0.3
        router.register_provider(
            "openrouter",
            OpenAIAdapter(
                OpenAIConfig(
                    api_keys=[openrouter_key],
                    base_url=openrouter_base,
                    model=or_model,
                    temperature=or_temp,
                )
            ),
        )
    elif config.provider == "openrouter":
        raise ValueError("OpenRouter provider selected but OPENROUTER_API_KEY not set")

    return router
