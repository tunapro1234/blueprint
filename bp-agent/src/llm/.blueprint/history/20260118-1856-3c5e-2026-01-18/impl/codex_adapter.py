"""Codex adapter using shared rotation policy."""

from __future__ import annotations

import json
import os
from dataclasses import dataclass
from pathlib import Path
from typing import Optional
from urllib import request as urlrequest, error as urlerror

from .rotation import RotationManager, RotationSlot
from .types import CompletionRequest, LLMResponse, ToolCall, ProviderError

CODEX_MODELS = [
    "gpt-5.2-codex",
    "gpt-5.1-codex-mini",
    "gpt-5.1-codex-max",
    "gpt-5.1-codex",
    "gpt-5-codex",
    "gpt-5-codex-mini",
    "gpt-5.2",
    "gpt-5.1",
    "gpt-5",
]


@dataclass
class CodexAuth:
    access_token: str
    refresh_token: str
    account_id: str
    id_token: Optional[str] = None
    source: str = "chatgpt"


@dataclass
class CodexConfig:
    api_keys: list[str] | None = None
    auth_files: list[str] | None = None
    model: str = "gpt-5.2-codex"
    reasoning_effort: str = "medium"
    base_url: str = "https://api.openai.com/v1"


class CodexAdapter:
    def __init__(self, config: CodexConfig, rotation: RotationManager | None = None):
        self.config = config
        self.rotation = rotation or RotationManager()
        self._slot_creds: dict[str, dict] = {}

        api_keys = config.api_keys or []
        auth_files = config.auth_files or []

        for idx, key in enumerate(api_keys):
            slot_id = f"api:{idx}"
            self.rotation.add_slot(RotationSlot(id=slot_id))
            self._slot_creds[slot_id] = {"type": "api_key", "value": key}

        for idx, path in enumerate(auth_files):
            auth = load_auth(path)
            slot_id = f"auth:{idx}"
            self.rotation.add_slot(RotationSlot(id=slot_id))
            self._slot_creds[slot_id] = {"type": "auth", "value": auth.access_token}

        if not self._slot_creds:
            raise ValueError("Codex requires api_keys or auth_files")

    def complete(self, request: CompletionRequest) -> LLMResponse:
        model = request.model or self.config.model
        if model not in CODEX_MODELS:
            raise ProviderError("invalid_model", f"Model {model} not allowed", retryable=False)

        payload = self._build_payload(request, model)

        attempt = 0
        while True:
            attempt += 1
            slot = self.rotation.select_slot()
            cred = self._slot_creds[slot.id]
            try:
                response = self._send_request(payload, cred)
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

    def _build_payload(self, request: CompletionRequest, model: str) -> dict:
        temperature = request.temperature
        messages = request.messages

        instructions = None
        input_items = []
        for msg in messages:
            if msg.role == "system":
                instructions = msg.content
            else:
                input_items.append({"role": msg.role, "content": msg.content})

        payload = {
            "model": model,
            "input": input_items,
            "stream": False,
            "store": False,
            "reasoning": {"effort": self.config.reasoning_effort},
        }
        if instructions is not None:
            payload["instructions"] = instructions
        if temperature is not None:
            payload["temperature"] = temperature
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
        return payload

    def _send_request(self, payload: dict, cred: dict) -> dict:
        url = f"{self.config.base_url}/responses"
        data = json.dumps(payload).encode("utf-8")
        req = urlrequest.Request(url, data=data, method="POST")
        req.add_header("Content-Type", "application/json")
        if cred["type"] == "api_key":
            req.add_header("Authorization", f"Bearer {cred['value']}")
        else:
            req.add_header("Authorization", f"Bearer {cred['value']}")

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
                        tool_calls.append(
                            ToolCall(
                                name=content.get("name", ""),
                                args=content.get("arguments", {}) or {},
                            )
                        )

        return LLMResponse(content=text, tool_calls=tool_calls if tool_calls else None, raw=response)


def load_auth(auth_file: str | None = None) -> CodexAuth:
    codex_home = Path(os.getenv("CODEX_HOME", Path.home() / ".codex"))
    path = Path(auth_file) if auth_file else codex_home / "auth.json"
    data = json.loads(path.read_text())
    tokens = data.get("tokens", {})
    return CodexAuth(
        access_token=tokens["access_token"],
        refresh_token=tokens.get("refresh_token", ""),
        account_id=tokens.get("account_id", ""),
        id_token=tokens.get("id_token"),
    )
