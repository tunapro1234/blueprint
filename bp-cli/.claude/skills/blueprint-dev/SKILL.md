---
name: blueprint-dev
description: >
  Blueprint-driven development workflow and bp CLI tool. Covers the full
  development process: designing packages with BLUEPRINT.yaml, snapshot-based
  versioning, symlink-pinned dependencies, iterative development cycle, and
  dependency management. Use when working in any project that contains
  BLUEPRINT.yaml files.
user-invocable: false
---

# Blueprint Development

## The Process

Blueprint development follows a **design-first, implement-second** cycle. You describe what a package should do in a BLUEPRINT.yaml file before writing any code. The tool then tracks your code through immutable snapshots.

### 1. Design the Package

Start by writing or updating `BLUEPRINT.yaml`. This is the source of truth — it defines what the package does, its public API, and what it depends on.

```yaml
_meta:
  version: "0.1.0"

intent: |
  HTTP request router with middleware support

api:
  exports:
    - name: Router
      type: interface
    - name: Middleware
      type: type

implementation:
  structure:
    - router.go
    - middleware.go
    - handlers/:
        has_blueprint: true   # Subdirectory has its own blueprint

dependencies:
  internal:
    - path: ./logger
    - path: ./config

tests:
  verification:
    - go test ./...
```

For large packages, split into separate files:
- `BLUEPRINT.yaml` — intent, API, dependencies
- `BLUEPRINT.api.yaml` — detailed API definition
- `BLUEPRINT.spec.yaml` — implementation spec, test scenarios, algorithms

### 2. Implement

Write code in the working tree. The working tree is always live, always editable — blueprint never locks you out during normal development (in default `meek` mode).

Your code imports dependencies from their real directories:
```go
// Dependencies are real directories, not virtual paths
import "myproject/logger"
import "myproject/config"
```

### 3. Snapshot When Stable

When your code works and tests pass, take a snapshot:

```bash
bp ss -m "add middleware chain support"
```

This validates the blueprint, runs `tests.verification`, copies code into `.bp/history/{id}/`, creates dependency symlinks to pinned versions, and sets everything read-only (chmod 0555).

**A snapshot is not a commit.** It's an immutable, self-contained copy of your code at that moment — including exact references to which dependency versions it was built against.

### 4. Iterate

Continue editing in the working tree. Take new snapshots as you reach stable points. Each snapshot is independent — you can always go back to any previous one.

```bash
bp ss -m "fix edge case in path matching"
bp ss -m "add timeout configuration"
```

### 5. Manage Dependencies

When a dependency publishes a new snapshot, you decide when to adopt it:

```bash
# See what's available
bp status                              # Shows pinned vs latest for each dep

# Upgrade to latest (runs YOUR tests to verify compatibility)
bp upgrade ./logger

# Pin to a specific version (works for downgrade too)
bp upgrade --to stable-c3d4 ./logger

# Upgrade everything at once
bp upgrade --all
```

**Upgrade safety**: bp runs your tests after changing the pin. If tests fail, the pin is rolled back and the target snapshot is marked **rotten**. Rotten snapshots are skipped in future upgrades (override with `--force`).

**API change detection**: If the dependency's API hash changed, `bp upgrade --safe` will skip it. You can review the change with `bp diff` before accepting.

---

## How Snapshots Work

A snapshot is a frozen directory under `.bp/history/{id}/`:

```
.bp/history/add-middleware-a1b2/
├── BLUEPRINT.yaml          # Blueprint at that point
├── meta.yaml               # Timestamp, hashes, message, source
├── router.go               # Code files (read-only copies)
├── middleware.go
├── logger/  → symlink      # Pinned to specific logger snapshot
└── config/  → symlink      # Pinned to specific config snapshot
```

Dependencies inside snapshots are **symlinks** pointing to other snapshots:

```
logger/ → ../../../logger/.bp/history/stable-c3d4/
config/ → ../../../config/.bp/history/init-f7e8/
```

This means every snapshot records **exactly which version** of every dependency it was built against. If you change the pinned version (via `bp upgrade`), the next snapshot will point to the new version.

### Snapshot IDs

| Pattern | Example | When |
|---------|---------|------|
| `{slug}-{hash4}` | `add-middleware-a1b2` | With message |
| `ss-{hash8}` | `ss-a3f2b7c1` | Without message |

### Two Snapshot Sources

**Local** — created by `bp ss`, stored in `.bp/history/`:
```bash
bp ss -m "release v2"      # → .bp/history/release-v2-d4e5/
```

**Git tag** — tags matching `bp/{path}/*` are automatically recognized as snapshots:
```bash
git tag bp/router/v1-abcd   # Appears in bp log with [tag] marker
```

