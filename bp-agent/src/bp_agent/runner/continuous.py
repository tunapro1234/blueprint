"""Debug CLI for continuous chat mode.

Shows agent thinking, tool calls, and sent messages in real-time.
Agent communicates ONLY via send_message tool — stream output is internal thinking.
"""

from __future__ import annotations

import io
import sys
import threading

from bp_agent.tools import GiveResultSignal


# ANSI colors
GRAY = "\033[90m"
YELLOW = "\033[33m"
CYAN = "\033[36m"
GREEN = "\033[32m"
RED = "\033[31m"
BOLD = "\033[1m"
DIM = "\033[2m"
RESET = "\033[0m"


def continuous_repl(agent) -> None:
    """Run continuous chat debug REPL."""
    # Force UTF-8 stdout
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8")
    elif sys.stdout.encoding != "utf-8":
        sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", line_buffering=True)

    print(f"{BOLD}bp-agent continuous{RESET} (type to chat, 'quit' to exit)")
    print(f"{GRAY}agent communicates via send_message tool only{RESET}")
    print(f"{GRAY}{'─' * 50}{RESET}")

    output_lock = threading.Lock()

    def debug(prefix: str, msg: str) -> None:
        with output_lock:
            sys.stdout.write(f"{GRAY}{prefix} {msg}{RESET}\n")
            sys.stdout.flush()

    def status(msg: str) -> None:
        with output_lock:
            sys.stdout.write(f"{DIM}  [{msg}]{RESET}\n")
            sys.stdout.flush()

    # Hook tools for debug output
    orig_execute = agent.tools.execute

    def hooked_execute(name, args):
        if name == "send_message":
            # Show sent messages prominently
            msg = args.get("message", str(args))
            with output_lock:
                sys.stdout.write(f"\n{CYAN}{BOLD}bot>{RESET} {msg}\n\n")
                sys.stdout.flush()
        else:
            debug("⚙", f"{name}({_trunc(str(args), 80)})")
        try:
            result = orig_execute(name, args)
            if name != "send_message":
                icon = f"{GREEN}✓{GRAY}" if result.success else f"{RED}✗{GRAY}"
                debug(icon, f"{name} → {_trunc(result.output, 80)}")
            return result
        except GiveResultSignal:
            raise

    agent.tools.execute = hooked_execute

    # Hook LLM for debug — show thinking
    orig_stream = agent.llm.complete_stream

    def hooked_stream(request):
        n = len(request.messages)
        last_user = ""
        for m in reversed(request.messages):
            if m.role == "user" and not last_user and not m.content.startswith("[tool:") and not m.content.startswith("[system]"):
                last_user = _trunc(m.content, 50)
                break
        debug("→", f"LLM ({n} msgs) context: {last_user}")
        text_acc = []
        try:
            for chunk in orig_stream(request):
                if chunk.delta:
                    text_acc.append(chunk.delta)
                yield chunk
        except Exception as e:
            debug(f"{RED}✗{GRAY}", f"LLM error: {e}")
            raise
        text = "".join(text_acc)
        if text:
            # Show thinking in dim
            with output_lock:
                sys.stdout.write(f"{DIM}  💭 {_trunc(text, 120)}{RESET}\n")
                sys.stdout.flush()

    agent.llm.complete_stream = hooked_stream

    # Hook inbox wait for status
    orig_wait = agent._inbox.wait

    def hooked_wait(timeout=None):
        if timeout:
            status(f"idle (waiting {timeout:.0f}s)...")
        else:
            status("waiting for first message...")
        result = orig_wait(timeout=timeout)
        return result

    agent._inbox.wait = hooked_wait

    # Consumer thread
    def consume():
        try:
            from bp_agent.agent import CONTINUOUS_SYSTEM_PROMPT
            for message in agent.run_chat(system_prompt=CONTINUOUS_SYSTEM_PROMPT):
                # Messages from send_message are already displayed by the tool hook.
                # This yield is for any non-hook consumers. In debug CLI, already shown.
                pass
            status("chat loop ended")
        except Exception as e:
            with output_lock:
                sys.stdout.write(f"\n{RED}[error] {e}{RESET}\n")
                sys.stdout.flush()

    t = threading.Thread(target=consume, daemon=True)
    t.start()

    # Input loop (main thread)
    while True:
        try:
            msg = input()
        except (EOFError, KeyboardInterrupt):
            break

        if not msg.strip():
            continue

        cmd = msg.strip().lower()
        if cmd in ("quit", "exit", "q"):
            break

        if cmd == "history":
            with output_lock:
                for m in agent.chat_history:
                    if m.role == "system":
                        continue
                    if m.role == "user" and (m.content.startswith("[tool:") or m.content.startswith("[system]")):
                        continue
                    prefix = "you" if m.role == "user" else "bot"
                    c = m.content[:100] + "..." if len(m.content) > 100 else m.content
                    c = c.replace("\n", " ")
                    print(f"  {GRAY}[{prefix}]{RESET} {c}")
            continue

        if cmd == "reset":
            agent.stop_chat()
            t.join(timeout=2)
            agent.reset_chat()
            agent._chat_running = False
            print(f"{GRAY}(reset){RESET}")
            t = threading.Thread(target=consume, daemon=True)
            t.start()
            continue

        with output_lock:
            sys.stdout.write(f"{YELLOW}you>{RESET} {msg}\n")
            sys.stdout.flush()

        agent.push_message(msg)

    agent.stop_chat()
    t.join(timeout=2)
    print(f"\n{GRAY}Bye!{RESET}")


def _trunc(s: str, maxlen: int) -> str:
    s = s.replace("\n", " ")
    if len(s) > maxlen:
        return s[: maxlen - 3] + "..."
    return s


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

    parser = argparse.ArgumentParser(description="bp-agent continuous chat (debug)")
    parser.add_argument("--provider", "-p", default=None)
    parser.add_argument("--model", "-m", default=None)
    args = parser.parse_args()

    from bp_agent.agent import Agent, AgentConfig

    kwargs = {}
    if args.provider:
        kwargs["provider"] = args.provider
    if args.model:
        kwargs["model"] = args.model

    config = AgentConfig(enable_task_store=False, **kwargs)
    agent = Agent("continuous-debug", config=config)
    continuous_repl(agent)


if __name__ == "__main__":
    main()
