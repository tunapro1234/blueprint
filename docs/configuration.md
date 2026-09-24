# bp configuration

The laptop setup creates a documented `~/.blueprint/config.yaml` file. If
`BP_HOME` is set, the file is stored in that directory. Existing files are left
untouched; running setup again preserves settings, the agentbook, and
conversation records. The file mode is `0600`.

```sh
bp config path
bp config check
```

The first command prints the path of the active file; the second reports whether
it is valid. Neither command prints token or password values. Defaults are used
when no file exists; `bp setup` creates the example file.

## Basic example

```yaml
# Relative paths are resolved from the configuration file's directory.
agentbooks: [agentbook.json]
msgqRoot: msgq
stateDir: state
localObservation: true
bar:
  context: used
  widgets: [ctx, temp, queue, model, quota]
waBridge: false
```

The list controls the order in the bottom bar. `ctx` is the context amount;
`temp` is the cache estimate or the age of the latest measurement; `queue` is
the number of waiting messages; `model` is the model/effort; and `quota` is the
quota used. `talk` adds the age of the latest user message and `clock` adds the
time. `widgets: []` hides all metric widgets. The activity symbol remains
separate.

The bar is installed automatically in local bp sessions. If an older install
still shows the default green tmux bar, rerun the current installer to repair
the bar in open bp sessions without closing the agents. `bp setup` with the
current binary performs the same repair. Other tmux sessions and global
key/mouse settings are unchanged.

## Local model and context observation

`localObservation: true` is the default for local launches. The agentbook does
not need to be prepared by hand: `bp run` creates it and records the session.
`stateDir/local/` stores only PID mappings, model/context measurements, and small
launch-specific settings files (in a private directory with mode 0600 files).
Conversations are not moved or copied: Claude uses its own `projects/` records
and Codex uses its own `sessions/` records. Custom `CLAUDE_CONFIG_DIR` and
`CODEX_HOME` values are preserved. Setting `stateDir: ~/.bp/state` puts bp's
small runtime records in that directory.

