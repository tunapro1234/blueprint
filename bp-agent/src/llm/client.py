"""Gemini LLM client with key rotation."""

from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Any, Optional
from urllib import request, error

try:
    from ..tools import ToolSchema
except ImportError:
    from tools import ToolSchema

ALLOWED_MODELS = [
    "gemini-3-pro-preview",
    "gemini-3-flash-preview",
]


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


class RateLimitError(Exception):
    pass


class AllKeysExhaustedError(Exception):
    pass


class APIError(Exception):
    pass


class LLMClient:
    def __init__(self, api_keys: list[str], model: str = "gemini-3-flash-preview"):
        normalized = self._normalize_model_name(model)
        if normalized not in ALLOWED_MODELS:
            raise ValueError(f"Model {model} not allowed. Use: {ALLOWED_MODELS}")

        if not api_keys:
            raise ValueError("At least one API key is required")

        self.api_keys = api_keys
        self.current_key_index = 0
        self.model = normalized

    @staticmethod
    def _normalize_model_name(model: str) -> str:
        if model.startswith("models/"):
            return model.split("/", 1)[1]
        return model

    def complete(
        self,
        messages: list[Message],
        tools: Optional[list[ToolSchema]] = None,
        temperature: Optional[float] = None,
    ) -> LLMResponse:
        request_body = self._build_request(messages, tools, temperature)

        while self.current_key_index < len(self.api_keys):
            try:
                response = self._send_request(request_body)
                return self._parse_response(response)
            except RateLimitError:
                self.current_key_index += 1
                if self.current_key_index >= len(self.api_keys):
                    raise AllKeysExhaustedError("All API keys exhausted")

        raise AllKeysExhaustedError("All API keys exhausted")

    def _build_request(
        self,
        messages: list[Message],
        tools: Optional[list[ToolSchema]],
        temperature: Optional[float] = None,
    ) -> dict:
        contents = []
        system_instruction = None

        for msg in messages:
            if msg.role == "system":
                system_instruction = msg.content
            else:
                role = "user" if msg.role == "user" else "model"
                contents.append({"role": role, "parts": [{"text": msg.content}]})

        request_body: dict[str, Any] = {
            "contents": contents,
            "generationConfig": {
                "temperature": temperature if temperature is not None else 0.3,
            },
        }

        if system_instruction:
            request_body["systemInstruction"] = {"parts": [{"text": system_instruction}]}

        if tools:
            declarations = [
                {
                    "name": t.name,
                    "description": t.description,
                    "parameters": t.parameters,
                }
                for t in tools
            ]
            request_body["tools"] = [{"functionDeclarations": declarations}]

        return request_body

    def _send_request(self, request_body: dict) -> dict:
        api_key = self.api_keys[self.current_key_index]
        url = (
            f"https://generativelanguage.googleapis.com/v1beta/models/"
            f"{self.model}:generateContent?key={api_key}"
        )

        data = json.dumps(request_body).encode("utf-8")
        req = request.Request(url, data=data, method="POST")
        req.add_header("Content-Type", "application/json")

        try:
            with request.urlopen(req) as resp:
                payload = resp.read().decode("utf-8")
                return json.loads(payload)
        except error.HTTPError as err:
            body = err.read().decode("utf-8") if err.fp else ""
            status = err.code
            if status == 429:
                raise RateLimitError()
            lowered = body.lower()
            if "quota" in lowered or "resource_exhausted" in lowered:
                raise RateLimitError()
            raise APIError(body or str(err))
        except error.URLError as err:
            raise APIError(str(err))

    def _parse_response(self, response: dict) -> LLMResponse:
        candidates = response.get("candidates", [])
        if not candidates:
            return LLMResponse(content="", tool_calls=None)

        content = candidates[0].get("content", {})
        text = ""
        tool_calls: list[ToolCall] = []

        for part in content.get("parts", []):
            if "text" in part:
                text += part["text"]
            if "functionCall" in part:
                fc = part["functionCall"]
                tool_calls.append(ToolCall(name=fc.get("name", ""), args=fc.get("args", {})))

        return LLMResponse(content=text, tool_calls=tool_calls if tool_calls else None)
