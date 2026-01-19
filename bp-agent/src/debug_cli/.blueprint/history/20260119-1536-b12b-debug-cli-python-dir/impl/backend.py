"""Backend implementations for debug CLI."""

from __future__ import annotations

import json
import sys
from abc import ABC, abstractmethod
from pathlib import Path
from typing import Optional
from urllib import request as urlrequest, error as urlerror

from .models import ChatMessage, CLIConfig, ExecuteResult, TaskInfo, ToolInfo


class Backend(ABC):
    @abstractmethod
    def execute(self, instruction: str, history: list[ChatMessage], config: CLIConfig) -> ExecuteResult:
        raise NotImplementedError

    @abstractmethod
    def list_tasks(self, limit: int = 10) -> list[TaskInfo]:
        raise NotImplementedError

    @abstractmethod
    def get_task(self, task_id: str) -> Optional[TaskInfo]:
        raise NotImplementedError

    @abstractmethod
    def list_tools(self) -> list[ToolInfo]:
        raise NotImplementedError

    @abstractmethod
    def list_models(self) -> list[str]:
        raise NotImplementedError


class HTTPBackend(Backend):
    def __init__(self, base_url: str, token: Optional[str] = None):
        self.base_url = base_url.rstrip("/")
        self.token = token

    def _headers(self) -> dict[str, str]:
        headers = {"Content-Type": "application/json"}
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        return headers

    def execute(self, instruction: str, history: list[ChatMessage], config: CLIConfig) -> ExecuteResult:
        transcript = self._build_transcript(history, instruction)
        payload = {
            "instruction": transcript,
            "provider": config.provider,
            "model": config.model,
            "temperature": config.temperature,
            "system_prompt": config.system_prompt,
            "debug": config.debug,
        }
        status, data, body = self._request_json("POST", "/execute", payload)
        if status >= 400:
            raise RuntimeError(body or f"HTTP {status}")
        trace = (data or {}).get("trace") or {}
        return ExecuteResult(
            success=bool((data or {}).get("success", False)),
            output=(data or {}).get("output", ""),
            task_id=(data or {}).get("task_id"),
            tool_calls=trace.get("tool_calls"),
        )

    def list_tasks(self, limit: int = 10) -> list[TaskInfo]:
        status, data, body = self._request_json("GET", f"/tasks?limit={limit}")
        if status >= 400:
            raise RuntimeError(body or f"HTTP {status}")
        tasks = (data or {}).get("tasks", [])
        return [TaskInfo(**t) for t in tasks]

    def get_task(self, task_id: str) -> Optional[TaskInfo]:
        status, data, _ = self._request_json("GET", f"/tasks/{task_id}")
        if status == 404:
            tasks = self.list_tasks(limit=50)
            for t in tasks:
                if t.id == task_id:
                    return t
            return None
        if status >= 400:
            return None
        if data:
            return TaskInfo(**data)
        return None

    def list_tools(self) -> list[ToolInfo]:
        status, data, _ = self._request_json("GET", "/tools")
        if status >= 400:
            return []
        return [ToolInfo(**t) for t in (data or {}).get("tools", [])]

    def list_models(self) -> list[str]:
        status, data, _ = self._request_json("GET", "/models")
        if status >= 400:
            return []
        return list((data or {}).get("models", []))

    def _request_json(self, method: str, path: str, payload: dict | None = None):
        url = f"{self.base_url}{path}"
        data = json.dumps(payload).encode("utf-8") if payload is not None else None
        req = urlrequest.Request(url, data=data, method=method)
        for key, value in self._headers().items():
            req.add_header(key, value)

        body = ""
        status = 0
        try:
            with urlrequest.urlopen(req) as resp:
                status = getattr(resp, "status", resp.getcode())
                body = resp.read().decode("utf-8")
        except urlerror.HTTPError as err:
            status = err.code
            body = err.read().decode("utf-8") if err.fp else ""
        except urlerror.URLError as err:
            raise RuntimeError(f"Connection error: {err}") from err

        data_out = None
        if body:
            try:
                data_out = json.loads(body)
            except json.JSONDecodeError:
                data_out = None
        return status, data_out, body

    def _build_transcript(self, history: list[ChatMessage], new_msg: str) -> str:
        if not history:
            return new_msg
        lines: list[str] = []
        for msg in history:
            lines.append(f"{msg.role}: {msg.content}")
        lines.append(f"user: {new_msg}")
        return "\n".join(lines)


