#!/usr/bin/env python3
"""Interactive Task TUI - live dashboard style."""

from __future__ import annotations

import os
import sys
import time
from pathlib import Path
from typing import Optional, Callable

from .queue import TaskQueue, QueuedTask


def clear_screen():
    sys.stdout.write("\033[2J\033[H")
    sys.stdout.flush()


def move_cursor(row: int, col: int):
    sys.stdout.write(f"\033[{row};{col}H")
    sys.stdout.flush()


def get_terminal_size() -> tuple[int, int]:
    try:
        size = os.get_terminal_size()
        return size.lines, size.columns
    except OSError:
        return 24, 80


class TaskTUI:
    def __init__(self, queue: TaskQueue, agent=None):
        self.queue = queue
        self.agent = agent
        self.status_message: str = ""
        self.running_output: list[str] = []

    def run(self):
        clear_screen()
        self._render()

        while True:
            try:
                cmd = input().strip()
            except (EOFError, KeyboardInterrupt):
                clear_screen()
                print("Bye!")
                break

            if not cmd:
                self._render()
                continue

            self._handle_command(cmd)
            self._render()

    def _handle_command(self, cmd: str):
        parts = cmd.split(maxsplit=1)
        action = parts[0].lower()
        arg = parts[1] if len(parts) > 1 else ""

        if action in ("q", "quit", "exit"):
            raise KeyboardInterrupt

        elif action == "new" or action == "add":
            if not arg:
                self.status_message = "Usage: new <instruction>"
            else:
                task = self.queue.add(arg)
                self.status_message = f"Added: {task.id}"

        elif action == "run" or action == "start":
            self._run_next_task()

        elif action == "runall":
            while self.queue.pending_count() > 0:
                self._run_next_task()
                self._render()

        elif action == "show":
            self._show_task(arg)

        elif action == "clear":
            count = self.queue.clear_completed()
            self.status_message = f"Cleared {count} tasks"

        elif action == "help" or action == "?":
            self.status_message = "Commands: new <task>, run, runall, show <id>, clear, quit"

        else:
            # Treat as new task if not a command
            task = self.queue.add(cmd)
            self.status_message = f"Added: {task.id}"

    def _run_next_task(self):
        if not self.agent:
            self.status_message = "No agent configured"
            return

        task = self.queue.get_next_pending()
        if not task:
            self.status_message = "No pending tasks"
            return

        self.queue.update(task.id, status="running")
        self.running_output = []
        self._render()

        # Hook into agent to capture tool calls and LLM responses
        from bp_agent.tools import GiveResultSignal
        original_tool_execute = None
        original_llm_complete = None

        # Hook LLM to see responses
        if hasattr(self.agent, 'llm'):
            original_llm_complete = self.agent.llm.complete
            def hooked_llm_complete(request):
                response = original_llm_complete(request)
                if response.content:
                    self.running_output.append(f"  💭 {_truncate(response.content, 70)}")
                    self._render()
                return response
            self.agent.llm.complete = hooked_llm_complete

        # Hook tools
        if hasattr(self.agent, 'tools') and self.agent.tools:
            original_tool_execute = self.agent.tools.execute
            def hooked_tool_execute(name, args):
                self.running_output.append(f"  → {name}({_truncate(str(args), 50)})")
                self._render()
                try:
                    result = original_tool_execute(name, args)
                    status = "✓" if result.success else "✗"
                    self.running_output.append(f"  {status} {_truncate(str(result.output), 60)}")
                    self._render()
                    return result
                except GiveResultSignal as sig:
                    self.running_output.append(f"  ✓ RESULT: {_truncate(sig.result, 60)}")
                    self._render()
                    raise
            self.agent.tools.execute = hooked_tool_execute

        try:
            result = self.agent.execute(task.instruction)
            if result.success:
                self.queue.update(task.id, status="completed", output=result.output)
                self.status_message = "Task completed"
                self.running_output.append("")
                self.running_output.append("─" * 40)
                self.running_output.append("OUTPUT:")
                for line in result.output.split("\n")[:15]:
                    self.running_output.append(f"  {line}")
            else:
                self.queue.update(task.id, status="failed", error=result.output)
                self.status_message = "Task failed"
        except Exception as exc:
            self.queue.update(task.id, status="failed", error=str(exc))
            self.status_message = f"Error: {exc}"
        finally:
            if original_tool_execute:
                self.agent.tools.execute = original_tool_execute
            if original_llm_complete:
                self.agent.llm.complete = original_llm_complete

    def _show_task(self, task_id: str):
        if not task_id:
            # Show last completed task
            tasks = self.queue.list_by_status("completed")
            if not tasks:
                self.status_message = "No completed tasks"
                return
            task = tasks[-1]
        else:
            task = self.queue.get(task_id)
            if not task:
                self.status_message = f"Task not found: {task_id}"
                return

        self.running_output = [
            f"ID: {task.id}",
            f"Status: {task.status}",
            f"Instruction: {task.instruction}",
            "",
        ]
        if task.output:
            self.running_output.append("OUTPUT:")
            for line in task.output.split("\n"):
                self.running_output.append(f"  {line}")
        if task.error:
            self.running_output.append(f"ERROR: {task.error}")

    def _render(self):
        rows, cols = get_terminal_size()
        clear_screen()

        # Prepare task list (oldest at top, newest at bottom)
        tasks = self.queue.list_all()
        tasks.sort(key=lambda t: t.created_at)  # ascending - oldest first
        tasks = tasks[-6:]  # keep last 6 (most recent)
        task_lines = max(1, len(tasks))

        status_chars = {
            "pending": "○",
            "running": "◐",
            "completed": "●",
            "failed": "✗",
        }

        # Header
        print(_separator(cols, "╔", "╗"))
        title = " TASK RUNNER "
        padding = (cols - 2 - len(title)) // 2
        print(_pad_line(" " * padding + title, cols))
        print(_separator(cols))

        # Output area (top, fills available space)
        output_lines = rows - task_lines - 8
        displayed = 0
        for line in self.running_output[-(output_lines):]:
            print(_pad_line(" " + _truncate(line, cols - 4), cols))
            displayed += 1

        # Fill remaining output space
        for _ in range(output_lines - displayed):
            print(_pad_line("", cols))

        print(_separator(cols))

        # Task list (bottom) - newest at bottom
        for task in tasks:
            icon = status_chars.get(task.status, "?")
            instr = _truncate(task.instruction, cols - 22)
            content = f" {icon} {task.status:9} | {instr}"
            print(_pad_line(content, cols))

        if not tasks:
            print(_pad_line("   (no tasks)", cols))

        print(_separator(cols))

        # Status line
        status_content = f" {self.status_message}" if self.status_message else ""
        print(_pad_line(status_content, cols))

        print(_separator(cols, "╚", "╝"))

        # Input prompt
        sys.stdout.write("> ")
        sys.stdout.flush()


