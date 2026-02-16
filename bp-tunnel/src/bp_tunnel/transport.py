"""Transport ABC — defines how messages are delivered."""

from abc import ABC, abstractmethod
from typing import Iterator

from .message import Message


class Transport(ABC):
    # --- messaging ---

    @abstractmethod
    def send(self, tunnel: str, msg: Message) -> None:
        """Send message to all members (except sender). Respects msg.to for direct."""

    @abstractmethod
    def receive(self, tunnel: str, agent: str, from_: str | None = None) -> Message | None:
        """Pop oldest message from mailbox. Filter by sender if from_ given. None if empty."""

    @abstractmethod
    def listen(self, tunnel: str, agent: str, from_: str | None = None) -> Iterator[Message]:
        """Continuously yield messages (blocking generator). Filter by from_."""

    # --- tunnel management ---

    @abstractmethod
    def create(self, tunnel: str, creator: str) -> None:
        """Create a tunnel. Creator becomes admin + member."""

    @abstractmethod
    def join(self, tunnel: str, agent: str) -> None:
        """Join a tunnel as member."""

    @abstractmethod
    def leave(self, tunnel: str, agent: str) -> None:
        """Leave a tunnel."""

    @abstractmethod
    def destroy(self, tunnel: str) -> None:
        """Delete a tunnel and all its data."""

    @abstractmethod
    def promote(self, tunnel: str, agent: str) -> None:
        """Promote agent to admin."""

    @abstractmethod
    def demote(self, tunnel: str, agent: str) -> None:
        """Remove admin privileges from agent."""

    # --- queries ---

    @abstractmethod
    def members(self, tunnel: str) -> list[str]:
        """List tunnel members."""

    @abstractmethod
    def admins(self, tunnel: str) -> list[str]:
        """List tunnel admins."""

    @abstractmethod
    def is_admin(self, tunnel: str, agent: str) -> bool:
        """Check if agent is admin."""

    @abstractmethod
    def tunnels(self) -> list[str]:
        """List all tunnels."""
