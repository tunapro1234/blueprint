"""Debug CLI models."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Optional


@dataclass
class CLIConfig:
    base_url: str = "http://localhost:8080"
    provider: str = "gemini"
    model: Optional[str] = None
    system_prompt: Optional[str] = None
    temperature: float = 0.3
    debug: bool = False


@dataclass
class ChatMessage:
    role: str
    content: str


@dataclass
class TaskInfo:
    id: str
    status: str
    instruction: str
    output: Optional[str] = None
    created_at: str = ""


@dataclass
class ToolInfo:
    name: str
    description: str


@dataclass
class ExecuteResult:
    success: bool
    output: str
    task_id: Optional[str] = None
    tool_calls: Optional[list[dict]] = None