def _truncate(s: str, maxlen: int) -> str:
    s = s.replace("\n", " ")
    if len(s) > maxlen:
        return s[:maxlen - 3] + "..."
    return s


def _pad_line(content: str, width: int) -> str:
    """Create a box line with proper padding."""
    # Strip existing box chars if any
    inner = content
    # Calculate visible length (approximate - assumes most chars are width 1)
    visible_len = len(inner)
    padding = max(0, width - 2 - visible_len)
    return "║" + inner + " " * padding + "║"


def _separator(width: int, left: str = "╠", right: str = "╣") -> str:
    """Create a separator line."""
    return left + "═" * (width - 2) + right


def main():
    import argparse

    parser = argparse.ArgumentParser(description="Task Runner TUI")
    parser.add_argument("--queue", "-q", default=".task_queue.json")
    args = parser.parse_args()

    queue_path = Path(args.queue).expanduser()
    queue = TaskQueue(storage_path=queue_path)

    agent = None
    try:
        from bp_agent.agent import Agent, AgentConfig
        config = AgentConfig(enable_task_store=False)
        agent = Agent("task-runner", config=config)
    except Exception as exc:
        print(f"Warning: Agent not available: {exc}", file=sys.stderr)
        time.sleep(1)

    tui = TaskTUI(queue, agent)
    tui.run()


if __name__ == "__main__":
    main()
