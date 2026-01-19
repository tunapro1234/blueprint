"""Command line interface for the debug client."""

from __future__ import annotations

import argparse
import json
import sys
from dataclasses import asdict

from .client import DebugClient
from .models import DebugConfig, ExecuteRequest


class DebugCLI:
    def __init__(self, client: DebugClient, config: DebugConfig):
        self.client = client
        self.config = config
        self._history: list[tuple[str, str]] = []
        self._system_prompt = config.system_prompt
        self._provider = config.provider
        self._model = config.model
        self._temperature = config.temperature
        self._debug = config.debug

    def run(self, instruction: str):
        request = ExecuteRequest(
            instruction=instruction,
            system_prompt=self._system_prompt,
            provider=self._provider,
            model=self._model,
            temperature=self._temperature,
            debug=self._debug,
        )
        response = self.client.execute(request)
        print(response.output)
        if self._debug and response.trace:
            self._print_trace(response.trace)

    def repl(self):
        print("Debug CLI (type /exit to quit)")
        while True:
            try:
                line = input("you> ").strip()
            except EOFError:
                print("")
                break

            if not line:
                continue

            if line.startswith("/"):
                if self._handle_command(line):
                    break
                continue

            prompt = render_transcript(self._history, line)
            request = ExecuteRequest(
                instruction=prompt,
                system_prompt=self._system_prompt,
                provider=self._provider,
                model=self._model,
                temperature=self._temperature,
                debug=self._debug,
            )
            response = self.client.execute(request)
            print(f"assistant> {response.output}")
            if self._debug and response.trace:
                self._print_trace(response.trace)
            self._history.append((line, response.output))

    def _handle_command(self, line: str) -> bool:
        if line in ("/exit", "/quit"):
            return True
        if line.startswith("/system "):
            self._system_prompt = line[len("/system ") :].strip() or None
            return False
        if line.startswith("/provider "):
            self._provider = line[len("/provider ") :].strip() or None
            return False
        if line.startswith("/model "):
            self._model = line[len("/model ") :].strip() or None
            return False
        if line.startswith("/temp "):
            raw = line[len("/temp ") :].strip()
            self._temperature = float(raw) if raw else None
            return False
        if line.startswith("/debug "):
            raw = line[len("/debug ") :].strip()
            self._debug = _parse_bool(raw)
            return False
        if line == "/help":
            _print_help()
            return False
        print("Unknown command. Use /help for options.")
        return False

    def _print_trace(self, trace: dict):
        tool_calls = trace.get("tool_calls") or []
        tool_results = trace.get("tool_results") or []
        if trace.get("provider") or trace.get("model"):
            print(f"trace: provider={trace.get('provider')} model={trace.get('model')}")
        if tool_calls:
            print("tool_calls:")
            print(json.dumps(tool_calls, indent=2))
        if tool_results:
            print("tool_results:")
            print(json.dumps(tool_results, indent=2))


def render_transcript(history: list[tuple[str, str]], new_message: str) -> str:
    lines: list[str] = []
    for user_msg, assistant_msg in history:
        lines.append(f"user: {user_msg}")
        lines.append(f"assistant: {assistant_msg}")
    lines.append(f"user: {new_message}")
    return "\n".join(lines)


def _parse_bool(value: str) -> bool:
    lowered = value.strip().lower()
    if lowered in ("on", "true", "1", "yes"):
        return True
    if lowered in ("off", "false", "0", "no"):
        return False
    raise ValueError(f"Invalid boolean: {value}")


def _print_help():
    print("/system <text>  - set system prompt")
    print("/provider <id>  - set provider")
    print("/model <id>     - set model")
    print("/temp <float>   - set temperature")
    print("/debug on|off   - toggle trace output")
    print("/exit           - quit")


def _build_config(args: argparse.Namespace) -> DebugConfig:
    return DebugConfig(
        base_url=args.url,
        token=args.token,
        provider=args.provider,
        model=args.model,
        temperature=args.temp,
        system_prompt=args.system,
        debug=args.debug,
    )


def main(argv: list[str] | None = None):
    parser = argparse.ArgumentParser(prog="debug-cli")
    parser.add_argument("--url", default="http://localhost:8080", help="Base URL")
    parser.add_argument("--token", default=None, help="Auth token")
    parser.add_argument("--provider", default=None, help="Provider override")
    parser.add_argument("--model", default=None, help="Model override")
    parser.add_argument("--temp", type=float, default=None, help="Temperature")
    parser.add_argument("--system", default=None, help="System prompt")
    parser.add_argument("--debug", action="store_true", help="Include trace output")

    subparsers = parser.add_subparsers(dest="command", required=True)

    subparsers.add_parser("health", help="Check server health")

    run_parser = subparsers.add_parser("run", help="Send a single instruction")
    run_parser.add_argument("instruction", nargs="*", help="Instruction text")

    subparsers.add_parser("repl", help="Interactive chat")

    tasks_parser = subparsers.add_parser("tasks", help="List recent tasks")
    tasks_parser.add_argument("--limit", type=int, default=10)

    args = parser.parse_args(argv)
    config = _build_config(args)
    client = DebugClient(config)
    cli = DebugCLI(client, config)

    if args.command == "health":
        payload = client.health()
        print(json.dumps(payload, indent=2))
        return 0

    if args.command == "run":
        if args.instruction:
            instruction = " ".join(args.instruction)
        else:
            instruction = sys.stdin.read().strip()
        if not instruction:
            print("No instruction provided", file=sys.stderr)
            return 1
        cli.run(instruction)
        return 0

    if args.command == "tasks":
        payload = client.tasks(limit=args.limit)
        print(json.dumps(payload, indent=2))
        return 0

    if args.command == "repl":
        cli.repl()
        return 0

    return 1


if __name__ == "__main__":
    raise SystemExit(main())
