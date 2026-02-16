# Blueprint Monorepo - Agent Guide

This monorepo contains the entire Blueprint ecosystem. Each package is self-contained with its own BLUEPRINT.yaml.

## Packages

| Package | Language | Depends On |
|---------|----------|------------|
| bp-tunnel | Python | - |
| bp-agent | Python | bp-tunnel (optional) |
| bp-multi | Python | bp-agent, bp-tunnel |
| bp-cli | Go | - |
| bp-tui | Go | - |
| bp-bench | Python | bp-agent, bp-multi |

## Dependency Order (leaf-first)

When making cross-package changes, follow this order:

1. `bp-tunnel` (no internal deps)
2. `bp-agent` (imports bp-tunnel optionally)
3. `bp-multi` (imports bp-agent + bp-tunnel)
4. `bp-cli` (standalone Go)
5. `bp-tui` (standalone Go)
6. `bp-bench` (uses everything)

## Working with Packages

Each package has its own blueprint. Read the relevant BLUEPRINT.yaml before modifying a package.

```bash
# Validate all blueprints
bp validate

# Check what needs work
bp plan

# See dependency graph
bp deps
```

## Build & Test

```bash
# Go packages
cd bp-cli/src-go && go build -o ../bp ./cmd/bp && go test ./...
cd bp-tui/src && go build -o ../bp-tui . && go test ./...

# Python packages
pip install -e bp-agent/ bp-tunnel/ bp-multi/
cd bp-agent && pytest
cd bp-tunnel && pytest
```

## Rules

1. **Read BLUEPRINT.yaml first** before touching any package
2. **HUMANS.md is read-only** - never modify, it contains the developer's personal notes
3. **Leaf-first** when making cross-package changes
4. **Each package versions independently** - no monorepo-wide version
5. **Snapshot before moving on** - use `bp ss` after completing a package change
