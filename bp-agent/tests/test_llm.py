from llm import LLMClient, Message, RateLimitError


def test_model_validation():
    client = LLMClient(["key1"], model="gemini-3-flash-preview")
    assert client.model == "gemini-3-flash-preview"

    try:
        LLMClient(["key1"], model="invalid-model")
        assert False, "Should have raised"
    except ValueError:
        pass


def test_key_rotation(monkeypatch):
    client = LLMClient(["key1", "key2", "key3"])

    calls = {"count": 0}

    def fake_send_request(_):
        calls["count"] += 1
        if calls["count"] < 2:
            raise RateLimitError()
        return {"candidates": [{"content": {"parts": [{"text": "ok"}]}}]}

    monkeypatch.setattr(client, "_send_request", fake_send_request)

    response = client.complete([Message(role="user", content="hi")])
    assert response.content == "ok"
    assert client.current_key_index == 1


def test_request_building():
    client = LLMClient(["key"])  # no network call here
    messages = [
        Message(role="system", content="Be helpful"),
        Message(role="user", content="Hello"),
    ]
    request = client._build_request(messages, None, temperature=0.7)
    assert "systemInstruction" in request
    assert len(request["contents"]) == 1
    assert request["generationConfig"]["temperature"] == 0.7


def test_response_parsing():
    client = LLMClient(["key"])
    raw = {"candidates": [{"content": {"parts": [{"text": "Hello!"}]}}]}
    response = client._parse_response(raw)
    assert response.content == "Hello!"
    assert response.tool_calls is None
