"""Gemini adapter using shared rotation policy."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Optional

import requests

from .rotation import RotationManager, RotationSlot
from .types import CompletionRequest, LLMResponse, ToolCall, ProviderError

GEMINI_ALLOWED_MODELS = ["gemini-3-flash-preview", "gemini-3-pro-preview"]


@dataclass
class GeminiConfig:
    api_keys: list[str]
    model: str = "gemini-3-flash-preview"
    temperature: float = 0.3
    base_url: str = "https://generativelanguage.googleapis.com"


class GeminiAdapter:
    def __init__(self, config: GeminiConfig, rotation: RotationManager | None = None):
        if not config.api_keys:
            raise ValueError("Gemini api_keys required")
        self.config = config
        self.rotation = rotation or RotationManager()
        for key in config.api_keys:
            self.rotation.add_slot(RotationSlot(id=key))

    def complete(self, request: CompletionRequest) -> LLMResponse:
        model = request.model or self.config.model
        if model not in GEMINI_ALLOWED_MODELS:
            raise ProviderError("invalid_model", f"Model {model} not allowed", retryable=False)

        temperature = request.temperature if request.temperature is not None else self.config.temperature
        payload = self._build_request(request, temperature)

        attempt = 0
        while True:
            attempt += 1
            slot = self.rotation.select_slot()
            try:
                response = self._send_request(payload, model, slot.id)
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

    def _build_request(self, request: CompletionRequest, temperature: float) -> dict:
        contents = []
        system_instruction = None

        for msg in request.messages:
            if msg.role == "system":
                system_instruction = msg.content
            else:
                role = "user" if msg.role == "user" else "model"
                contents.append({"role": role, "parts": [{"text": msg.content}]})

        payload = {
            "contents": contents,
            "generationConfig": {"temperature": temperature},
        }

        if system_instruction:
            payload["systemInstruction"] = {"parts": [{"text": system_instruction}]}

        if request.tools:
            payload["tools"] = [
                {
                    "functionDeclarations": [
                        {
                            "name": t.name,
                            "description": t.description,
                            "parameters": t.parameters,
                        }
                        for t in request.tools
                    ]
                }
            ]

        return payload

    def _send_request(self, payload: dict, model: str, api_key: str) -> dict:
        base_url = self.config.base_url.rstrip("/")
        url = f"{base_url}/v1beta/models/{model}:generateContent"
        headers = {
            "Content-Type": "application/json",
            "x-goog-api-key": api_key,
        }
        try:
            resp = requests.post(url, json=payload, headers=headers, timeout=30)
        except requests.RequestException as err:  # pragma: no cover - network issues
            raise ProviderError("network_error", str(err), retryable=True)

        if resp.status_code >= 400:
            body = resp.text or ""
            lowered = body.lower()
            if resp.status_code in (401, 403):
                raise ProviderError("auth_error", body or "auth error", retryable=True)
            if resp.status_code == 429 or "quota" in lowered or "resource_exhausted" in lowered:
                raise ProviderError("rate_limit", body or "rate limit", retryable=True)
            if resp.status_code >= 500:
                raise ProviderError("server_error", body or "server error", retryable=True)
            raise ProviderError("api_error", body or "api error", retryable=False)

        return resp.json()

    def _parse_response(self, response: dict) -> LLMResponse:
        candidates = response.get("candidates", [])
        if not candidates:
            return LLMResponse(content="", tool_calls=None, raw=response)

        content = candidates[0].get("content", {})
        text = ""
        tool_calls: list[ToolCall] = []

        for part in content.get("parts", []):
            if "text" in part:
                text += part["text"]
            if "functionCall" in part:
                fc = part["functionCall"]
                tool_calls.append(ToolCall(name=fc.get("name", ""), args=fc.get("args", {})))

        return LLMResponse(content=text, tool_calls=tool_calls if tool_calls else None, raw=response)
