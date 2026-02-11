"""Generic Opus adapter using shared rotation policy."""

from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Optional
from urllib import request as urlrequest, error as urlerror

import requests as http_requests

from .rotation import RotationManager, RotationSlot
from .types import CompletionRequest, LLMResponse, ToolCall, ProviderError, StreamChunk, StreamIterator, ToolCallDelta


@dataclass
class OpusConfig:
    api_keys: list[str]
    base_url: str
    endpoint: str = "/responses"
    model: Optional[str] = None
    temperature: float = 0.3


class OpusAdapter:
    def __init__(self, config: OpusConfig, rotation: RotationManager | None = None):
        if not config.api_keys:
            raise ValueError("Opus api_keys required")
        self.config = config
        self.rotation = rotation or RotationManager()
        for idx, key in enumerate(config.api_keys):
            self.rotation.add_slot(RotationSlot(id=f"k{idx}"))
        self._keys = list(config.api_keys)

    def complete(self, request: CompletionRequest) -> LLMResponse:
        payload = self._build_payload(request)

        attempt = 0
        while True:
            attempt += 1
            slot = self.rotation.select_slot()
            key = self._keys[int(slot.id[1:])]
            try:
                response = self._send_request(payload, key)
                self.rotation.report_success(slot.id)
                return self._parse_response(response)
            except ProviderError as exc:
                if exc.code in ("rate_limit", "quota"):
                    self.rotation.report_rate_limit(slot.id, exc.message)
                elif exc.code == "auth_error":
                    self.rotation.report_auth_error(slot.id)
                if not exc.retryable or attempt > self.rotation.policy.max_retries:
                    raise
                self.rotation.backoff(attempt)

    def _build_payload(self, request: CompletionRequest) -> dict:
        model = request.model or self.config.model
        payload = {
            "model": model,
            "messages": [{"role": m.role, "content": m.content} for m in request.messages],
            "temperature": request.temperature if request.temperature is not None else self.config.temperature,
        }
        if request.tools:
            payload["tools"] = [
                {
                    "type": "function",
                    "function": {
                        "name": t.name,
                        "description": t.description,
                        "parameters": t.parameters,
                    },
                }
                for t in request.tools
            ]
        if request.response_schema:
            payload["text"] = {
                "format": {
                    "type": "json_schema",
                    "json_schema": {
                        "name": request.response_schema_name or "response",
                        "schema": request.response_schema,
                        "strict": True,
                    },
                }
            }
        return payload

    def complete_stream(self, request: CompletionRequest) -> StreamIterator:
        payload = self._build_payload(request)
        payload["stream"] = True

        slot = self.rotation.select_slot()
        key = self._keys[int(slot.id[1:])]
        url = f"{self.config.base_url}{self.config.endpoint}"
        headers = {
            "Content-Type": "application/json",
            "Authorization": f"Bearer {key}",
        }
        try:
            resp = http_requests.post(url, json=payload, headers=headers, timeout=60, stream=True)
        except http_requests.RequestException as err:
            raise ProviderError("network_error", str(err), retryable=True)

        if resp.status_code >= 400:
            body = resp.text or ""
            if resp.status_code in (401, 403):
                raise ProviderError("auth_error", body or "auth error", retryable=True)
            if resp.status_code == 429:
                raise ProviderError("rate_limit", body or "rate limit", retryable=True)
            if resp.status_code >= 500:
                raise ProviderError("server_error", body or "server error", retryable=True)
            raise ProviderError("api_error", body or "api error", retryable=False)

        self.rotation.report_success(slot.id)
        return self._iter_sse(resp)

    def _iter_sse(self, resp) -> StreamIterator:
        for line in resp.iter_lines(decode_unicode=True):
            if not line or not line.startswith("data: "):
                continue
            data_str = line[len("data: "):]
            if data_str.strip() == "[DONE]":
                yield StreamChunk(finish_reason="stop")
                return
            try:
                event = json.loads(data_str)
            except (ValueError, json.JSONDecodeError):
                continue
            etype = event.get("type", "")
            if etype == "response.output_text.delta":
                yield StreamChunk(delta=event.get("delta", ""))
            elif etype == "response.function_call_arguments.delta":
                yield StreamChunk(
                    tool_call_delta=ToolCallDelta(
                        index=event.get("output_index", 0),
                        args_delta=event.get("delta", ""),
                    )
                )
            elif etype == "response.output_item.added":
                item = event.get("item", {})
                if item.get("type") == "function_call":
                    yield StreamChunk(
                        tool_call_delta=ToolCallDelta(
                            index=event.get("output_index", 0),
                            name=item.get("name", ""),
                        )
                    )
            elif etype == "response.completed":
                yield StreamChunk(finish_reason="stop")
                return
        yield StreamChunk(finish_reason="stop")

    def _send_request(self, payload: dict, api_key: str) -> dict:
        url = f"{self.config.base_url}{self.config.endpoint}"
        data = json.dumps(payload).encode("utf-8")
        req = urlrequest.Request(url, data=data, method="POST")
        req.add_header("Content-Type", "application/json")
        req.add_header("Authorization", f"Bearer {api_key}")

        try:
            with urlrequest.urlopen(req) as resp:
                body = resp.read().decode("utf-8")
                return json.loads(body)
        except urlerror.HTTPError as err:
            body = err.read().decode("utf-8") if err.fp else ""
            status = err.code
            if status in (401, 403):
                raise ProviderError("auth_error", body or "auth error", retryable=True)
            if status == 429:
                raise ProviderError("rate_limit", body or "rate limit", retryable=True)
            if status >= 500:
                raise ProviderError("server_error", body or "server error", retryable=True)
            raise ProviderError("api_error", body or "api error", retryable=False)
        except urlerror.URLError as err:
            raise ProviderError("network_error", str(err), retryable=True)

    def _parse_response(self, response: dict) -> LLMResponse:
        text = response.get("output_text") or ""
        tool_calls: list[ToolCall] = []

        if not text and "output" in response:
            for item in response.get("output", []):
                for content in item.get("content", []):
                    ctype = content.get("type")
                    if ctype in ("output_text", "text"):
                        text += content.get("text", "")
                    if ctype in ("tool_call", "function_call"):
                        args = content.get("arguments", {})
                        if isinstance(args, str):
                            try:
                                args = json.loads(args)
                            except (json.JSONDecodeError, ValueError):
                                args = {}
                        tool_calls.append(
                            ToolCall(
                                name=content.get("name", ""),
                                args=args or {},
                            )
                        )

        if not text and "text" in response:
            text = response.get("text") or ""

        parsed = None
        if text:
            try:
                parsed = json.loads(text)
            except (json.JSONDecodeError, ValueError):
                pass

        return LLMResponse(content=text, tool_calls=tool_calls if tool_calls else None, raw=response, parsed=parsed)
