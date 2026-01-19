import json
from http import HTTPStatus
from urllib import request

import pytest

from api.server import AgentServer
from agent import AgentResult


class DummyAgent:
    def __init__(self):
        self.system_prompt = ""
        self.tools = DummyTools()
        self.tasks = DummyTaskStore()
        self._outputs = []

    def execute(self, instruction: str):
        if self._outputs:
            output = self._outputs.pop(0)
        else:
            output = f"echo: {instruction}"
        task = self.tasks.create(instruction)
        self.tasks.update(task.id, status="completed", output=output)
        return AgentResult(success=True, output=output, task_id=task.id)


class DummyTools:
    def __init__(self):
        self._names = set()

    def has(self, name: str) -> bool:
        return name in self._names


class DummyTask:
    def __init__(self, task_id, instruction):
        self.id = task_id
        self.instruction = instruction
        self.status = "completed"
        self.output = ""

    def to_dict(self):
        return {
            "id": self.id,
            "instruction": self.instruction,
            "status": self.status,
            "output": self.output,
        }


class DummyTaskStore:
    def __init__(self):
        self._tasks = []

    def create(self, instruction: str):
        task = DummyTask(str(len(self._tasks) + 1), instruction)
        self._tasks.append(task)
        return task

    def update(self, task_id: str, status=None, output=None, error=None):
        for task in self._tasks:
            if task.id == task_id:
                if status:
                    task.status = status
                if output is not None:
                    task.output = output
                return task
        raise ValueError("Task not found")

    def list(self, limit=10):
        return list(reversed(self._tasks))[:limit]


@pytest.fixture

def server():
    agent = DummyAgent()
    srv = AgentServer(port=0, agent=agent)
    thread = srv.start_in_thread()
    try:
        yield srv
    finally:
        srv.shutdown()
        thread.join(timeout=1)


def _url(server, path):
    return f"http://127.0.0.1:{server.port}{path}"


def test_health(server):
    with request.urlopen(_url(server, "/health")) as resp:
        payload = json.loads(resp.read().decode("utf-8"))
    assert resp.status == HTTPStatus.OK
    assert payload["status"] == "ok"


def test_execute_and_tasks(server):
    data = json.dumps({"instruction": "Hello"}).encode("utf-8")
    req = request.Request(_url(server, "/execute"), data=data, method="POST")
    req.add_header("Content-Type", "application/json")

    with request.urlopen(req) as resp:
        payload = json.loads(resp.read().decode("utf-8"))
    assert resp.status == HTTPStatus.OK
    assert payload["success"] is True
    assert payload["output"]
    assert payload["task_id"]

    with request.urlopen(_url(server, "/tasks?limit=5")) as resp:
        tasks_payload = json.loads(resp.read().decode("utf-8"))
    assert resp.status == HTTPStatus.OK
    assert len(tasks_payload["tasks"]) >= 1


def test_execute_missing_instruction(server):
    data = json.dumps({}).encode("utf-8")
    req = request.Request(_url(server, "/execute"), data=data, method="POST")
    req.add_header("Content-Type", "application/json")

    try:
        request.urlopen(req)
        assert False, "Expected HTTPError"
    except request.HTTPError as err:
        assert err.code == HTTPStatus.BAD_REQUEST
        body = json.loads(err.read().decode("utf-8"))
        assert "instruction" in body["error"]
