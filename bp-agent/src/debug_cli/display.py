"""Display helpers for debug CLI."""

from __future__ import annotations

from typing import Iterable

from .models import CLIConfig, ExecuteResult, TaskInfo, ToolInfo

HELP_TEXT = (
    "Commands:\n"
    "  .model       Select model (interactive)\n"
    "  .provider    Select provider (gemini/codex/opus)\n"
    "  .tasks       List recent tasks\n"
    "  .task <id>   Show task detail\n"
    "  .tools       List registered tools\n"
    "  .system <t>  Set system prompt\n"
    "  .clear       Clear chat history\n"
    "  .debug       Toggle debug mode\n"
    "  .info        Show current settings\n"
    "  .help        Show this help\n"
    "  .exit        Quit"
)


def print_welcome(config: CLIConfig) -> None:
    print("base-agent CLI [HTTP]")
    print(f"Provider: {config.provider} | Model: {config.model or 'default'}")
    print("Type .help for commands\n")


def print_help() -> None:
    print(HELP_TEXT)


def print_info(config: CLIConfig) -> None:
    print(format_info(config))


def format_help() -> str:
    return HELP_TEXT


def format_info(config: CLIConfig) -> str:
    return "\n".join(
        [
            f"URL:      {config.base_url}",
            f"Provider: {config.provider}",
            f"Model:    {config.model or '(default)'}",
            f"System:   {config.system_prompt or '(default)'}",
            f"Temp:     {config.temperature}",
            f"Debug:    {config.debug}",
        ]
    )


def print_response(result: ExecuteResult, debug: bool) -> None:
    if debug and result.tool_calls:
        for tc in result.tool_calls:
            name = tc.get("name")
            args = tc.get("args")
            print(f"  [tool] {name}({args})")
    print(f"assistant> {result.output}")


def format_task_table(tasks: Iterable[TaskInfo]) -> str:
    tasks = list(tasks)
    if not tasks:
        return "No tasks."
    lines = [f"{'ID':<16} {'Status':<10} {'Output':<50}", "-" * 76]
    for t in tasks:
        output = t.output or "-"
        if len(output) > 50:
            output = output[:47] + "..."
        lines.append(f"{t.id:<16} {t.status:<10} {output:<50}")
    return "\n".join(lines)


def format_task_detail(task: TaskInfo) -> str:
    return "\n".join(
        [
            f"ID:          {task.id}",
            f"Status:      {task.status}",
            f"Instruction: {task.instruction}",
            f"Output:      {task.output or '-'}",
            f"Created:     {task.created_at}",
        ]
    )


def format_tool_list(tools: Iterable[ToolInfo]) -> str:
    tools = list(tools)
    if not tools:
        return "No tools registered."
    lines = ["Registered tools:"]
    for t in tools:
        desc = t.description or "(no description)"
        lines.append(f"  - {t.name}: {desc}")
    return "\n".join(lines)
