# bp-agent

Minimal task execution agent framework with multi-provider LLM support.

## Features

- **Multi-provider**: Gemini, Codex (OpenAI), Opus with automatic key rotation
- **Streaming**: Real-time token streaming for chat responses
- **Tool system**: Built-in tools (bash, read_file, write_file, list_dir) + custom tools
- **Subagents**: Spawn worker agents for parallel task execution
- **Chat mode**: Multi-turn conversation with tool support
- **Task queue**: Persistent JSON-based task scheduling

## Install

```bash
pip install bp-agent
```

## Quick Start

```bash
# Set API key
export GEMINI_API_KEY=your_key

# Interactive chat (streaming)
bp-chat

# Chat with a specific provider
bp-chat --provider codex
bp-chat --provider opus --model my-model
```

## Providers

| Provider | Env Vars | Models |
|----------|----------|--------|
| Gemini (default) | `GEMINI_API_KEY` | gemini-3-flash-preview, gemini-3-pro-preview |
| Codex | `CODEX_API_KEY` or `~/.codex/auth.json` | gpt-5.2-codex, gpt-5.1-codex-mini, ... |
| Opus | `OPUS_API_KEY` + `OPUS_BASE_URL` | configurable |

Multiple keys supported via `GEMINI_API_KEY_2`, `_3`, etc. or comma-separated `GEMINI_API_KEYS`.

## Python API

```python
from bp_agent import Agent, AgentConfig

# Task execution
agent = Agent("my-agent")
result = agent.execute("List all Python files in src/")
print(result.output)

# Multi-turn chat
response = agent.chat("What files are in this directory?")
print(response)

# Streaming chat
for chunk in agent.chat_stream("Explain this codebase"):
    print(chunk, end="", flush=True)

# Custom tools
from bp_agent.tools import ToolSchema
agent.add_tool("greet", lambda name: f"Hello, {name}!", ToolSchema(
    name="greet",
    description="Greet someone",
    parameters={"type": "object", "properties": {"name": {"type": "string"}}, "required": ["name"]},
))
```

## License

GPL-3.0
