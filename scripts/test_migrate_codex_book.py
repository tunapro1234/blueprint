import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("migration", Path(__file__).with_name("migrate-codex-book.py"))
migration = importlib.util.module_from_spec(spec)
spec.loader.exec_module(migration)
THREAD = "01a0617e-29f9-79a3-be66-72ea1dec4718"


class MigrationTest(unittest.TestCase):
    def test_preserves_entries_and_unknown_metadata_and_is_idempotent(self):
        books = [{"orchestrator": "server-main-claude", "extra": {"keep": 1}, "agents": [
            {"name": "server-main", "folder": "/srv", "parent": "server-main-claude", "custom": 7},
            {"name": "server-main-claude", "folder": "/srv"},
            {"name": "child", "parent": "server-main-claude"},
        ]}, {"orchestrator": "child", "parent": "server-main-claude", "agents": [
            {"name": "grandchild", "parent": "child"}]}]
        result = migration.migrate(books, THREAD)
        self.assertEqual(books[0]["orchestrator"], "server-main-claude")
        self.assertEqual(result[0]["orchestrator"], "server-main")
        self.assertEqual(result[0]["extra"], {"keep": 1})
        root, old, child = result[0]["agents"]
        self.assertNotIn("parent", root)
        self.assertEqual(root["custom"], 7)
        self.assertEqual(root["launch"]["resumeId"], THREAD)
        self.assertNotIn("model", root["launch"])
        self.assertEqual(old["parent"], "server-main")
        self.assertEqual(child["parent"], "server-main")
        self.assertEqual(result[1]["parent"], "server-main")
        self.assertEqual(result[1]["agents"], books[1]["agents"])
        self.assertEqual(migration.migrate(result, THREAD), result)

    def test_identity_binding_preserves_launch_and_rejects_conflicts(self):
        child_thread = "11111111-1111-1111-1111-111111111111"
        books = [{"orchestrator": "server-main", "agents": [
            {"name": "server-main", "folder": "/srv"},
            {"name": "astra", "folder": "/any", "launch": {"codex": True, "remote": "unix://", "noSandbox": False}},
        ]}]
        result = migration.migrate(books, THREAD, {"astra": child_thread})
        self.assertEqual(result[0]["agents"][1]["launch"], books[0]["agents"][1]["launch"])
        self.assertEqual(result[0]["agents"][1]["identityThreadId"], child_thread)
        self.assertEqual(result[0]["agents"][0]["identityThreadId"], THREAD)
        with self.assertRaises(ValueError):
            migration.migrate(books, THREAD, {"unknown-agent": child_thread})
        with self.assertRaises(ValueError):
            migration.migrate(books, THREAD, {"server-main": child_thread})
        with self.assertRaises(ValueError):
            migration.migrate(books, THREAD, {"astra": THREAD})

    def test_unknown_root_or_missing_existing_thread_workspace_refused(self):
        for book in [{"orchestrator": "someone-else", "agents": []},
                     {"orchestrator": "server-main", "agents": []},
                     {"orchestrator": "server-main", "agents": [{"name": "server-main", "folder": "/other"}]}]:
            with self.assertRaises(ValueError):
                migration.migrate([book], THREAD)


if __name__ == "__main__":
    unittest.main()
