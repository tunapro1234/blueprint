# bp-tunnel

Inter-agent messaging for the [Blueprint](https://github.com/tunapro1234/blueprint) ecosystem.

Lightweight tunnel system that lets AI agents (or any process) communicate through named channels with pluggable transport backends.

## Install

```bash
pip install bp-tunnel
```

## Quick Start

### Python API

```python
from bp_tunnel import Tunnel

# Agent 1: create and send
alice = Tunnel.create("alice", name="my-tunnel")
alice.send("hello everyone")           # broadcast
alice.send("hey bob", to="bob")        # direct message

# Agent 2: join and receive
bob = Tunnel.join("my-tunnel", "bob")
msg = bob.receive()                    # non-blocking, single message
msgs = bob.receive_all()              # drain all messages
msg = bob.wait_for(from_="alice")      # blocking wait
msg = bob.wait_for(timeout=10)         # with timeout
```

### AI Tool Interface

Stateless dict-based functions for AI agent integration:

```python
from bp_tunnel.tools import create_tunnel, join_tunnel, send_message, wait_for_message

result = create_tunnel("manager", name="team")
# {"tunnel_id": "team", "agent": "manager", "role": "admin"}

join_tunnel("team", "worker")
send_message("team", "manager", "build the feature", to="worker")

msg = wait_for_message("team", "worker", from_agent="manager", timeout=30)
# {"status": "message", "from": "manager", "message": "build the feature", "ts": ...}
```

### CLI

```bash
# Set your identity once (or use --as every time)
export BP_TUNNEL_AGENT=alice

# Create a tunnel (named or random)
bp-tunnel create my-tunnel
bp-tunnel create                   # random hex ID

# Join from another terminal
bp-tunnel join my-tunnel --as bob

# Send messages
bp-tunnel send my-tunnel "hello"
bp-tunnel send my-tunnel "hey bob" --to bob

# Receive
bp-tunnel recv my-tunnel --as bob
bp-tunnel recv my-tunnel --as bob --all          # drain all
bp-tunnel recv my-tunnel --as bob --wait         # block until message
bp-tunnel recv my-tunnel --as bob --from alice

# Listen continuously
bp-tunnel listen my-tunnel --as bob

# JSON output for scripting
bp-tunnel ls --json
bp-tunnel recv my-tunnel --as bob --json

# Admin operations
bp-tunnel promote my-tunnel bob
bp-tunnel demote my-tunnel bob
bp-tunnel destroy my-tunnel

# Info
bp-tunnel info my-tunnel
bp-tunnel ls
```

## Features

- **Multi-agent tunnels** — any number of agents can join a channel
- **Named tunnels** — human-readable names or auto-generated hex IDs
- **Broadcast & direct messaging** — `to=None` broadcasts, `to="agent"` sends direct
- **Blocking patterns** — `wait_for()`, `send_wait()`, `receive_all()` for workflows
- **Admin system** — creator is auto-admin, can promote/demote/destroy
- **Pluggable transport** — abstract `Transport` base class, file-based included
- **AI tool interface** — stateless dict in/out functions for LLM tool calling
- **Selective filtering** — `from_` parameter filters without consuming other messages
- **Atomic writes** — tempfile + rename for crash safety
- **File locking** — fcntl locks for concurrent access
- **JSON output** — `--json` flag for machine-readable CLI output
- **Env var identity** — `BP_TUNNEL_AGENT` to avoid `--as` on every command

## Architecture

```
Python API / AI Tools / CLI
         |
    Tunnel (agent-centric wrapper)
         |
    Transport (abstract interface)
         |
    FileTransport (/tmp/bp-tunnel/)
         |
    Message (YAML format)
```

## License

MIT
