"""Debug CLI entrypoint and REPL."""

from __future__ import annotations

import argparse
import os
import sys
from typing import Optional

from .backend import DirectBackend, HTTPBackend
from .display import (
    format_help,
    format_info,
    format_task_detail,
    format_task_table,
    format_tool_list,
    print_welcome,
)
from .models import ChatMessage, CLIConfig, RunMode


class REPL:
    def __init__(self, config: CLIConfig, token: Optional[str] = None):
        self.config = config
        self.token = token
        self.backend = self._create_backend()
        self.history: list[ChatMessage] = []
        self.notice: str | None = None

    def _create_backend(self):
        if self.config.mode == RunMode.HTTP:
            return HTTPBackend(self.config.base_url, token=self.token)
        return DirectBackend()

    def run(self):
        if not self._is_tty():
            print_welcome(self.config)
        while True:
            try:
                line = self._read_input()
            except (EOFError, KeyboardInterrupt):
                print("\nBye!")
                break

            if not line:
                continue

            if line.startswith("."):
                if self._handle_command(line):
                    break
                continue

            result = self.backend.execute(line, self.history, self.config)
            output = result.output
            if self.config.debug and result.tool_calls:
                tool_lines = [f"[tool] {tc.get('name')}({tc.get('args')})" for tc in result.tool_calls]
                output = output + "\n" + "\n".join(tool_lines)

            self.history.append(ChatMessage("user", line))
            self.history.append(ChatMessage("assistant", output))

    def _handle_command(self, line: str) -> bool:
        cmd, args = self._parse_command(line)

        if cmd in (".exit", ".quit", ".q"):
            return True

        if cmd == ".help":
            self.history.append(ChatMessage("assistant", format_help()))
            return False

        if cmd == ".info":
            self.history.append(ChatMessage("assistant", format_info(self.config)))
            return False

        if cmd == ".model":
            self._select_model()
            return False

        if cmd == ".provider":
            self._select_provider()
            return False

        if cmd == ".tasks":
            tasks = self.backend.list_tasks()
            self.history.append(ChatMessage("assistant", format_task_table(tasks)))
            return False

        if cmd == ".task":
            if not args:
                self.notice = "Usage: .task <id>"
                return False
            task = self.backend.get_task(args)
            if task:
                self.history.append(ChatMessage("assistant", format_task_detail(task)))
            else:
                self.notice = f"Task not found: {args}"
            return False

        if cmd == ".tools":
            tools = self.backend.list_tools()
            self.history.append(ChatMessage("assistant", format_tool_list(tools)))
            return False

        if cmd == ".system":
            self.config.system_prompt = args or None
            self.notice = "System prompt updated"
            return False

        if cmd == ".clear":
            self.history.clear()
            self.notice = "History cleared"
            return False

        if cmd == ".debug":
            self.config.debug = not self.config.debug
            self.notice = f"Debug: {'on' if self.config.debug else 'off'}"
            return False

        if cmd == ".mode":
            if args in ("http", "direct"):
                self.config.mode = RunMode.HTTP if args == "http" else RunMode.DIRECT
                self.backend = self._create_backend()
                self.notice = f"Mode: {self.config.mode.value}"
            else:
                self.notice = "Usage: .mode http|direct"
            return False

        self.notice = f"Unknown command: {cmd}. Type .help"
        return False

    def _select_model(self):
        models = self.backend.list_models()
        if not models:
            self.notice = "No models available"
            return
        selection = self._select_from_list("Models", models, self.config.model)
        if selection is None:
            return
        self.config.model = selection
        self.notice = f"Model: {self.config.model}"

    def _select_provider(self):
        providers = ["gemini", "codex", "opus"]
        selection = self._select_from_list("Providers", providers, self.config.provider)
        if selection is None:
            return
        self.config.provider = selection
        self.config.model = None
        self.notice = f"Provider: {self.config.provider}"

    def _select_from_list(self, title: str, items: list[str], current: Optional[str]) -> Optional[str]:
        prompt_lines = [f"{title}:"]
        for idx, item in enumerate(items, 1):
            cur = " (current)" if item == current else ""
            prompt_lines.append(f"  [{idx}] {item}{cur}")
        prompt_lines.append("Select: ")
        self._render_screen(extra="\n".join(prompt_lines))
        try:
            choice = input()
        except EOFError:
            return None
        choice = choice.strip()
        if not choice:
            return None
        if choice.isdigit():
            index = int(choice) - 1
            if 0 <= index < len(items):
                return items[index]
            self.notice = "Invalid selection"
            return None
        return choice

    def _parse_command(self, line: str) -> tuple[str, str]:
        parts = line.strip().split(maxsplit=1)
        cmd = parts[0]
        args = parts[1] if len(parts) > 1 else ""
        return cmd, args

    def _read_input(self) -> str:
        if self._is_tty():
            self._render_screen()
            return input().strip()
        return input("you> ").strip()

    def _render_screen(self, extra: str | None = None):
        if not self._is_tty():
            return
        sys.stdout.write("\033[2J\033[H")
        sys.stdout.write("Debug CLI (type .help for commands)\n")
        sys.stdout.write(self._status_line() + "\n")
        if self.notice:
            sys.stdout.write(f"notice: {self.notice}\n")
        sys.stdout.write("-" * 48 + "\n")
        for msg in self.history:
            sys.stdout.write(self._format_message(msg))
        if extra:
            sys.stdout.write(extra + "\n")
        sys.stdout.write("you> ")
        sys.stdout.flush()
        self.notice = None

    def _status_line(self) -> str:
        model = self.config.model or "default"
        temp = f"{self.config.temperature:.2f}" if self.config.temperature else "default"
        return (
            f"mode={self.config.mode.value}  provider={self.config.provider}  "
            f"model={model}  temp={temp}  debug={'on' if self.config.debug else 'off'}"
        )

    def _format_message(self, msg: ChatMessage) -> str:
        prefix = f"{msg.role}: "
        lines = msg.content.splitlines() or [""]
        rendered = []
        for idx, line in enumerate(lines):
            if idx == 0:
                rendered.append(prefix + line)
            else:
                rendered.append(" " * len(prefix) + line)
        return "\n".join(rendered) + "\n\n"

    @staticmethod
    def _is_tty() -> bool:
        try:
            return sys.stdin.isatty() and sys.stdout.isatty()
        except Exception:
            return False


