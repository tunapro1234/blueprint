# blueprint (`bp`)

`bp` combines the server's agent/tmux, message queue, usage reporting, and WhatsApp
client operations in one standalone Go binary. `bp daemon` is a supervisor that
schedules the existing Python/Node jobs without rewriting their business logic.

Build and verify:

```sh
make check
```

The binary is written to `/srv/blueprint/bp`.

Run `bp help` for a command summary. `blueprint.service` is at the repository
root. Source changes require a rebuilt binary and a restart of that service;
restarting the shared Codex app-server is a separate operation.

## Local laptop agents

`bp setup` adds Bash/Zsh shell functions for `codex`, `claude`, `opencode`, and
`hermes`. Starting one from an ordinary interactive terminal creates a named bp
agent in its own tmux session, using the current directory and original CLI
arguments. `bp run --name work codex` selects a predictable messaging name.

Existing aliases remain defined. For example, `alias claude="claude --my-options"`
expands its options before calling the bp function; quoting and extra arguments
are preserved. Re-sourcing does not duplicate options. Existing custom functions
are left alone. Aliases that explicitly use an absolute executable path or
`command claude` bypass the function, keeping their original behavior. After
upgrading from the old alias-removing setup, open a new terminal to reload your
aliases from the unchanged startup file.

When the CLI process exits, its pane closes and the terminal returns to the
outer shell. A Ctrl-C that only cancels an agent turn keeps the session alive;
use the CLI's actual quit action to exit. Other sessions are unaffected.
Detaching keeps the agent running; `tmux attach -t <name>` reconnects. Existing
tmux sessions do not get nested. Each bp session gets the bp status bar automatically;
re-running the local installer or `bp setup` repairs the bar of existing local bp
sessions without restarting their agents. Global scroll/key/clipboard settings
and other tmux sessions are not changed.

Batch commands (`codex exec`, `claude -p`, `opencode run`), pipes, help and version
queries keep their native behavior and exit status. `command codex` bypasses the
shell function explicitly. The underlying CLIs must already be installed and
signed in; bp does not alter their model, effort, remote endpoint or permissions.

A small local worker retries queued messages while the CLI is alive and exits
with it. It reuses bp's delivery locks and busy/draft guards; no separate daemon
setup is needed. State lives in `~/.blueprint` (or `BP_HOME`), shell integration in
`~/.config/bp/shell.sh`. Startup files are backed up before the source line is
appended; repeating setup does not duplicate it. Agentbook/history are retained
after exit. An existing sandbox must permit the bp state writes required for
outgoing messages; the wrapper does not loosen it.

Setup creates `~/.blueprint/config.yaml` with commented settings, preserving any
existing YAML or JSON config. `bp config path` locates the active file and
`bp config check` validates it without printing secrets. For example:

```yaml
bar:
  widgets: [ctx, model, quota]
```

Relative YAML paths resolve under `BP_HOME`, independent of the agent's cwd.
Unknown YAML keys, invalid values and multiple config files are errors.
Local model/context observation is enabled by default (`localObservation: true`).
Claude's launch-scoped hooks/status-line input and Codex's held writer lock bind
the conversation automatically, including untitled sessions. Native transcripts
stay in their CLI directories; bp stores small per-launch records under `stateDir`.
The local bar defaults to `bar.context: used`, matching the server; missing metrics stay unknown.
Older already-running sessions pick up launch integration when reopened, using
`claude --resume` or `codex resume` to keep their conversation.
See [configuration](docs/configuration.md) for settings and platform limits.

Linux and macOS, amd64/arm64, Bash/Zsh are the intended platforms. Linux real-tmux
integration is tested with fake CLIs; macOS builds are cross-compiled, not tested
on a laptop in this workspace. [Release/install instructions](docs/local-release.md).

