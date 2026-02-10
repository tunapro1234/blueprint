"""Task runner CLI - task-based interface."""

from __future__ import annotations

import argparse
import os
import sys
import time
from pathlib import Path
from typing import Optional

from .queue import TaskQueue, QueuedTask
from .runner import TaskRunner


def _format_time(ts: Optional[float]) -> str:
    if not ts:
        return "-"
    return time.strftime("%H:%M:%S", time.localtime(ts))


def _format_task_line(task: QueuedTask, width: int = 50) -> str:
    status_icons = {
        "pending": "○",
        "running": "◐",
        "completed": "●",
        "failed": "✗",
    }
    icon = status_icons.get(task.status, "?")
    instr = task.instruction[:width] + "..." if len(task.instruction) > width else task.instruction
    return f"  {icon} [{task.status:9}] {task.id}: {instr}"


def _print_queue(queue: TaskQueue, show_all: bool = False):
    tasks = queue.list_all()
    if not tasks:
        print("  (empty queue)")
        return

    # Sort: running first, then pending, then completed/failed
    order = {"running": 0, "pending": 1, "completed": 2, "failed": 3}
    tasks.sort(key=lambda t: (order.get(t.status, 9), t.created_at))

    for task in tasks:
        if not show_all and task.status in ("completed", "failed"):
            continue
        print(_format_task_line(task))

    pending = queue.pending_count()
    if pending > 0:
        print(f"\n  {pending} task(s) pending")


def _print_task_detail(task: QueuedTask):
    print(f"  ID:          {task.id}")
    print(f"  Status:      {task.status}")
    print(f"  Instruction: {task.instruction}")
    print(f"  Created:     {_format_time(task.created_at)}")
    print(f"  Started:     {_format_time(task.started_at)}")
    print(f"  Completed:   {_format_time(task.completed_at)}")
    if task.output:
        print(f"  Output:")
        for line in task.output.split("\n")[:10]:
            print(f"    {line}")
        if task.output.count("\n") > 10:
            print("    ...")
    if task.error:
        print(f"  Error: {task.error}")


class TaskCLI:
    def __init__(self, queue: TaskQueue, runner: Optional[TaskRunner] = None):
        self.queue = queue
        self.runner = runner

    def run_repl(self):
        print("Task Runner CLI")
        print("Commands: new <task>, list, show <id>, run, start, stop, clear, quit")
        print("-" * 50)

        while True:
            try:
                line = input("\n> ").strip()
            except (EOFError, KeyboardInterrupt):
                print("\nBye!")
                break

            if not line:
                continue

            parts = line.split(maxsplit=1)
            cmd = parts[0].lower()
            arg = parts[1] if len(parts) > 1 else ""

            if cmd in ("quit", "exit", "q"):
                break
            elif cmd == "new":
                self._cmd_new(arg)
            elif cmd in ("list", "ls"):
                self._cmd_list(arg == "-a" or arg == "--all")
            elif cmd == "show":
                self._cmd_show(arg)
            elif cmd == "run":
                self._cmd_run()
            elif cmd == "start":
                self._cmd_start()
            elif cmd == "stop":
                self._cmd_stop()
            elif cmd == "status":
                self._cmd_status()
            elif cmd == "clear":
                self._cmd_clear()
            elif cmd == "help":
                self._cmd_help()
            else:
                print(f"Unknown command: {cmd}")

    def _cmd_new(self, instruction: str):
        if not instruction:
            print("Usage: new <instruction>")
            return
        task = self.queue.add(instruction)
        print(f"Added: {task.id}")

    def _cmd_list(self, show_all: bool = False):
        _print_queue(self.queue, show_all)

    def _cmd_show(self, task_id: str):
        if not task_id:
            print("Usage: show <task_id>")
            return
        task = self.queue.get(task_id)
        if not task:
            print(f"Task not found: {task_id}")
            return
        _print_task_detail(task)

    def _cmd_run(self):
        if not self.runner:
            print("No runner configured")
            return
        print("Running next pending task...")
        if self.runner.run_once():
            print("Done")
        else:
            print("No pending tasks")

    def _cmd_start(self):
        if not self.runner:
            print("No runner configured")
            return
        if self.runner.is_running:
            print("Runner already running")
            return
        self.runner.start()
        print("Runner started (background)")

    def _cmd_stop(self):
        if not self.runner:
            print("No runner configured")
            return
        if not self.runner.is_running:
            print("Runner not running")
            return
        self.runner.stop()
        print("Runner stopped")

    def _cmd_status(self):
        pending = self.queue.pending_count()
        running = "yes" if self.runner and self.runner.is_running else "no"
        current = self.runner.current_task if self.runner else None
        print(f"  Pending tasks: {pending}")
        print(f"  Runner active: {running}")
        if current:
            print(f"  Current task:  {current}")

    def _cmd_clear(self):
        count = self.queue.clear_completed()
        print(f"Cleared {count} completed/failed tasks")

    def _cmd_help(self):
        print("Commands:")
        print("  new <instruction>  - Add new task")
        print("  list [-a]          - List tasks (-a for all)")
        print("  show <id>          - Show task details")
        print("  run                - Run next task (sync)")
        print("  start              - Start background runner")
        print("  stop               - Stop background runner")
        print("  status             - Show runner status")
        print("  clear              - Remove completed/failed tasks")
        print("  quit               - Exit")


def main(argv: Optional[list[str]] = None) -> int:
    parser = argparse.ArgumentParser(prog="task-runner", description="Task Runner CLI")
    parser.add_argument("--queue", "-q", default=".task_queue.json", help="Queue file path")
    parser.add_argument("--no-agent", action="store_true", help="Run without agent (queue only)")

    subparsers = parser.add_subparsers(dest="command")
    subparsers.add_parser("repl", help="Interactive mode")

    add_p = subparsers.add_parser("add", help="Add task")
    add_p.add_argument("instruction", nargs="+")

    subparsers.add_parser("list", help="List tasks")

    run_p = subparsers.add_parser("run", help="Run tasks")
    run_p.add_argument("--once", action="store_true", help="Run single task")
    run_p.add_argument("--daemon", action="store_true", help="Run in background")

    args = parser.parse_args(argv)

    queue_path = Path(args.queue).expanduser()
    queue = TaskQueue(storage_path=queue_path)

    runner = None
    if not args.no_agent:
        try:
            from agent import Agent, AgentConfig
            config = AgentConfig(enable_task_store=False)  # We use our own queue
            agent = Agent("task-runner", config=config)
            runner = TaskRunner(agent, queue)
        except Exception as exc:
            print(f"Warning: Could not create agent: {exc}", file=sys.stderr)

    command = args.command or "repl"

    if command == "repl":
        TaskCLI(queue, runner).run_repl()
        return 0

    if command == "add":
        instruction = " ".join(args.instruction)
        task = queue.add(instruction)
        print(f"Added: {task.id}")
        return 0

    if command == "list":
        _print_queue(queue, show_all=True)
        return 0

    if command == "run":
        if not runner:
            print("No agent available", file=sys.stderr)
            return 1
        if args.daemon:
            runner.start()
            print("Runner started. Press Ctrl+C to stop.")
            try:
                while runner.is_running:
                    time.sleep(1)
            except KeyboardInterrupt:
                runner.stop()
        elif args.once:
            runner.run_once()
        else:
            # Run all pending
            while runner.run_once():
                pass
        return 0

    return 1


if __name__ == "__main__":
    raise SystemExit(main())
