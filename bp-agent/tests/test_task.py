from pathlib import Path

from task import TaskStore, TaskStatus


def test_create_task():
    store = TaskStore()
    task = store.create("Test instruction")

    assert task.id is not None
    assert task.instruction == "Test instruction"
    assert task.status == TaskStatus.PENDING
    assert task.created_at is not None


def test_update_task():
    store = TaskStore()
    task = store.create("Test")

    updated = store.update(task.id, status="completed", output="Done")

    assert updated.status == TaskStatus.COMPLETED
    assert updated.output == "Done"
    assert updated.completed_at is not None


def test_get_task():
    store = TaskStore()
    task = store.create("Test")

    retrieved = store.get(task.id)
    assert retrieved.id == task.id

    not_found = store.get("nonexistent")
    assert not_found is None


def test_list_tasks():
    store = TaskStore()
    store.create("Task 1")
    store.create("Task 2")
    store.create("Task 3")

    tasks = store.list(limit=2)
    assert len(tasks) == 2


def test_persistence(tmp_path: Path):
    path = tmp_path / "test_tasks.json"

    store1 = TaskStore(persist=True, path=str(path))
    task = store1.create("Persist test")
    store1.update(task.id, status="completed", output="Done")

    store2 = TaskStore(persist=True, path=str(path))
    loaded = store2.get(task.id)

    assert loaded is not None
    assert loaded.instruction == "Persist test"
    assert loaded.status == TaskStatus.COMPLETED
