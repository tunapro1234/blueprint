"""Common LLM types."""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from typing import Any, Iterator, Optional


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


@dataclass
class ToolCallDelta:
    index: int = 0
    name: Optional[str] = None
    args_delta: str = ""


@dataclass
class StreamChunk:
    delta: str = ""
    tool_call_delta: Optional[ToolCallDelta] = None
    finish_reason: Optional[str] = None
    raw: Optional[Any] = None


StreamIterator = Iterator[StreamChunk]


def accumulate_stream(stream: StreamIterator) -> LLMResponse:
    """Collect stream chunks into a complete LLMResponse."""
    text_parts: list[str] = []
    # index -> (name, args_json_parts)
    tool_call_acc: dict[int, tuple[str, list[str]]] = {}

    for chunk in stream:
        if chunk.delta:
            text_parts.append(chunk.delta)
        if chunk.tool_call_delta:
            tcd = chunk.tool_call_delta
            if tcd.index not in tool_call_acc:
                tool_call_acc[tcd.index] = (tcd.name or "", [])
            entry = tool_call_acc[tcd.index]
            if tcd.name and not entry[0]:
                tool_call_acc[tcd.index] = (tcd.name, entry[1])
            if tcd.args_delta:
                entry[1].append(tcd.args_delta)

    tool_calls: list[ToolCall] = []
    for idx in sorted(tool_call_acc):
        name, args_parts = tool_call_acc[idx]
        args_str = "".join(args_parts)
        try:
            args = json.loads(args_str) if args_str else {}
        except (json.JSONDecodeError, ValueError):
            args = {}
        tool_calls.append(ToolCall(name=name, args=args))

    return LLMResponse(
        content="".join(text_parts),
        tool_calls=tool_calls if tool_calls else None,
    )


class ProviderError(Exception):
    def __init__(self, code: str, message: str, retryable: bool = False):
        super().__init__(message)
        self.code = code
        self.message = message
        self.retryable = retryable
