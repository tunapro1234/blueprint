"""OpenAI-compatible adapter — works with OpenRouter, Together, Groq, etc."""

from __future__ import annotations

import json
import os
from dataclasses import dataclass, field
from typing import Optional

import requests

from .rotation import RotationManager, RotationSlot
from .types import (
    CompletionRequest,
    LLMResponse,
    ProviderError,
    StreamChunk,
    StreamIterator,
    ToolCall,
    ToolCallDelta,
)


@dataclass
class OpenAIConfig:
    api_keys: list[str]
    base_url: str = "https://openrouter.ai/api/v1"
    model: str = "openai/gpt-4o"
    temperature: float = 0.3
    extra_headers: dict = field(default_factory=dict)


class OpenAIAdapter:
    """Adapter for any OpenAI-compatible chat completions API."""

    def __init__(self, config: OpenAIConfig, rotation: RotationManager | None = None):
        if not config.api_keys:
            raise ValueError("api_keys required")
        self.config = config
        self.rotation = rotation or RotationManager()
        for key in config.api_keys:
            self.rotation.add_slot(RotationSlot(id=key))

    # ------------------------------------------------------------------
    # Public interface
    # ------------------------------------------------------------------

    def complete(self, request: CompletionRequest) -> LLMResponse:
        model = request.model or self.config.model
        temperature = request.temperature if request.temperature is not None else self.config.temperature
        payload = self._build_payload(request, model, temperature)

        attempt = 0
        while True:
            attempt += 1
            slot = self.rotation.select_slot()
            try:
                data = self._post(payload, slot.id)
                self.rotation.report_success(slot.id)
                return self._parse_response(data)
            except ProviderError as exc:
                if exc.code in ("rate_limit", "quota"):
                    self.rotation.report_rate_limit(slot.id, exc.message)
                elif exc.code == "auth_error":
                    self.rotation.report_auth_error(slot.id)
                if not exc.retryable or attempt > self.rotation.policy.max_retries:
                    raise
                self.rotation.backoff(attempt)

    def complete_stream(self, request: CompletionRequest) -> StreamIterator:
        model = request.model or self.config.model
        temperature = request.temperature if request.temperature is not None else self.config.temperature
        payload = self._build_payload(request, model, temperature)
        payload["stream"] = True

        slot = self.rotation.select_slot()
        url = f"{self.config.base_url.rstrip('/')}/chat/completions"
        headers = self._headers(slot.id)

        try:
            resp = requests.post(url, json=payload, headers=headers, timeout=120, stream=True)
        except requests.RequestException as err:
            raise ProviderError("network_error", str(err), retryable=True)

        self._check_status(resp)
        self.rotation.report_success(slot.id)
        return self._iter_sse(resp)

    # ------------------------------------------------------------------
    # Request building
    # ------------------------------------------------------------------

    def _build_payload(self, request: CompletionRequest, model: str, temperature: float) -> dict:
        messages = []
        for msg in request.messages:
            messages.append({"role": msg.role, "content": msg.content})

        payload: dict = {
            "model": model,
            "messages": messages,
            "temperature": temperature,
        }

        if request.tools:
            payload["tools"] = [
                {
                    "type": "function",
                    "function": {
                        "name": t.name,
                        "description": t.description,
                        "parameters": t.parameters or {"type": "object", "properties": {}},
                    },
                }
                for t in request.tools
            ]

        if request.response_schema:
            payload["response_format"] = {
                "type": "json_schema",
                "json_schema": {
                    "name": request.response_schema_name or "response",
                    "schema": request.response_schema,
                },
            }

        return payload

    # ------------------------------------------------------------------
    # HTTP
    # ------------------------------------------------------------------

    def _headers(self, api_key: str) -> dict:
        h = {
            "Content-Type": "application/json",
            "Authorization": f"Bearer {api_key}",
        }
        h.update(self.config.extra_headers)
        return h

    def _post(self, payload: dict, api_key: str) -> dict:
        url = f"{self.config.base_url.rstrip('/')}/chat/completions"
        try:
            resp = requests.post(url, json=payload, headers=self._headers(api_key), timeout=120)
        except requests.RequestException as err:
            raise ProviderError("network_error", str(err), retryable=True)
        self._check_status(resp)
        return resp.json()

    @staticmethod
    def _check_status(resp: requests.Response):
        if resp.status_code < 400:
            return
        body = resp.text or ""
        low = body.lower()
        if resp.status_code in (401, 403):
            raise ProviderError("auth_error", body, retryable=True)
        if resp.status_code == 429 or "rate" in low:
            raise ProviderError("rate_limit", body, retryable=True)
        if resp.status_code >= 500:
            raise ProviderError("server_error", body, retryable=True)
        raise ProviderError("api_error", body, retryable=False)

    # ------------------------------------------------------------------
    # Response parsing
    # ------------------------------------------------------------------

    def _parse_response(self, data: dict) -> LLMResponse:
        choices = data.get("choices", [])
        if not choices:
            return LLMResponse(content="", tool_calls=None, raw=data)

        msg = choices[0].get("message", {})
        content = msg.get("content") or ""
        tool_calls: list[ToolCall] = []

        for tc in msg.get("tool_calls", []):
            fn = tc.get("function", {})
            name = fn.get("name", "")
            try:
                args = json.loads(fn.get("arguments", "{}"))
            except (json.JSONDecodeError, ValueError):
                args = {}
            tool_calls.append(ToolCall(name=name, args=args))

        parsed = None
        if content:
            try:
                parsed = json.loads(content)
            except (json.JSONDecodeError, ValueError):
                pass

        return LLMResponse(
            content=content,
            tool_calls=tool_calls if tool_calls else None,
            raw=data,
            parsed=parsed,
        )

    # ------------------------------------------------------------------
    # Streaming
    # ------------------------------------------------------------------

    def _iter_sse(self, resp: requests.Response) -> StreamIterator:
        resp.encoding = "utf-8"
        for line in resp.iter_lines(decode_unicode=True):
            if not line or not line.startswith("data: "):
                continue
            data_str = line[len("data: "):]
            if data_str.strip() == "[DONE]":
                yield StreamChunk(finish_reason="stop")
                return
            try:
                data = json.loads(data_str)
            except (ValueError, json.JSONDecodeError):
                continue

            choices = data.get("choices", [])
            if not choices:
                continue
            delta = choices[0].get("delta", {})

            text = delta.get("content") or ""
            tcd = None

            for i, tc in enumerate(delta.get("tool_calls", [])):
                fn = tc.get("function", {})
                tcd = ToolCallDelta(
                    index=tc.get("index", i),
                    name=fn.get("name"),
                    args_delta=fn.get("arguments", ""),
                )

            finish = choices[0].get("finish_reason")
            yield StreamChunk(delta=text, tool_call_delta=tcd, finish_reason=finish)
