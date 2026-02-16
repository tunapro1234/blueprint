# bp-multi

Multi-agent orchestration for the Blueprint ecosystem.

## Install

```bash
pip install bp-multi
```

## Usage

```python
from bp_multi import TeamChat
from bp_agent import AgentConfig

team = TeamChat(AgentConfig(provider="gemini"))
team.run_repl()
```

Or via CLI:

```bash
bp-team --provider gemini
```
