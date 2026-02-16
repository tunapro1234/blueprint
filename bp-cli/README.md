# bp-cli

> For the developer's vision and thoughts behind this project, see [HUMANS.md](../HUMANS.md).

## Overview
Blueprint (`bp`) is a local, file-system based tool for authoring and tracking `BLUEPRINT.yaml` files across a repository. Each folder is treated as a package; a blueprint captures intent, API, implementation structure, and tests. `bp` validates blueprints, checks staleness, snapshots blueprint history, and shows diffs between snapshots.

This tool is **snapshot-based** (not Git-based): it stores state alongside each package under a configurable state dir (`_meta.state_dir`, default: `.blueprint/`).

## Features
- **Validate** blueprint files (syntax + minimal schema rules)
- **Status** check for changes vs. last snapshot
- **Snapshot history** with logs and diffs
- **Dependency graph** across nested packages
- **Show** a specific snapshot’s blueprint
- **Blueprint parser** tailored for Blueprint content (tolerant of commas, colons, type annotations, etc.)

## Install / Build
Requires Go 1.22+

```
cd src-go && go build -o ../bp ./cmd/bp
```

## Quick Start
```
./bp init
./bp validate
./bp impl                    # agentic derleme + snapshot
./bp ss -m "initial"         # manuel snapshot
./bp status
./bp log
./bp diff
```

Default (recursive) validation:
```
./bp validate
```

## Root Blueprint
Mark a blueprint as project root with `_meta.root: true`. When running commands without an explicit path from a subdirectory:
- bp finds the root blueprint by walking up the directory tree
- Commands run recursively from the root
- Use `.` for current package only (still recursive): `bp validate .`
- Use `--no-recursive` to target only the current package

```yaml
# Root BLUEPRINT.yaml
_meta:
  root: true
```

## Commands
- `bp validate [path] [--no-recursive]`
- `bp status [path] [--no-recursive]`
- `bp plan [path] [--no-recursive]`
- `bp deps [path] [--upgrades]`
- `bp map [path] [--no-recursive]` - project map showing all packages and dependencies
- `bp init [path]`
- `bp log [path] [-n|--count]`
- `bp diff [path] [id1] [id2]`
- `bp show [path] [id]`
- `bp impl [path] [--no-snapshot|-ns]` (alias of `bp implement`)
- `bp ss [path] [-m|--message] [--skip-tests]`
- `bp new-lang <lang> [--from <lang>] [--root <path>] [--target <dir>]`
- `bp upgrade [dep-path] [--safe] [--all] [--force]`
- `bp cancel [path]`

## Development Modes
Configure with `_meta.mode` (default: `meek`):

| Mode | `bp impl` | `bp ss` |
|------|-----------|---------|
| **meek** (default) | busy flag only | snapshot only |
| **ro** | derleme boyunca writable, sonra read-only | izin değiştirmez |
| **hide** | derleme için görünür, sonra gizle | snapshot alır (kod yoksa boş) |

## Snapshot Model
Each package keeps its own snapshot state under the configured state dir (default `.blueprint/`):
```
.blueprint/
  current
  state.yaml
  history/
    {snapshot_id}/
      BLUEPRINT.yaml
      meta.yaml
      code.go                   # kod direkt (impl/ yok)
      util.go
      yamlparser/ → ...         # symlink direkt (deps/ yok)
      commands/ → ...
```
Working tree'de dependency klasörleri gerçek dizinlerdir; symlink'ler sadece history içindeki snapshot'larda bulunur.

Snapshot IDs:
- With message: `{slug}-{hash4}` (e.g. `add-validation-a1b2`)
- Without message: `ss-{hash8}` (e.g. `ss-a3f2b7c1`)
Timestamp is stored in `meta.yaml`.

## Multi-language Roots
- Default language root is `src-go/` (configurable)
- Additional language roots are discovered via `language_policy.language_roots` and `language_policy.language_root_patterns` (default pattern: `src-*`)
- `bp new-lang <lang>` creates a new root (default target uses `language_policy.language_root_template`, default `src-{lang}`):
  - Package directories are created
  - Blueprint files that contain an API section are symlinked; others are copied
  - If a package has a single blueprint file with API+spec, `bp` prompts to split it
  - Implementation files are **not** copied
- `bp map --langs` shows API hash alignment across roots

Example config:
```
language_policy:
  default_root: src-go
  default_language: go
  language_root_template: src-{lang}
  language_roots:
    - src-go
  language_root_patterns:
    - src-*
```

`bp ss` will:
1) Validate the blueprint
2) Run `tests.verification` commands (unless `--skip-tests`)
3) Copy files to `history/{id}/` (directly, no impl/)
4) Create symlinks for dependencies (directly, no deps/)
5) Mark all files under `history/{id}/` as read+execute (chmod 0555, symlinks skipped)
6) Update state.yaml + current
7) Leave working tree untouched

`bp impl` will:
1) Validate + agentic compile
2) Auto-snapshot unless `--no-snapshot`
3) Toggle mode idle state (ro/hide)
4) Clear impl.lock (busy flag)

`bp upgrade` will:
1) Update dependency pins in state.yaml
2) Snapshot symlink'leri bir sonraki `bp ss`/`bp impl` ile güncellenir

## Blueprint File Discovery
Supported patterns include:
- `BLUEPRINT.yaml`, `.BLUEPRINT.yaml`
- `BLUEPRINT.*.yaml`, `.BLUEPRINT.*.yaml`
- `*.BP.yaml`, `*.bp.yaml`

If multiple are present, the tool prefers `BLUEPRINT.yaml`.

## Parser Behavior (Blueprint-specific)
Blueprints often contain values like:
```
type: map<string, any>
signature: "(ctx: Context) -> Result"
```
The custom parser:
- Uses indentation as structure
- Accepts colons/commas inside scalar values
- Preserves numbers as strings (unless explicitly boolean/null)
- Supports literal (`|`) and folded (`>`) blocks
- Converts tabs to 2 spaces and emits a warning

## Repo Layout
```
BLUEPRINT.yaml     # Root project blueprint
README.md          # This file
.gitignore         # Git ignore rules
src-go/            # Go implementation (default root)
```

## Development
Run tests:
```
cd src-go && go test ./...
```

Validate all blueprints (default recursive):
```
./bp validate
```

Build:
```
cd src-go && go build -o ../bp ./cmd/bp
```

## Notes
- This tool intentionally does **not** require Git.
- Snapshot history lives next to each package and is independent.
- `tests.verification` is executed in the blueprint’s directory and must be valid shell commands.

## License

GPL-3.0
