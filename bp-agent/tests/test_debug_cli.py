from debug_cli.backend import HTTPBackend
from debug_cli.cli import REPL
from debug_cli.models import ChatMessage, CLIConfig


def test_config_defaults():
    config = CLIConfig()
    assert config.provider == "gemini"


def test_http_backend_transcript():
    backend = HTTPBackend("http://test:8080")
    history = [ChatMessage("user", "hi"), ChatMessage("assistant", "hello")]
    transcript = backend._build_transcript(history, "how are you")
    assert "user: hi" in transcript
    assert "assistant: hello" in transcript


def test_command_parse_exit_and_help():
    repl = REPL(CLIConfig())
    assert repl._handle_command(".exit") is True
    assert repl._handle_command(".help") is False
