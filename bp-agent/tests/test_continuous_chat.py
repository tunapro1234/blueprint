"""Tests for continuous chat mode (run_chat / push_message / send_message tool)."""

import threading
import time
import json

import pytest

import bp_agent.agent as agent_mod
from bp_agent.agent import Agent, AgentConfig, MessageInbox
from bp_agent.llm import LLMResponse, ToolCall
from bp_agent.llm.types import StreamChunk, ToolCallDelta


class SlowRouter:
    """Router that streams one char per 10ms."""

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
                for ch in resp.content:
                    time.sleep(0.01)
                    yield StreamChunk(delta=ch)
            if resp.tool_calls:
                for i, tc in enumerate(resp.tool_calls):
                    yield StreamChunk(tool_call_delta=ToolCallDelta(index=i, name=tc.name))
                    yield StreamChunk(tool_call_delta=ToolCallDelta(index=i, args_delta=json.dumps(tc.args)))
            yield StreamChunk(finish_reason="stop")
        else:
            yield StreamChunk(delta="", finish_reason="stop")


@pytest.fixture(autouse=True)
def _patch_router(monkeypatch):
    monkeypatch.setattr(agent_mod, "_build_llm_router", lambda config: SlowRouter())


def _make_agent(router, **kwargs):
    a = Agent("test", config=AgentConfig(enable_task_store=False, **kwargs))
    a.llm = router
    return a


def _send_msg_response(text: str) -> LLMResponse:
    """Helper: LLM response that calls send_message tool."""
    return LLMResponse(
        content="ok",  # short thinking (fast stream)
        tool_calls=[ToolCall(name="send_message", args={"message": text})],
    )


# ── MessageInbox ──

def test_inbox_push_pull():
    inbox = MessageInbox()
    inbox.push("hello")
    inbox.push("world")
    msgs = inbox.pull_all()
    assert msgs == ["hello", "world"]
    assert inbox.pull_all() == []


def test_inbox_wait():
    inbox = MessageInbox()

    def push_later():
        time.sleep(0.05)
        inbox.push("delayed")

    t = threading.Thread(target=push_later)
    t.start()
    assert inbox.wait(timeout=1) is True
    assert inbox.pull_all() == ["delayed"]
    t.join()


def test_inbox_wait_timeout():
    inbox = MessageInbox()
    assert inbox.wait(timeout=0.05) is False


def test_inbox_has_messages():
    inbox = MessageInbox()
    assert inbox.has_messages is False
    inbox.push("x")
    assert inbox.has_messages is True
    inbox.pull_all()
    assert inbox.has_messages is False


# ── run_chat basic flow (send_message model) ──

def test_run_chat_sends_via_tool():
    """Agent sends message to user via send_message tool, not stream."""
    router = SlowRouter()
    router.responses = [_send_msg_response("hello!")]
    ag = _make_agent(router)

    collected = []

    def consume():
        for msg in ag.run_chat():
            collected.append(msg)

    t = threading.Thread(target=consume)
    t.start()
    time.sleep(0.02)
    ag.push_message("hi")
    time.sleep(0.5)
    ag.stop_chat()
    t.join(timeout=2)

    assert collected == ["hello!"]


def test_run_chat_auto_send_fallback():
    """If model doesn't call send_message on user message, text is auto-sent."""
    router = SlowRouter()
    router.responses = [
        LLMResponse(content="hmm let me think about this...", tool_calls=None),
    ]
    ag = _make_agent(router)

    collected = []

    def consume():
        for msg in ag.run_chat():
            collected.append(msg)

    t = threading.Thread(target=consume)
    t.start()
    time.sleep(0.02)
    ag.push_message("hi")
    time.sleep(0.5)
    ag.stop_chat()
    t.join(timeout=2)

    # Auto-sent because model didn't call send_message
    assert collected == ["hmm let me think about this..."]


def test_run_chat_multiple_messages_before_response():
    """Multiple messages pushed before agent starts responding — all seen at once."""
    router = SlowRouter()
    router.responses = [_send_msg_response("got both")]
    ag = _make_agent(router)

    collected = []

    def consume():
        for msg in ag.run_chat():
            collected.append(msg)

    t = threading.Thread(target=consume)
    t.start()
    time.sleep(0.02)
    ag.push_message("first")
    ag.push_message("second")
    time.sleep(0.5)
    ag.stop_chat()
    t.join(timeout=2)

    user_msgs = [m for m in ag.chat_history if m.role == "user" and not m.content.startswith("[")]
    assert len(user_msgs) == 2
    assert user_msgs[0].content == "first"
    assert user_msgs[1].content == "second"


def test_run_chat_sequential_conversations():
    """Agent responds, waits, then responds to next message."""
    router = SlowRouter()
    router.responses = [
        _send_msg_response("first reply"),
        _send_msg_response("second reply"),
    ]
    ag = _make_agent(router)

    collected = []

    def consume():
        for msg in ag.run_chat():
            collected.append(msg)

    t = threading.Thread(target=consume)
    t.start()
    time.sleep(0.02)
    ag.push_message("hello")
    time.sleep(0.3)
    ag.push_message("another question")
    time.sleep(0.3)
    ag.stop_chat()
    t.join(timeout=2)

    assert "first reply" in collected
    assert "second reply" in collected


