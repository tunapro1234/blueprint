"""Built-in tools for Base Agent."""

from __future__ import annotations

import os
import subprocess
from pathlib import Path
from typing import Optional

from .registry import ToolRegistry, ToolSchema, build_schema


def _bash_handler(command: str, timeout: int = 30, cwd: Optional[str] = None) -> str:
    """Execute a bash command and return output."""
    try:
        result = subprocess.run(
            command,
            shell=True,
            capture_output=True,
            text=True,
            timeout=timeout,
            cwd=cwd,
        )
        output = result.stdout
        if result.stderr:
            output += f"\n[stderr]\n{result.stderr}"
        if result.returncode != 0:
            output += f"\n[exit code: {result.returncode}]"
        return output.strip() or "(no output)"
    except subprocess.TimeoutExpired:
        return f"[error] Command timed out after {timeout}s"
    except Exception as exc:
        return f"[error] {exc}"


def _read_file_handler(path: str, encoding: str = "utf-8") -> str:
    """Read file contents."""
    try:
        p = Path(path).expanduser().resolve()
        if not p.exists():
            return f"[error] File not found: {path}"
        if not p.is_file():
            return f"[error] Not a file: {path}"
        content = p.read_text(encoding=encoding)
        return content
    except Exception as exc:
        return f"[error] {exc}"


def _write_file_handler(path: str, content: str, encoding: str = "utf-8") -> str:
    """Write content to file."""
    try:
        p = Path(path).expanduser().resolve()
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(content, encoding=encoding)
        return f"[ok] Wrote {len(content)} bytes to {path}"
    except Exception as exc:
        return f"[error] {exc}"


def _list_dir_handler(path: str = ".") -> str:
    """List directory contents."""
    try:
        p = Path(path).expanduser().resolve()
        if not p.exists():
            return f"[error] Path not found: {path}"
        if not p.is_dir():
            return f"[error] Not a directory: {path}"
        entries = sorted(p.iterdir(), key=lambda x: (not x.is_dir(), x.name.lower()))
        lines = []
        for entry in entries:
            prefix = "d " if entry.is_dir() else "f "
            lines.append(f"{prefix}{entry.name}")
        return "\n".join(lines) or "(empty directory)"
    except Exception as exc:
        return f"[error] {exc}"


BASH_SCHEMA = build_schema(
    "bash",
    "Execute a bash command and return output",
    command={"type": "string", "description": "The command to execute", "required": True},
    timeout={"type": "integer", "description": "Timeout in seconds (default 30)"},
    cwd={"type": "string", "description": "Working directory"},
)

READ_FILE_SCHEMA = build_schema(
    "read_file",
    "Read the contents of a file",
    path={"type": "string", "description": "Path to the file", "required": True},
    encoding={"type": "string", "description": "File encoding (default utf-8)"},
)

WRITE_FILE_SCHEMA = build_schema(
    "write_file",
    "Write content to a file (creates parent directories if needed)",
    path={"type": "string", "description": "Path to the file", "required": True},
    content={"type": "string", "description": "Content to write", "required": True},
    encoding={"type": "string", "description": "File encoding (default utf-8)"},
)

LIST_DIR_SCHEMA = build_schema(
    "list_dir",
    "List contents of a directory",
    path={"type": "string", "description": "Path to the directory (default: current)"},
)

GIVE_RESULT_SCHEMA = build_schema(
    "give_result",
    "REQUIRED: Call this tool to deliver your final answer to the user. The task is NOT complete until you call this tool.",
    result={"type": "string", "description": "The final result/answer to show the user", "required": True},
)


class GiveResultSignal(Exception):
    """Signal that give_result was called."""
    def __init__(self, result: str):
        self.result = result
        super().__init__(result)


def _give_result_handler(result: str) -> str:
    """Signal completion with final result."""
    raise GiveResultSignal(result)


def register_builtins(registry: ToolRegistry) -> None:
    """Register all built-in tools."""
    registry.register("bash", _bash_handler, BASH_SCHEMA)
    registry.register("read_file", _read_file_handler, READ_FILE_SCHEMA)
    registry.register("write_file", _write_file_handler, WRITE_FILE_SCHEMA)
    registry.register("list_dir", _list_dir_handler, LIST_DIR_SCHEMA)
    registry.register("give_result", _give_result_handler, GIVE_RESULT_SCHEMA)
