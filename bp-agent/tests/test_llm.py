import json

from bp_agent.llm import LLMRouter, CompletionRequest, Message, LLMResponse
from bp_agent.llm.rotation import RotationManager, RotationPolicy, RotationSlot
from bp_agent.llm.gemini_adapter import GeminiAdapter, GeminiConfig
from bp_agent.llm.opus_adapter import OpusAdapter, OpusConfig
from bp_agent.llm.types import StreamChunk, ToolCallDelta, accumulate_stream


def test_router_requires_provider():
    router = LLMRouter()
    try:
        router.complete(CompletionRequest(messages=[]))
        assert False, "Expected ValueError"
    except ValueError:
        pass


def test_rotation_select_round_robin():
    policy = RotationPolicy(cooldown_seconds=0)
    mgr = RotationManager(policy=policy)
    mgr.add_slot(RotationSlot(id="a"))
    mgr.add_slot(RotationSlot(id="b"))

    slot1 = mgr.select_slot()
    slot2 = mgr.select_slot()
    assert slot1.id in ["a", "b"]
    assert slot2.id in ["a", "b"]


def test_gemini_adapter_response_parsing():
    adapter = GeminiAdapter(GeminiConfig(api_keys=["k1"]))

    def fake_send_request(payload, model, api_key):
        assert api_key == "k1"
        return {
            "candidates": [
                {"content": {"parts": [{"text": "Hello!"}]}}
            ]
        }

    adapter._send_request = fake_send_request  # type: ignore[attr-defined]

    request = CompletionRequest(messages=[Message(role="user", content="Hi")])
    response = adapter.complete(request)
    assert response.content == "Hello!"


# --- Opus adapter tests ---

def _make_opus_adapter():
    return OpusAdapter(OpusConfig(api_keys=["k1"], base_url="http://localhost", endpoint="/responses"))


def test_opus_text_only_response():
    adapter = _make_opus_adapter()

    def fake_send(payload, api_key):
        return {"output_text": "Hello from Opus!"}

    adapter._send_request = fake_send

    request = CompletionRequest(messages=[Message(role="user", content="Hi")])
    response = adapter.complete(request)
    assert response.content == "Hello from Opus!"
    assert response.tool_calls is None


def test_opus_tool_call_response():
    adapter = _make_opus_adapter()

    def fake_send(payload, api_key):
        return {
            "output": [
                {
                    "content": [
                        {"type": "function_call", "name": "bash", "arguments": {"command": "ls"}}
                    ]
                }
            ]
        }

    adapter._send_request = fake_send

    request = CompletionRequest(messages=[Message(role="user", content="list files")])
    response = adapter.complete(request)
    assert response.tool_calls is not None
    assert len(response.tool_calls) == 1
    assert response.tool_calls[0].name == "bash"
    assert response.tool_calls[0].args == {"command": "ls"}


def test_opus_mixed_text_and_tool_call():
    adapter = _make_opus_adapter()

    def fake_send(payload, api_key):
        return {
            "output": [
                {
                    "content": [
                        {"type": "text", "text": "Let me check."},
                        {"type": "function_call", "name": "read_file", "arguments": {"path": "a.txt"}},
                    ]
                }
            ]
        }

    adapter._send_request = fake_send

    request = CompletionRequest(messages=[Message(role="user", content="read a.txt")])
    response = adapter.complete(request)
    assert response.content == "Let me check."
    assert response.tool_calls is not None
    assert response.tool_calls[0].name == "read_file"


def test_opus_arguments_as_json_string():
    adapter = _make_opus_adapter()

    def fake_send(payload, api_key):
        return {
            "output": [
                {
                    "content": [
                        {"type": "function_call", "name": "bash", "arguments": json.dumps({"command": "pwd"})}
                    ]
                }
            ]
        }

    adapter._send_request = fake_send

    request = CompletionRequest(messages=[Message(role="user", content="pwd")])
    response = adapter.complete(request)
    assert response.tool_calls is not None
    assert response.tool_calls[0].args == {"command": "pwd"}


def test_opus_fallback_text_field():
    adapter = _make_opus_adapter()

    def fake_send(payload, api_key):
        return {"text": "fallback text"}

    adapter._send_request = fake_send

    request = CompletionRequest(messages=[Message(role="user", content="Hi")])
    response = adapter.complete(request)
    assert response.content == "fallback text"


# --- Streaming tests ---

def test_accumulate_stream_text_only():
    chunks = [
        StreamChunk(delta="Hello"),
        StreamChunk(delta=" world"),
        StreamChunk(finish_reason="stop"),
    ]
    result = accumulate_stream(iter(chunks))
    assert result.content == "Hello world"
    assert result.tool_calls is None


def test_accumulate_stream_with_tool_calls():
    chunks = [
        StreamChunk(delta="Let me check."),
        StreamChunk(tool_call_delta=ToolCallDelta(index=0, name="bash")),
        StreamChunk(tool_call_delta=ToolCallDelta(index=0, args_delta='{"comma')),
        StreamChunk(tool_call_delta=ToolCallDelta(index=0, args_delta='nd": "ls"}')),
        StreamChunk(finish_reason="stop"),
    ]
    result = accumulate_stream(iter(chunks))
    assert result.content == "Let me check."
    assert result.tool_calls is not None
    assert len(result.tool_calls) == 1
    assert result.tool_calls[0].name == "bash"
    assert result.tool_calls[0].args == {"command": "ls"}


def test_accumulate_stream_multiple_tool_calls():
    chunks = [
        StreamChunk(tool_call_delta=ToolCallDelta(index=0, name="read_file")),
        StreamChunk(tool_call_delta=ToolCallDelta(index=0, args_delta='{"path": "a.txt"}')),
        StreamChunk(tool_call_delta=ToolCallDelta(index=1, name="read_file")),
        StreamChunk(tool_call_delta=ToolCallDelta(index=1, args_delta='{"path": "b.txt"}')),
        StreamChunk(finish_reason="stop"),
    ]
    result = accumulate_stream(iter(chunks))
    assert result.tool_calls is not None
    assert len(result.tool_calls) == 2
    assert result.tool_calls[0].args == {"path": "a.txt"}
    assert result.tool_calls[1].args == {"path": "b.txt"}


def test_accumulate_stream_empty():
    result = accumulate_stream(iter([]))
    assert result.content == ""
    assert result.tool_calls is None


def test_router_complete_stream_fallback():
    """Router should wrap non-streaming adapter in a single-chunk stream."""
    class SimpleAdapter:
        def complete(self, request):
            return LLMResponse(content="fallback response")

    router = LLMRouter(default_provider="test")
    router.register_provider("test", SimpleAdapter())

    request = CompletionRequest(messages=[Message(role="user", content="Hi")])
    chunks = list(router.complete_stream(request))
    assert len(chunks) == 1
    assert chunks[0].delta == "fallback response"
    assert chunks[0].finish_reason == "stop"
