"""Tests for AgentStore — directory-based persistent storage."""

import json
from pathlib import Path

from bp_agent.store import AgentStore


def test_session_save_load(tmp_path):
    store = AgentStore("test-agent", base_dir=tmp_path)
    messages = [
        {"role": "user", "content": "merhaba"},
        {"role": "assistant", "content": "selam!"},
    ]
    store.save_session("s1", messages)

    loaded = store.load_session("s1")
    assert loaded == messages


def test_session_not_found(tmp_path):
    store = AgentStore("test-agent", base_dir=tmp_path)
    assert store.load_session("nonexistent") is None


def test_session_full_metadata(tmp_path):
    store = AgentStore("test-agent", base_dir=tmp_path)
    messages = [{"role": "user", "content": "hi"}]
    store.save_session("s1", messages, metadata={"source": "web"})

    data = store.load_session_full("s1")
    assert data["id"] == "s1"
    assert data["agent"] == "test-agent"
    assert data["message_count"] == 1
    assert data["metadata"]["source"] == "web"
    assert "created_at" in data
    assert "updated_at" in data


def test_session_update_preserves_created_at(tmp_path):
    store = AgentStore("test-agent", base_dir=tmp_path)
    store.save_session("s1", [{"role": "user", "content": "hi"}])
    first = store.load_session_full("s1")

    store.save_session("s1", [
        {"role": "user", "content": "hi"},
        {"role": "assistant", "content": "hello"},
    ])
    second = store.load_session_full("s1")

    assert second["created_at"] == first["created_at"]
    assert second["message_count"] == 2


def test_session_metadata_merge(tmp_path):
    store = AgentStore("test-agent", base_dir=tmp_path)
    store.save_session("s1", [], metadata={"a": 1})
    store.save_session("s1", [], metadata={"b": 2})

    data = store.load_session_full("s1")
    assert data["metadata"] == {"a": 1, "b": 2}


def test_list_sessions(tmp_path):
    store = AgentStore("test-agent", base_dir=tmp_path)
    store.save_session("s1", [{"role": "user", "content": "one"}])
    store.save_session("s2", [
        {"role": "user", "content": "two"},
        {"role": "assistant", "content": "reply"},
    ])

    sessions = store.list_sessions()
    assert len(sessions) == 2
    ids = {s["id"] for s in sessions}
    assert ids == {"s1", "s2"}


def test_list_sessions_empty(tmp_path):
    store = AgentStore("test-agent", base_dir=tmp_path)
    assert store.list_sessions() == []


def test_delete_session(tmp_path):
    store = AgentStore("test-agent", base_dir=tmp_path)
    store.save_session("s1", [{"role": "user", "content": "hi"}])
    store.save_summary("s1", "greeting")

    assert store.delete_session("s1") is True
    assert store.load_session("s1") is None
    assert store.load_summary("s1") is None


def test_delete_session_not_found(tmp_path):
    store = AgentStore("test-agent", base_dir=tmp_path)
    assert store.delete_session("nope") is False


def test_summary_save_load(tmp_path):
    store = AgentStore("test-agent", base_dir=tmp_path)
    store.save_summary("s1", "User asked about pricing, agent provided info.")

    assert store.load_summary("s1") == "User asked about pricing, agent provided info."


def test_summary_not_found(tmp_path):
    store = AgentStore("test-agent", base_dir=tmp_path)
    assert store.load_summary("nope") is None


def test_new_session_id(tmp_path):
    store = AgentStore("test-agent", base_dir=tmp_path)
    id1 = store.new_session_id()
    id2 = store.new_session_id()
    assert isinstance(id1, str)
    assert len(id1) > 0
    assert id1 != id2


def test_directory_layout(tmp_path):
    store = AgentStore("my-agent", base_dir=tmp_path)
    store.save_session("s1", [{"role": "user", "content": "hi"}])
    store.save_summary("s1", "greeting")
    root = tmp_path / "my-agent"
    assert (root / "sessions" / "s1.json").exists()
    assert (root / "summaries" / "s1.txt").exists()


def test_exists_property(tmp_path):
    store = AgentStore("test-agent", base_dir=tmp_path)
    assert store.exists is False

    store.save_session("s1", [{"role": "user", "content": "hi"}])
    assert store.exists is True


def test_repr(tmp_path):
    store = AgentStore("test-agent", base_dir=tmp_path)
    r = repr(store)
    assert "test-agent" in r
