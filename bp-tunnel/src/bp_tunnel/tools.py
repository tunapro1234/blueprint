"""AI-callable tool functions. Stateless, dict in/out."""

import os
import time

from .file_transport import FileTransport, DEFAULT_BASE_DIR
from .message import _rand_id
from .tunnel import Tunnel

_transport: FileTransport | None = None


def _get_transport() -> FileTransport:
    global _transport
    if _transport is None:
        _transport = FileTransport(DEFAULT_BASE_DIR)
    return _transport


def configure(base_dir: str = DEFAULT_BASE_DIR) -> None:
    """Set the base directory for the transport. Call before any other function."""
    global _transport
    _transport = FileTransport(base_dir)


def _get_tunnel(tunnel_id: str, agent_name: str) -> Tunnel:
    return Tunnel(tunnel_id, agent_name, _get_transport())


def create_tunnel(agent_name: str, name: str | None = None) -> dict:
    """Create a new tunnel.

    Args:
        agent_name: Creator agent (becomes admin).
        name: Tunnel name. Random hex if not given.

    Returns:
        {"tunnel_id": "...", "agent": "...", "role": "admin"}
    """
    tunnel_id = name or _rand_id()
    _get_transport().create(tunnel_id, agent_name)
    return {"tunnel_id": tunnel_id, "agent": agent_name, "role": "admin"}


def join_tunnel(tunnel_id: str, agent_name: str) -> dict:
    """Join an existing tunnel.

    Returns:
        {"tunnel_id": "...", "agent": "...", "members": [...]}
    """
    _get_transport().join(tunnel_id, agent_name)
    members = _get_transport().members(tunnel_id)
    return {"tunnel_id": tunnel_id, "agent": agent_name, "members": members}


def send_message(tunnel_id: str, agent_name: str, message: str, to: str | None = None, type: str | None = None) -> dict:
    """Send a message to the tunnel.

    Args:
        tunnel_id: Target tunnel.
        agent_name: Sender agent.
        message: Message content.
        to: (optional) Direct message to specific agent. None = broadcast.
        type: (optional) Message type (e.g. "request", "result", "error").

    Returns:
        {"status": "sent", "from": "...", "to": "..." or "broadcast", "type": "...", "message_id": "..."}
    """
    tun = _get_tunnel(tunnel_id, agent_name)
    msg = tun.send(message, to=to, type=type)
    result = {
        "status": "sent",
        "from": agent_name,
        "to": to or "broadcast",
        "message_id": msg.id,
    }
    if type:
        result["type"] = type
    return result


def _msg_dict(msg) -> dict:
    d = {"status": "message", "from": msg.from_, "message": msg.payload, "ts": msg.ts}
    if msg.type:
        d["type"] = msg.type
    return d


def read_message(tunnel_id: str, agent_name: str, from_agent: str | None = None, type: str | None = None) -> dict:
    """Read one message from mailbox (non-blocking).

    Returns:
        {"status": "message", "from": "...", "message": "...", "ts": ..., "type": "..."}
        or {"status": "empty"}
    """
    tun = _get_tunnel(tunnel_id, agent_name)
    msg = tun.receive(from_=from_agent, type=type)
    if msg:
        return _msg_dict(msg)
    return {"status": "empty"}


def read_all_messages(tunnel_id: str, agent_name: str, from_agent: str | None = None, type: str | None = None) -> dict:
    """Read all messages from mailbox (non-blocking).

    Returns:
        {"status": "messages", "messages": [...], "count": N}
    """
    tun = _get_tunnel(tunnel_id, agent_name)
    msgs = tun.receive_all(from_=from_agent, type=type)
    return {
        "status": "messages",
        "messages": [_msg_dict(m) for m in msgs],
        "count": len(msgs),
    }


def wait_for_message(tunnel_id: str, agent_name: str, from_agent: str | None = None, type: str | None = None, timeout: int = 30) -> dict:
    """Wait for a message (blocking).

    Returns:
        {"status": "message", "from": "...", "message": "...", "ts": ..., "type": "..."}
        or {"status": "timeout"}
    """
    tun = _get_tunnel(tunnel_id, agent_name)
    msg = tun.wait_for(from_=from_agent, type=type, timeout=timeout)
    if msg:
        return _msg_dict(msg)
    return {"status": "timeout"}


def send_and_wait(tunnel_id: str, agent_name: str, message: str, to: str | None = None, type: str | None = None, wait_from: str | None = None, wait_type: str | None = None, timeout: int = 30) -> dict:
    """Send a message and wait for a reply.

    Returns:
        {"status": "message", "from": "...", "message": "...", "ts": ..., "type": "..."}
        or {"status": "timeout", "sent_message_id": "..."}
    """
    tun = _get_tunnel(tunnel_id, agent_name)
    sent = tun.send(message, to=to, type=type)
    msg = tun.wait_for(from_=wait_from or to, type=wait_type, timeout=timeout)
    if msg:
        return _msg_dict(msg)
    return {"status": "timeout", "sent_message_id": sent.id}


def assign_task(tunnel_id: str, admin_name: str, instruction: str, to: str) -> dict:
    """Assign a task through the tunnel (admin only).

    Args:
        tunnel_id: Target tunnel.
        admin_name: Admin agent sending the task.
        instruction: Task instruction.
        to: Target agent name.

    Returns:
        {"status": "assigned", "from": "...", "to": "...", "message_id": "..."}
    """
    tun = _get_tunnel(tunnel_id, admin_name)
    msg = tun.assign(instruction, to=to)
    return {"status": "assigned", "from": admin_name, "to": to, "message_id": msg.id}


def tunnel_info(tunnel_id: str) -> dict:
    """Get tunnel members and admins.

    Returns:
        {"tunnel_id": "...", "members": [...], "admins": [...]}
    """
    return {
        "tunnel_id": tunnel_id,
        "members": _get_transport().members(tunnel_id),
        "admins": _get_transport().admins(tunnel_id),
    }


def list_tunnels() -> dict:
    """List all tunnels.

    Returns:
        {"tunnels": [{"id": "...", "members": [...]}]}
    """
    t = _get_transport()
    result = []
    for tid in t.tunnels():
        result.append({"id": tid, "members": t.members(tid)})
    return {"tunnels": result}