Git tags are **read-only** — `bp ss` never creates tags. When a git tag snapshot is needed as a dependency target, it's materialized into `.bp/cache/` on demand via `git archive`.

---

## Dependency Graph

Blueprints form a directed acyclic graph. `bp` discovers this automatically:

- **Implicit deps**: Subdirectories with their own `BLUEPRINT.yaml`
- **Explicit deps**: Listed in `dependencies.internal`
- **Child wins**: A child blueprint is excluded from parent's scope

```bash
bp plan          # Shows packages in leaf-first order (build order)
bp deps          # Full dependency graph
bp map           # Project-wide snapshot status
```

**Leaf-first order** matters: always build/snapshot dependencies before dependents. `bp plan` gives you this order.

---

## State Tracking

Everything bp knows about a package lives in `.bp/`:

```
.bp/
├── current         # Active snapshot ID (text file)
├── state.yaml      # Dependency pins, file hashes, reverse dependents
├── impl.lock       # Present only during agentic compilation
├── history/        # Immutable local snapshots
└── cache/          # Materialized git tag snapshots (disposable)
```

### state.yaml

```yaml
snapshot_id: add-middleware-a1b2
deps:
  "./logger":
    pinned: stable-c3d4       # Exact version in use
    latest: new-format-g8h9   # Newest available
    api_changed: true          # API broke between pinned and latest
    rotten: false              # true = upgrade tests failed
dependents:
  "../app":
    using: add-middleware-a1b2  # Reverse: who uses our snapshot
```

---

## Development Modes

Set via `_meta.mode` in BLUEPRINT.yaml:

| Mode | Working Tree | After Compilation | Use Case |
|------|-------------|-------------------|----------|
| **meek** (default) | Always writable | No change | Normal development |
| **ro** | Writable during compile | Read-only | Enforced stability |
| **hide** | Visible during compile | Code deleted | Proprietary/generated |

---

## Agentic Compilation

`bp implement` runs an agentic compilation cycle — generates code from the blueprint definition, optionally snapshots the result:

```bash
bp implement                 # Compile + snapshot
bp implement --no-snapshot   # Compile only
bp cancel                    # Abort if stuck
```

During compilation, `impl.lock` exists as a busy flag. Modes (ro/hide) enforce their rules after compilation completes.

---

## Multi-Language Support

Blueprint supports multiple language implementations of the same API:

```bash
bp new-lang js              # Create src-js/ from src-go/
bp remove-lang ts           # Archive src-ts/
bp restore-lang ts          # Restore from archive
bp map --langs              # Compare API hashes across languages
```

API blueprints are **symlinked** across languages (shared contract). Spec blueprints are **copied** (language-specific implementation).

---

## Command Reference

| Command | Purpose |
|---------|---------|
| `bp ss -m "msg"` | Snapshot (validate + test + freeze) |
| `bp implement` | Agentic compile + optional snapshot |
| `bp upgrade [dep]` | Pin dependency to latest |
| `bp upgrade --to <id> <dep>` | Pin to specific version (up or down) |
| `bp status` | Freshness check, dependency state |
| `bp log [-n N]` | Snapshot history (local + git tags) |
| `bp diff [id1] [id2]` | Compare two snapshots |
| `bp show [id]` | View snapshot contents |
| `bp deps` | Dependency graph (topological) |
| `bp plan` | Stale packages in build order |
| `bp validate` | Check blueprint syntax |
| `bp map [--langs]` | Project-wide snapshot map |
| `bp init` | Create new BLUEPRINT.yaml |
| `bp cancel` | Abort active compilation |
| `bp new-lang <lang>` | Create language variant |
| `bp remove-lang <lang>` | Archive language |
| `bp restore-lang <lang>` | Restore archived language |
| `bp purge` | Clean old snapshots and archives |

### Key Flags

| Flag | Commands | Effect |
|------|----------|--------|
| `--no-recursive` | Most | Single package only |
| `--skip-tests` | `ss` | Skip verification |
| `--no-snapshot` | `implement` | Compile without snapshot |
| `--all` | `upgrade` | All dependencies |
| `--safe` | `upgrade` | Skip if API changed |
| `--force` | `upgrade` | Allow rotten snapshots |
| `--to <id>` | `upgrade` | Target specific version |

---

## Principles

1. **Blueprint first** — Design in BLUEPRINT.yaml before writing code
2. **Snapshot at stable points** — Not every save, but every meaningful milestone
3. **Leaf-first** — Build dependencies before dependents
4. **Pin explicitly** — Each dependency uses an exact snapshot ID, never "latest"
5. **Trust the tests** — Upgrades auto-rollback on test failure
6. **Snapshots are immutable** — Never modify `.bp/history/` contents
7. **Review API changes** — `api_changed: true` signals potential breakage
