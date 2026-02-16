# Blueprint Development Guide

Blueprint (`bp`) is a snapshot-based code management tool. Code is organized into packages defined by `BLUEPRINT.yaml` files. Each package maintains immutable snapshots of its code and tracks dependencies on other packages through pinned snapshot versions.

## Core Concepts

### Snapshots Are the Unit of Work

A **snapshot** is a frozen copy of a package's code at a point in time. Snapshots live in `.bp/history/{id}/` and are **immutable** (read+execute permissions, no writes).

```
.bp/history/add-parser-a1b2/
├── BLUEPRINT.yaml          # Blueprint definition at that point
├── meta.yaml               # Timestamp, hashes, message, source
├── main.go                 # Code files (direct copies)
├── util.go
├── yamlparser/ → symlink   # Dependency: pinned version
└── commands/  → symlink    # Dependency: pinned version
```

Working tree files are always live and editable. Snapshots are the stable, versioned artifacts.

### Dependencies Are Symlinks to Pinned Snapshots

When a snapshot is created, each dependency becomes a **symlink** pointing to a specific snapshot of that dependency:

```
# Inside a snapshot:
yamlparser/ → ../../../yamlparser/.bp/history/stable-c3d4/
commands/   → ../../../commands/.bp/history/v2-api-e5f6/
```

This means each snapshot records **exactly which version** of every dependency it was built against. Changing the pinned version changes which snapshot the symlink targets.

### Two Snapshot Sources

**Local snapshots** — created by `bp ss`, stored in `.bp/history/`:
```bash
bp ss -m "add validation"    # Creates: add-validation-a1b2
```

**Git tag snapshots** — tags matching `bp/{path}/*` are auto-recognized:
```bash
git tag bp/commands/v1-release    # Appears in bp log as [tag]
```

Git tags are materialized on demand into `.bp/cache/` when needed as dependency targets. `bp ss` never creates git tags — it only creates local snapshots.

### Scope and Discovery

Each `BLUEPRINT.yaml` defines its own **scope** (which folders it manages). Default scope: the directory containing the blueprint.

**Child wins rule**: If a subdirectory has its own `BLUEPRINT.yaml`, it is excluded from the parent's scope automatically.

```
project/
├── BLUEPRINT.yaml              # Scope: project/ (excluding src-go/)
└── src-go/
    ├── BLUEPRINT.yaml          # Scope: src-go/ (excluding commands/)
    └── commands/
        └── BLUEPRINT.yaml      # Scope: commands/
```

`bp` recursively discovers all blueprints and builds the dependency graph.

---

## Workflow

### Creating Snapshots

```bash
# 1. Edit code in working tree
# 2. Validate + test + freeze
bp ss -m "feature: new parser"

# What happens:
#   - BLUEPRINT.yaml validated
#   - tests.verification commands run
#   - Code copied to .bp/history/{id}/
#   - Dependency symlinks created (pointing to pinned versions)
#   - Files set to read+execute (0555)
#   - state.yaml and .bp/current updated
```

### Upgrading Dependencies

```bash
# Upgrade to latest available snapshot
bp upgrade ./yamlparser

# What happens:
#   - Finds latest snapshot of yamlparser
#   - Updates pinned version in state.yaml
#   - Runs tests — if fail: rollback + mark rotten
#   - If pass: pin is updated
```

### Pinning to a Specific Version

```bash
# Pin to any snapshot (upgrade or downgrade)
bp upgrade --to stable-c3d4 ./yamlparser

# Works with:
#   - Local snapshot IDs
#   - Git tag snapshot IDs
#   - Partial ID prefix matching
```

### Checking Status

```bash
bp status              # Health check: fresh/stale, dependency upgrades
bp log -n 10           # Snapshot history (local + git tags)
bp deps                # Dependency graph
bp diff id1 id2        # Compare two snapshots
```

### Agentic Compilation

```bash
bp implement           # Compile from blueprint + auto-snapshot
bp implement --no-snapshot   # Compile without snapshotting
bp cancel              # Abort active compilation
```

---

## Blueprint File Structure

### Minimal

```yaml
_meta:
  version: "0.1.0"

intent: |
  What this package does and why
```

### Full

```yaml
_meta:
  version: "0.1.0"
  status: draft           # draft | ready | deprecated
  state_dir: ".bp"        # Default
  mode: meek              # meek | ro | hide
  root: true              # Mark as project root

intent: |
  Purpose description

api:
  exports:
    - name: Parser
      type: interface

implementation:
  structure:
    - main.go
    - util.go
    - commands/:
        has_blueprint: true

dependencies:
  internal:
    - path: ./yamlparser
    - path: ./commands

tests:
  verification:
    - go test ./...
```

