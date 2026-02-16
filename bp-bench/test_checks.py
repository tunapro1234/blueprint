"""Tests for the judge/scoring logic in tasks/blueprint.py.

Run: pytest test_checks.py -v
"""

import textwrap
from pathlib import Path
from tasks.blueprint import (
    check_file,
    check_no_third_party,
    judge,
    ScoreCard,
    BlueprintTask,
)


def _make_complete_workspace(tmp_path: Path):
    """Create a workspace with all expected files — perfect score target."""
    for pkg in ["store", "api", "cli"]:
        (tmp_path / pkg).mkdir(exist_ok=True)
        (tmp_path / pkg / "__init__.py").write_text("")

    (tmp_path / "store" / "models.py").write_text(textwrap.dedent("""\
        from dataclasses import dataclass

        @dataclass
        class Item:
            id: str
            name: str
            price: float
            quantity: int = 0
    """))

    (tmp_path / "store" / "repository.py").write_text(textwrap.dedent("""\
        from typing import Protocol
        from store.models import Item

        class ItemRepository(Protocol):
            def add(self, item: Item) -> Item: ...
            def get(self, id: str) -> Item: ...
            def list_all(self) -> list[Item]: ...
            def update(self, id: str, **kwargs) -> Item: ...
            def delete(self, id: str) -> None: ...
    """))

    (tmp_path / "store" / "memory.py").write_text(textwrap.dedent("""\
        from store.models import Item

        class MemoryRepository:
            def __init__(self):
                self._data = {}

            def add(self, item):
                self._data[item.id] = item
                return item

            def get(self, id):
                if id not in self._data:
                    raise KeyError(id)
                return self._data[id]

            def list_all(self):
                return list(self._data.values())

            def update(self, id, **kwargs):
                if id not in self._data:
                    raise KeyError(id)
                item = self._data[id]
                for k, v in kwargs.items():
                    setattr(item, k, v)
                return item

            def delete(self, id):
                if id not in self._data:
                    raise KeyError(id)
                del self._data[id]
    """))

    (tmp_path / "api" / "server.py").write_text(textwrap.dedent("""\
        from http.server import HTTPServer, BaseHTTPRequestHandler
        import json

        class Handler(BaseHTTPRequestHandler):
            pass

        def run(port=8080):
            HTTPServer(("", port), Handler).serve_forever()
    """))

    (tmp_path / "cli" / "main.py").write_text(textwrap.dedent("""\
        import argparse

        def main():
            parser = argparse.ArgumentParser()
            parser.parse_args()

        if __name__ == "__main__":
            main()
    """))


def test_judge_empty_workspace(tmp_path):
    """Empty workspace should score near 0."""
    score = judge(tmp_path)
    assert score.total < 0.1
    assert score.functional == 0.0


def test_judge_complete_workspace(tmp_path):
    """Complete workspace should score high."""
    _make_complete_workspace(tmp_path)
    score = judge(tmp_path)
    assert score.structure > 0.8
    assert score.total > 0.5
    assert score.details["store/models.py"] is True
    assert score.details["api/server.py"] is True
    assert score.details["has_protocol"] is True


def test_check_no_third_party_clean(tmp_path):
    (tmp_path / "app.py").write_text("import os\nimport json\n")
    assert check_no_third_party(tmp_path) is True


def test_check_no_third_party_dirty(tmp_path):
    (tmp_path / "app.py").write_text("import flask\n")
    assert check_no_third_party(tmp_path) is False


def test_blueprint_task_setup_workspace(tmp_path):
    bt = BlueprintTask()
    task = bt.load_tasks()[0]
    bt.setup_workspace(task, tmp_path)
    assert (tmp_path / "store").is_dir()
    assert not (tmp_path / "store" / "BLUEPRINT.yaml").exists()


def test_blueprint_task_setup_blueprint(tmp_path):
    bt = BlueprintTask()
    task = bt.load_tasks()[0]
    bt.setup_workspace(task, tmp_path)
    bt.setup_blueprint(task, tmp_path)
    assert (tmp_path / "store" / "BLUEPRINT.yaml").exists()
    assert (tmp_path / "api" / "BLUEPRINT.yaml").exists()
    assert (tmp_path / "cli" / "BLUEPRINT.yaml").exists()


def test_blueprint_task_score(tmp_path):
    bt = BlueprintTask()
    task = bt.load_tasks()[0]
    bt.setup_workspace(task, tmp_path)
    _make_complete_workspace(tmp_path)
    result = bt.score(task, tmp_path)
    assert result.score > 0.5
    assert "structure" in result.details
    assert "functional" in result.details


def test_score_card_defaults():
    sc = ScoreCard()
    assert sc.total == 0.0
    assert sc.details == {}


def test_blueprint_task_load_tasks():
    bt = BlueprintTask()
    tasks = bt.load_tasks()
    assert len(tasks) == 1
    assert tasks[0]["task_id"] == "blueprint/store-api-cli"
