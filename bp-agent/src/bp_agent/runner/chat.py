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
            response = agent.chat(user_input)
            print(f"\nbot> {response}")
        except Exception as exc:
            print(f"\n[error] {exc}", file=sys.stderr)


def main():
    import argparse

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
