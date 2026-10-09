# bp command reference

`bp help` prints every command on one screen; this page explains them by task.
Settings are in [configuration.md](configuration.md), the HTTP API, MCP server,
rooms and board in [api.md](api.md), and delivery states in
[message-delivery.md](message-delivery.md).

A fresh install adds only bp's own files and a short skill note for Claude Code
and Codex. Features that change your environment belong to
[modules](#modules); their commands say so when the module is off. With the `sessions` and `bar` modules, bp runs CLI agents in
tmux with guarded messaging, model and context indicators and configurable
colors, on a laptop or on configured servers.

- [Install, update and remove](#install-update-and-remove)
- [Modules](#modules)
- [Agents and messages](#agents-and-messages)
- [Running agents in tmux](#running-agents-in-tmux)
- [Resuming conversations](#resuming-conversations)
- [Archive and agent lifetime](#archive-and-agent-lifetime)
- [Rooms, board, API and MCP](#rooms-board-api-and-mcp)
- [Guided setup](#guided-setup)
- [Configuration and diagnostics](#configuration-and-diagnostics)
- [Shell integration (sessions module)](#shell-integration-sessions-module)
- [Colors, windows and attention](#colors-windows-and-attention)
- [Remote servers](#remote-servers)
- [Portable project trees](#portable-project-trees)
- [Across machines (P2P)](#across-machines-p2p)
- [Audit](#audit)
- [Claude accounts (accounts module)](#claude-accounts-accounts-module)
- [WhatsApp (wa module)](#whatsapp-wa-module)
- [Workflows, monitoring and other commands](#workflows-monitoring-and-other-commands)

## Install, update and remove

### Install

Install and sign in to your preferred agent CLI first, then:

```sh
curl -fsSL https://bp.tunapro.xyz/install.sh | sh
```

The installer supports Linux and macOS 13 or later, on amd64 and arm64. It
downloads the release for your platform, verifies the signed checksum list
(Ed25519) and the binary's SHA-256, checks the new binary with
`bp setup --check`, installs it as `~/.local/bin/bp` and runs `bp setup`. If setup fails, the previous binary is
restored; on a first install the failed binary is kept as
`~/.local/bin/bp.failed-install.*`. The installer refuses to run as a user who
does not own `$HOME` (for example `sudo` with `HOME` kept).

It needs `curl` and OpenSSL with Ed25519 support; on macOS,
`brew install openssl@3` provides one, which the installer finds by itself.

By default nothing changes outside `~/.local/bin/bp`, bp's home
(`~/.blueprint`) and the agent skill files described under
[configuration](#configuration-and-diagnostics); a reinstall also keeps the
previous binary as `~/.local/bin/bp.before-local.*`. Options:

| Option | Environment | Effect |
|---|---|---|
| `--enable <module,...>` | `BP_ENABLE` | run `bp enable` for each listed module after installing |
| `--onboard` | `BP_ONBOARD=yes` | start [guided setup](#guided-setup) on a fresh install that has a terminal |
| `--yes`, `-y` | `BP_YES=1` | allow installing a missing tmux (Homebrew or apt-get) when one of the options above needs it |
| | `BP_VERSION=<x.y.z>` | install that version instead of the latest |
| | `BP_HOME=<dir>` | use another bp home |
| `--local` | `BP_INSTALL_MODE=local` | this mode; the default |
| `--server`, `--client` | `BP_INSTALL_MODE` | the legacy `/srv/blueprint` server layout and its thin clients |

Environment variables go on the installer side of the pipe:

```sh
curl -fsSL https://bp.tunapro.xyz/install.sh | BP_VERSION=1.9.30 sh
```

The installer itself does not need tmux, but bp uses it to find and reach
agents, and the `sessions` and `bar` modules and onboarding run agents in it.
Install it with your package manager, or pass `--yes` together with
`--enable` or `--onboard` to let the installer do it. Reinstalling and
updating preserve existing agents, configuration and records and never start
onboarding by themselves; without a terminal the installer prints the next
command instead of starting an agent. Older installers onboarded by default
and `BP_ONBOARD=skip` turned that off; skipping is now the default.

### Update

```sh
bp version --json
bp update --check
bp update
```

Releases are versioned and signed. The updater verifies the signed manifest and
binary checksum, keeps a backup, and restores the previous binary if setup
fails. Running agents are not restarted. Server installations use an explicit
host rollout so CLI and daemon versions are verified together. `bp update
--check --json` is a manual, machine-readable check.

Local agent startup can show a cached update notice. `updateCheck: false` in
YAML (or `BP_NO_UPDATE_CHECK=1`) disables its daily background check. No model
prompt is sent, and the notice never becomes an agent message.

Fleet updates go further:

```sh
bp update --clis --dry-run
bp update --models claude-opus-5=claude-opus-5-5 gpt-5.6-sol=gpt-6-sol --dry-run
```

`bp update` without fleet flags keeps the binary-only behavior. `--clis` updates
installed native harness CLIs and rolling-restarts eligible live agents;
`--models from=to ...` migrates only matching models while preserving each
agent's observed reasoning effort and launch mode. `--all` combines both
operations. Use repeatable `--agent <name>` to limit the plan and, with
`--clis`, the native harnesses to update. The coordinator is restarted last.
Busy, modal, draft-bearing, unloaded, or incompletely observed agents are
deferred without touching their panes. Closed resumable records retain the
model map for their next `bp open --resume`.

Fleet updates print a plan and require confirmation on a terminal.
Non-interactive runs require `--yes`; `--dry-run` never changes CLIs, panes,
defaults, or records. Add `--json` for the same per-agent result as structured
data. Future-launch defaults change only with `--set-defaults`, which writes
timestamped backups first. The native update commands are configurable
(`cliUpdates` in [configuration.md](configuration.md#native-cli-update-commands)).

### Remove

```sh
bp uninstall --dry-run      # list what would be undone
bp uninstall
bp uninstall --purge        # also delete bp's home and ~/.config/bp; asks first (--yes skips it)
```

`bp uninstall` disables every module in reverse order and undoes what the
installer and `bp setup` recorded: the binary at `~/.local/bin/bp` (only while
it is still bp), the agent skill files, shell startup lines, the lines the
installer added to `~/.tmux.conf`, and the bar on bp's own tmux sessions. A
file you changed after bp wrote it is reported and kept. Configuration, agent
books, state and logs stay in bp's home unless you pass `--purge`, which
refuses a home that lacks bp's marker file. Installer backups
(`~/.local/bin/bp.before-*`) and a systemd unit bp did not install are listed,
not deleted. Native CLI transcripts are never touched. Agents already running
in tmux keep running; close them with `bp close <name>` first if you want them
gone.

A `bp` that bp's installer did not put in place is reported, not removed:
remove an npm installation with `npm uninstall -g @tunapro/blueprint`. On a
release without `bp uninstall`, run `bp setup --disable`, open a new terminal,
check `command -v bp`, remove `~/.local/bin/bp`, and delete the line ending
`# bp local agents` from your shell startup file if you want it gone. If bp is
missing, its shell wrappers fall back to the native CLI.

To roll back an update: installer and updater output names the previous binary
backup. Copy that backup over `~/.local/bin/bp`, then run `bp setup`, or
reinstall a signed older release with `BP_VERSION`. Use npm to change versions
of an npm-managed installation.

## Modules

```sh
bp modules [--json]
bp enable <module> [--dry-run] [--force]
bp disable <module> [--dry-run]
```

Every feature that changes your environment (shell startup files, tmux
options, daemon jobs, credentials) belongs to one module and runs only while
that module is enabled. The enabled set lives under `modules:` in
`config.yaml`. `bp enable` checks for conflicts and refuses when it finds one;
`--force` enables anyway and records the conflict in the audit log. Each change
it makes outside bp's home is recorded in `<stateDir>/modules/<name>.json`;
`bp disable` and `bp uninstall` replay that journal in reverse and undo only a
change whose target still holds what bp wrote. `--dry-run` prints the steps
without changing anything. A running daemon reads modules when it starts;
restart it to apply a change there. An installation from before modules
existed keeps what it already used: those modules are detected and recorded
on its first run.

| Module | Enabling it | Refused when |
|---|---|---|
| `sessions` | writes `~/.config/bp/shell.sh` and one source line in your shell startup file (backed up first), so `claude`, `codex`, `opencode` and `hermes` start through bp; the daemon reopens the coordinator; Claude Code and Codex get the longer operational skill instead of the short note | `shell.sh` exists and was not written by bp |
| `bar` | bp's tmux status bar on the sessions bp opens | your global tmux `status-left` or `status-right` is customized (add `#(bp bar)` to it yourself instead) |
| `accounts` | the `bp account` commands that change logins, and the daemon's account job | another tool manages Claude credentials: `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN` is set, `apiKeyHelper` is configured, or a known account switcher is installed |
| `wa` | `bp wa` and the daemon's WhatsApp bridge | another WhatsApp Web process runs on this machine |
| `ui` | the daemon serves the monitor dashboard on 127.0.0.1:8787; it reads `/srv/monitor/site` or the site named by `DASH_URL` in `~/.config/bp/config`, which defaults to the maintainer's `https://monitor.tunapro.xyz` | |
| `monitor` | the daemon's server monitoring jobs for the `/srv/blueprint` layout | |
| `guard-hooks` | a `PreToolUse` tripwire in the per-agent settings bp gives the Claude sessions it starts; alerts appear in `bp audit --kind guard.reach` | `sessions` or `localObservation` is off, or Claude Code hooks are disabled by policy |
| `compaction-hooks` | writes an OpenCode plugin (`~/.config/opencode/plugins/bp-compaction.js`), a Hermes hook script (`~/.hermes/agent-hooks/bp-compaction.sh`) and one `hooks.pre_llm_call` entry in `~/.hermes/config.yaml`, so OpenCode and Hermes agents keep their bp identity and queued messages after a context compaction; Hermes's in-place compression is not covered ([details](security/compaction-hooks-module.md)) | `sessions` is off |

`bp enable sessions` also accepts `--shell bash|zsh` to choose the startup file
and `--wrappers` for the optional `lush` and `rush` helpers.

`accounts`, `wa`, `ui` and `monitor` are maintainer modules, not supported for
general use yet: they serve the maintainer's own setup (`ui` contacts
monitor.tunapro.xyz, `monitor` expects the `/srv/blueprint` server). Review
your provider's terms before enabling `accounts`.

## Agents and messages

```sh
bp status [--json] [--all]
bp tree [--all]
bp msg work 'Please review the change'   # bp msg [--force-busy] <name> <message...>
bp qstat <channel-id> [--json]
bp q [--retry]
bp qcancel <channel-id>...
bp peek work                              # bp peek <name> [n]
bp whoami
bp announce <message...> [--dry-run]
```

`bp status` lists the agents in your agent books with their live state, cache
and context; `--json` follows [runtime-status.md](runtime-status.md) and adds
`display_name`, `awaiting_user` and `unread`. Closed ephemeral agents are hidden
unless you pass `--all`. `bp tree` shows the hierarchy. Known issue: when tmux
is not installed, or no tmux server is running yet (for example after a reboot,
before the first `bp run`), `bp status`, `bp tree` and the agent list of the
MCP and HTTP API fail with a raw tmux error instead of showing no live agents.

`bp msg` stamps a `[sender]` envelope (never write your own) and enters the
durable queue; every online send has a channel id. A ready agent gets the
message at once (`sent`, `RESULT=delivered CHANNEL=…`); otherwise it is queued
(`QUEUED (channel: …)`) with a `WAITING REASON`. Messages wait when the target
is working, its state is uncertain, or the user is typing. A transport
acknowledgement alone is not proof of agent delivery: inspect the channel
again before reporting its state, because a `queued` result can already have
become `delivered`. A message to an agent without a live session goes to its
offline spool and arrives as a digest when the agent opens
(`queued for <name> (offline; delivered when it opens)`). Canonical names win;
a native display title also works when it matches exactly one live session.

Known issue: on macOS, messages to Codex agents started by bp currently stay
queued with `runtime unknown: waiting for local Codex writer lock and
transcript`, because bp cannot yet see Codex's writer lock there (fix in
progress). For the same reason, messages a Codex agent sends carry the
unverified label `codex?:<thread>` instead of its agent name. Messages to
Claude Code agents, and from Codex to Claude, are delivered.

`bp msg` takes the message from its arguments; it does not read standard
input. Quote it with single quotes: inside double quotes the sender's shell
expands `$(…)`, backticks and `$VAR` before bp sees the text, which matters
most when an agent composes the command. MCP `bp_send` and the HTTP API take
the text as data and avoid the shell entirely.

Inside a bp terminal the pane decides who the sender is; from a plain terminal
it is your login name; a label ending in `?` is unverified. `bp whoami` prints
the evidence as JSON ([sender evidence](message-delivery.md#sender-evidence)).
A message that starts with `/` is a slash command for the target CLI: it goes
without an envelope, only from a verified sender, and only down the hierarchy
(the root, or an ancestor of the target). `--force-busy` (also `--force`) puts
a message in front of a busy agent; it is reserved for infrastructure (the
root coordinator, bp and the WhatsApp bridge), cannot reach a federated
address and never types into a busy Hermes turn.

`bp qstat <channel-id>` reports one message:

| State | Meaning |
|---|---|
| `PENDING: <agent> — <reason>` | stored and waiting; the reason says what for |
| `PENDING (will not be pasted again): …` | typed but not yet confirmed; it is never pasted again and closes as delivered or unconfirmed |
| `DELIVERED: <agent> (at HH:MM)` | confirmed; a qualifier such as `(HOOK)` names the path |
| `UNCONFIRMED: …` | typed but never confirmed; it is not sent again |
| `NOT DELIVERED (…)`, `CANCELED (…)` | final, with the reason |

`--json` prints the record, including `sender_evidence` and, for messages from
another machine, `origin`. Records older than two days are removed; final
states stay in `<msgq>/messages.jsonl` (`bp messages reindex` adds older
`done/` records).

`bp q` lists the queue with each message's waiting reason and the offline
spools. `bp q --retry` processes the existing queue once with the current
binary without creating a new message; it does not bypass busy, draft, or
identity checks. Use it, without closing the agent, when a local worker left
over from before an update still runs the old binary. `bp qcancel` withdraws
queued messages; a send already in progress refuses the cancellation rather
than claiming the recipient can no longer receive it. `bp peek <name> [n]`
prints the last `n` non-empty lines of the agent's screen (default 8).

`bp announce` sends `[ANNOUNCE <sender>] <text>` to the sender's descendants
(everyone, when the root sends it). Agents with a warm cache receive it now;
the others find it in their next digest. It needs a verified sender;
`--dry-run` prints the counts and the context it would cost.

## Running agents in tmux

These commands work with every module off. The `bar` module styles the
sessions they open, and the `sessions` module routes your plain `claude`,
`codex`, `opencode` and `hermes` commands through `bp run`.

```sh
bp run --name work codex
bp open <name> <directory> [--claude|--codex|--hermes|--opencode]
bp attach work                    # attach if live, otherwise revive its recorded launch
bp close <name>
bp rename <old-name> <new-name> [--dry-run] [--no-retitle] [--archived]
bp reparent <agent> <new-parent>
bp worktree add|list|rm ...
```

**`bp run [--name <name>] [--parent <name>] [--role <text>] [--ephemeral|--persistent] [--adopt] [--allow-codex] <codex|claude|opencode|hermes> [arguments...]`**
starts the CLI in a new tmux session named after the agent (without `--name`,
a name is made from the harness and folder) and attaches to it. Everything
after the harness name belongs to the native CLI. Claude receives a
launch-scoped settings layer with a session observer, a status-line reader and
bp's delivery hooks, plus `--name <agent>`; existing `--settings`, hooks and
status-line commands are preserved and global Claude settings files are not
changed. Run it from a regular terminal: inside tmux, without a terminal, and
for batch commands, pipes and help, `bp run` executes the native CLI unchanged;
without tmux it falls back to the native CLI with a notice. Exiting the CLI
closes its pane; detaching keeps it alive.

**`bp open <name> <directory>`** opens a managed agent in a detached tmux
session and records its launch in the agent book: folder, harness,
conversation, permission mode, model, effort, search flags and parent. Without
a harness flag it starts Codex (Claude when `codex.disabled` is set). Claude
agents opened this way run with `--dangerously-skip-permissions`; use `bp run`
to keep Claude's own permission prompts. Codex keeps its sandbox unless you
pass `--no-sandbox`. More options: `--resume|--fresh`, `--thread <id>`,
`--rebind`, `--adopt`, `--worktree <topic>`, `--parent <name>`,
`--role <text>`, `--ephemeral`, `--remote unix://…`, `--no-prompt`,
`--account <N|email|alias|default>` and `-- <native flags>`. For a new local
registration an explicit `--parent` wins; otherwise bp uses the verified
caller, or the local coordinator when the caller is unverified.

`bp open` accepts `--fresh` to ignore a stored conversation binding and start a
new one; it cannot be combined with `--resume` or `--thread`. Project schemas
use this for closed agents when `history: none` is selected. Managed OpenCode
launches use `bp open ... --opencode`.

When a thread is also recorded on another closed registration, `bp open` and
named `bp run` report every holder and require `--adopt` to move the binding to
the requested name. For example, `bp run --name work --adopt claude --resume <uuid>`
clears that thread reference from the other closed or archived rows, keeps those
rows intact, and prints each move. A live tmux session always blocks adoption;
attach to that session with `bp attach <name>` first.

**`bp attach <agent> [--no-revive]`** resolves an exact canonical name first,
then an unambiguous native title. It never creates an unregistered or empty
tmux session. A closed agent is resumed with its recorded folder, harness,
conversation, permission mode, model, effort, search flags and parent;
`--no-revive` limits the command to live sessions. For an unknown local name, a
running P2P service can show which connected peer has it. Inside tmux it
switches the current client, while an outer terminal attaches a new client
without detaching any other client. `bp attach <agent>@<server>` delegates the
same command to a [registered remote](#remote-servers).

**`bp rename`** updates local resume claims and both bar commands.
`--no-retitle` skips Claude's `/rename` and the Codex native title, preserving
native input; use it for self-renames or an unavailable Codex thread binding.
The Codex title then stays old and bp warns after the rename; the Claude
transcript keeps its old title until it types `/rename <new>`, and the cache
and `--resume` depend on that title. When a native transcript title drifts
from the canonical name, `bp rename <name> <name>` reapplies it; if it already
matches, bp reports that no repair is needed. `bp doctor` warns about
mismatches and prints this repair command. `bp rename` also reports schemas
that still contain the old name and leaves those committed files for a human
to update. Native Claude and Codex `/rename` changes update the display name
without changing authority ([details](configuration.md#native-rename-and-live-identity-display)).

`bp close <name>` closes the agent's session. `bp reparent <agent> <new-parent>`
moves an agent in the hierarchy. `bp worktree add <repo-directory> <topic>`
creates (or finds) a managed git worktree at `<repo>/.worktrees/<topic>` on
branch `<topic>/dev` and prints its path; `bp worktree list <repo-directory>`
lists them and `bp worktree rm <repo-directory> <topic> [--force]` removes one
(a dirty worktree needs `--force`).

## Resuming conversations

`claude --resume` (or `-r`) and `codex resume` open the CLI's own interactive
resume screen inside tmux. Search, selection, cancellation and named-session
lookup belong to the native CLI; bp does not replace the picker or read its
keys. The temporary bp session is observed after selection, using Claude's
callbacks or Codex's writer lock. Until then its conversation is unknown and
messages wait.

For explicit UUIDs, `claude -c` and `codex resume --last`, bp can attach to an
existing verified owner before starting another CLI. This pre-launch routing
does not replace a native picker. If Codex exits with its specific
active-writer resume error, bp can attach that client to one kernel-verified
existing tmux writer. It never kills that writer or removes its lock. Other
native errors are preserved in a private `exit.json` and printed after tmux
exits; `bp doctor --agent <name> --json` locates them. Shared app-server
connections retain `--remote`. More on continue and resume ownership:
[configuration.md](configuration.md#local-claude-continueresume-ownership).

## Archive and agent lifetime

Archive a closed registration without deleting its conversation:

```sh
bp archive work
bp archive --list --json
bp restore work
```

Archived records stay in their original agent book with an `archivedAt`
timestamp; they leave active bp lists but retain their name, launch metadata and
native conversation files. Restore makes the record available again without
launching a CLI. Native resume pickers are not modified. Exit the agent first; a
coordinator, an agent with children, or an agent with pending messages cannot be
archived. Remote Codex threads must also be confirmed unloaded. Restore an
archived parent before its children. `bp open` and named local launches require
an explicit restore instead of silently reusing an archived name.

`bp run` marks new registrations ephemeral by default. Use
`bp run --persistent` to keep one, or `bp run --ephemeral` to make the choice
explicit. Named `bp open` registrations are persistent by default;
`bp open <name> <directory> --ephemeral` opts into automatic archival. Existing
agent book rows without a `lifetime` field remain persistent.

`bp keep <name>` makes an active registration persistent and `bp release <name>`
marks it ephemeral. A successful `bp rename` or a native Claude/Codex retitle
observed by bp promotes an ephemeral record to persistent. Closing an ephemeral
agent archives its registration when `lifecycle.archiveOnClose` is enabled.
Archives retain registration metadata and do not touch native transcripts.
Parents with children stay active and report the archive refusal.

Closed ephemeral rows are hidden from `bp status` and `bp tree` by default;
`--all` includes archived and closed rows. `bp archive --stale --dry-run`
previews closed registrations whose bound transcript is missing and which are
not marked persistent; `bp archive --stale` applies that migration without
guessing from an agent name. The defaults are configurable
([configuration.md](configuration.md#ephemeral-agent-lifetime)).

## Rooms, board, API and MCP

```sh
bp room list
bp room join <name> [--topic <text>] [--with <agent,agent,...>]
bp room post <name> <text...>
bp room read <name> [--after <post-id>] [--limit <n>]
bp room leave <name> [<agent>]
bp board list
bp board get <board> [<key>] [--prefix <key-prefix>]
bp board put <board> <key> <value...> [--expect <version>] [--del]
bp board history <board> <key> [--limit <n>]
```

A room is a shared thread: each post is kept in the room's history and queued
to every other member like a normal message, so it never interrupts a busy
agent. A board is shared key/value state with versions and history, for agents
to coordinate without posting into each other's queues; `--expect` makes a
write conditional on the version you read. Run these commands inside a bp
agent's terminal; the pane proves who you are.

```console
$ bp room join review --topic 'PR #12' --with bob
joined review (2 members: alice, bob)
$ bp board put main release '1.10 is frozen'
main/release = 1.10 is frozen (v1)
```

The same features, plus inboxes for agents without a bp terminal, are served
over HTTP and MCP:

```sh
bp serve --api [--listen 127.0.0.1:PORT] [--no-socket]
bp mcp [--as <name>]
bp api token [--path]
bp api config <claude|codex|gemini|antigravity|grok|opencode|cursor|hermes> [--http]
bp api gateway clients|pending|pair|token|revoke
```

`bp serve --api` runs the local API in the foreground on a unix socket under
the state directory and, with `--listen`, on a loopback TCP port; with
`api.enabled` set, the daemon serves it instead. `bp mcp` serves MCP on stdio
for the calling agent; inside a bp terminal the pane decides its identity.
`bp api token --path` prints where the TCP bearer token is kept; scripts
should read it from that file (`TOKEN="$(cat "$(bp api token --path)")"`).
`bp api token` prints the token itself, so avoid it where a transcript or log
can capture the output. `bp api config <client>` prints a ready MCP snippet,
and `--http` the streamable-HTTP form with the token header (it needs
`api.listen`). Outside a bp terminal, `bp mcp` currently speaks as your login
user and `bp_register` is refused; HTTP callers name themselves with
`X-BP-Agent` and are labeled `http:<name>`. Today both need tmux and at least
one agent started with bp. The remote gateway for web chat apps is off by
default. Endpoints, auth, rooms, board and gateway: [api.md](api.md).

## Guided setup

```sh
bp onboard                          # choose a CLI and start/attach the coordinator
bp onboard --cli codex --prepare    # prepare only; no agent or model request
bp book --json                      # inspect the actual coordinator and agent records
```

`bp book --json` prints recorded launch arguments, including any secret you
passed as a command-line flag; do not paste it into an issue.

`bp onboard` asks which CLI to use (codex, claude, opencode or custom) and opens
a `main` coordinator agent in tmux with a generic onboarding prompt. The
coordinator learns the machine's setup through a small, scoped inspection,
explains the opt-in modules, enables only the ones you choose and helps
configure bp. It prepares `$BP_HOME/main/ONBOARDING.md`; the coordinator creates
tailored `MACHINE.md` guidance there. Existing files and real coordinators are
preserved. Optional desktop integration is proposed, not installed
automatically. Native model, effort and permission settings are not
overridden. Onboarding enables no module by itself. Run interactive onboarding
outside tmux; `--prepare` is safe inside an existing session. Reinstallation
and updates never start it; run `bp onboard` when you explicitly want to
configure the coordinator.

For another CLI, specify how it accepts a prompt:

```sh
bp onboard --cli custom -- /path/to/agent --prompt '{prompt}'
```

Custom CLIs can run in tmux; structured activity and automatic delivery require
a supported harness.

## Configuration and diagnostics

```sh
bp setup                   # refresh bp's own config and book, and what enabled modules own
bp setup --check           # preflight only; changes nothing
bp config path
bp config check
bp doctor --json
bp doctor --agent <canonical-name> --json
```

Settings live in `~/.blueprint/config.yaml`, or under `BP_HOME`. Shell
integration, when the `sessions` module is on, lives in `~/.config/bp/shell.sh`.
Native transcripts stay in their CLI directories. Setup preserves existing
configuration, aliases and records. Outside bp's home it writes only the
bundled `blueprint` skill for Codex and Claude (unless a module is enabled),
respecting `CODEX_HOME` and `CLAUDE_CONFIG_DIR`: a short note on installs that
only communicate, the operational skill when `sessions` is on.
User-owned skills are preserved; a managed copy you edited is backed up before
an update. Every file setup writes outside bp's home is recorded for
`bp uninstall`.

For local session problems, run `bp doctor --agent <canonical-name> --json`. It
checks runtime binding, conflicting live registrations, stale resume names and
bar commands, without changing sessions or records. Closed historical entries
do not block the current session. A live thread conflict blocks message
delivery, but keeps model/context visible as a shared thread snapshot.

Configuration reference and platform limits: [configuration.md](configuration.md).

## Shell integration (sessions module)

```sh
bp enable sessions [--shell bash|zsh] [--wrappers]
bp disable sessions
bp setup --disable
```

With `sessions` enabled, your usual `codex`, `claude`, `opencode` and `hermes`
commands start through bp after you open a new terminal (or run
`. "$HOME/.config/bp/shell.sh"`). Existing aliases and their arguments are
preserved; existing custom shell functions take precedence; `command codex`
bypasses the wrapper. Batch commands, pipes and help retain native behavior.
Exiting the CLI closes its pane; detaching keeps it alive. The optional `lush`
and `rush` wrappers (`--wrappers`) call `bp attach` and `bp shell`; bp does not
replace an existing alias or function with either name. `bp setup --shell` and
`bp setup --wrappers` remain as older spellings that enable `sessions`.

`bp disable sessions` removes the shell file and the startup line it added.
`bp setup --disable` instead replaces the shell file with a disabled stub and
keeps a backup; it preserves aliases, agent records and open sessions, and the
leftover source line is harmless. Open a new terminal to use native commands
again.

## Colors, windows and attention

```sh
bp color work purple
bp color <agent> [--json|auto]
bp windows [--json]
bp windows watch
bp focus <agent>
bp con [agent-name]
```

`bp color <agent>` prints the agent's bar color as `#rrggbb`; a color name or
xterm index sets a persistent accent and `auto` removes it
([details](configuration.md#local-mouse-and-colors-for-external-applications)).

`bp windows` maps local compositor windows to registered tmux agents; add
`--json` for launcher integrations. `bp focus <agent>` focuses a mapped window
and records that you looked at the agent. `bp con <agent>` records the same
when it attaches to a local session; when no local session exists and
`~/.config/bp/config` names a remote, it attaches there over mosh or ssh.
`bp windows watch` paints active and dimmed inactive borders in each agent's bp
accent and resets departed windows to the configured color.

`bp status --json` includes `awaiting_user` and `unread`. Remote agents reached
through mosh are outside the local window map. See
[window integration](windows.md) for compositor support and the JSON fields.

## Remote servers

Register interactive remote bp servers separately from P2P message peers:

```sh
bp remote add server --host server.example --user tuna --transport mosh \
  --identity ~/.ssh/server --mosh-ports 60000:61000 --elevate "sudo -i"
bp remote list
bp attach worker@server           # delegates to `bp attach worker` on server
bp shell server                   # interactive remote shell
bp shell server worker            # delegates to remote attach; unknown names fail
bp remote rm server
```

SSH uses a PTY; mosh can use a configured SSH port, identity path and UDP port
range. Remote records contain no credential values beyond the identity-file
path. The existing `bp remote [<agent>...]` Claude Remote Control action
remains available, except that `list`, `add`, and `rm` are reserved subcommand
words. It dismisses the Continue menu an already-connected session opens,
including on sessions where delivery could not be verified, and reports any
session showing a fresh claude.ai/code link as active. `bp doctor` warns about
Claude sessions whose Remote Control dropped after it was active (for example
after the signed-in account or organization changed); `bp remote <agent>`
reconnects them. Optional `lush` and `rush` wrappers call `bp attach` and
`bp shell` ([sessions module](#shell-integration-sessions-module)). The
`remotes:` settings are described in
[configuration.md](configuration.md#interactive-remote-servers).

## Portable project trees

Keep a project's agent tree in `<project>/.blueprint/schema.yaml` and recreate
it on another machine:

```sh
bp schema export [<project-dir>] [--lead <agent>]
bp continue [<project-dir>] [--dry-run] [--yes]
bp history export [<project-dir>] [--agent <name>] [--keep N]
bp history export --stdout --agent <name>  # remote history transfer
```

The version 1 schema records relative folders, parents, runtime, role, color,
and optional model, effort and launch mode. `bp continue --dry-run` prints the
full plan. The first real use in a project, and every schema content change,
requires review and confirmation; a non-interactive run must pass `--yes`.
Live agents are left running. Closed agents with `history: none` start fresh;
`history: file` imports portable transcripts before resuming. A name already
registered for another folder is refused; there is no automatic prefix or
suffix override. `bp rename` reports schemas that still contain the old name
and leaves those committed files for a human to update.

History modes are `none`, `file`, and `remote`. Portable history supports Claude
JSONL transcripts and Codex rollout files with their session-index rows; use
`history: none` for Hermes or OpenCode trees. Export keeps one session per agent
by default; `--keep N` changes that limit. Different existing transcript
contents are never overwritten. `.blueprint/.gitignore` ignores history by
default because it can contain private conversation data; use
`git add -f .blueprint/history` only when you intend to commit it. Remote
history accepts `ssh://user@host[:port]` or a name from the local `remotes:`
config block and transfers tar data over SSH. No credentials belong in the
project schema. More: [configuration.md](configuration.md#portable-project-schemas-and-history).

## Across machines (P2P)

```sh
bp p2p id
bp p2p start | stop | serve
bp p2p status [--json]
bp p2p ping <peer>
bp p2p lookup <agent> [--timeout <dur>] [--json]
bp msg main@laptop 'Please review the change'
bp qstat <channel-id> --json
bp p2p channels [--json]
bp p2p pauses [--json]
bp p2p resume <peer> [agent]
```

P2P messaging is opt-in. It uses libp2p connections authenticated with
persistent Ed25519 machine keys, your own rendezvous and relay, and explicit
peer permissions: each machine lists the other's Peer ID and which of its own
agents that peer may reach (`expose`). `bp p2p id` prints this machine's public
Peer ID. Incoming messages carry an `external:sender@peer` envelope, arrive
framed as untrusted text, and cannot use force-busy. Inbound messages pass a
per-peer rate limit and a per-pair loop cap; a pair that reaches the cap
pauses until `bp p2p resume` or until 30 minutes pass, and `bp p2p pauses`
lists the pauses. Lookup answers only for agents exposed to the asking peer.
Setup, relays, channel states and guarantees: [p2p.md](p2p.md). The older HTTP
federation remains available as `bp fed status|ping|token|log [n]`.

## Audit

```sh
bp audit [--since <dur>] [--kind <prefix>] [--severity info|warn|alert] [--peer <alias>] [-n N] [--json]
bp guard hook claude [--ask] [--canary <path>]...
```

`bp audit` reads `<state>/audit.jsonl`: API requests and rejections, P2P
decisions, module changes, uninstall, and guard findings and alerts.
`bp guard hook claude` is the `PreToolUse` tripwire the `guard-hooks` module
installs; it reads the hook payload on stdin and reports its alerts as
`guard.reach.*` events. See the [threat model](security/threat-model.md) and
[guard-hooks module](security/guard-hooks-module.md).

## Claude accounts (accounts module)

A maintainer module, not supported for general use yet; review your provider's
terms before using it. `bp account` keeps several Claude Code subscription
logins on one machine and switches the live login between them. `list`,
`status` and `bindings` work on any install; every other `bp account` command
needs `bp enable accounts`. Log in with `claude`, then store it:

```bash
bp account add --alias work    # store the current Claude login as a slot
bp account list                # slots, token status and 5h/7d usage
bp account switch work         # install another slot as the live login
bp account auto --once         # switch only if the active account is near its limit
bp account keepalive           # the staggered five-hour window plan; --once pings due accounts
```

A switch rewrites Claude Code's credentials file and the `oauthAccount` key of
its global config under Claude Code's own locks; running Claude agents pick up
the new login on their next request. Stored tokens stay in private files under
the bp state directory and are never printed. Automatic switching, per-account
usage limits and keeping every account's five-hour window running are opt-in;
see [Claude accounts](configuration.md#claude-accounts). Not supported on
macOS, where Claude Code keeps its login in the Keychain.

Different agent trees can also run on different accounts at the same time.
Bind an agent to a stored account; it and every descendant without a binding of
its own then start in that account's profile home:

```bash
bp account bind research work      # research and its tree use the "work" account
bp account login work              # one-time Claude login inside the profile
bp account bindings                # who runs on which account, and who needs a reopen
bp account bind research-scratch default # opt one subtree back out
```

`bp open --account <N|email|alias|default>` binds a new agent as it opens. A
binding applies when an agent is launched or resumed. Profiles share
transcripts, settings, skills and history with the default home, but hold their
own login, which is never copied from a stored slot. Automatic switching only
changes the default login. The full command list is in `bp help`.

## WhatsApp (wa module)

```sh
bp wa send [--to <target>] [--reply <msgId>] [--from <label>] [--mention <jid|number>]... [--mention-all] <message...>
bp wa read <target> [n]
bp wa chats
```

A maintainer module for the maintainer's own setup, not supported for general
use yet. These commands need `bp enable wa` and a configured outbox
(`waOutbox`). `--from` states the sender outside tmux (cron, scripts); inside a
pane the session name is the sender. The bridge itself is not part of this
repository.

## Workflows, monitoring and other commands

| Command | What it does |
|---|---|
| `bp workflow add\|list\|show\|check\|start\|status\|stop\|resume\|times` | runs a saved directory of prompt templates over unit records with selected tmux agents ([workflow.md](workflow.md)) |
| `bp wait <agent> --until idle\|working [--confirm N] [--timeout D] [--json]` | waits until an agent reaches a state, for example before reading a receipt |
| `bp compact [--idle-hours N] [--min-ctx N] [--apply]` | compaction by policy (idle Claude agents with a full context) for the agents below a verified sender; it only lists until you add `--apply` |
| `bp compact --all [--min-age <minutes>] [--exclude <name,...>] [--apply]` | every agent below the sender, except excluded ones and those compacted within `--min-age`; lists by default |
| `bp img`, `bp img recv` | sends a clipboard image over SSH to the server in `~/.config/bp/config`, which stores it in `clipboardDir` and prints the path |
| `bp dash [--port N]` | serves the monitor dashboard on loopback (the `ui` module lets the daemon do it); its data comes from `DASH_URL`, by default the maintainer's monitor site |
| `bp monitor [usage\|cost\|agents\|projects\|services\|radar]` | renders the same monitor views in the terminal, from the same source |
| `bp usage`, `bp policy status\|override <hours>` | quota helpers for installs that set `usageHistory` and `usageBin` |
| `bp tokens [--day YYYY-MM-DD \| --since 7d] [--hours \| --prompts] [--agent <name>] [--json]`, `bp tokens collect\|gc` | token usage collected from agent transcripts |
| `bp service` | the daemon's job states |
| `bp daemon` | the background scheduler: queue dispatch, the API when enabled, and the jobs of enabled modules |
| `bp messages reindex` | adds older `done/` records to `messages.jsonl` |