### File Variants

| File | Purpose |
|------|---------|
| `BLUEPRINT.yaml` | Main definition (intent, api, dependencies) |
| `BLUEPRINT.api.yaml` | API section (separated for large projects) |
| `BLUEPRINT.spec.yaml` | Implementation spec + test scenarios |

---

## State Directory (`.bp/`)

```
.bp/
├── current              # Active snapshot ID (text file)
├── state.yaml           # Pinned deps, file hashes, dependents
├── impl.lock            # Present only during compilation
├── history/
│   └── {snapshot-id}/   # Immutable snapshot directories
└── cache/               # Materialized git tag snapshots (disposable)
```

### state.yaml

```yaml
snapshot_id: "add-parser-a1b2"
deps:
  "./yamlparser":
    pinned: "stable-c3d4"       # Currently used version
    latest: "new-api-e5f6"      # Newest available
    api_hash: "sha256:..."
    api_changed: true            # Breaking change detected
    rotten: false                # true if upgrade tests failed
dependents:
  "../root":
    using: "add-parser-a1b2"    # Reverse tracking
```

---

## Snapshot ID Format

| Pattern | Example | When |
|---------|---------|------|
| `{slug}-{hash4}` | `add-validation-a1b2` | With message |
| `ss-{hash8}` | `ss-a3f2b7c1` | Without message |

Slug: lowercase, max 20 chars. Hash: SHA-256 truncated.

---

## Modes

| Mode | Working Tree | After Compile | Use Case |
|------|-------------|---------------|----------|
| **meek** (default) | Always writable | No change | Development |
| **ro** | Writable during compile | Read-only | Production |
| **hide** | Visible during compile | Files deleted | Proprietary |

---

## Rotten Snapshots

When `bp upgrade` runs tests and they **fail**:
1. Pin is rolled back to previous version
2. Target snapshot marked `rotten: true`
3. All commands warn about rotten dependencies
4. Future upgrades skip rotten snapshots (use `--force` to override)

---

## Multi-Language

```bash
bp new-lang js           # Create src-js/ from src-go/
bp remove-lang ts        # Archive src-ts/ → .bp/archive/
bp restore-lang ts       # Restore from archive
bp purge                 # Clean archives and old snapshots
bp map --langs           # Compare API hashes across languages
```

API blueprints are **symlinked** (shared interface). Spec blueprints are **copied** (language-specific).

---

## Command Reference

| Command | Description |
|---------|-------------|
| `bp ss -m "msg"` | Create snapshot (validate + test + freeze) |
| `bp implement` | Agentic compile + optional snapshot |
| `bp upgrade [dep]` | Update dependency pin to latest |
| `bp upgrade --to <id> <dep>` | Pin dependency to specific version |
| `bp status` | Check freshness and dependency state |
| `bp log [-n N]` | Snapshot history (local + git tags) |
| `bp diff [id1] [id2]` | Compare snapshots |
| `bp show [id]` | View snapshot contents |
| `bp deps` | Dependency graph (topological order) |
| `bp plan` | List stale packages (leaf-first) |
| `bp validate` | Check blueprint syntax and schema |
| `bp map` | Full project snapshot map |
| `bp init` | Create new BLUEPRINT.yaml |
| `bp cancel` | Abort active compilation |
| `bp new-lang <lang>` | Create language variant |
| `bp remove-lang <lang>` | Archive language |
| `bp restore-lang <lang>` | Restore archived language |
| `bp purge` | Clean old snapshots and archives |

### Common Flags

| Flag | Where | Effect |
|------|-------|--------|
| `--no-recursive` | Most commands | Run on single package only |
| `--skip-tests` | `bp ss` | Skip test verification |
| `--no-snapshot` | `bp implement` | Compile without snapshotting |
| `--all` | `bp upgrade` | Upgrade all dependencies |
| `--safe` | `bp upgrade` | Skip if API changed |
| `--force` | `bp upgrade` | Allow rotten snapshots |
| `--to <id>` | `bp upgrade` | Pin to specific version |
| `-n <count>` | `bp log` | Limit history entries |

---

## Key Rules

1. **Blueprint first, code second** — Write or update BLUEPRINT.yaml before implementing
2. **Snapshots are immutable** — Never modify files inside `.bp/history/`
3. **Working tree is always live** — Edit freely, snapshot when ready
4. **Leaf-first order** — Build dependencies before dependents (`bp plan` shows the order)
5. **Pin explicitly** — Dependencies use exact snapshot IDs, not "latest"
6. **Test before snapshot** — `bp ss` runs verification by default
7. **Review API changes** — `api_changed: true` means breaking change; review with `bp diff`