[Authority and P2P/hub options](docs/security-and-transport.md) are research only.
[Tuna's deferred decisions](docs/tuna-pending.md) track these and tmux scrolling.

## Vim message delivery

Claude and Codex messages use terminal bracketed paste in both Normal and Insert
modes. bp does not send Escape or `i` to force an editor mode. Multiline human
drafts, active keyboard input, busy turns and dialogs block delivery; Codex's
Vim `/` and `?` search editors are also left alone. Paste chips and expand/submit
retries have quota-free regression tests in `internal/tmux/vim_test.go`.

The Codex fixtures follow the 0.153.4 composer source: explicit paste enters text
through `handle_paste`, whereas Normal mode disables implicit paste-burst
recognition. Its search footer receives paste separately from the draft.
[Codex composer source](https://github.com/openai/codex/blob/rust-v0.153.4/codex-rs/tui/src/bottom_pane/chat_composer.rs),
[Claude Vim controls](https://code.claude.com/docs/en/interactive-mode#vim-editor-mode).
Actual tmux bracketed-paste and local session lifecycle are exercised by
`scripts/test_local_cli.py`, using a private socket and stateful fake CLIs;
these tests neither spend model quota nor prove live model receipt.

## Codex sessions

`bp open <name> <dir>` defaults to Codex for new registrations. `--claude` and
`--hermes` select the other harnesses. Reopening a registered launch preserves
its harness and recorded thread. `--resume` resolves an existing interactive
Codex thread in that cwd; `--thread <UUID>` pins it explicitly. Missing or
mismatched threads are errors, not a reason to start another conversation.
Model, effort and service tier come from Codex's thread/configuration; bp does
not hardcode them or run the legacy automatic model-switching policy.

Embedded Codex stays sandboxed by default. To connect an already approved,
unconfined agent to the local shared app-server:

```sh
bp open <name> <dir> --codex --remote unix:// --thread <UUID> --no-sandbox
```

Remote commands execute in the server's security context. A bwrap client does
not confine that server, so bp refuses remote launches without explicit
`--no-sandbox`. It never starts/restarts the app-server or migrates other agents.
The existing server thread must belong to the requested cwd.

Status, bar and message guards share one runtime reader. Codex requires an
explicit thread binding; cwd, mtime and inherited daemon environment cannot
identify a live thread. It detects threads loaded in the default local
app-server using read-only `thread/read`, including when host pane PIDs are
invisible inside bwrap. Remote busy/model/effort come from that server; context
comes from durable `token_count` records, not lifetime token totals or transient
notifications. Large tool output and partial writes do not erase the last
measurement. A registered remote connection failure retains measurements and
blocks delivery with an explicit `unknown` state. `bp status --json` schema 2
exposes activity evidence and its age separately from historical usage, plus
CLI/daemon executable identity. See [runtime contract and rollout](docs/runtime-status.md).
The bar selects the observed provider's quota as compact `gpt` / `cc` used
percentages (Claude: 5h/weekly; Codex: weekly/short window); an unknown provider never falls back to Claude.
Cache temperature uses the latest Claude request's explicit 5m/1h write breakdown:
`warm~` / `cold~` are estimates, never a guaranteed future cache hit. Missing or
mixed TTLs (including current Codex telemetry) show only `age`. Announcement
cache gating defers unknown TTLs. See [cache lifetime evidence](docs/cache-lifetime.md).

Messages still use the attached TUI; bp does not create a second writer with
`thread/resume` or `turn/start`. Keepalive reopens only the configured fleet root
with a recorded launch and exact Codex thread; missing metadata requires manual
recovery. The bar's display cache is two seconds.

Quota-free regression checks use simulated Claude/Codex/Hermes terminals and
local RPC fixtures; they never invoke a model:

```sh
go test ./...
go test -race ./internal/tmux ./internal/msgq ./internal/pending ./internal/book ./internal/cache ./internal/codexrpc
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts -p 'test_*.py'
```

## Talking to agents

`bp msg <name> <message>` delivers to the agent's tmux session. A target that is
working or has half-typed input is never interrupted: the message is queued and
the printed channel id follows it with `bp qstat`. An agent that is not open at
all keeps its messages until it opens. Read what an agent says with `bp peek
<name>`, and see the fleet with `bp status` or `bp tree` (`closed` / `idle` /
`working` / `unknown` / `blocked` / `dead` — `dead` means the session is up but its pane no longer runs
an agent). `bp status --json` prints the same fleet as one JSON object, with
context tokens, conversation age, and model where they are known.
Pending messages do not expire by age or count. Acknowledgement archives only
the delivered snapshot, preserving messages appended during delivery. Queue
history is retained, and a target closing does not cancel its pending messages.

Delivery is reported honestly, in three outcomes. `sent` means the composer was
seen holding the message and then seen to clear. `GONDERILEMEDI: … kuyruga
alindi` means bp proved it did not land (the pane's login expired, or the
composer held someone else's text where the paste should be) and queued it for
another attempt. `gonderildi ama DOGRULANAMADI` means the keystrokes went in but
nothing confirmed them: bp exits non-zero and does NOT queue a retry, because
the message may well have arrived — look with `bp peek <name>` before resending.

A composer that is not empty no longer automatically means "someone is typing".
bp first reads the WHOLE composer box and compares it with the message it is
about to send and with the records already queued for that target. Text that is
exactly one of them is bp's OWN paste whose Enter never registered: it is
finished with Enter and reported as a normal delivery (this is what used to
deadlock the queue behind itself — one message sat for four days). A damaged
version of one of them is cleared with Ctrl-u and pasted again, never submitted,
because pressing Enter on it delivers a silently truncated message. Anything else
is someone's own text and is left completely untouched, exactly as before. bp
never sends Escape into a pane — it would cancel a running turn. Before the queue
re-pastes anything it also checks the target's transcript, so a message that
already arrived some other way is closed instead of delivered twice; when that
same text is still hanging in the composer, bp clears it (with Ctrl-u, never
Enter) and says so, because the closed record would otherwise be the last thing
able to recognise it.

### Machine-readable outcome and --force-busy (plumbing only)

Every `bp msg` run ends with one STABLE line for scripts:
`RESULT=delivered|queued|unverified|duplicate` plus ` CHANNEL=<id>` when a queue
record exists. The keys and verdict words never change; new information arrives
as new keys. Errors keep signalling through the exit code.

`bp msg --force-busy <name> <message>` is restricted to a verified main-agent
identity in the fleet-root / `bp` / `wa` / `whatsapp` allowlist. Self-declared
`AGENT=whatsapp` no longer grants this privilege. The current bridge's force
calls therefore require a verified service identity before rollout; changing its
environment label is not a fix. Ordinary messages still queue.
Force skips only the busy gate: human drafts, keyboard activity, expired login
and non-agent panes still refuse. Working-pane receipt needs transcript evidence.

A message that waits says WHY it waits. `bp q` prints a `!` line under it and
`bp qstat` names the cause instead of a bare "is still busy" — `composer'da
okunamayan bir paste var (chip)` (only a human can clear that one),
`composer'da yabanci metin var`, `pane calisiyor`, or the concrete delivery
failure (an expired login). `bp msg` prints the same reason immediately under its
queue line. The reason is refreshed on every dispatch pass and cleared when the
pane frees up, so a stuck target is one glance away from being fixed instead of
days away from being noticed.

`bp` stamps every message with `[<sender>] `. Never add a manual sender prefix.
`bp whoami` reports the resolved label, source, thread, parent and `authority`.

Codex attribution first checks execution context, then the agentbook's explicit
`identityThreadId` and Codex session metadata. It never uses inherited tmux,
`AGENT`, cwd or a latest-rollout guess as a thread identity. A CLI subagent is
labelled `<parent>/subagent:<thread>` and does not inherit parent authority.
An unproven or unmapped thread is labelled `codex?:<thread>`; an unknown caller
is `bilinmiyor`. There is no `server-main` fallback. `AGENT` declarations are
visibly marked `agent?:<name>`. An alternative `AGENTBOOK` cannot redefine
hierarchy authority unless it is an installation-configured book.

Normal tmux attribution targets `TMUX_PANE` and checks that the pane process is
in the caller's process chain, rejecting dead/unrelated panes and shared
app-server ancestors. Current Codex execution proof supports the Linux sandbox
helper's original context; an unconfined/shared-server or macOS context that
cannot be proved stays unverified. This can restrict hierarchy commands until
an authenticated execution adapter is available. Plain messages remain possible
with an explicitly uncertain label. [Identity incident and rollout](docs/identity-fix.md).

Slash commands (`/compact`, `/goal`, ...) are delivered bare, since a prefix
would break the command, so authority is checked instead: only a verified main agent that is the fleet root or
an ancestor of the target may send one, and sideways or upward is refused.
`/goal <text>` is rewritten to `/goal [<sender>] <text>` so the target records
who set the goal; a bare `/goal` passes through untouched.

`bp compact` looks for agents worth compacting — an open, not busy `claude` pane
whose last human turn is older than `--idle-hours` (24) and whose context is
above `--min-ctx` (200000 tokens) — and prints one decision row per agent. It
sends nothing; `--apply` is what sends. Under `--apply` every pane is read again
right before its `/compact`, and a target that has started working is skipped
rather than queued. `--all` sweeps every descendant of the sender instead of the
policy set. Both selectors respect the sender's hierarchy.

`bp open <name> <dir> --hermes` launches the Hermes Agent TUI instead of
Codex. A Hermes pane reports `python` as its command, so bp recognises it by
command AND screen together; delivery is fully supported with one hard rule —
a WORKING Hermes pane refuses even `--force-busy`, because in Hermes
submitting text mid-turn CANCELS the running turn (the pane itself says
`msg=interrupt` while busy). Messages to a busy Hermes queue normally and go
in when the turn ends. Hermes keeps no Claude-style transcript, so delivery
is screen-verified. Codex can additionally verify the complete inbound user
message in its own rollout; assistant/tool quotations do not count as delivery.

`bp open <name> <dir> --codex --no-sandbox` opens a codex agent with BOTH
sandboxes off: ours (the session is created with `CODEX_BWRAPPED=1`, which
disables the bwrap wrapper) and codex's own
(`--dangerously-bypass-approvals-and-sandbox`). It is opt-in and changes nothing
for any other agent. The reason it exists is that the default is not merely
stricter but broken in some directories: codex's sandbox binds `.git` read-only
inside the writable root, and where there is no `.git` — `/srv`,
`/srv/kavram/.agents` — bwrap tries to mkdir it and dies, leaving an agent that
cannot run a single command. After opening, bp re-reads the pane and WARNS if
the command still says `bwrap`, which means the environment never reached the
launch line. What the flag buys is a shell with no confinement at all, so the
agent's own brief has to carry the boundary the sandbox no longer does.

The fleet lives in one agentbook, `/srv/server-main/agentbook.json` (override
with `AGENTBOOK`). `bp open` takes `--parent <name>` and `--role <text>` to
write that entry correctly instead of guessing from the folder path; the parent
is validated before anything is opened, and both flags also correct an
already-open agent's entry.

Use managed Git worktrees when agents need isolated branches in the same
repository:

```sh
bp worktree add /srv/project shop
bp worktree list /srv/project
bp open shop-agent /srv/project --worktree shop
bp worktree rm /srv/project shop
```

Managed worktrees live at `<repo>/.worktrees/<topic>` on `<topic>/dev`.
Removal refuses uncommitted changes unless `--force` is supplied.

Mirror the monitor dashboard in a terminal with `bp monitor`. Detailed views
are available as `bp monitor usage|cost|agents|projects|services|radar`. The
CLI reads `/srv/monitor/site/data.json` when available and otherwise uses the
configured `DASH_URL` (default `https://monitor.tunapro.xyz`).

Local installation is now the laptop default in the source installer. After
publishing the matching installer and binaries:

```sh
curl -fsSL https://bp.tunapro.xyz/install.sh | sh -s -- --local
```

It installs `~/.local/bin/bp` and runs shell setup. Open a new terminal or source
`~/.config/bp/shell.sh` once. Missing tmux is installed with existing Homebrew on
macOS or apt on Debian/Ubuntu; other systems need tmux installed first.
The prepared local release has **not been published** by this source change;
see [local release notes](docs/local-release.md).

`--client` retains the older SSH/mosh remote-client setup and creates
`~/.config/bp/config`. Connect with `bp con <agent-name>`; run `bp con` to list
available sessions. `bp img` uploads a local clipboard image over SSH and copies
the resulting server path back to the local clipboard. On this server, the
installer's default still links `/srv/blueprint/bp` into `/usr/local/bin`.

The npm wrapper package is prepared in `npm/`. It intentionally is not
published by the release build; see `npm/README.md` for the manual publish step.
