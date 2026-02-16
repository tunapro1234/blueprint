"""Tests for bp-multi: tunnel, workflow, and team system."""

import threading
import time

from bp_multi.tunnel import Tunnel, TunnelHub, TunnelMessage
from bp_multi.workflow import Workflow
from bp_multi.team import TeamChat, _is_satisfied


# --- Tunnel primitive ---


def test_tunnel_send_receive():
    t = Tunnel("test")
    t.send("hello", sender="alice")
    msg = t.receive(timeout=1)
    assert msg is not None
    assert msg.content == "hello"
    assert msg.sender == "alice"


def test_tunnel_receive_empty():
    t = Tunnel("test")
    msg = t.receive(timeout=0.01)
    assert msg is None


def test_tunnel_receive_all():
    t = Tunnel("test")
    t.send("a", sender="x")
    t.send("b", sender="y")
    t.send("c", sender="z")
    msgs = t.receive_all()
    assert len(msgs) == 3
    assert [m.content for m in msgs] == ["a", "b", "c"]
    assert t.receive_all() == []


def test_tunnel_pending_count():
    t = Tunnel("test")
    assert t.pending == 0
    t.send("x", sender="s")
    t.send("y", sender="s")
    assert t.pending == 2
    t.receive()
    assert t.pending == 1


def test_tunnel_message_str():
    m = TunnelMessage(sender="bob", content="hi there")
    assert str(m) == "[from:bob] hi there"


# --- TunnelHub ---


def test_hub_get_or_create():
    hub = TunnelHub()
    t1 = hub.get_or_create("ch1")
    t2 = hub.get_or_create("ch1")
    assert t1 is t2


def test_hub_send_receive():
    hub = TunnelHub()
    hub.send("inbox", "msg1", sender="a")
    hub.send("inbox", "msg2", sender="b")
    msg = hub.receive("inbox", timeout=1)
    assert msg.content == "msg1"
    msgs = hub.receive_all("inbox")
    assert len(msgs) == 1
    assert msgs[0].content == "msg2"


def test_hub_list_channels():
    hub = TunnelHub()
    hub.send("alpha", "x", sender="s")
    hub.send("beta", "y", sender="s")
    channels = hub.list_channels()
    assert set(channels) == {"alpha", "beta"}


def test_hub_pending_count():
    hub = TunnelHub()
    assert hub.pending_count("nonexistent") == 0
    hub.send("ch", "a", sender="s")
    hub.send("ch", "b", sender="s")
    assert hub.pending_count("ch") == 2


