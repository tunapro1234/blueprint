"""TunnelHub adapter — bridges bp_tunnel.Tunnel to bp_agent's connect_hub() interface."""

from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from .tunnel import Tunnel


@dataclass
class TunnelMessage:
    """Message wrapper that bp-agent displays via str()."""

    from_: str
    payload: str
    type: str | None = None
    ts: int = 0

    def __str__(self) -> str:
        return f"[{self.from_}]: {self.payload}"


class TunnelHub:
    """Adapter: bp_tunnel.Tunnel → bp_agent connect_hub() interface.

    Channel conventions used by bp-agent's _register_tunnel_tools():
      - "agent:<name>" → direct messages to/from that agent
      - anything else   → treated as a typed channel
    """

    def __init__(self, tunnel: Tunnel) -> None:
        self.tunnel = tunnel

    def send(self, channel: str, message: str, sender: str | None = None) -> None:
        """Send a message through the tunnel.

        "agent:bob" → tunnel.send(message, to="bob")
        other       → tunnel.send(message, type=channel)  (broadcast)
        """
        if channel.startswith("agent:"):
            target = channel[len("agent:"):]
            self.tunnel.send(message, to=target)
        else:
            self.tunnel.send(message, type=channel)

    def receive_all(self, channel: str) -> list[TunnelMessage]:
        """Receive all pending messages for a channel.

        "agent:<me>" → tunnel.receive_all()  (all messages addressed to me)
        other        → tunnel.receive_all(type=channel)
        """
        if channel.startswith("agent:"):
            msgs = self.tunnel.receive_all()
        else:
            msgs = self.tunnel.receive_all(type=channel)

        return [
            TunnelMessage(
                from_=m.from_,
                payload=m.payload,
                type=m.type,
                ts=m.ts,
            )
            for m in msgs
        ]
