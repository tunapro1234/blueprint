"""Task runner - processes tasks from queue."""

from __future__ import annotations

import time
from threading import Event, Thread
from typing import TYPE_CHECKING, Optional

from .queue import TaskQueue

if TYPE_CHECKING:
    from bp_agent.agent import Agent


class TaskRunner:
    def __init__(self, agent: "Agent", queue: TaskQueue):
        self.agent = agent
        self.queue = queue
        self._running = False
        self._thread: Optional[Thread] = None
        self._stop_event = Event()
        self._current_task_id: Optional[str] = None

    @property
    def is_running(self) -> bool:
        return self._running

    @property
    def current_task(self) -> Optional[str]:
        return self._current_task_id

    def start(self):
        if self._running:
            return
        self._running = True
        self._stop_event.clear()
        self._thread = Thread(target=self._run_loop, daemon=True)
        self._thread.start()

    def stop(self):
        self._running = False
        self._stop_event.set()
        if self._thread:
            self._thread.join(timeout=5)
            self._thread = None

    def _run_loop(self):
        while self._running and not self._stop_event.is_set():
            task = self.queue.get_next_pending()
            if not task:
                self._stop_event.wait(timeout=1)
                continue

            self._current_task_id = task.id
            self.queue.update(task.id, status="running")

            try:
                result = self.agent.execute(task.instruction)
                if result.success:
                    self.queue.update(task.id, status="completed", output=result.output)
                else:
                    self.queue.update(task.id, status="failed", error=result.output or "Unknown error")
            except Exception as exc:
                self.queue.update(task.id, status="failed", error=str(exc))

            self._current_task_id = None

    def run_once(self) -> bool:
        """Run single task synchronously. Returns True if a task was processed."""
        task = self.queue.get_next_pending()
        if not task:
            return False

        self._current_task_id = task.id
        self.queue.update(task.id, status="running")

        try:
            result = self.agent.execute(task.instruction)
            if result.success:
                self.queue.update(task.id, status="completed", output=result.output)
            else:
                self.queue.update(task.id, status="failed", error=result.output or "Unknown error")
        except Exception as exc:
            self.queue.update(task.id, status="failed", error=str(exc))

        self._current_task_id = None
        return True
