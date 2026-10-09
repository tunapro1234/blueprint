# W2 plan: modules, zero-change install, uninstall

Owner: bp-modules. Branch: feat/modules. Base: dev de916f9.

Goal: installing bp changes nothing; every environment-changing feature is an
opt-in module; existing installs keep behaving exactly as they do today.

## 1. `internal/modules`

- Registry of modules, each with: name, one-line description, the config
  subtree it owns, `Enable`, `Disable`, `Conflict` hooks.
- Modules: `sessions`, `bar`, `accounts`, `wa`, `ui`, plus `monitor` (the
  owner server's periodic jobs with hard-coded `/srv/monitor` and
  `/srv/blueprint/scripts` paths). The registry is a slice, so adding one is a
  single entry.
- Enabled state lives in config under `modules` as a map of name to bool
  (`modules: {sessions: true, bar: true}`). `config.Config` gains
  `Modules map[string]bool` and `ModulesSet bool` (key present). YAML
  `KnownFields` accepts the new key.
- Config writer (new, none exists today): `config.SetModules(path, map)`
  rewrites only the `modules` key. JSON keeps top-level key order and every
  unknown key; YAML goes through `yaml.Node` and keeps comments. Atomic
  temp-and-rename, same mode, under a lock in the state dir.
- Change journal: `<stateDir>/modules/<name>.json` lists every change enable
  made (file created, line appended, file replaced with backup, tmux option
  set with its previous value, process started). Disable replays it in reverse
  and only undoes a change whose target still holds what bp wrote; anything a
  user changed since is reported and left alone.
- Conflict checks, refusing enable unless `--force`:
  - `bar`: a global tmux `status-right`/`status-format` that is not tmux's
    default and not bp's: offer `#(bp bar)` instead of replacing it.
  - `accounts`: another tool manages Claude credentials (`apiKeyHelper` in
    Claude settings, `CLAUDE_CODE_OAUTH_TOKEN`/`ANTHROPIC_API_KEY` in the
    environment, known switchers' state dirs).
  - `wa`: another WhatsApp/Baileys process that is not bp's bridge.
  - `sessions`: an existing `claude`/`codex` shell function or alias is kept,
    reported as a note (not a refusal; the wrapper already yields to it).
- `modules.Enabled(name)` is the shared-contract entry point; `main` loads the
  set once after config load. Audit: one TODO in the enable/disable path until
  `internal/audit` lands on dev, then `audit.Append`.

## 2. CLI

- `bp enable <module> [--force] [--dry-run]`
- `bp disable <module> [--dry-run]`
- `bp modules [--json]`: name, enabled, description, conflicts, recorded
  changes.

## 3. Upgrade migration

When a config file exists and has no `modules` key, detect what the install
uses and record it (written once, under the lock; if the write fails the
derived set is still used in memory so behavior never changes):

| Module | Detected when |
|---|---|
| sessions | legacy server home, or a book has agents/coordinator, or the shell.sh rc line is present, or `main/onboarding.json` exists |
| bar | sessions detected (bp styles every session it opens today) |
| accounts | `claudeAccounts.autoSwitch` or `keepAlive` true, or stored account slots exist |
| wa | `waOutbox` set (the legacy default sets it; `bp wa` needs only the outbox) |
| ui | legacy server (dash-server runs today), or `usageBin`/`usageHistory` set |
| monitor | legacy server |

Existing rc lines and skill files found during migration are recorded in the
journal so `bp disable`/`bp uninstall` can remove them later.

Test: a copy-shaped fixture of a full install (legacy-shaped config.json with
fed, p2p, codex, claudeAccounts, two books, no `waBridge` key, no `bar`) is
upgraded in a temp root; asserts every gate the server uses reports the same
answer as before, config bytes outside `modules` are unchanged, and nothing
outside the temp root is written.

## 4. Zero-change installer and first run

Gate every environment change found in the survey:

| Site | Gate |
|---|---|
| `bp setup` rc line + `shell.sh` wrappers | `sessions` (moves into `bp enable sessions`; `bp setup` keeps refreshing it when enabled) |
| `configureLocalBar`, `applyOpenBar`, `applyAttachBar`, daemon bar renderer | `bar` |
| daemon `keepalive` (reopens the coordinator) | `sessions` |
| daemon claude-accounts job, `bp account switch/auto/bind/login` | `accounts` |
| daemon wa-bridge, `bp wa send` | `wa` |
| daemon dash-server, `bp dash` | `ui` |
| daemon usage-pulse, usage-watch, watch-radar, hermes-usage, watch-reset, busy/merge/pane-sanity alarms to `server-main` | `monitor` |
| `bp onboard` (opens a coordinator) | explicit command; enables `sessions` and `bar` with a printed note |
| `install.sh` default path: tmux auto-install, `bp setup` rc edits, auto `bp onboard` | removed: installs the binary, writes config with `modules: {}`, installs the agent hint, prints next steps |
| `install.sh --server/--client`: tmux.conf block | kept as is for old users until blueprint says otherwise; the block is recorded so uninstall can remove it |
| `bp update` rerunning `bp setup` | setup itself becomes zero-change for modules that are off |

Explicit commands (`bp open`, `bp run`, `bp attach`) keep working with every
module off; they just do not restyle tmux unless `bar` is on.

## 5. `bp uninstall [--purge] [--dry-run]`

Removes only what bp recorded: every module journal (disable in reverse), the
agent hint files, the binary link it installed (only if it still points at a
bp binary), the tmux.conf block under bp's marker, the rc source line, a
systemd unit only if bp installed it. Keeps state, logs, books and config
unless `--purge`. Prints each action.

## 6. Agent hint

Until W3 lands an MCP server: a short skill/hint file (`skills/bp/SKILL.md`
for Claude Code and Codex, written only if absent or bp-marked, recorded in
the install manifest). Draft text goes to blueprint for approval before it
replaces the current long skill.

## Order

1. config `modules` key + writer + tests
2. `internal/modules` registry, journal, detection, conflicts + tests
3. `bp enable/disable/modules`
4. gates at every site in the table, test fixtures updated to enable what
   they exercise
5. migration fixture test
6. install.sh zero-change path, `bp uninstall`
7. agent hint (after blueprint approves the text)

Each step: commit, `bp msg blueprint` with commit and test commands.
Tests: `flock /run/lock/bp-gobuild.lock nice -n 10 go test ./...` after the heat
check; temp roots only, `env -u TMUX -u TMUX_PANE`.

## Appendix: agent hint (done)

Blueprint's text (docs/agent-hint.md) ships as `internal/bpskill/HINT.md`.
`bp setup` installs it as `skills/blueprint/SKILL.md` (same marker, so managed
copies are updated and user copies are kept) on installs where only
communication is in use. Installs with `sessions` enabled, and the legacy
server, keep the operational skill (`internal/bpskill/SKILL.md`); enabling or
disabling `sessions` switches between the two. The written files are recorded
in the install journal, so `bp uninstall` removes them. An MCP server entry
replaces this once W3 lands.
