"""HTTP API server for Base Agent."""

from __future__ import annotations

import json
import os
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from threading import Lock, Thread
from typing import Optional
from urllib.parse import parse_qs, urlparse


try:
    from ..agent import Agent, AgentConfig, AgentResult
except ImportError:
    from agent import Agent, AgentConfig, AgentResult


VERSION = "0.1.0"


class AgentServer:
    def __init__(self, port: int = 8080, agent: Optional[Agent] = None):
        self.port = port
        if agent is None:
            config = AgentConfig(enable_task_store=True)
            self.agent = Agent("api-agent", config=config)
        else:
            self.agent = agent
        if not self.agent.tasks:
            raise RuntimeError("TaskStore disabled. Enable task store for /tasks endpoint.")

        self._httpd: Optional[ThreadingHTTPServer] = None
        self._lock = Lock()

    def start(self):
        self._httpd = ThreadingHTTPServer(("0.0.0.0", self.port), self._make_handler())
        self.port = self._httpd.server_address[1]
        self._httpd.serve_forever()

    def start_in_thread(self) -> Thread:
        self._httpd = ThreadingHTTPServer(("127.0.0.1", self.port), self._make_handler())
        self.port = self._httpd.server_address[1]
        thread = Thread(target=self._httpd.serve_forever, daemon=True)
        thread.start()
        return thread

    def shutdown(self):
        if self._httpd:
            self._httpd.shutdown()
            self._httpd.server_close()
            self._httpd = None

    def _make_handler(self):
        server = self

        class Handler(BaseHTTPRequestHandler):
            def _send_json(self, status: int, payload: dict):
                body = json.dumps(payload).encode("utf-8")
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def _parse_json_body(self):
                length = int(self.headers.get("Content-Length", 0))
                if length <= 0:
                    return None, "empty body"
                data = self.rfile.read(length)
                try:
                    return json.loads(data.decode("utf-8")), None
                except json.JSONDecodeError:
                    return None, "invalid json"

            def do_GET(self):
                parsed = urlparse(self.path)
                if parsed.path == "/health":
                    self._send_json(HTTPStatus.OK, {"status": "ok", "version": VERSION})
                    return

                if parsed.path == "/tasks":
                    if not server.agent.tasks:
                        self._send_json(HTTPStatus.SERVICE_UNAVAILABLE, {"error": "task store disabled"})
                        return
                    query = parse_qs(parsed.query)
                    limit = int(query.get("limit", [10])[0])
                    tasks = server.agent.tasks.list(limit=limit)
                    self._send_json(HTTPStatus.OK, {"tasks": [t.to_dict() for t in tasks]})
                    return

                self._send_json(HTTPStatus.NOT_FOUND, {"error": "not found"})

            def do_POST(self):
                if self.path != "/execute":
                    self._send_json(HTTPStatus.NOT_FOUND, {"error": "not found"})
                    return

                body, err = self._parse_json_body()
                if err:
                    self._send_json(HTTPStatus.BAD_REQUEST, {"error": err})
                    return

                instruction = body.get("instruction") if isinstance(body, dict) else None
                if not instruction:
                    self._send_json(HTTPStatus.BAD_REQUEST, {"error": "instruction required"})
                    return

                tools = body.get("tools")
                if tools:
                    for tool_def in tools:
                        name = tool_def.get("name") if isinstance(tool_def, dict) else None
                        if not name:
                            self._send_json(HTTPStatus.BAD_REQUEST, {"error": "tool name required"})
                            return
                        if not server.agent.tools.has(name):
                            self._send_json(HTTPStatus.BAD_REQUEST, {"error": f"tool not registered: {name}"})
                            return

                system_prompt = body.get("system_prompt") if isinstance(body, dict) else None
                provider = body.get("provider") if isinstance(body, dict) else None
                model = body.get("model") if isinstance(body, dict) else None
                temperature = body.get("temperature") if isinstance(body, dict) else None
                debug = bool(body.get("debug")) if isinstance(body, dict) else False

                try:
                    result, trace = server._execute_with_overrides(
                        instruction=instruction,
                        system_prompt=system_prompt,
                        provider=provider,
                        model=model,
                        temperature=temperature,
                        debug=debug,
                    )
                    payload = {
                        "success": result.success,
                        "output": result.output,
                        "task_id": result.task_id,
                    }
                    if debug and trace:
                        payload["trace"] = trace
                    self._send_json(HTTPStatus.OK, payload)
                except Exception as exc:  # pragma: no cover - generic safeguard
                    self._send_json(HTTPStatus.INTERNAL_SERVER_ERROR, {"error": str(exc)})

            def log_message(self, format, *args):  # noqa: A003
                return

        return Handler

    def _execute_with_overrides(
        self,
        instruction: str,
        system_prompt: Optional[str] = None,
        provider: Optional[str] = None,
        model: Optional[str] = None,
        temperature: Optional[float] = None,
        debug: bool = False,
    ):
        with self._lock:
            restore: dict[str, object] = {}

            if system_prompt is not None and hasattr(self.agent, "system_prompt"):
                restore["system_prompt"] = self.agent.system_prompt
                self.agent.system_prompt = system_prompt

            config = getattr(self.agent, "config", None)
            if config is not None:
                if provider is not None and hasattr(config, "provider"):
                    restore["provider"] = getattr(config, "provider", None)
                    setattr(config, "provider", provider)
                if model is not None and hasattr(config, "model"):
                    restore["model"] = getattr(config, "model", None)
                    setattr(config, "model", model)
                if temperature is not None and hasattr(config, "temperature"):
                    restore["temperature"] = getattr(config, "temperature", None)
                    setattr(config, "temperature", temperature)

            if debug and hasattr(self.agent, "_trace_enabled"):
                restore["_trace_enabled"] = getattr(self.agent, "_trace_enabled", False)
                setattr(self.agent, "_trace_enabled", True)

            try:
                result = self.agent.execute(instruction)
                trace = getattr(result, "trace", None)
                if not trace and debug and hasattr(self.agent, "_last_trace"):
                    trace = getattr(self.agent, "_last_trace", None)
                return result, trace
            finally:
                if "system_prompt" in restore:
                    self.agent.system_prompt = restore["system_prompt"]
                if config is not None:
                    if "provider" in restore:
                        setattr(config, "provider", restore["provider"])
                    if "model" in restore:
                        setattr(config, "model", restore["model"])
                    if "temperature" in restore:
                        setattr(config, "temperature", restore["temperature"])
                if "_trace_enabled" in restore:
                    setattr(self.agent, "_trace_enabled", restore["_trace_enabled"])


if __name__ == "__main__":
    port = int(os.getenv("PORT", 8080))
    server = AgentServer(port=port)
    print(f"Starting server on port {server.port}")
    server.start()
