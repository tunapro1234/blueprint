"""HTTP client for the debug CLI."""

from __future__ import annotations

import json
from dataclasses import asdict
from typing import Any
from urllib import request as urlrequest, error as urlerror

from .models import DebugConfig, ExecuteRequest, ExecuteResponse


class DebugClient:
    def __init__(self, config: DebugConfig):
        self.config = config

    def _headers(self) -> dict[str, str]:
        headers = {"Content-Type": "application/json"}
        if self.config.token:
            headers["Authorization"] = f"Bearer {self.config.token}"
        return headers

    def health(self) -> dict[str, Any]:
        return self._request_json("GET", "/health")

    def execute(self, request: ExecuteRequest) -> ExecuteResponse:
        payload = _strip_none(asdict(request))
        response = self._request_json("POST", "/execute", payload)
        return ExecuteResponse(
            success=bool(response.get("success")),
            output=response.get("output", ""),
            task_id=response.get("task_id"),
            trace=response.get("trace"),
        )

    def tasks(self, limit: int = 10) -> dict[str, Any]:
        return self._request_json("GET", f"/tasks?limit={limit}")

    def _request_json(self, method: str, path: str, payload: dict | None = None) -> dict[str, Any]:
        url = f"{self.config.base_url.rstrip('/')}{path}"
        data = json.dumps(payload).encode("utf-8") if payload is not None else None
        req = urlrequest.Request(url, data=data, method=method)
        for key, value in self._headers().items():
            req.add_header(key, value)
        try:
            with urlrequest.urlopen(req) as resp:
                body = resp.read().decode("utf-8")
        except urlerror.HTTPError as err:
            body = err.read().decode("utf-8") if err.fp else ""
            raise RuntimeError(f"HTTP {err.code}: {body}") from err
        except urlerror.URLError as err:
            raise RuntimeError(f"Connection error: {err}") from err

        if not body:
            return {}
        return json.loads(body)


def _strip_none(payload: dict[str, Any]) -> dict[str, Any]:
    return {key: value for key, value in payload.items() if value is not None}
