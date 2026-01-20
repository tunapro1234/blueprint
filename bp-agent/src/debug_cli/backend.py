"""Backend implementations for debug CLI."""

from __future__ import annotations

import json
from abc import ABC, abstractmethod
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

