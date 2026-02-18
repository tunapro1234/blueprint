"""Tests for stream cancellation and message merging."""

import threading
import time
import json

import pytest

import bp_agent.agent as agent_mod
from bp_agent.agent import Agent, AgentConfig
from bp_agent.llm import LLMResponse, ToolCall
from bp_agent.llm.types import StreamChunk, ToolCallDelta


class SlowRouter:
    """Router that streams chunks with a delay, allowing cancel tests."""

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
                # Yield one chunk per character with small delay
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


# --- cancel() ---

def test_cancel_stops_stream():
    router = SlowRouter()
    router.responses = [LLMResponse(content="A" * 100, tool_calls=None)]
    ag = _make_agent(router)

    collected = []
    def stream():
        for delta in ag.chat_stream("hello"):
            collected.append(delta)

    t = threading.Thread(target=stream)
    t.start()
    time.sleep(0.05)  # let some chunks through
    ag.cancel()
    t.join(timeout=2)

    partial = "".join(collected)
    # Should have gotten some but not all
    assert 0 < len(partial) < 100


def test_cancel_saves_partial_as_interrupted():
    router = SlowRouter()
    router.responses = [LLMResponse(content="A" * 100, tool_calls=None)]
    ag = _make_agent(router)

    def stream():
        for _ in ag.chat_stream("hello"):
            pass

    t = threading.Thread(target=stream)
    t.start()
    time.sleep(0.05)
    ag.cancel()
    t.join(timeout=2)

    # History should have: system, user, assistant (interrupted)
    msgs = ag.chat_history
    assistant_msgs = [m for m in msgs if m.role == "assistant"]
    assert len(assistant_msgs) == 1
    assert assistant_msgs[0].content.endswith("[interrupted]")


def test_cancel_does_not_persist_session(tmp_path):
    router = SlowRouter()
    router.responses = [LLMResponse(content="A" * 100, tool_calls=None)]
    ag = _make_agent(router, store_dir=str(tmp_path))

    def stream():
        for _ in ag.chat_stream("hello"):
            pass

    t = threading.Thread(target=stream)
    t.start()
    time.sleep(0.05)
    ag.cancel()
    t.join(timeout=2)

    # No session file should exist
    sessions = ag.store.list_sessions()
    assert len(sessions) == 0


def test_is_streaming_flag():
    router = SlowRouter()
    router.responses = [LLMResponse(content="A" * 50, tool_calls=None)]
    ag = _make_agent(router)

    assert ag.is_streaming is False

    streaming_observed = []

    def stream():
        for _ in ag.chat_stream("hello"):
            streaming_observed.append(ag.is_streaming)

    t = threading.Thread(target=stream)
    t.start()
    t.join(timeout=2)

    assert all(streaming_observed)  # was True during streaming
    assert ag.is_streaming is False  # cleared after


def test_cancel_clears_flag():
    router = SlowRouter()
    router.responses = [LLMResponse(content="A" * 100, tool_calls=None)]
    ag = _make_agent(router)

    def stream():
        for _ in ag.chat_stream("hello"):
            pass

    t = threading.Thread(target=stream)
    t.start()
    time.sleep(0.05)
    ag.cancel()
    t.join(timeout=2)

    assert ag.is_streaming is False


# --- Message merging (cancel + new message preserves context) ---

def test_message_merge_after_cancel():
    """After cancel, next chat_stream sees partial context and new message."""
    router = SlowRouter()
    router.responses = [
        LLMResponse(content="A" * 100, tool_calls=None),
        LLMResponse(content="Merged response", tool_calls=None),
    ]
    ag = _make_agent(router)

    # First stream — cancel midway
    def stream1():
        for _ in ag.chat_stream("first message"):
            pass

    t = threading.Thread(target=stream1)
    t.start()
    time.sleep(0.05)
    ag.cancel()
    t.join(timeout=2)

    # Second stream with new message
    chunks = list(ag.chat_stream("second message"))
    full = "".join(chunks)
    assert full == "Merged response"

    # History should have the interrupted partial + both user messages
    history = ag.chat_history
    user_msgs = [m for m in history if m.role == "user"]
    assistant_msgs = [m for m in history if m.role == "assistant"]
    assert len(user_msgs) == 2
    # One interrupted partial + one final response
    assert any("[interrupted]" in m.content for m in assistant_msgs)
    assert any(m.content == "Merged response" for m in assistant_msgs)


