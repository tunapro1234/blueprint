import json
from unittest.mock import patch, MagicMock

from bp_agent.llm import LLMRouter, CompletionRequest, Message, LLMResponse
from bp_agent.llm.rotation import RotationManager, RotationPolicy, RotationSlot
from bp_agent.llm.gemini_adapter import GeminiAdapter, GeminiConfig
from bp_agent.llm.codex_adapter import CodexAdapter, CodexConfig
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


# --- Opus streaming tests ---

def _make_mock_sse_response(lines, status_code=200):
    """Build a mock requests.Response for SSE streaming."""
    mock_resp = MagicMock()
    mock_resp.status_code = status_code
    mock_resp.iter_lines = MagicMock(return_value=iter(lines))
    return mock_resp


@patch("bp_agent.llm.opus_adapter.http_requests.post")
def test_opus_complete_stream_text(mock_post):
    adapter = _make_opus_adapter()
    sse_lines = [
        'data: {"type":"response.output_text.delta","delta":"Hello"}',
        'data: {"type":"response.output_text.delta","delta":" world"}',
        'data: {"type":"response.completed"}',
    ]
    mock_post.return_value = _make_mock_sse_response(sse_lines)

    request = CompletionRequest(messages=[Message(role="user", content="Hi")])
    chunks = list(adapter.complete_stream(request))

    assert len(chunks) == 3
    assert chunks[0].delta == "Hello"
    assert chunks[1].delta == " world"
    assert chunks[2].finish_reason == "stop"


@patch("bp_agent.llm.opus_adapter.http_requests.post")
def test_opus_complete_stream_tool_calls(mock_post):
    adapter = _make_opus_adapter()
    sse_lines = [
        'data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","name":"bash"}}',
        'data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\\"cmd\\": \\"ls\\"}"}',
        'data: {"type":"response.completed"}',
    ]
    mock_post.return_value = _make_mock_sse_response(sse_lines)

    request = CompletionRequest(messages=[Message(role="user", content="list files")])
    chunks = list(adapter.complete_stream(request))

    assert len(chunks) == 3
    assert chunks[0].tool_call_delta is not None
    assert chunks[0].tool_call_delta.name == "bash"
    assert chunks[1].tool_call_delta is not None
    assert chunks[1].tool_call_delta.args_delta == '{"cmd": "ls"}'
    assert chunks[2].finish_reason == "stop"


# --- Structured output payload tests ---

def test_opus_structured_output_payload():
    adapter = _make_opus_adapter()
    schema = {"type": "object", "properties": {"answer": {"type": "string"}}}
    request = CompletionRequest(
        messages=[Message(role="user", content="Hi")],
        response_schema=schema,
        response_schema_name="my_schema",
    )
    payload = adapter._build_payload(request)
    assert "text" in payload
    fmt = payload["text"]["format"]
    assert fmt["type"] == "json_schema"
    assert fmt["json_schema"]["name"] == "my_schema"
    assert fmt["json_schema"]["schema"] == schema
    assert fmt["json_schema"]["strict"] is True


def test_codex_structured_output_payload():
    adapter = CodexAdapter(CodexConfig(api_keys=["k1"]))
    schema = {"type": "object", "properties": {"answer": {"type": "string"}}}
    request = CompletionRequest(
        messages=[Message(role="user", content="Hi")],
        response_schema=schema,
    )
    payload = adapter._build_payload(request, "gpt-5.2-codex")
    assert "text" in payload
    fmt = payload["text"]["format"]
    assert fmt["type"] == "json_schema"
    assert fmt["json_schema"]["name"] == "response"  # default name
    assert fmt["json_schema"]["schema"] == schema
    assert fmt["json_schema"]["strict"] is True


def test_gemini_structured_output_payload():
    adapter = GeminiAdapter(GeminiConfig(api_keys=["k1"]))
    schema = {"type": "object", "properties": {"answer": {"type": "string"}}}
    request = CompletionRequest(
        messages=[Message(role="user", content="Hi")],
        response_schema=schema,
    )
    payload = adapter._build_request(request, 0.3)
    assert payload["generationConfig"]["responseMimeType"] == "application/json"
    assert payload["generationConfig"]["responseSchema"] == schema


# --- Parsed JSON response tests ---

def test_opus_parsed_json_response():
    adapter = _make_opus_adapter()

    def fake_send(payload, api_key):
        return {"output_text": '{"answer": "42"}'}

    adapter._send_request = fake_send

    request = CompletionRequest(messages=[Message(role="user", content="Hi")])
    response = adapter.complete(request)
    assert response.parsed == {"answer": "42"}


def test_opus_parsed_non_json_response():
    adapter = _make_opus_adapter()

    def fake_send(payload, api_key):
        return {"output_text": "Just plain text"}

    adapter._send_request = fake_send

    request = CompletionRequest(messages=[Message(role="user", content="Hi")])
    response = adapter.complete(request)
    assert response.parsed is None


def test_gemini_parsed_json_response():
    adapter = GeminiAdapter(GeminiConfig(api_keys=["k1"]))

    def fake_send(payload, model, api_key):
        return {
            "candidates": [
                {"content": {"parts": [{"text": '{"result": true}'}]}}
            ]
        }

    adapter._send_request = fake_send

    request = CompletionRequest(messages=[Message(role="user", content="Hi")])
    response = adapter.complete(request)
    assert response.parsed == {"result": True}
