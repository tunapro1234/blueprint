"""Workflow: multi-agent orchestration using tunnels."""

from __future__ import annotations

import concurrent.futures
from typing import TYPE_CHECKING

from .tunnel import TunnelHub

if TYPE_CHECKING:
    from bp_agent.agent import Agent, AgentResult


class Workflow:
    """Multi-agent workflow orchestrator.

    Usage::

        hub = TunnelHub()
        wf = Workflow(hub)
        wf.add("manager", manager_agent)
        wf.add("worker", worker_agent)
        wf.add("tester", tester_agent)
        results = wf.run_pipeline(["manager", "worker", "tester"], instruction)
    """

    def __init__(self, hub: TunnelHub | None = None):
        self.hub = hub or TunnelHub()
        self._agents: dict[str, "Agent"] = {}

    def add(self, role: str, agent: "Agent"):
        """Register an agent with a role and connect it to the hub."""
        self._agents[role] = agent
        # Ensure agent has tunnel tools via the hub
        if agent._hub is not self.hub:
            agent.connect_hub(self.hub, alias=role)

    # --- Execution patterns ---

    def run_pipeline(
        self, roles: list[str], instruction: str
    ) -> dict[str, "AgentResult"]:
        """Run agents sequentially — each receives previous agent's output via tunnel."""
        results: dict[str, "AgentResult"] = {}

        for i, role in enumerate(roles):
            agent = self._agents[role]
            if i == 0:
                prompt = instruction
            else:
                prev_role = roles[i - 1]
                prev_output = results[prev_role].output
                prompt = (
                    f"You are the '{role}' in a pipeline. "
                    f"Previous stage ('{prev_role}') produced:\n\n{prev_output}\n\n"
                    f"Original instruction: {instruction}"
                )

            results[role] = agent.execute(prompt)

        return results

    def run_parallel(
        self, roles: list[str], instruction: str
    ) -> dict[str, "AgentResult"]:
        """Run agents in parallel with the same instruction."""
        results: dict[str, "AgentResult"] = {}

        with concurrent.futures.ThreadPoolExecutor(max_workers=len(roles)) as pool:
            futures = {
                pool.submit(self._agents[role].execute, instruction): role
                for role in roles
            }
            for future in concurrent.futures.as_completed(futures):
                role = futures[future]
                try:
                    results[role] = future.result()
                except Exception as e:
                    from bp_agent.agent import AgentResult

                    results[role] = AgentResult(success=False, output=str(e))

        return results

    def run_manager_workers(
        self,
        instruction: str,
        worker_roles: list[str] | None = None,
    ) -> dict[str, "AgentResult"]:
        """Manager -> Workers (parallel) -> Tester (optional).

        Expects at least a 'manager' agent. Worker roles default to all
        agents whose role starts with 'worker'. If 'tester' exists it
        validates at the end.
        """
        if "manager" not in self._agents:
            raise ValueError("No 'manager' agent registered")

        # 1. Manager
        manager_result = self._agents["manager"].execute(instruction)
        results: dict[str, "AgentResult"] = {"manager": manager_result}

        # 2. Workers
        workers = worker_roles or [
            r for r in self._agents if r.startswith("worker")
        ]
        if workers:
            for w in workers:
                self.hub.send(
                    f"agent:{w}", manager_result.output, sender="manager"
                )

            worker_instruction = (
                "Check your messages for the task from manager. Execute it."
            )
            worker_results = self.run_parallel(workers, worker_instruction)
            results.update(worker_results)

        # 3. Tester
        if "tester" in self._agents:
            summary = "\n---\n".join(
                f"[{role}]: {r.output}" for role, r in results.items()
            )
            tester_instruction = (
                f"Review and validate all results:\n\n{summary}\n\n"
                f"Original instruction: {instruction}"
            )
            results["tester"] = self._agents["tester"].execute(
                tester_instruction
            )

        return results
