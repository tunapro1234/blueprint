import types

import bp_agent.agent as agent
from bp_agent.agent import Agent, AgentConfig, AgentResult
from bp_agent.llm import LLMResponse, ToolCall
from bp_agent.llm.types import StreamChunk
from bp_agent.tools import ToolSchema


class DummyRouter:
    def __init__(self):
        self.responses = []
        self.calls = []

    def complete(self, request):
        self.calls.append(request)
        if self.responses:
            return self.responses.pop(0)
        return LLMResponse(content="", tool_calls=None)

    def complete_stream(self, request):
        self.calls.append(request)
        if self.responses:
            resp = self.responses.pop(0)
            if resp.content:
                yield StreamChunk(delta=resp.content)
            if resp.tool_calls:
                from bp_agent.llm.types import ToolCallDelta
                import json
                for i, tc in enumerate(resp.tool_calls):
                    yield StreamChunk(tool_call_delta=ToolCallDelta(index=i, name=tc.name))
                    yield StreamChunk(tool_call_delta=ToolCallDelta(index=i, args_delta=json.dumps(tc.args)))
            yield StreamChunk(finish_reason="stop")
        else:
            yield StreamChunk(delta="", finish_reason="stop")


def test_agent_creation(monkeypatch):
    monkeypatch.setattr(agent, "_build_llm_router", lambda config: DummyRouter())
    inst = Agent("test-agent")
    assert inst.name == "test-agent"
    assert inst.config.model == "gemini-3-flash-preview"
    assert inst.config.provider == "gemini"


def test_execute_simple(monkeypatch):
    router = DummyRouter()
    router.responses = [LLMResponse(content="Hello!", tool_calls=None)]
    monkeypatch.setattr(agent, "_build_llm_router", lambda config: router)

    inst = Agent("test", system_prompt="Say hello")
    result = inst.execute("Hi")

    assert isinstance(result, AgentResult)
    assert result.success is True
    assert result.output == "Hello!"


def test_execute_with_tools(monkeypatch):
    router = DummyRouter()
    router.responses = [
        LLMResponse(content="Calling tool", tool_calls=[ToolCall(name="add", args={"a": 2, "b": 3})]),
        LLMResponse(content="Final is 5", tool_calls=None),
    ]
    monkeypatch.setattr(agent, "_build_llm_router", lambda config: router)

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


def test_chat_stream_text_only(monkeypatch):
    router = DummyRouter()
    router.responses = [LLMResponse(content="Streamed hello!", tool_calls=None)]
    monkeypatch.setattr(agent, "_build_llm_router", lambda config: router)

    inst = Agent("test", system_prompt="Say hello")
    chunks = list(inst.chat_stream("Hi"))

    assert "".join(chunks) == "Streamed hello!"


def test_chat_stream_with_tools(monkeypatch):
    router = DummyRouter()
    router.responses = [
        LLMResponse(content="Calling tool", tool_calls=[ToolCall(name="add", args={"a": 2, "b": 3})]),
        LLMResponse(content="Result is 5", tool_calls=None),
    ]
    monkeypatch.setattr(agent, "_build_llm_router", lambda config: router)

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

    chunks = list(inst.chat_stream("What is 2 + 3?"))
    full_output = "".join(chunks)
    assert "Calling tool" in full_output
    assert "Result is 5" in full_output