Claude receives a launch-scoped SessionStart observer and status-line reader.
Existing `--settings`, hooks, and status-line command input/output are
preserved; global Claude settings files are not changed. The status line's real
`context_window` input supplies capacity: [Claude status data](https://code.claude.com/docs/en/statusline#available-data).
For Codex, the writer lock actually held by the pane process selects the thread;
the existing JSONL record supplies the model, effort, and tokens. There is no
newest-file/cwd guess and no bypass of Codex hook trust. Linux uses `/proc`, and
macOS uses `ps`/`lsof`. This method requires Codex versions that create
`thread-writer-locks`; read-only live verification was performed with server
version 0.153.4. Codex launched explicitly with `--remote` stays on the existing
remote observation path; no new remote daemon is installed.

`bar.context: used` is the default on both server and laptop. The optional
`remaining` setting reports the amount left in the reported window, for example
`170k left`; the automatic compact threshold may differ. `used` reports the
amount consumed. Before a measurement arrives the bar shows `ctx —`; when only
the used amount is known but the window is not, it shows `30k/?`. Capacity is
never invented, and the first Codex model/token measurement may arrive after
the first turn. A compact/resume notice does not refresh a stale context
measurement. The quota indicator is separate and shows the percentage used; no
fake limit is shown when the laptop has no quota source.

New CLI launch settings are not injected into a session left open by an older
version. The installer repairs the bar and, when needed, provides instructions
for the next launch. Reopening through the native `claude --resume` or
`codex resume` selector preserves the conversation and enables automatic
mapping. `localObservation: false` disables observation for future launches.
This mapping grants neither sender authority nor hierarchy authority.

## Other available settings

| Key | Purpose |
| --- | --- |
| `agentbooks` | Authoritative agentbook files |
| `tokenAgentbooks` | Agentbooks used for token reports; defaults to `agentbooks` on laptops |
| `msgqRoot`, `stateDir` | Message queue and local runtime records |
| `usageHistory`, `usageBin` | Existing quota data and helper-program directory |
| `clipboardDir` | Clipboard files |
| `waBridge`, `waOutbox`, `waStore` | Existing WhatsApp integration |
| `codex.sockets` | Read-only Codex app-server observation sockets |
| `ntfy.url`, `ntfy.topic`, `ntfy.token` | Existing notification integration |
| `fed.mode`, `fed.listen`, `fed.hub`, `fed.peerName`, `fed.token`, `fed.expose` | Existing federation settings |

Optional integrations are disabled on laptops by default. Fields use the same
names as the existing JSON settings; YAML alone does not start a new service or
connection. `~/` expands to the home directory. There is no `$VAR` expansion,
command execution, or secret configuration merge. Federation's loopback, TLS,
and token checks also apply to YAML.

## Interactive remote servers

The `remotes` mapping configures terminal transports, independently of P2P
messaging peers. Use `bp remote add`, `bp remote list [--json]`, and
`bp remote rm` so updates are locked and written atomically. Each entry accepts
`host`, `port`, `user`, `identity`, `transport` (`mosh` or `ssh`), `moshPorts`,
and optional `elevate` (for example `sudo -i`). No password, token, or private
key content is stored; `identity` is only a file path.

`bp attach agent@server` runs `bp attach agent` on that server. `bp shell server`
opens an interactive shell, and `bp shell server agent` delegates to the same
remote attach command, so a typo cannot create an empty tmux session. The words
`list`, `add`, and `rm` are reserved after `bp remote`; every other argument
keeps the existing Claude `/remote-control` behavior.

```yaml
remotes:
  server:
    host: server.example
    port: 22
    user: tuna
    identity: ~/.ssh/server
    transport: mosh
    moshPorts: 60000:61000
    elevate: sudo -i
```

## Portable project schemas and history

`bp schema export [<project-dir>] [--lead <agent>]` writes the version 1 agent
tree to `<project>/.blueprint/schema.yaml`. It includes registered agents whose
folders are inside the project and only preserves parent links to agents in
that exported set. Paths are relative to the project root. The schema accepts
`history: none|file|remote`; each agent records its name, folder, runtime, role
and color, with optional model, effort and launch mode.

`bp open ... --fresh` ignores a stored conversation binding and starts a new
conversation. It cannot be combined with `--resume` or `--thread`. OpenCode
agents can be launched in managed sessions with `bp open ... --opencode`.

`bp continue [<project-dir>] [--dry-run] [--yes]` validates folders, checks for
name collisions and prints the complete plan before opening agents parent
first. Live agents remain open. Closed agents with `history: none` start fresh;
file or remote history is imported before a closed agent resumes. A name already
registered for a different folder is an error; there is no prefix override.
The first run in each project folder and every change to the schema content
requires confirmation. Non-interactive use must pass `--yes`; that approval is
stored under the local bp state directory by project path and schema hash.

`bp history export [<project-dir>] [--agent <name>] [--keep N]` copies Claude
transcripts and Codex rollout files plus their session-index rows into
`.blueprint/history`. It keeps one session per agent by default. Existing files
with different contents are never overwritten. `bp history export --stdout
--agent <name>` writes the same layout as a tar stream for remote transfer.
Portable history currently supports Claude and Codex; use `history: none` for
Hermes or OpenCode schemas.
`history: remote` accepts `ssh://user@host[:port]` or a server name from the
local `remotes:` block; it uses SSH and does not put credentials in the project
schema.

History is ignored by default because it can contain private conversation data.
Schema export creates or updates `.blueprint/.gitignore` with `history/` while
preserving existing rules. To commit conversations intentionally, add them
explicitly with `git add -f .blueprint/history`. `history: none` does not move
conversation files. `bp rename` prints a manual-update hint when a schema still
declares the old agent name; it never edits the project schema.

## Compatibility and applying changes

`config.yaml`, `config.yml`, and legacy `config.json` are supported. bp reports
an error if more than one exists in the same directory. Rerunning setup does not
create YAML over a JSON installation. When migrating from JSON, first make a
copy or backup and leave exactly one active file. Existing server JSON is
preserved in this release.

YAML with unknown keys, duplicate keys, wrong types, or multiple documents is
rejected. bp never falls back to defaults and proceeds when YAML is malformed.
Legacy JSON loading behavior is preserved; `bp config check` also rejects
malformed JSON.

New CLI invocations and bottom-bar updates reread the file. Long-running daemons
and local workers that are already open keep their own configuration copy; their
settings change on the next natural launch or an appropriate service refresh.
This feature does not change an agent's model/effort or tmux scroll settings.

Mosh wake checks and automatic summary messages to Claude are not yet
implemented.

## Local mouse and colors for external applications

`localMouse: true` (the default) enables tmux mouse scrolling and selection only
in `bp run` sessions. `false` disables it. `bp setup --shell bash` or
`bp setup --shell zsh` also applies the setting to open local sessions under the
same `BP_HOME`; it does not restart the agent. Global tmux settings, the prefix,
and user key bindings are preserved. Copying to the system clipboard depends on
terminal clipboard support.

`bp color <agent>` prints the agent's bottom-bar color as `#rrggbb`.
`bp color <agent> --json` returns the agent, index (0–255), and hex fields. bp
reads the live Claude color badge, then the stored color, then its default. The
HEX values for the first 16 ANSI colors are the xterm defaults; custom terminal
palettes may display them differently. The command does not change the window
border: a window manager must map windows to agents and apply the color. A color
is not evidence of identity or authority.

`bp color <agent> blue` selects a persistent bp color for any harness and takes
precedence over the current Claude color. `auto` removes that selection and
returns to the native/stored color. Available names are red, orange, yellow,
green, cyan, blue, purple, pink, gray, and white; xterm indexes 0–255 are also
accepted. Open sessions update on the next bar refresh. The selection belongs to
the agent name; use `bp run --name work codex` for a stable local name.

## Default agent color for this computer

```yaml
bar:
  context: used
  defaultColor: purple
```

Add this to the existing `bar` section; do not create a second `bar` section.
Precedence is: per-agent `bp color` → `bar.defaultColor` → native/stored color →
bp default. Removing the field or setting it to an empty string restores the
legacy automatic behavior. Color names and xterm indexes 0–255 are supported.
Open sessions apply the change on the next bar refresh; it does not change the
agent's model or native TUI theme.

## Delivery after compact

After a manual compact completes, the `compact_boundary` (`trigger=manual`),
compact summary, and native `Compacted` command output together confirm that the
turn has closed. Automatic compact is not a closure. A real new turn, an
incomplete record, or uncertain state continues to block delivery. No extra
observer hook is required; existing local sessions can read these records with
the new bp message/worker version.

`bp q --retry` processes the existing queue once with the current binary without
creating a new message. It does not bypass busy, draft, or identity checks. Use
this path without closing the agent when a local worker left over from before an
update is still running the old binary.

## Native /rename and live identity display

Claude's `/rename orch` is observed through the verified transcript. The Codex
`/rename` title is read from native `session_index.jsonl` using the bound thread
UUID; cwd or screen text is not identity evidence. `bp name` shows the new name
in the bottom bar, and `bp status --json` returns `display_name` next to the
stable `name`. `bp msg orch`, `bp peek orch`, and `bp color orch` accept the
display name when it matches exactly one live session. Duplicate titles produce
an error. Canonical agent names take precedence over titles; display metadata
cannot impersonate the protected coordinator or another registered agent.

The tmux session name, queue target, parent, and authority identity remain
stable. Title observation is saved in the small `nativeTitle` metadata; bp does
not revert to an old title after the rename record falls out of the transcript
tail. Names mentioned in user/tool text and sidechain records are not treated as
titles. Updating does not require a new prompt or hook.

When Embedded Codex has a live writer lock, or Claude has a verified process
session ID, telemetry is read independently of the old launch pin. This
observation does not change the `identityThreadId` authority pin. Remote Codex
mapping and pin checks are preserved.

## Local Claude continue/resume ownership

`claude -c` / `--continue` resolves the latest local conversation UUID once and
starts Claude with the explicit UUID. If that UUID is already bound to a live bp
pane, bp attaches to that pane; it does not create a process, message, or agent
record. In that case, model, permission, and prompt arguments from the new
command do not apply to the existing process. A closed conversation reuses its
previous agent name. Symlinked working directories are normalized to physical
paths, while the old logical-path transcript directory is also searched for
continue.

The `stateDir/local-resume/` lock serializes the ownership check with tmux
creation. A persistent claim closes the gap until the `_session` record appears;
a live observation verified by PID takes precedence. If a UUID has multiple
live legacy owners, bp prints their names and stops without closing a pane or
choosing a winner. Equally recent but different conversations are not selected
silently either.

This protection covers `bp run claude -c` and UUID-bearing `--resume` on the
same `BP_HOME` and tmux server. UUID-less `claude --resume` / `-r`,
`codex resume`, and title queries pass unchanged to the native CLI. The CLI draws
its own resume screen; bp neither generates a numbered list nor reads selection
keys. Until a conversation is selected, bp knows the temporary pane but does not
invent a thread/model or deliver messages. Existing native callback/writer-lock
observation binds the conversation after selection.

Selecting an open conversation in the native picker does not pass through the
pre-launch attach protection above; the native CLI owns the active-writer check
and warning. bp does not close the existing writer or remove its lock.
`--fork-session` intentionally creates a new conversation and therefore does not
reattach. This protection does not lock Claude processes outside bp or later
`/resume` transitions from inside the TUI. Old records and transcripts are not
deleted, and existing multiple writers are not merged automatically.

## Update notification

`updateCheck: true` enables at most one background check per day on local
interactive launches. If a new release is present in the ready cache, the
terminal shows a short notice; this does not create an agent message or model
turn. `false` disables the check. `bp update --check --json` provides a manual,
machine-readable check.

Machine mode is explicit: `BP_HOME` selects a configuration directory. Without
it, bp reads an optional `/etc/blueprint/home` selector written by
`install.sh --server`; otherwise it uses `~/.blueprint`. A checkout at
`/srv/blueprint` alone does not activate server integrations. Local installs are
the default; `--client` and `--server` must be selected explicitly. On a
configured server, set `BP_HOME` to your personal configuration directory to use
a separate local installation.

## Changing the working directory while preserving a conversation

This section records research results; BP does not yet provide a `move` command
or automatically move directories or transcripts.

- Codex accepts `resume` with a specific UUID and `--cd`. `tui.resume_cwd` can
  select `current` or `session`; without the setting, the native selector is
  shown when the directories differ. Do not use `fork` to continue the same
  conversation. See the [CLI reference](https://developers.openai.com/codex/cli/reference/)
  and [configuration reference](https://developers.openai.com/codex/config-reference/).
- When Claude resumes by UUID, current versions also search other project
  records (documented threshold: 2.1.223). If the old project directory no
  longer exists, the native picker can continue in the current directory;
  selecting from another existing project may instead offer a `cd`/resume
  command. Finding the UUID alone therefore does not prove that the new cwd was
  applied. See [Claude resume](https://code.claude.com/docs/en/cli-reference)
  and [session behavior](https://code.claude.com/docs/en/sessions).

The recommended BP integration flow is: preview the old and new physical paths
and full thread identity; after a natural CLI exit, resume natively with the same
UUID; prove the new cwd and same thread through the callback or app-server; only
then update the agentbook/local runtime record. Preserve the model, effort,
service tier, and isolation. On failure, leave the old records and files in
place.

More code work is required: `bp open --resume` and local-observation validation
match cwd against the existing record. A transition to a new cwd must not bypass
that validation by changing metadata directly. For Remote Codex, the executor
is the app-server rather than the TUI, so the loaded thread and writer ownership
must also be verified. Moving project files must be a separate, explicit action
from changing the conversation's working directory. No move test has yet been
run with a real user conversation.

## Local Codex launch and sandbox diagnostics

For new local `bp open` registrations, an explicit `--parent` wins; otherwise
BP uses the verified registered caller, or the local coordinator when the caller
is unverified. Sharing a folder with another agent never selects the parent.
Reopening an existing registration preserves its parent unless explicitly
changed.

`role` is descriptive text. Custom roles do not disable local bar maintenance,
rename refreshes or doctor checks. BP identifies its sessions from the matching
local runtime PID/state path, or the session installation marker when
observation is disabled. Changing a role does not clear runtime observations.

Local `bp open --codex` (including explicit resume) uses the same session worker
and bar setup as `bp run codex`. The managed role and parent stay intact. Codex
observations come from the native writer lock and transcript; an observation
path in the book does not imply that Codex writes a Claude-style
`observation.json`. Existing running panes are not replaced by an update. Remote
and legacy server launches retain their existing execution boundaries.

If `bp doctor` reports `tmux_access`, run it from the outer terminal as well. A
denied socket is not evidence that the agent exited. Codex's command sandbox can
deny tmux access while the worker outside that sandbox still updates the bar. BP
does not automatically disable the sandbox or allow the raw tmux socket: that
socket can execute host commands, so it is broader than a status/message
interface. See [Codex Unix socket permissions](https://learn.chatgpt.com/docs/permissions#unix-sockets).
A readable agent label or running worker alone also does not grant sender
authority; missing per-execution thread evidence remains explicitly unverified.
