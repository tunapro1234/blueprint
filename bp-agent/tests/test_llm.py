from llm import LLMRouter, CompletionRequest, Message
from llm.rotation import RotationManager, RotationPolicy, RotationSlot
from llm.gemini_adapter import GeminiAdapter, GeminiConfig


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
