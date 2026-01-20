"""Generic Opus adapter using shared rotation policy."""

from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Optional
from urllib import request as urlrequest, error as urlerror

from .rotation import RotationManager, RotationSlot
from .types import CompletionRequest, LLMResponse, ProviderError


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
                return LLMResponse(content=self._extract_text(response), raw=response)
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
                    "name": t.name,
                    "description": t.description,
                    "parameters": t.parameters,
                }
                for t in request.tools
            ]
        return payload

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

    def _extract_text(self, response: dict) -> str:
        if "output_text" in response:
            return response.get("output_text") or ""
        if "text" in response:
            return response.get("text") or ""
        return ""
