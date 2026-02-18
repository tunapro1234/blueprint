"""Simple chat REPL - multi-turn conversation with tool support."""

from __future__ import annotations

import sys


def chat_repl(agent) -> None:
    """Run a simple chat REPL with the given agent."""
    print("bp-agent chat (type 'quit' to exit, 'reset' to clear history)")
    print("-" * 50)

    while True:
        try:
            user_input = input("\nyou> ").strip()
        except (EOFError, KeyboardInterrupt):
            print("\nBye!")
            break

        if not user_input:
            continue

        if user_input.lower() in ("quit", "exit", "q"):
            break

        if user_input.lower() == "reset":
            agent.reset_chat()
            print("(chat history cleared)")
            continue

        if user_input.lower() == "history":
            for msg in agent.chat_history:
                if msg.role == "system":
                    continue
                prefix = "you" if msg.role == "user" else "bot"
                content = msg.content[:80] + "..." if len(msg.content) > 80 else msg.content
                print(f"  [{prefix}] {content}")
            continue

        try:
            if hasattr(agent, "chat_stream"):
                sys.stdout.write("\nbot> ")
                sys.stdout.flush()
                for delta in agent.chat_stream(user_input):
                    sys.stdout.write(delta)
                    sys.stdout.flush()
                sys.stdout.write("\n")
                sys.stdout.flush()
            else:
                response = agent.chat(user_input)
                print(f"\nbot> {response}")
        except Exception as exc:
            print(f"\n[error] {exc}", file=sys.stderr)


def _load_dotenv():
    """Load .env file from cwd or parents."""
    from pathlib import Path
    for d in [Path.cwd()] + list(Path.cwd().parents):
        env_file = d / ".env"
        if env_file.exists():
            import os
            for line in env_file.read_text().splitlines():
                line = line.strip()
                if not line or line.startswith("#"):
                    continue
                if "=" in line:
                    key, _, val = line.partition("=")
                    os.environ.setdefault(key.strip(), val.strip())
            break


def main():
    import argparse

    _load_dotenv()

    parser = argparse.ArgumentParser(description="bp-agent chat")
    parser.add_argument("--provider", "-p", default=None)
    parser.add_argument("--model", "-m", default=None)
    args = parser.parse_args()

    from bp_agent.agent import Agent, AgentConfig, CHAT_SYSTEM_PROMPT

    kwargs = {}
    if args.provider:
        kwargs["provider"] = args.provider
    if args.model:
        kwargs["model"] = args.model

    config = AgentConfig(enable_task_store=False, **kwargs)
    agent = Agent("chat", config=config, system_prompt=CHAT_SYSTEM_PROMPT)
    chat_repl(agent)


if __name__ == "__main__":
    main()
