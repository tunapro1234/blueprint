"""Tunnel — named channel for agent communication."""

from __future__ import annotations

import time
from typing import Iterator

from .file_transport import FileTransport, DEFAULT_BASE_DIR
from .message import Message, new_message, _rand_id
from .transport import Transport


class Tunnel:
    def __init__(self, name: str, agent_name: str, transport: Transport):
        self.name = name
        self.agent_name = agent_name
        self.transport = transport
        self._stash: list[Message] = []  # messages pulled but not yet consumed (type filter)

    @classmethod
    def create(cls, agent_name: str, name: str | None = None, base_dir: str = DEFAULT_BASE_DIR) -> Tunnel:
        """Create a new tunnel and return a Tunnel instance.

        Args:
            agent_name: Creator agent (becomes admin).
            name: Tunnel name. Random hex if not given.
            base_dir: Transport base directory.
        """
        t = FileTransport(base_dir)
        tunnel_name = name or _rand_id()
        t.create(tunnel_name, agent_name)
        return cls(tunnel_name, agent_name, t)

    @classmethod
    def join(cls, name: str, agent_name: str, base_dir: str = DEFAULT_BASE_DIR) -> Tunnel:
        """Join an existing tunnel and return a Tunnel instance."""
        t = FileTransport(base_dir)
        t.join(name, agent_name)
        return cls(name, agent_name, t)

    # --- messaging ---

    def send(self, payload: str, to: str | None = None, type: str | None = None) -> Message:
        """Send a message. to=None broadcasts, to='x' sends only to x."""
        msg = new_message(from_=self.agent_name, payload=payload, to=to, type=type)
        self.transport.send(self.name, msg)
        return msg

    def receive(self, from_: str | None = None, type: str | None = None) -> Message | None:
        """Pop one message. Filter by from_ and/or type. Non-matching stay available."""
        # Check stash first (messages pulled earlier but didn't match type)
        for i, msg in enumerate(self._stash):
            if (from_ is None or msg.from_ == from_) and (type is None or msg.type == type):
                return self._stash.pop(i)

        # Pull from transport
        while True:
            msg = self.transport.receive(self.name, self.agent_name, from_=from_)
            if msg is None:
                return None
            if type is None or msg.type == type:
                return msg
            # Wrong type — stash it for later
            self._stash.append(msg)

    def receive_all(self, from_: str | None = None, type: str | None = None) -> list[Message]:
        """Pop all available messages. Filter by from_ and/or type."""
        messages = []
        while True:
            msg = self.receive(from_=from_, type=type)
            if msg is None:
                break
            messages.append(msg)
        return messages

    def listen(self, from_: str | None = None) -> Iterator[Message]:
        """Continuously yield messages (blocking). Filter by from_."""
        return self.transport.listen(self.name, self.agent_name, from_=from_)

    def wait_for(self, from_: str | None = None, type: str | None = None, timeout: float | None = None) -> Message | None:
        """Block until a message arrives. Returns None on timeout."""
        deadline = time.time() + timeout if timeout is not None else None
        while True:
            msg = self.receive(from_=from_, type=type)
            if msg:
                return msg
            if deadline is not None and time.time() >= deadline:
                return None
            time.sleep(0.1)

    def send_wait(self, payload: str, to: str | None = None, type: str | None = None, wait_from: str | None = None, wait_type: str | None = None, timeout: float | None = None) -> Message | None:
        """Send a message and wait for a reply. Returns None on timeout."""
        self.send(payload, to=to, type=type)
        return self.wait_for(from_=wait_from or to, type=wait_type, timeout=timeout)

    # --- tunnel management ---

    def promote(self, agent: str) -> None:
        if not self.transport.is_admin(self.name, self.agent_name):
            raise PermissionError(f"{self.agent_name} is not admin")
        self.transport.promote(self.name, agent)

    def demote(self, agent: str) -> None:
        if not self.transport.is_admin(self.name, self.agent_name):
            raise PermissionError(f"{self.agent_name} is not admin")
        self.transport.demote(self.name, agent)

    def destroy(self) -> None:
        """Delete this tunnel and all its data. Must be admin."""
        if not self.transport.is_admin(self.name, self.agent_name):
            raise PermissionError(f"{self.agent_name} is not admin")
        self.transport.destroy(self.name)

    def leave(self) -> None:
        self.transport.leave(self.name, self.agent_name)

    # --- queries ---

    def members(self) -> list[str]:
        return self.transport.members(self.name)

    def admins(self) -> list[str]:
        return self.transport.admins(self.name)

    def is_admin(self, agent: str | None = None) -> bool:
        return self.transport.is_admin(self.name, agent or self.agent_name)

    # --- task assignment ---

    def assign(self, instruction: str, to: str) -> Message:
        """Assign a task to an agent. Sends a type='task' message."""
        return self.send(instruction, to=to, type="task")

    def report(self, result: str, to: str | None = None) -> Message:
        """Report task result. Sends a type='result' message."""
        return self.send(result, to=to, type="result")
