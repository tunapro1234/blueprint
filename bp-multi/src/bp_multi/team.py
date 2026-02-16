"""TeamChat: non-blocking manager-worker-tester orchestration.

User always chats with Manager.  Worker and Tester run in a background
thread and report back via tunnels.  Manager sees their updates at the
start of every turn and can relay / approve / reject.
"""

from __future__ import annotations

import sys
import threading
from typing import Optional, TYPE_CHECKING

from bp_multi.tunnel import TunnelHub

if TYPE_CHECKING:
    from bp_agent.agent import Agent, AgentConfig


# ── System prompts ───────────────────────────────────────────────

MANAGER_PROMPT = """\
You are a project manager coordinating a worker (implements code) and a tester (validates).

You ALWAYS chat with the user.  You have these coordination tools:
  start_task  — kick off a task with your high-level plan (worker & tester plan/execute in background)
  approve_plan — approve the plans so worker begins implementing
  reject_plan  — reject with a reason; agents will re-plan

At the start of messages you may see "--- Team Updates ---".
These are reports from worker/tester.  Read them carefully.

WORKFLOW:
1. Chat normally for questions / discussion.
2. When there is a task, explore the codebase, then call start_task with a CONCISE plan.
3. You will receive worker + tester plans as updates.  Present them to the user.
4. When user says approve (or you are confident), call approve_plan.
5. Worker implements, tester validates — they iterate automatically.
6. You get final results via updates.  Summarise for the user.

Keep plans short — what & why, not how.  You have filesystem tools."""

WORKER_PROMPT = """\
You are an implementation specialist.

WHEN ASKED TO PLAN:
  Explore the codebase, then list exact file changes with reasoning.

WHEN ASKED TO IMPLEMENT:
  Execute step by step with bash / read_file / write_file.
  When done, summarise every change you made.

WHEN ASKED TO FIX:
  Read the tester's feedback carefully, fix only what is broken.

You NEVER test — the tester handles that."""

TESTER_PROMPT = """\
You are a QA specialist.

WHEN ASKED TO PLAN:
  List what to test, how, and expected results.

WHEN ASKED TO VALIDATE:
  Run tests (pytest, bash, manual checks).
  Give CLEAR, ACTIONABLE feedback — say exactly what is wrong.
  When everything is correct say "ALL TESTS PASS" as the first line.

Be strict but fair.  Focus on correctness, not style."""


# ── TeamChat ─────────────────────────────────────────────────────

