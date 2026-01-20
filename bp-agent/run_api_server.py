"""Snapshot-aware entrypoint for the API server."""

from __future__ import annotations

import argparse
import os
import sys
from pathlib import Path


def _read_snapshot_id(path: Path) -> str:
    if not path.exists():
        raise SystemExit(f"Snapshot file not found: {path}")
    value = path.read_text(encoding="utf-8").strip()
    if not value:
        raise SystemExit(f"Snapshot file empty: {path}")
    return value


def _inject_snapshot_paths(repo_root: Path) -> Path:
    state_dir = repo_root / "src" / ".blueprint"
    snapshot_id = _read_snapshot_id(state_dir / "current")
    snapshot_dir = state_dir / "history" / snapshot_id
    if not snapshot_dir.exists():
        raise SystemExit(f"Snapshot not found: {snapshot_dir}")
    sys.path.insert(0, str(snapshot_dir))
    return snapshot_dir


def _build_echo_agent():
    from agent import AgentResult
    from task import TaskStore
    from tools import ToolRegistry

    class EchoAgent:
        def __init__(self):
            self.system_prompt = ""
            self.tools = ToolRegistry()
            self.tasks = TaskStore()

        def execute(self, instruction: str):
            task = self.tasks.create(instruction)
            output = f"echo: {instruction}"
            self.tasks.update(task.id, status="completed", output=output)
            return AgentResult(success=True, output=output, task_id=task.id)

    return EchoAgent()


def main() -> int:
    parser = argparse.ArgumentParser(description="Run Base Agent API server from snapshots")
    parser.add_argument("--port", type=int, default=int(os.getenv("PORT", "8080")))
    parser.add_argument(
        "--mock",
        action="store_true",
        help="Use echo agent if no API keys are found",
    )
    parser.add_argument(
        "--force-mock",
        action="store_true",
        help="Always use echo agent (no LLM calls)",
    )
    args = parser.parse_args()

    repo_root = Path(__file__).resolve().parent
    _inject_snapshot_paths(repo_root)

    from api.server import AgentServer

    if args.force_mock:
        server = AgentServer(port=args.port, agent=_build_echo_agent())
    else:
        try:
            server = AgentServer(port=args.port)
        except ValueError as exc:
            if args.mock or "No API keys found" in str(exc):
                print("No API keys found; starting echo agent.", file=sys.stderr)
                server = AgentServer(port=args.port, agent=_build_echo_agent())
            else:
                raise

    print(f"Starting server on port {server.port}")
    server.start()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