def test_hub_thread_safety():
    """Multiple threads sending to the same channel should not lose messages."""
    hub = TunnelHub()
    n_threads = 10
    n_per_thread = 50

    def writer(tid: int):
        for i in range(n_per_thread):
            hub.send("shared", f"{tid}:{i}", sender=f"t{tid}")

    threads = [threading.Thread(target=writer, args=(i,)) for i in range(n_threads)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    msgs = hub.receive_all("shared")
    assert len(msgs) == n_threads * n_per_thread


# --- Agent tunnel tools (unit-level, no LLM) ---


def test_agent_connect_hub_registers_tools():
    """Agent.connect_hub should register 4 messaging tools."""
    from bp_agent.agent import Agent, AgentConfig
    from bp_agent.tools import ToolRegistry

    hub = TunnelHub()
    agent = Agent.__new__(Agent)
    agent.name = "test-agent"
    agent.config = AgentConfig()
    agent.tools = ToolRegistry()
    agent._hub = None
    agent._hub_alias = None

    agent.connect_hub(hub, alias="tester")

    assert agent.tools.has("send_message")
    assert agent.tools.has("check_messages")
    assert agent.tools.has("send_to_channel")
    assert agent.tools.has("read_channel")
    assert agent._hub is hub
    assert agent._hub_alias == "tester"


def test_agent_messaging_round_trip():
    """Two agents on the same hub can exchange messages via tools."""
    from bp_agent.agent import Agent, AgentConfig
    from bp_agent.tools import ToolRegistry

    hub = TunnelHub()

    def make_fake_agent(name):
        a = Agent.__new__(Agent)
        a.name = name
        a.config = AgentConfig()
        a.tools = ToolRegistry()
        a._hub = None
        a._hub_alias = None
        a.connect_hub(hub, alias=name)
        return a

    alice = make_fake_agent("alice")
    bob = make_fake_agent("bob")

    result = alice.tools.execute("send_message", {"to": "bob", "message": "ping"})
    assert result.success

    result = bob.tools.execute("check_messages", {})
    assert result.success
    assert "ping" in result.output
    assert "alice" in result.output


def test_agent_channel_round_trip():
    """Agents can communicate via shared channels."""
    from bp_agent.agent import Agent, AgentConfig
    from bp_agent.tools import ToolRegistry

    hub = TunnelHub()

    def make_fake_agent(name):
        a = Agent.__new__(Agent)
        a.name = name
        a.config = AgentConfig()
        a.tools = ToolRegistry()
        a._hub = None
        a._hub_alias = None
        a.connect_hub(hub, alias=name)
        return a

    alice = make_fake_agent("alice")
    bob = make_fake_agent("bob")

    alice.tools.execute("send_to_channel", {"channel": "updates", "message": "deployed v2"})

    result = bob.tools.execute("read_channel", {"channel": "updates"})
    assert result.success
    assert "deployed v2" in result.output
    assert "alice" in result.output


def test_check_messages_empty():
    from bp_agent.agent import Agent, AgentConfig
    from bp_agent.tools import ToolRegistry

    hub = TunnelHub()
    a = Agent.__new__(Agent)
    a.name = "lonely"
    a.config = AgentConfig()
    a.tools = ToolRegistry()
    a._hub = None
    a._hub_alias = None
    a.connect_hub(hub, alias="lonely")

    result = a.tools.execute("check_messages", {})
    assert result.success
    assert result.output == "[no messages]"


# --- Workflow (structural tests, no LLM) ---


def test_workflow_add_connects_hub():
    """Workflow.add should connect agent to the workflow's hub."""
    from bp_agent.agent import Agent, AgentConfig
    from bp_agent.tools import ToolRegistry

    hub = TunnelHub()
    wf = Workflow(hub)

    a = Agent.__new__(Agent)
    a.name = "w1"
    a.config = AgentConfig()
    a.tools = ToolRegistry()
    a._hub = None
    a._hub_alias = None

    wf.add("worker", a)
    assert a._hub is hub
    assert a._hub_alias == "worker"
    assert a.tools.has("send_message")


# --- TeamChat (structural tests, no LLM) ---


def test_is_satisfied_positive():
    assert _is_satisfied("ALL TESTS PASS\nEverything looks good.")
    assert _is_satisfied("Looks good, no issues found.")
    assert _is_satisfied("LGTM")
    assert _is_satisfied("all pass, nice work")


def test_is_satisfied_negative():
    assert not _is_satisfied("Found 3 failing tests.")
    assert not _is_satisfied("There are some issues to fix.")
    assert not _is_satisfied("Hmm, interesting approach.")


def test_team_creates_three_agents():
    """TeamChat should create manager, worker, tester with hub connected."""
    from unittest.mock import patch

    with patch("bp_agent.agent._build_llm_router") as mock_router:
        mock_router.return_value = type("FakeRouter", (), {
            "complete": lambda *a, **kw: None,
            "complete_stream": lambda *a, **kw: iter([]),
        })()
        team = TeamChat()

    assert team.manager._hub is team.hub
    assert team.worker._hub is team.hub
    assert team.tester._hub is team.hub
    assert team.manager._hub_alias == "manager"
    assert team.worker._hub_alias == "worker"
    assert team.tester._hub_alias == "tester"


def test_team_manager_has_coordination_tools():
    """Manager should have start_task, approve_plan, reject_plan."""
    from unittest.mock import patch

    with patch("bp_agent.agent._build_llm_router") as mock_router:
        mock_router.return_value = type("FakeRouter", (), {
            "complete": lambda *a, **kw: None,
            "complete_stream": lambda *a, **kw: iter([]),
        })()
        team = TeamChat()

    assert team.manager.tools.has("start_task")
    assert team.manager.tools.has("approve_plan")
    assert team.manager.tools.has("reject_plan")
    assert team.manager.tools.has("send_message")
    assert team.manager.tools.has("check_messages")


def test_team_start_task_prevents_double_start():
    """start_task should reject if a task is already running."""
    from unittest.mock import patch

    with patch("bp_agent.agent._build_llm_router") as mock_router:
        mock_router.return_value = type("FakeRouter", (), {
            "complete": lambda *a, **kw: None,
            "complete_stream": lambda *a, **kw: iter([]),
        })()
        team = TeamChat()

    team._task_active = True
    result = team.manager.tools.execute("start_task", {"plan": "do stuff"})
    assert result.success
    assert "already running" in result.output


def test_team_approve_sends_to_tunnel():
    """approve_plan should send approval via team:approval tunnel."""
    from unittest.mock import patch

    with patch("bp_agent.agent._build_llm_router") as mock_router:
        mock_router.return_value = type("FakeRouter", (), {
            "complete": lambda *a, **kw: None,
            "complete_stream": lambda *a, **kw: iter([]),
        })()
        team = TeamChat()

    team.manager.tools.execute("approve_plan", {})
    msg = team.hub.receive("team:approval", timeout=1)
    assert msg is not None
    assert msg.content == "approved"


def test_team_reject_sends_to_tunnel():
    """reject_plan should send rejection via team:approval tunnel."""
    from unittest.mock import patch

    with patch("bp_agent.agent._build_llm_router") as mock_router:
        mock_router.return_value = type("FakeRouter", (), {
            "complete": lambda *a, **kw: None,
            "complete_stream": lambda *a, **kw: iter([]),
        })()
        team = TeamChat()

    team.manager.tools.execute("reject_plan", {"reason": "too complex"})
    msg = team.hub.receive("team:approval", timeout=1)
    assert msg is not None
    assert "rejected" in msg.content
    assert "too complex" in msg.content


def test_team_manager_turn_injects_updates():
    """_manager_turn should prepend pending hub messages to user input."""
    from unittest.mock import patch

    with patch("bp_agent.agent._build_llm_router") as mock_router:
        mock_router.return_value = type("FakeRouter", (), {
            "complete": lambda *a, **kw: None,
            "complete_stream": lambda *a, **kw: iter([]),
        })()
        team = TeamChat()

    team.hub.send("agent:manager", "worker finished", sender="worker")

    captured = []
    def fake_chat_stream(msg, **kw):
        captured.append(msg)
        return iter(["ok"])

    team.manager.chat_stream = fake_chat_stream
    team._manager_turn("what happened?")

    assert len(captured) == 1
    assert "Team Updates" in captured[0]
    assert "worker finished" in captured[0]
    assert "what happened?" in captured[0]


def test_team_manager_turn_no_updates():
    """When no pending messages, user input goes through unchanged."""
    from unittest.mock import patch

    with patch("bp_agent.agent._build_llm_router") as mock_router:
        mock_router.return_value = type("FakeRouter", (), {
            "complete": lambda *a, **kw: None,
            "complete_stream": lambda *a, **kw: iter([]),
        })()
        team = TeamChat()

    captured = []
    def fake_chat_stream(msg, **kw):
        captured.append(msg)
        return iter(["ok"])

    team.manager.chat_stream = fake_chat_stream
    team._manager_turn("hello")

    assert len(captured) == 1
    assert captured[0] == "hello"
    assert "Team Updates" not in captured[0]
