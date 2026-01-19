"""Common LLM types."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Optional


@dataclass
class Message:
    role: str
    content: str


@dataclass
class ToolCall:
    name: str
    args: dict


@dataclass
class LLMResponse:
    content: str
    tool_calls: Optional[list[ToolCall]] = None
    raw: Optional[Any] = None


@dataclass
class CompletionRequest:
    messages: list[Message]
    tools: Optional[list[Any]] = None
    temperature: Optional[float] = None
    model: Optional[str] = None
    provider: Optional[str] = None
    metadata: Optional[dict] = None


class ProviderError(Exception):
    def __init__(self, code: str, message: str, retryable: bool = False):
        super().__init__(message)
        self.code = code
        self.message = message
        self.retryable = retryable