class DirectBackend(Backend):
    def __init__(self):
        self._agent = None
        self._current_config_hash = None

    def execute(self, instruction: str, history: list[ChatMessage], config: CLIConfig) -> ExecuteResult:
        self._ensure_agent(config)
        full_instruction = self._build_transcript(history, instruction)
        result = self._agent.execute(full_instruction)
        tool_calls = None
        if config.debug and getattr(result, "trace", None):
            trace = result.trace or {}
            tool_calls = trace.get("tool_calls")
        return ExecuteResult(
            success=bool(result.success),
            output=result.output,
            task_id=getattr(result, "task_id", None),
            tool_calls=tool_calls,
        )

    def list_tasks(self, limit: int = 10) -> list[TaskInfo]:
        if self._agent and self._agent.tasks:
            tasks = self._agent.tasks.list(limit=limit)
            info: list[TaskInfo] = []
            for t in tasks:
                status = getattr(t.status, "value", t.status)
                info.append(
                    TaskInfo(
                        id=t.id,
                        status=str(status),
                        instruction=t.instruction,
                        output=t.output,
                        created_at=t.created_at,
                    )
                )
            return info
        return []

    def get_task(self, task_id: str) -> Optional[TaskInfo]:
        if self._agent and self._agent.tasks:
            task = self._agent.tasks.get(task_id)
            if task:
                status = getattr(task.status, "value", task.status)
                return TaskInfo(
                    id=task.id,
                    status=str(status),
                    instruction=task.instruction,
                    output=task.output,
                    created_at=task.created_at,
                )
        return None

    def list_tools(self) -> list[ToolInfo]:
        if self._agent:
            tools = []
            for schema in self._agent.tools.get_schemas():
                tools.append(ToolInfo(name=schema.name, description=schema.description))
            return tools
        return []

    def list_models(self) -> list[str]:
        return [
            "gemini-3-flash-preview",
            "gemini-3-pro-preview",
            "gpt-5.2-codex",
            "gpt-5.1-codex-mini",
            "opus",
        ]

    def _ensure_agent(self, config: CLIConfig):
        config_hash = f"{config.provider}:{config.model}:{config.temperature}:{config.system_prompt}"
        if self._agent is not None and self._current_config_hash == config_hash:
            return

        agent_module = _import_agent_module()
        Agent = agent_module.Agent
        AgentConfig = agent_module.AgentConfig

        model = config.model or AgentConfig().model
        agent_config = AgentConfig(
            provider=config.provider,
            model=model,
            temperature=config.temperature,
        )
        self._agent = Agent("debug-cli", config=agent_config, system_prompt=config.system_prompt)
        self._register_default_tools()
        self._current_config_hash = config_hash

    def _register_default_tools(self):
        if not self._agent:
            return
        from tools import ToolSchema

        self._agent.add_tool(
            "calculator",
            _safe_calculate,
            ToolSchema(
                name="calculator",
                description="Evaluate basic arithmetic expression",
                parameters={
                    "type": "object",
                    "properties": {"expr": {"type": "string"}},
                    "required": ["expr"],
                },
            ),
        )
        self._agent.add_tool(
            "echo",
            lambda text: text,
            ToolSchema(
                name="echo",
                description="Echo input text",
                parameters={
                    "type": "object",
                    "properties": {"text": {"type": "string"}},
                    "required": ["text"],
                },
            ),
        )

    def _build_transcript(self, history: list[ChatMessage], new_msg: str) -> str:
        if not history:
            return new_msg
        lines = [f"{m.role}: {m.content}" for m in history]
        lines.append(f"user: {new_msg}")
        return "\n".join(lines)


def _import_agent_module():
    try:
        import agent

        return agent
    except ImportError:
        pass

    base_dir = _detect_repo_root(Path(__file__).resolve())
    if base_dir:
        impl_dir = _snapshot_impl_dir(base_dir)
        if impl_dir and str(impl_dir) not in sys.path:
            sys.path.insert(0, str(impl_dir))
    import agent

    return agent


def _detect_repo_root(start: Path) -> Optional[Path]:
    for parent in [start] + list(start.parents):
        state_dir = parent / ".blueprint"
        current = state_dir / "current"
        if not current.exists():
            continue
        snapshot_id = current.read_text(encoding="utf-8").strip()
        if not snapshot_id:
            continue
        impl_dir = state_dir / "history" / snapshot_id / "impl"
        if (impl_dir / "agent.py").exists():
            return parent
    return None


def _snapshot_impl_dir(repo_root: Path) -> Optional[Path]:
    state_dir = repo_root / ".blueprint"
    if not state_dir.exists():
        return None
    current = state_dir / "current"
    if not current.exists():
        return None
    snapshot_id = current.read_text(encoding="utf-8").strip()
    if not snapshot_id:
        return None
    impl_dir = state_dir / "history" / snapshot_id / "impl"
    if impl_dir.exists():
        return impl_dir
    return None


def _safe_calculate(expr: str) -> str:
    import ast
    import operator as op

    operators = {
        ast.Add: op.add,
        ast.Sub: op.sub,
        ast.Mult: op.mul,
        ast.Div: op.truediv,
        ast.Mod: op.mod,
        ast.Pow: op.pow,
    }

    def _eval(node):
        if isinstance(node, ast.Num):
            return node.n
        if isinstance(node, ast.UnaryOp) and isinstance(node.op, (ast.UAdd, ast.USub)):
            value = _eval(node.operand)
            return value if isinstance(node.op, ast.UAdd) else -value
        if isinstance(node, ast.BinOp) and type(node.op) in operators:
            return operators[type(node.op)](_eval(node.left), _eval(node.right))
        raise ValueError("Unsupported expression")

    tree = ast.parse(expr, mode="eval")
    return str(_eval(tree.body))
