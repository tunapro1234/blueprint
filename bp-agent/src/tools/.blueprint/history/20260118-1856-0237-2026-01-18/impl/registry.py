"""Tool registry and helpers."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Callable, Optional


@dataclass
class ToolSchema:
    name: str
    description: str
    parameters: Optional[dict] = None

    def to_dict(self) -> dict:
        return {
            "name": self.name,
            "description": self.description,
            "parameters": self.parameters or {"type": "object", "properties": {}},
        }


@dataclass
class ToolResult:
    success: bool
    output: Any
    error: Optional[str] = None


@dataclass
class ToolEntry:
    name: str
    handler: Callable
    schema: ToolSchema


class ToolRegistry:
    def __init__(self):
        self._tools: dict[str, ToolEntry] = {}

    def register(self, name: str, handler: Callable, schema: ToolSchema):
        if name in self._tools:
            raise ValueError(f"Tool {name} already registered")

        if not schema.name:
            schema.name = name
        elif schema.name != name:
            raise ValueError(f"Tool schema name mismatch: {schema.name} != {name}")

        self._tools[name] = ToolEntry(name=name, handler=handler, schema=schema)

    def execute(self, name: str, args: dict) -> ToolResult:
        if name not in self._tools:
            return ToolResult(success=False, output=None, error=f"Tool {name} not found")

        tool = self._tools[name]

        try:
            output = tool.handler(**args)
            return ToolResult(success=True, output=output, error=None)
        except Exception as exc:  # pragma: no cover - generic safeguard
            return ToolResult(success=False, output=None, error=str(exc))

    def get_schemas(self) -> list[ToolSchema]:
        return [entry.schema for entry in self._tools.values()]

    def has(self, name: str) -> bool:
        return name in self._tools

    def count(self) -> int:
        return len(self._tools)

    def list_names(self) -> list[str]:
        return list(self._tools.keys())


def build_schema(name: str, description: str, **params: dict) -> ToolSchema:
    """Helper to build tool schema."""
    properties: dict[str, dict] = {}
    required: list[str] = []

    for param_name, param_def in params.items():
        properties[param_name] = {
            "type": param_def.get("type", "string"),
            "description": param_def.get("description", ""),
        }
        if param_def.get("required", False):
            required.append(param_name)

    return ToolSchema(
        name=name,
        description=description,
        parameters={
            "type": "object",
            "properties": properties,
            "required": required,
        },
    )
