"""BP Agent - Minimal task execution agent framework."""

from bp_agent.agent import Agent, AgentConfig, AgentResult, CHAT_SYSTEM_PROMPT, DEFAULT_SYSTEM_PROMPT

__version__ = "0.3.0"
__all__ = [
    "Agent", "AgentConfig", "AgentResult",
    "CHAT_SYSTEM_PROMPT", "DEFAULT_SYSTEM_PROMPT",
]

try:
    from bp_tunnel import Tunnel, TunnelHub, TunnelMessage
    __all__ += ["Tunnel", "TunnelHub", "TunnelMessage"]
except ImportError:
    pass
