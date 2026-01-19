"""Data models for the debug CLI."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Optional


@dataclass
class DebugConfig:
    base_url: str = "http://localhost:8080"
    token: Optional[str] = None
    provider: Optional[str] = None
    model: Optional[str] = None
    temperature: Optional[float] = None
    system_prompt: Optional[str] = None
    debug: bool = False


@dataclass
class ToolCall:
    name: str
    args: dict[str, Any]


@dataclass
class ToolResult:
    name: str
    output: Any | None = None
    error: Optional[str] = None


@dataclass
class Trace:
    provider: Optional[str] = None
    model: Optional[str] = None
    tool_calls: Optional[list[ToolCall]] = None
    tool_results: Optional[list[ToolResult]] = None
    raw: Any | None = None


@dataclass
class ExecuteRequest:
    instruction: str
    system_prompt: Optional[str] = None
    provider: Optional[str] = None
    model: Optional[str] = None
    temperature: Optional[float] = None
    debug: Optional[bool] = None


@dataclass
class ExecuteResponse:
    success: bool
    output: str
    task_id: Optional[str] = None
    trace: Optional[dict] = None