def test_run_chat_message_during_stream_cancels():
    """New message during streaming cancels and re-generates."""
    router = SlowRouter()
    router.responses = [
        LLMResponse(content="A" * 100, tool_calls=None),  # slow, will be interrupted
        _send_msg_response("combined answer"),
    ]
    ag = _make_agent(router)

    collected = []

    def consume():
        for msg in ag.run_chat():
            collected.append(msg)

    t = threading.Thread(target=consume)
    t.start()
    time.sleep(0.02)
    ag.push_message("first question")
    time.sleep(0.05)
    ag.push_message("actually, different question")
    time.sleep(0.5)
    ag.stop_chat()
    t.join(timeout=2)

    assert "combined answer" in collected
    # Interrupted thinking should be in history
    assistant_msgs = [m for m in ag.chat_history if m.role == "assistant"]
    interrupted = [m for m in assistant_msgs if "[interrupted]" in m.content]
    assert len(interrupted) >= 1


# ── send_message + check_messages tools ──

def test_send_message_tool_registered():
    router = SlowRouter()
    router.responses = [_send_msg_response("ok")]
    ag = _make_agent(router)

    def consume():
        for _ in ag.run_chat():
            pass

    t = threading.Thread(target=consume)
    t.start()
    time.sleep(0.02)
    ag.push_message("start")
    time.sleep(0.2)
    ag.stop_chat()
    t.join(timeout=2)

    assert ag.tools.has("send_message")
    assert ag.tools.has("check_messages")


def test_check_messages_tool_pulls_from_inbox():
    router = SlowRouter()
    ag = _make_agent(router)

    ag._register_continuous_tools()

    ag._inbox.push("msg1")
    ag._inbox.push("msg2")
    result = ag.tools.execute("check_messages", {})
    assert result.success
    assert "msg1" in result.output
    assert "msg2" in result.output

    result2 = ag.tools.execute("check_messages", {})
    assert "no new messages" in result2.output


def test_send_message_tool_pushes_to_outbox():
    router = SlowRouter()
    ag = _make_agent(router)

    ag._register_continuous_tools()

    result = ag.tools.execute("send_message", {"message": "hello user"})
    assert result.success
    assert "hello user" in result.output
    assert "delivered" in result.output.lower()
    assert ag._outbox == ["hello user"]


def test_check_messages_during_tool_execution():
    """Agent uses check_messages tool mid-turn, sees new user message."""
    router = SlowRouter()
    router.responses = [
        LLMResponse(content="", tool_calls=[ToolCall(name="check_messages", args={})]),
        _send_msg_response("I see both messages"),
    ]
    ag = _make_agent(router)

    collected = []

    def consume():
        for msg in ag.run_chat():
            collected.append(msg)

    t = threading.Thread(target=consume)
    t.start()
    time.sleep(0.02)
    ag.push_message("first")
    time.sleep(0.05)
    ag._inbox.push("second via check")
    time.sleep(0.5)
    ag.stop_chat()
    t.join(timeout=2)

    assert "I see both messages" in collected


# ── stop_chat ──

def test_stop_chat():
    router = SlowRouter()
    ag = _make_agent(router)

    stopped = threading.Event()

    def consume():
        for _ in ag.run_chat():
            pass
        stopped.set()

    t = threading.Thread(target=consume)
    t.start()
    time.sleep(0.05)
    ag.stop_chat()
    stopped.wait(timeout=2)
    t.join(timeout=2)

    assert stopped.is_set()
    assert ag._chat_running is False


def test_stop_chat_during_stream():
    router = SlowRouter()
    router.responses = [LLMResponse(content="A" * 200, tool_calls=None)]
    ag = _make_agent(router)

    collected = []

    def consume():
        for msg in ag.run_chat():
            collected.append(msg)

    t = threading.Thread(target=consume)
    t.start()
    time.sleep(0.02)
    ag.push_message("start")
    time.sleep(0.05)
    ag.stop_chat()
    t.join(timeout=2)

    # Nothing yielded (stream is internal, no send_message was called)
    assert collected == []
    assert ag._chat_running is False


# ── push_message cancels stream ──

def test_push_message_cancels_active_stream():
    router = SlowRouter()
    router.responses = [
        LLMResponse(content="X" * 100, tool_calls=None),
        _send_msg_response("fresh"),
    ]
    ag = _make_agent(router)

    collected = []

    def consume():
        for msg in ag.run_chat():
            collected.append(msg)

    t = threading.Thread(target=consume)
    t.start()
    time.sleep(0.02)
    ag.push_message("first")
    time.sleep(0.05)
    ag.push_message("interrupt!")
    time.sleep(0.5)
    ag.stop_chat()
    t.join(timeout=2)

    assert "fresh" in collected


# ── Session persistence in continuous mode ──

def test_run_chat_persists_session(tmp_path):
    router = SlowRouter()
    router.responses = [_send_msg_response("saved")]
    ag = _make_agent(router, store_dir=str(tmp_path))

    def consume():
        for _ in ag.run_chat():
            pass

    t = threading.Thread(target=consume)
    t.start()
    time.sleep(0.02)
    ag.push_message("persist this")
    time.sleep(0.3)
    ag.stop_chat()
    t.join(timeout=2)

    sessions = ag.store.list_sessions()
    assert len(sessions) == 1


def test_interrupted_turn_not_persisted(tmp_path):
    router = SlowRouter()
    router.responses = [
        LLMResponse(content="A" * 100, tool_calls=None),
        _send_msg_response("final"),
    ]
    ag = _make_agent(router, store_dir=str(tmp_path))

    def consume():
        for _ in ag.run_chat():
            pass

    t = threading.Thread(target=consume)
    t.start()
    time.sleep(0.02)
    ag.push_message("first")
    time.sleep(0.05)
    ag.push_message("interrupt")
    time.sleep(0.5)
    ag.stop_chat()
    t.join(timeout=2)

    sessions = ag.store.list_sessions()
    assert len(sessions) == 1
    msgs = ag.store.load_session(sessions[0]["id"])
    # The persisted session should have the send_message tool result
    all_contents = [m["content"] for m in msgs]
    assert any("send_message" in c or "message sent" in c.lower() for c in all_contents)
