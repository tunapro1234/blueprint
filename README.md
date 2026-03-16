# BLUEPRINT

> **This project is no longer maintained.** The coding-related tooling that Blueprint aimed to provide is being absorbed into AI assistants like Claude natively. The author is now focused on [Omega](https://github.com/trasumanar-ai/omega).

---

> For the developer's vision and thoughts behind this project, see [HUMANS.md](HUMANS.md).

Blueprint is a paradigm for AI-assisted software development. It makes plans permanent, scopes context per-package, and coordinates multiple agents on the same codebase.

## Packages

| Package | Language | Description |
|---------|----------|-------------|
| [bp-cli](bp-cli/) | Go | Blueprint authoring, validation, and snapshot tracking |
| [bp-agent](bp-agent/) | Python | Minimal task execution agent with multi-provider LLM support |
| [bp-tunnel](bp-tunnel/) | Python | Inter-agent messaging with pluggable transports |
| [bp-multi](bp-multi/) | Python | Multi-agent orchestration and workflow management |
| [bp-tui](bp-tui/) | Go | Terminal UI for blueprint management |
| [bp-bench](bp-bench/) | Python | Benchmark and testing suite |

## Quick Start

```bash
# Build the CLI
cd bp-cli/src-go && go build -o ../bp ./cmd/bp

# Install the agent
pip install -e bp-agent/

# Install the tunnel
pip install -e bp-tunnel/
```

## Architecture

```
bp-cli          blueprint files, validation, snapshots
bp-agent        LLM routing, tool execution, task queue
bp-tunnel       named channels for agent-to-agent messaging
bp-multi        team orchestration on top of agent + tunnel
```

## License

GPL-3.0 — see [LICENSE](LICENSE).
