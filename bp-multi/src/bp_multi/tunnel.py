"""Tunnel: thread-safe named message channels between agents."""

from __future__ import annotations

import queue
import time
import threading
from dataclasses import dataclass, field
from typing import Optional


@dataclass
class TunnelMessage:
    sender: str
    content: str
    timestamp: float = field(default_factory=time.time)

    def __str__(self) -> str:
        return f"[from:{self.sender}] {self.content}"


class Tunnel:
    """Named, thread-safe message channel."""

    def __init__(self, name: str):
        self.name = name
        self._queue: queue.Queue[TunnelMessage] = queue.Queue()

    def send(self, content: str, sender: str = ""):
        self._queue.put(TunnelMessage(sender=sender, content=content))

    def receive(self, timeout: float | None = None) -> TunnelMessage | None:
        try:
            return self._queue.get(timeout=timeout)
        except queue.Empty:
            return None

    def receive_all(self) -> list[TunnelMessage]:
        msgs: list[TunnelMessage] = []
        while True:
            try:
                msgs.append(self._queue.get_nowait())
            except queue.Empty:
                break
        return msgs

    @property
    def pending(self) -> int:
        return self._queue.qsize()


class TunnelHub:
    """Central registry for named tunnels. Shared across agents."""

    def __init__(self):
        self._tunnels: dict[str, Tunnel] = {}
        self._lock = threading.Lock()

    def get_or_create(self, name: str) -> Tunnel:
        with self._lock:
            if name not in self._tunnels:
                self._tunnels[name] = Tunnel(name)
            return self._tunnels[name]

    def send(self, channel: str, content: str, sender: str = ""):
        self.get_or_create(channel).send(content, sender)

    def receive(self, channel: str, timeout: float | None = None) -> TunnelMessage | None:
        return self.get_or_create(channel).receive(timeout)

    def receive_all(self, channel: str) -> list[TunnelMessage]:
        return self.get_or_create(channel).receive_all()

    def list_channels(self) -> list[str]:
        with self._lock:
            return list(self._tunnels.keys())

    def pending_count(self, channel: str) -> int:
        with self._lock:
            t = self._tunnels.get(channel)
            return t.pending if t else 0
