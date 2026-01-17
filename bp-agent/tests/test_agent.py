import types

import agent
from agent import Agent, AgentConfig, AgentResult
from llm import LLMResponse
from tools import ToolSchema


class DummyLLM:
    def __init__(self, api_keys=None, model=None):
        self.api_keys = api_keys or []
        self.model = model
        self.calls = []
        self.responses = []

    def complete(self, messages, tools=None, temperature=None):
        self.calls.append({"messages": messages, "tools": tools, "temperature": temperature})
        if self.responses:
            return self.responses.pop(0)
        return LLMResponse(content="", tool_calls=None)


def test_agent_creation(monkeypatch):
    monkeypatch.setattr(agent, "load_api_keys", lambda: ["key"])
    monkeypatch.setattr(agent, "LLMClient", DummyLLM)

    inst = Agent("test-agent")
    assert inst.name == "test-agent"
    assert inst.config.model == "gemini-3-flash-preview"


def test_execute_simple(monkeypatch):
    monkeypatch.setattr(agent, "load_api_keys", lambda: ["key"])

    dummy = DummyLLM(["key"], "gemini-3-flash-preview")
    dummy.responses = [LLMResponse(content="Hello!", tool_calls=None)]
    monkeypatch.setattr(agent, "LLMClient", lambda api_keys, model: dummy)

    inst = Agent("test", system_prompt="Say hello")
    result = inst.execute("Hi")

    assert isinstance(result, AgentResult)
    assert result.success is True
    assert result.output == "Hello!"


def test_execute_with_tools(monkeypatch):
    monkeypatch.setattr(agent, "load_api_keys", lambda: ["key"])

    dummy = DummyLLM(["key"], "gemini-3-flash-preview")
    dummy.responses = [
        LLMResponse(content="Calling tool", tool_calls=[types.SimpleNamespace(name="add", args={"a": 2, "b": 3})]),
        LLMResponse(content="Final is 5", tool_calls=None),
    ]
    monkeypatch.setattr(agent, "LLMClient", lambda api_keys, model: dummy)

    inst = Agent("test")
    inst.add_tool(
        "add",
        lambda a, b: a + b,
        ToolSchema(
            name="add",
            description="Add",
            parameters={
                "type": "object",
                "properties": {
                    "a": {"type": "integer"},
                    "b": {"type": "integer"},
                },
                "required": ["a", "b"],
            },
        ),
    )

    result = inst.execute("What is 2 + 3?")
    assert result.success is True
    assert "5" in result.output
