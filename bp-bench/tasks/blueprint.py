"""Blueprint task type — original store/api/cli benchmark.

Deterministic scoring: checks file structure, imports, Protocol usage,
and integration between the three packages.
"""

from __future__ import annotations

import importlib.util
import sys
from dataclasses import dataclass, field
from pathlib import Path

from .base import TaskType, TaskResult

# ---------------------------------------------------------------------------
# Blueprint YAML content for with_blueprint mode
# ---------------------------------------------------------------------------

STORE_BLUEPRINT = """\
_meta:
  version: "1"
intent: In-memory data store with Protocol-based repository pattern
api:
  - "class Item: id(str), name(str), price(float), quantity(int)"
  - "Protocol ItemRepository: add, get, list_all, update, delete"
  - "class MemoryRepository(ItemRepository)"
  - "get/update/delete raise KeyError on missing id"
"""

API_BLUEPRINT = """\
_meta:
  version: "1"
intent: REST API server using http.server stdlib
dependencies:
  - path: ../store
api:
  - "GET /items -> [{id, name, price, quantity}, ...]"
  - "GET /items/{id} -> {id, name, price, quantity} | 404"
  - "POST /items {name, price, quantity?} -> 201 {id, ...}"
  - "PUT /items/{id} {fields} -> 200 | 404"
  - "DELETE /items/{id} -> 200 | 404"
  - "Content-Type: application/json"
"""

CLI_BLUEPRINT = """\
_meta:
  version: "1"
intent: CLI client for the API server
dependencies:
  - path: ../api
api:
  - "cli list -> table of items"
  - "cli add --name NAME --price PRICE [--quantity QTY]"
  - "cli get ID"
  - "cli delete ID"
"""

INSTRUCTION_WITH_BP = """\
You are in a workspace with 3 directories: store/, api/, cli/.
Each has a BLUEPRINT.yaml describing what to build.
Read each BLUEPRINT.yaml first, then implement all 3 packages using ONLY Python stdlib.

Required files:
- store/__init__.py, store/models.py, store/repository.py, store/memory.py
- api/__init__.py, api/server.py
- cli/__init__.py, cli/main.py

Follow the specs in BLUEPRINT.yaml exactly. Write ALL files using write_file.
Do NOT stop until every file is written. Call give_result only after ALL files are created."""

INSTRUCTION_NO_BP = """\
Create 3 Python packages in the current directory (store/, api/, cli/) using ONLY stdlib:

1. store/ — Item dataclass (id, name, price, quantity all str/float/int) and a
   Protocol-based ItemRepository with MemoryRepository implementation.
   CRUD methods: add, get, list_all, update, delete. get/update/delete raise
   KeyError on missing id.
   Files needed: store/__init__.py, store/models.py, store/repository.py, store/memory.py

2. api/ — REST API server using http.server. Endpoints:
   GET /items, GET /items/{id}, POST /items, PUT /items/{id}, DELETE /items/{id}
   JSON throughout, proper HTTP status codes (200, 201, 404, 400).
   Files needed: api/__init__.py, api/server.py

3. cli/ — argparse CLI: list, add --name --price, get ID, delete ID.
   Talks to the API server via urllib.
   Files needed: cli/__init__.py, cli/main.py

Each package needs __init__.py. Write ALL files listed above using write_file.
Do NOT stop until every file is written. Call give_result only after ALL files are created."""


# ---------------------------------------------------------------------------
# Scoring helpers
# ---------------------------------------------------------------------------

@dataclass
class ScoreCard:
    structure: float = 0.0
    functional: float = 0.0
    spec: float = 0.0
    integration: float = 0.0
    total: float = 0.0
    details: dict = None

    def __post_init__(self):
        if self.details is None:
            self.details = {}


def check_file(ws: Path, rel: str) -> bool:
    return (ws / rel).is_file()


def check_import(ws: Path, module: str) -> bool:
    """Try to import a module from the workspace."""
    init = ws / module.replace(".", "/") / "__init__.py"
    mod_file = ws / (module.replace(".", "/") + ".py")
    target = mod_file if mod_file.exists() else init
    if not target.exists():
        return False
    spec = importlib.util.spec_from_file_location(
        module, str(target), submodule_search_locations=[str(ws)]
    )
    if spec is None:
        return False
    try:
        mod = importlib.util.module_from_spec(spec)
        sys.modules[module] = mod
        spec.loader.exec_module(mod)
        return True
    except Exception:
        return False
    finally:
        sys.modules.pop(module, None)


