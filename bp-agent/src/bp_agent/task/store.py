"""Task store implementation."""

from __future__ import annotations

import json
import os
import random
import string
from dataclasses import dataclass
from datetime import datetime
from enum import Enum
from pathlib import Path
from typing import Optional


class TaskStatus(Enum):
    PENDING = "pending"
    RUNNING = "running"
    COMPLETED = "completed"
    FAILED = "failed"


@dataclass
class Task:
    id: str
    instruction: str
    status: TaskStatus
    created_at: str
    output: Optional[str] = None
    error: Optional[str] = None
    completed_at: Optional[str] = None

    def to_dict(self) -> dict:
        return {
            "id": self.id,
            "instruction": self.instruction,
            "status": self.status.value,
            "output": self.output,
            "error": self.error,
            "created_at": self.created_at,
            "completed_at": self.completed_at,
        }

    @classmethod
    def from_dict(cls, data: dict) -> "Task":
        return cls(
            id=data["id"],
            instruction=data["instruction"],
            status=TaskStatus(data["status"]),
            output=data.get("output"),
            error=data.get("error"),
            created_at=data["created_at"],
            completed_at=data.get("completed_at"),
        )


class TaskNotFoundError(Exception):
    pass


class TaskStore:
    def __init__(self, persist: bool = False, path: str | None = None):
        self.persist = persist
        self.path = Path(path or "tasks.json")
        self._tasks: dict[str, Task] = {}

        if self.persist:
            self._load()

    def create(self, instruction: str) -> Task:
        task = Task(
            id=generate_task_id(),
            instruction=instruction,
            status=TaskStatus.PENDING,
            created_at=datetime.now().isoformat(),
        )

        self._tasks[task.id] = task
        self._save_if_persist()
        return task

    def update(
        self,
        id: str,
        status: str | TaskStatus | None = None,
        output: Optional[str] = None,
        error: Optional[str] = None,
    ) -> Task:
        if id not in self._tasks:
            raise TaskNotFoundError(f"Task {id} not found")

        task = self._tasks[id]

        if status is not None:
            task.status = TaskStatus(status) if isinstance(status, str) else status

        if output is not None:
            task.output = output

        if error is not None:
            task.error = error

        if task.status in (TaskStatus.COMPLETED, TaskStatus.FAILED):
            task.completed_at = datetime.now().isoformat()

        self._save_if_persist()
        return task

    def get(self, id: str) -> Task | None:
        return self._tasks.get(id)

    def list(self, limit: int = 10) -> list[Task]:
        def sort_key(t: Task):
            try:
                return datetime.fromisoformat(t.created_at)
            except ValueError:
                return datetime.min

        tasks = sorted(self._tasks.values(), key=sort_key, reverse=True)
        return tasks[:limit]

    def _save_if_persist(self):
        if not self.persist:
            return

        if self.path.parent:
            os.makedirs(self.path.parent, exist_ok=True)

        data = [t.to_dict() for t in self._tasks.values()]
        with self.path.open("w", encoding="utf-8") as handle:
            json.dump(data, handle, indent=2)

    def _load(self):
        if not self.path.exists():
            return

        with self.path.open("r", encoding="utf-8") as handle:
            data = json.load(handle)

        for item in data:
            task = Task.from_dict(item)
            self._tasks[task.id] = task


def generate_task_id() -> str:
    timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    suffix = "".join(random.choices(string.ascii_lowercase + string.digits, k=4))
    return f"{timestamp}_{suffix}"
