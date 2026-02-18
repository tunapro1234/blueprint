"""Agent store — directory-based persistent storage for agent data."""

from __future__ import annotations

import json
import random
import string
from datetime import datetime
from pathlib import Path
from typing import Any


def _generate_id(prefix: str = "") -> str:
    ts = datetime.now().strftime("%Y%m%d_%H%M%S")
    suffix = "".join(random.choices(string.ascii_lowercase + string.digits, k=4))
    return f"{prefix}{ts}_{suffix}" if prefix else f"{ts}_{suffix}"


class AgentStore:
    """Directory-based persistent storage for a single agent.

    Layout:
        {base_dir}/{agent_name}/
            sessions/
                {session_id}.json      # full chat history
            summaries/
                {session_id}.txt       # session summary
            data/
                {key}.json             # arbitrary key-value data
    """

    def __init__(self, agent_name: str, base_dir: str | Path = ".bp-agent/agents"):
        self.agent_name = agent_name
        self.root = Path(base_dir) / agent_name

    def _ensure(self, *parts: str) -> Path:
        p = self.root.joinpath(*parts)
        p.mkdir(parents=True, exist_ok=True)
        return p

    # ── Sessions ──

    def save_session(
        self,
        session_id: str,
        messages: list[dict],
        metadata: dict | None = None,
    ) -> Path:
        """Save chat history for a session."""
        d = self._ensure("sessions")
        path = d / f"{session_id}.json"

        existing = self._read_json(path) or {}
        data = {
            "id": session_id,
            "agent": self.agent_name,
            "created_at": existing.get("created_at", datetime.now().isoformat()),
            "updated_at": datetime.now().isoformat(),
            "message_count": len(messages),
            "messages": messages,
        }
        if metadata:
            data["metadata"] = {**existing.get("metadata", {}), **metadata}
        elif existing.get("metadata"):
            data["metadata"] = existing["metadata"]

        self._write_json(path, data)
        return path

    def load_session(self, session_id: str) -> list[dict] | None:
        """Load chat messages for a session. Returns None if not found."""
        path = self.root / "sessions" / f"{session_id}.json"
        data = self._read_json(path)
        if data is None:
            return None
        return data.get("messages", [])

    def load_session_full(self, session_id: str) -> dict | None:
        """Load full session data (messages + metadata)."""
        path = self.root / "sessions" / f"{session_id}.json"
        return self._read_json(path)

    def list_sessions(self) -> list[dict]:
        """List all sessions with basic info (no messages)."""
        d = self.root / "sessions"
        if not d.exists():
            return []
        sessions = []
        for f in sorted(d.glob("*.json"), key=lambda p: p.stat().st_mtime, reverse=True):
            data = self._read_json(f)
            if data:
                sessions.append({
                    "id": data.get("id", f.stem),
                    "created_at": data.get("created_at"),
                    "updated_at": data.get("updated_at"),
                    "message_count": data.get("message_count", 0),
                })
        return sessions

    def delete_session(self, session_id: str) -> bool:
        path = self.root / "sessions" / f"{session_id}.json"
        if path.exists():
            path.unlink()
            # also delete summary if exists
            summary_path = self.root / "summaries" / f"{session_id}.txt"
            if summary_path.exists():
                summary_path.unlink()
            return True
        return False

    def new_session_id(self) -> str:
        return _generate_id()

    # ── Summaries ──

    def save_summary(self, session_id: str, summary: str) -> Path:
        d = self._ensure("summaries")
        path = d / f"{session_id}.txt"
        path.write_text(summary, encoding="utf-8")
        return path

    def load_summary(self, session_id: str) -> str | None:
        path = self.root / "summaries" / f"{session_id}.txt"
        if path.exists():
            return path.read_text(encoding="utf-8")
        return None

    # ── Helpers ──

    def _write_json(self, path: Path, data: Any) -> None:
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open("w", encoding="utf-8") as f:
            json.dump(data, f, ensure_ascii=False, indent=2)

    def _read_json(self, path: Path) -> Any:
        if not path.exists():
            return None
        with path.open("r", encoding="utf-8") as f:
            return json.load(f)

    @property
    def exists(self) -> bool:
        return self.root.exists()

    def __repr__(self) -> str:
        return f"AgentStore({self.agent_name!r}, root={self.root})"
