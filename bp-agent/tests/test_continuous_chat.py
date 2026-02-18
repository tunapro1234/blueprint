"""Tests for chat_stream cancellation and push_message integration."""

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


# ── chat_stream basic ──

def test_chat_stream_basic():
    """chat_stream yields text deltas."""
    router = SlowRouter()
    router.responses = [LLMResponse(content="hello!", tool_calls=None)]
    ag = _make_agent(router)

    result = "".join(ag.chat_stream("hi"))
    assert result == "hello!"


def test_chat_stream_no_message_no_inbox():
    """chat_stream with no message and empty inbox does nothing."""
    router = SlowRouter()
    ag = _make_agent(router)

    result = list(ag.chat_stream())
    assert result == []
    # No LLM call should have been made
    assert len(router.calls) == 0


def test_chat_stream_message_optional():
    """chat_stream(message=None) pulls from inbox."""
    router = SlowRouter()
    router.responses = [LLMResponse(content="from inbox!", tool_calls=None)]
    ag = _make_agent(router)

    ag._inbox.push("queued message")
    result = "".join(ag.chat_stream())
    assert result == "from inbox!"

    # Check that queued message is in history
    user_msgs = [m for m in ag.chat_history if m.role == "user"]
    assert any("queued message" in m.content for m in user_msgs)


# ── push_message + chat_stream ──

def test_push_message_cancels_stream():
    """push_message cancels active chat_stream."""
    router = SlowRouter()
    router.responses = [LLMResponse(content="A" * 100, tool_calls=None)]
    ag = _make_agent(router)

    collected = []
    done = threading.Event()

    def consume():
        for delta in ag.chat_stream("hello"):
            collected.append(delta)
        done.set()

    t = threading.Thread(target=consume)
    t.start()
    time.sleep(0.05)  # let stream start
    ag.push_message("interrupt!")
    done.wait(timeout=2)
    t.join(timeout=2)

    # Stream was interrupted — got partial response
    full = "".join(collected)
    assert len(full) < 100  # didn't get all 100 chars
    # Interrupted message in history
    assistant_msgs = [m for m in ag.chat_history if m.role == "assistant"]
    assert any("[interrupted]" in m.content for m in assistant_msgs)


def test_push_message_then_chat_stream_consumes_inbox():
    """After push_message interrupts, next chat_stream() pulls inbox."""
    router = SlowRouter()
    router.responses = [
        LLMResponse(content="A" * 100, tool_calls=None),  # will be interrupted
        LLMResponse(content="response to interrupt", tool_calls=None),
    ]
    ag = _make_agent(router)

    # First call — gets interrupted
    done = threading.Event()

    def consume_first():
        for _ in ag.chat_stream("hello"):
            pass
        done.set()

    t = threading.Thread(target=consume_first)
    t.start()
    time.sleep(0.05)
    ag.push_message("new question")
    done.wait(timeout=2)
    t.join(timeout=2)

    # Second call — no explicit message, pulls from inbox
    result = "".join(ag.chat_stream())
    assert result == "response to interrupt"

    # "new question" should be in history
    user_msgs = [m for m in ag.chat_history if m.role == "user"]
    assert any("new question" in m.content for m in user_msgs)


def test_push_message_multiple_queued():
    """Multiple push_message calls queue up, all consumed by next chat_stream."""
    router = SlowRouter()
    router.responses = [LLMResponse(content="got all", tool_calls=None)]
    ag = _make_agent(router)

    ag._inbox.push("msg1")
    ag._inbox.push("msg2")
    ag._inbox.push("msg3")

    result = "".join(ag.chat_stream())
    assert result == "got all"

    user_msgs = [m for m in ag.chat_history if m.role == "user"]
    user_contents = [m.content for m in user_msgs]
    assert "msg1" in user_contents
    assert "msg2" in user_contents
    assert "msg3" in user_contents


def test_push_message_with_explicit_message():
    """push_message + explicit chat_stream(message) — both added."""
    router = SlowRouter()
    router.responses = [LLMResponse(content="combined", tool_calls=None)]
    ag = _make_agent(router)

    ag._inbox.push("from inbox")
    result = "".join(ag.chat_stream("explicit"))
    assert result == "combined"

    user_msgs = [m for m in ag.chat_history if m.role == "user"]
    user_contents = [m.content for m in user_msgs]
    assert "explicit" in user_contents
    assert "from inbox" in user_contents


# ── cancel() still works directly ──

def test_cancel_stops_stream():
    """Direct cancel() call stops chat_stream."""
    router = SlowRouter()
    router.responses = [LLMResponse(content="X" * 200, tool_calls=None)]
    ag = _make_agent(router)

    collected = []
    done = threading.Event()

    def consume():
        for delta in ag.chat_stream("hi"):
            collected.append(delta)
        done.set()

    t = threading.Thread(target=consume)
    t.start()
    time.sleep(0.05)
    ag.cancel()
    done.wait(timeout=2)
    t.join(timeout=2)

    full = "".join(collected)
    assert len(full) < 200


# ── Session persistence ──

def test_chat_stream_persists_session(tmp_path):
    router = SlowRouter()
    router.responses = [LLMResponse(content="saved", tool_calls=None)]
    ag = _make_agent(router, store_dir=str(tmp_path))

    list(ag.chat_stream("persist this"))

    sessions = ag.store.list_sessions()
    assert len(sessions) == 1


def test_interrupted_stream_not_persisted(tmp_path):
    """Interrupted stream doesn't persist, but next complete call does."""
    router = SlowRouter()
    router.responses = [
        LLMResponse(content="A" * 100, tool_calls=None),  # interrupted
        LLMResponse(content="final", tool_calls=None),
    ]
    ag = _make_agent(router, store_dir=str(tmp_path))

    done = threading.Event()

    def consume():
        for _ in ag.chat_stream("first"):
            pass
        done.set()

    t = threading.Thread(target=consume)
    t.start()
    time.sleep(0.05)
    ag.push_message("interrupt")
    done.wait(timeout=2)
    t.join(timeout=2)

    # No session persisted yet (interrupted)
    sessions = ag.store.list_sessions()
    assert len(sessions) == 0

    # Now complete a normal call
    list(ag.chat_stream())
    sessions = ag.store.list_sessions()
    assert len(sessions) == 1