class TeamChat:
    """Non-blocking team orchestration.  User always chats with Manager."""

    def __init__(self, config: "AgentConfig | None" = None):
        from bp_agent.agent import AgentConfig

        self.hub = TunnelHub()
        base = config or AgentConfig()

        self.manager = self._make_agent("manager", base, MANAGER_PROMPT, 15)
        self.worker = self._make_agent("worker", base, WORKER_PROMPT, 25)
        self.tester = self._make_agent("tester", base, TESTER_PROMPT, 20)

        self._bg_thread: Optional[threading.Thread] = None
        self._task_active = False
        self._max_review_rounds = 5

        self._register_manager_tools()

    # ── agent factory ────────────────────────────────────────────

    def _make_agent(
        self, name: str, base: "AgentConfig", prompt: str, max_iter: int
    ) -> "Agent":
        from bp_agent.agent import Agent, AgentConfig

        cfg = AgentConfig(
            provider=base.provider,
            model=base.model,
            temperature=base.temperature,
            max_iterations=max_iter,
            enable_task_store=False,
            enable_builtin_tools=True,
        )
        agent = Agent(name, config=cfg, system_prompt=prompt)
        agent.connect_hub(self.hub, alias=name)
        return agent

    # ── manager-only tools ───────────────────────────────────────

    def _register_manager_tools(self):
        from bp_agent.tools import build_schema

        team = self

        def _start_task(plan: str) -> str:
            if team._task_active:
                return "[error] a task is already running — wait for it to finish"
            team._task_active = True
            team._bg_thread = threading.Thread(
                target=team._bg_pipeline, args=(plan,), daemon=True
            )
            team._bg_thread.start()
            return "[task started — worker and tester are planning in background]"

        def _approve_plan() -> str:
            team.hub.send("team:approval", "approved", sender="manager")
            return "[approved — worker will begin implementation]"

        def _reject_plan(reason: str = "") -> str:
            team.hub.send("team:approval", f"rejected: {reason}", sender="manager")
            return "[rejected — sent back for re-planning]"

        self.manager.add_tool(
            "start_task",
            _start_task,
            build_schema(
                "start_task",
                "Begin a task — provide your high-level plan.",
                plan={
                    "type": "string",
                    "description": "Concise high-level plan",
                    "required": True,
                },
            ),
        )
        self.manager.add_tool(
            "approve_plan",
            _approve_plan,
            build_schema(
                "approve_plan",
                "Approve worker/tester plans so implementation can begin.",
            ),
        )
        self.manager.add_tool(
            "reject_plan",
            _reject_plan,
            build_schema(
                "reject_plan",
                "Reject plans — agents will re-plan.",
                reason={"type": "string", "description": "Why rejected"},
            ),
        )

    # ── REPL ─────────────────────────────────────────────────────

    def run_repl(self):
        print("bp-multi team  (manager + worker + tester)")
        print("chat normally — manager is always available")
        print("commands: status | reset | quit")
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
            if user_input.lower() == "status":
                self._print_status()
                continue
            if user_input.lower() == "reset":
                self._reset()
                continue

            self._manager_turn(user_input)

    def _manager_turn(self, user_message: str):
        """Chat with manager, injecting pending team updates first."""
        pending = self.hub.receive_all("agent:manager")

        parts: list[str] = []
        if pending:
            parts.append("--- Team Updates ---")
            for m in pending:
                parts.append(str(m))
            parts.append("--- End Updates ---\n")
        parts.append(user_message)

        full = "\n".join(parts)

        sys.stdout.write("\nmanager> ")
        sys.stdout.flush()
        for delta in self.manager.chat_stream(full):
            sys.stdout.write(delta)
            sys.stdout.flush()
        sys.stdout.write("\n")
        sys.stdout.flush()

    def _print_status(self):
        pending = self.hub.pending_count("agent:manager")
        print(f"  task active : {'yes' if self._task_active else 'no'}")
        print(f"  pending msgs: {pending}")
        channels = self.hub.list_channels()
        if channels:
            print(f"  channels    : {', '.join(channels)}")

    def _reset(self):
        self.manager.reset_chat()
        self.worker.reset_chat()
        self.tester.reset_chat()
        self._task_active = False
        print("[all agents reset]")

    # ── Background pipeline ──────────────────────────────────────

    def _bg_pipeline(self, plan: str):
        try:
            self._bg_run(plan)
        except Exception as exc:
            self._notify(f"[pipeline error] {exc}")
        finally:
            self._task_active = False

    def _notify(self, msg: str):
        self.hub.send("agent:manager", msg, sender="system")

    def _bg_run(self, plan: str):
        # ── 1. Worker plans ──
        self._notify("worker is planning implementation...")
        worker_plan = self.worker.chat(
            f"Create a detailed implementation plan for this task.\n"
            f"Explore the codebase first, then list exact changes.\n\n"
            f"Manager's plan:\n{plan}"
        )
        self._notify(f"WORKER PLAN:\n{worker_plan}")

        # ── 2. Tester plans ──
        self._notify("tester is planning tests...")
        tester_plan = self.tester.chat(
            f"Create a test plan.\n\n"
            f"Manager's plan:\n{plan}\n\n"
            f"Worker's plan:\n{worker_plan}"
        )
        self._notify(f"TESTER PLAN:\n{tester_plan}")

        # ── 3. Wait for approval ──
        self._notify(
            "Both plans ready.  Please review and call approve_plan or reject_plan."
        )

        approval = self.hub.receive("team:approval", timeout=None)
        if not approval or "reject" in approval.content.lower():
            reason = approval.content if approval else "timeout"
            self._notify(f"Plan rejected ({reason}).  Task cancelled.")
            return

        # ── 4. Worker implements ──
        self._notify("worker is implementing...")
        worker_output = self.worker.chat(
            "Implement your plan now.  Make the actual file changes."
        )
        self._notify(f"WORKER DONE:\n{worker_output}")

        # ── 5. Test <-> Fix loop ──
        for rnd in range(1, self._max_review_rounds + 1):
            self._notify(f"tester validating (round {rnd})...")
            tester_output = self.tester.chat(
                f"Validate the implementation.  Run tests.\n\n"
                f"Worker's latest output:\n{worker_output}"
            )

            if _is_satisfied(tester_output):
                self._notify(
                    f"ALL TESTS PASS (round {rnd}).  Task complete!\n\n{tester_output}"
                )
                return

            self._notify(
                f"Round {rnd} issues:\n{tester_output}\n\nworker is fixing..."
            )
            worker_output = self.worker.chat(
                f"Tester found issues — fix them:\n\n{tester_output}"
            )
            self._notify(f"WORKER FIX (round {rnd}):\n{worker_output}")

        # ── Exhausted rounds ──
        self._notify(
            f"Reached {self._max_review_rounds} review rounds without full pass.  "
            f"Needs your intervention.\n\n"
            f"Last worker output:\n{worker_output}\n\n"
            f"Last tester feedback:\n{tester_output}"
        )


def _is_satisfied(output: str) -> bool:
    low = output.lower()
    for sig in (
        "all tests pass",
        "all pass",
        "everything passes",
        "lgtm",
        "no issues found",
        "no issues",
        "approved",
    ):
        if sig in low:
            return True
    return False


# ── CLI entry ────────────────────────────────────────────────────

def main():
    import argparse

    parser = argparse.ArgumentParser(description="bp-multi team chat")
    parser.add_argument("--provider", "-p", default=None)
    parser.add_argument("--model", "-m", default=None)
    args = parser.parse_args()

    kwargs: dict = {}
    if args.provider:
        kwargs["provider"] = args.provider
    if args.model:
        kwargs["model"] = args.model

    from bp_agent.agent import AgentConfig

    team = TeamChat(AgentConfig(**kwargs))
    team.run_repl()


if __name__ == "__main__":
    main()
