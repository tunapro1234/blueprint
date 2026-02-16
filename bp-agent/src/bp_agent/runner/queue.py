"""Persistent task queue with scheduling, dependencies, and cron support."""

from __future__ import annotations

import json
import time
from dataclasses import dataclass, field
from pathlib import Path
from threading import Lock
from typing import Optional

from .cron import parse_cron


@dataclass
class QueuedTask:
    id: str
    instruction: str
    status: str = "pending"  # pending, running, completed, failed
    output: Optional[str] = None
    error: Optional[str] = None
    created_at: float = field(default_factory=time.time)
    started_at: Optional[float] = None
    completed_at: Optional[float] = None
    # Scheduling
    run_at: Optional[float] = None  # Don't run before this time
    # Dependencies
    requires: list[str] = field(default_factory=list)  # Task IDs that must complete first
    # Recurring
    cron: Optional[str] = None  # Cron expression for recurring tasks
    parent_id: Optional[str] = None  # ID of the cron parent that spawned this

    def to_dict(self) -> dict:
        d = {
            "id": self.id,
            "instruction": self.instruction,
            "status": self.status,
            "output": self.output,
            "error": self.error,
            "created_at": self.created_at,
            "started_at": self.started_at,
            "completed_at": self.completed_at,
        }
        if self.run_at is not None:
            d["run_at"] = self.run_at
        if self.requires:
            d["requires"] = self.requires
        if self.cron:
            d["cron"] = self.cron
        if self.parent_id:
            d["parent_id"] = self.parent_id
        return d

    @classmethod
    def from_dict(cls, data: dict) -> "QueuedTask":
        return cls(
            id=data["id"],
            instruction=data["instruction"],
            status=data.get("status", "pending"),
            output=data.get("output"),
            error=data.get("error"),
            created_at=data.get("created_at", time.time()),
            started_at=data.get("started_at"),
            completed_at=data.get("completed_at"),
            run_at=data.get("run_at"),
            requires=data.get("requires", []),
            cron=data.get("cron"),
            parent_id=data.get("parent_id"),
        )

    @property
    def is_ready(self) -> bool:
        """Check if this task is ready to run (time + deps satisfied)."""
        if self.status != "pending":
            return False
        if self.run_at and time.time() < self.run_at:
            return False
        return True


class TaskQueue:
    def __init__(self, storage_path: Optional[Path] = None):
        self.storage_path = storage_path
        self._tasks: dict[str, QueuedTask] = {}
        self._lock = Lock()
        self._counter = 0
        if storage_path and storage_path.exists():
            self._load()

    def _generate_id(self) -> str:
        self._counter += 1
        return f"task_{int(time.time())}_{self._counter:04d}"

    def add(
        self,
        instruction: str,
        run_at: Optional[float] = None,
        requires: Optional[list[str]] = None,
        cron: Optional[str] = None,
    ) -> QueuedTask:
        with self._lock:
            # Validate cron expression early
            if cron:
                parse_cron(cron)

            # Validate dependencies exist
            if requires:
                for req_id in requires:
                    if req_id not in self._tasks:
                        raise ValueError(f"Required task not found: {req_id}")

            # For cron tasks with no explicit run_at, schedule first run
            if cron and run_at is None:
                run_at = parse_cron(cron).next_run()

            task = QueuedTask(
                id=self._generate_id(),
                instruction=instruction,
                run_at=run_at,
                requires=requires or [],
                cron=cron,
            )
            self._tasks[task.id] = task
            self._save()
            return task

    def get(self, task_id: str) -> Optional[QueuedTask]:
        return self._tasks.get(task_id)

    def get_next_pending(self) -> Optional[QueuedTask]:
        """Get next task that is ready: pending + time ok + deps satisfied."""
        with self._lock:
            for task in self._tasks.values():
                if not task.is_ready:
                    continue
                if not self._deps_satisfied(task):
                    continue
                return task
            return None

    def _deps_satisfied(self, task: QueuedTask) -> bool:
        """Check if all required tasks are completed."""
        for req_id in task.requires:
            req = self._tasks.get(req_id)
            if not req or req.status != "completed":
                return False
        return True

    def update(
        self,
        task_id: str,
        status: Optional[str] = None,
        output: Optional[str] = None,
        error: Optional[str] = None,
    ) -> Optional[QueuedTask]:
        with self._lock:
            task = self._tasks.get(task_id)
            if not task:
                return None
            if status:
                task.status = status
                if status == "running":
                    task.started_at = time.time()
                elif status in ("completed", "failed"):
                    task.completed_at = time.time()
                    # Auto-schedule next occurrence for cron tasks
                    if status == "completed" and task.cron:
                        self._schedule_next_cron(task)
            if output is not None:
                task.output = output
            if error is not None:
                task.error = error
            self._save()
            return task

    def _schedule_next_cron(self, task: QueuedTask):
        """Create next occurrence of a recurring task. Called inside lock."""
        expr = parse_cron(task.cron)
        next_time = expr.next_run()
        next_task = QueuedTask(
            id=self._generate_id(),
            instruction=task.instruction,
            run_at=next_time,
            cron=task.cron,
            parent_id=task.id,
        )
        self._tasks[next_task.id] = next_task

    def list_all(self) -> list[QueuedTask]:
        return list(self._tasks.values())

    def list_by_status(self, status: str) -> list[QueuedTask]:
        return [t for t in self._tasks.values() if t.status == status]

    def list_ready(self) -> list[QueuedTask]:
        """List all tasks that are ready to run right now."""
        with self._lock:
            return [t for t in self._tasks.values()
                    if t.is_ready and self._deps_satisfied(t)]

    def pending_count(self) -> int:
        return len([t for t in self._tasks.values() if t.status == "pending"])

    def clear_completed(self) -> int:
        with self._lock:
            to_remove = [tid for tid, t in self._tasks.items()
                         if t.status in ("completed", "failed") and not t.cron]
            for tid in to_remove:
                del self._tasks[tid]
            self._save()
            return len(to_remove)

    def _save(self):
        if not self.storage_path:
            return
        self.storage_path.parent.mkdir(parents=True, exist_ok=True)
        data = [t.to_dict() for t in self._tasks.values()]
        self.storage_path.write_text(json.dumps(data, indent=2))

    def _load(self):
        if not self.storage_path or not self.storage_path.exists():
            return
        try:
            data = json.loads(self.storage_path.read_text())
            for item in data:
                task = QueuedTask.from_dict(item)
                self._tasks[task.id] = task
        except Exception:
            pass