def check_no_third_party(ws: Path) -> bool:
    """Scan .py files for obvious third-party imports."""
    third_party = {
        "flask", "fastapi", "django", "requests", "httpx",
        "aiohttp", "pydantic", "sqlalchemy", "click",
    }
    for py in ws.rglob("*.py"):
        try:
            text = py.read_text()
        except Exception:
            continue
        for line in text.splitlines():
            stripped = line.strip()
            if stripped.startswith("import ") or stripped.startswith("from "):
                for pkg in third_party:
                    if pkg in stripped:
                        return False
    return True


def judge(ws: Path) -> ScoreCard:
    """Run all deterministic checks and return a ScoreCard."""
    d = {}

    # Structure (7 checks)
    d["store/models.py"] = check_file(ws, "store/models.py")
    d["store/repository.py"] = check_file(ws, "store/repository.py")
    d["store/memory.py"] = check_file(ws, "store/memory.py")
    d["api/server.py"] = check_file(ws, "api/server.py")
    d["cli/main.py"] = check_file(ws, "cli/main.py")
    d["__init__.py files"] = all(
        check_file(ws, f"{pkg}/__init__.py") for pkg in ["store", "api", "cli"]
    )
    d["no_third_party"] = check_no_third_party(ws)
    structure = sum(d[k] for k in list(d.keys())[:7]) / 7

    # Functional (basic — file-level, no server spin-up in MVP)
    d["import store.models"] = check_import(ws, "store.models")
    d["import store.memory"] = check_import(ws, "store.memory")
    d["import api.server"] = check_import(ws, "api.server")
    func_checks = ["import store.models", "import store.memory", "import api.server"]
    functional = sum(d[k] for k in func_checks) / len(func_checks)

    # Spec (Protocol check)
    d["has_protocol"] = False
    repo_file = ws / "store" / "repository.py"
    if repo_file.exists():
        text = repo_file.read_text()
        d["has_protocol"] = "Protocol" in text
    spec_score = 1.0 if d["has_protocol"] else 0.0

    # Integration placeholder (to be expanded)
    d["integration_placeholder"] = (
        d.get("import store.models", False) and d.get("import api.server", False)
    )
    integration = 1.0 if d["integration_placeholder"] else 0.0

    total = structure * 0.3 + functional * 0.3 + spec_score * 0.2 + integration * 0.2
    return ScoreCard(
        structure=round(structure, 3),
        functional=round(functional, 3),
        spec=round(spec_score, 3),
        integration=round(integration, 3),
        total=round(total, 3),
        details=d,
    )


# ---------------------------------------------------------------------------
# TaskType implementation
# ---------------------------------------------------------------------------

class BlueprintTask(TaskType):
    """Blueprint benchmark — store/api/cli code generation, deterministic scoring."""

    name = "blueprint"

    def load_tasks(self, limit: int | None = None) -> list[dict]:
        return [{"task_id": "blueprint/store-api-cli"}]

    def setup_workspace(self, task: dict, ws: Path) -> None:
        for pkg in ["store", "api", "cli"]:
            (ws / pkg).mkdir(exist_ok=True)

    def setup_blueprint(self, task: dict, ws: Path) -> None:
        (ws / "store" / "BLUEPRINT.yaml").write_text(STORE_BLUEPRINT)
        (ws / "api" / "BLUEPRINT.yaml").write_text(API_BLUEPRINT)
        (ws / "cli" / "BLUEPRINT.yaml").write_text(CLI_BLUEPRINT)

    def get_instruction(self, task: dict, ws: Path) -> str:
        return INSTRUCTION_NO_BP

    def get_blueprint_instruction(self, task: dict, ws: Path) -> str:
        return INSTRUCTION_WITH_BP

    def score(self, task: dict, ws: Path) -> TaskResult:
        sc = judge(ws)
        return TaskResult(
            task_id=task["task_id"],
            passed=round(sc.total * 21),  # 21 total checks
            total=21,
            score=sc.total,
            details={
                "structure": sc.structure,
                "functional": sc.functional,
                "spec": sc.spec,
                "integration": sc.integration,
                "checks": sc.details,
            },
        )