def test_multiple_cancels_accumulate_context():
    """Multiple cancel+new message cycles build up context correctly."""
    router = SlowRouter()
    router.responses = [
        LLMResponse(content="B" * 50, tool_calls=None),
        LLMResponse(content="C" * 50, tool_calls=None),
        LLMResponse(content="Final answer", tool_calls=None),
    ]
    ag = _make_agent(router)

    # Cancel first
    def stream1():
        for _ in ag.chat_stream("msg1"):
            pass
    t = threading.Thread(target=stream1)
    t.start()
    time.sleep(0.03)
    ag.cancel()
    t.join(timeout=2)

    # Cancel second
    def stream2():
        for _ in ag.chat_stream("msg2"):
            pass
    t = threading.Thread(target=stream2)
    t.start()
    time.sleep(0.03)
    ag.cancel()
    t.join(timeout=2)

    # Third completes
    chunks = list(ag.chat_stream("msg3"))
    full = "".join(chunks)
    assert full == "Final answer"

    # Should have 3 user messages in history
    history = ag.chat_history
    user_msgs = [m for m in history if m.role == "user"]
    assistant_msgs = [m for m in history if m.role == "assistant"]
    assert len(user_msgs) == 3
    # 2 interrupted + 1 final
    interrupted = [m for m in assistant_msgs if "[interrupted]" in m.content]
    assert len(interrupted) == 2
    assert any(m.content == "Final answer" for m in assistant_msgs)


# --- Normal flow still works ---

def test_normal_stream_unaffected():
    """Without cancel, streaming works exactly as before."""
    router = SlowRouter()
    router.responses = [LLMResponse(content="hello world", tool_calls=None)]
    ag = _make_agent(router)

    chunks = list(ag.chat_stream("hi"))
    assert "".join(chunks) == "hello world"

    msgs = ag.chat_history
    assistant_msgs = [m for m in msgs if m.role == "assistant"]
    assert len(assistant_msgs) == 1
    assert "[interrupted]" not in assistant_msgs[0].content


def test_normal_stream_persists_session(tmp_path):
    router = SlowRouter()
    router.responses = [LLMResponse(content="saved", tool_calls=None)]
    ag = _make_agent(router, store_dir=str(tmp_path))

    list(ag.chat_stream("hi"))

    sessions = ag.store.list_sessions()
    assert len(sessions) == 1


# --- Edge cases ---

def test_cancel_before_any_chunks():
    """Cancel before streaming starts — no partial, no crash."""
    router = SlowRouter()
    router.responses = [LLMResponse(content="hello", tool_calls=None)]
    ag = _make_agent(router)

    ag.cancel()  # pre-cancel
    chunks = list(ag.chat_stream("hi"))

    # Should work normally because chat_stream clears cancel at start
    assert "".join(chunks) == "hello"


def test_cancel_with_empty_partial():
    """Cancel when no text has been yielded yet."""
    router = SlowRouter()
    # Long delay before first chunk
    original_stream = router.complete_stream

    def slow_stream(request):
        time.sleep(0.1)
        yield from original_stream(request)

    router.complete_stream = slow_stream
    router.responses = [LLMResponse(content="hello", tool_calls=None)]
    ag = _make_agent(router)

    def stream():
        for _ in ag.chat_stream("hi"):
            pass

    t = threading.Thread(target=stream)
    t.start()
    time.sleep(0.05)  # cancel before any chunk
    ag.cancel()
    t.join(timeout=2)

    # No assistant message since nothing was yielded
    assistant_msgs = [m for m in ag.chat_history if m.role == "assistant"]
    assert len(assistant_msgs) == 0