def build_config(args: argparse.Namespace) -> CLIConfig:
    base_url = args.url or os.getenv("BASE_AGENT_URL", "http://localhost:8080")
    mode = RunMode.DIRECT if args.direct else RunMode.HTTP
    return CLIConfig(
        mode=mode,
        base_url=base_url,
        provider=args.provider,
        model=args.model,
        system_prompt=args.system,
        temperature=args.temp,
        debug=args.debug,
    )


def main(argv: Optional[list[str]] = None) -> int:
    parser = argparse.ArgumentParser(prog="debug-cli", description="Base Agent Debug CLI")
    parser.add_argument("--url", default=None, help="API server URL")
    parser.add_argument("--direct", action="store_true", help="Direct mode (no server)")
    parser.add_argument("--provider", default="gemini")
    parser.add_argument("--model", default=None)
    parser.add_argument("--system", default=None, help="System prompt")
    parser.add_argument("--temp", type=float, default=0.3)
    parser.add_argument("--debug", action="store_true")
    parser.add_argument("--token", default=None)

    subparsers = parser.add_subparsers(dest="command")
    subparsers.add_parser("repl", help="Interactive chat")

    run_p = subparsers.add_parser("run", help="Single instruction")
    run_p.add_argument("instruction", nargs="*")

    args = parser.parse_args(argv)
    config = build_config(args)
    token = args.token

    command = args.command or "repl"

    if command == "repl":
        REPL(config, token=token).run()
        return 0

    if command == "run":
        if args.instruction:
            instruction = " ".join(args.instruction).strip()
        else:
            if sys.stdin.isatty():
                instruction = input("Instruction: ").strip()
            else:
                instruction = sys.stdin.read().strip()
        if not instruction:
            print("No instruction provided", file=sys.stderr)
            return 1
        backend = DirectBackend() if config.mode == RunMode.DIRECT else HTTPBackend(config.base_url, token=token)
        result = backend.execute(instruction, [], config)
        print(result.output)
        return 0

    return 1


if __name__ == "__main__":
    raise SystemExit(main())
