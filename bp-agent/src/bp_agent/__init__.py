"""BP Agent - Minimal task execution agent framework."""

from bp_agent.agent import Agent, AgentConfig, AgentResult, CHAT_SYSTEM_PROMPT, DEFAULT_SYSTEM_PROMPT
from bp_agent.store import AgentStore

__version__ = "0.5.1"
__all__ = [
    "Agent", "AgentConfig", "AgentResult", "AgentStore",
    "CHAT_SYSTEM_PROMPT", "DEFAULT_SYSTEM_PROMPT",
]

try:
    from bp_tunnel import Tunnel, TunnelHub, TunnelMessage
    __all__ += ["Tunnel", "TunnelHub", "TunnelMessage"]
except ImportError:
    pass
