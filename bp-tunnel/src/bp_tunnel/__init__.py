"""bp-tunnel: Inter-agent messaging for Blueprint ecosystem."""

__version__ = "0.3.1"

from .message import Message, new_message
from .transport import Transport
from .file_transport import FileTransport, DEFAULT_BASE_DIR
from .tunnel import Tunnel
from .hub import TunnelHub, TunnelMessage

__all__ = ["Message", "new_message", "Transport", "FileTransport", "DEFAULT_BASE_DIR", "Tunnel", "TunnelHub", "TunnelMessage"]
