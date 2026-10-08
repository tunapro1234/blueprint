# Architecture

Blueprint is a Go CLI with local tmux integration and an optional server daemon.
Machine paths, agentbooks and optional services come from configuration.

## Main components

| Component | Responsibility |
| --- | --- |
| `cmd/bp` | CLI, local startup, shell integration, onboarding, status rendering |
| `internal/config` | YAML/JSON loading, defaults and validation |
| `internal/book` | Agent registry, hierarchy, native metadata and runtime binding |
| `internal/identity` | Sender evidence and authority checks |
| `internal/tmux` | Pane/process observation, guarded input and harness adapters |
| `internal/msgq` | Durable local queue, ordering, retries and delivery records |
| `internal/cache` | Native context, turn and cache observations |
| `internal/codexrpc` | Shared Codex app-server observations |
| `internal/daemon` | Optional server scheduling and queue dispatch |
| `internal/fed` | Existing HTTP hub/client federation |
| `internal/p2p` | Authenticated libp2p transport, discovery/relay, durable outgoing channels |

## Local startup

Shell wrappers preserve normal CLI arguments and aliases. Interactive launches
outside tmux enter a named session whose foreground process becomes the native
CLI. Exiting the process closes the pane. A local worker retries pending messages
while the CLI is alive. Batch commands and existing tmux sessions retain native
behavior.

Claude continue/resume pins a native conversation UUID before creating a pane.
Bare resume lists saved conversations across projects before selecting an owner.
Owner lookup and creation are serialized. An existing owner is attached; multiple
live owners produce an error. Physical cwd paths prevent symlink aliases from
creating mismatched registry folders. This does not lock processes outside bp or
native in-TUI conversation switching.

Codex resume also selects a UUID before creating a pane. It routes only to an
owner with a held writer lock, and rejects an unmatched active writer before
launching. Remote TUI argv is not proof of current ownership. Explicit `--remote`
continues through the native app-server flow.

Fresh Codex openings receive onboarding as a native startup prompt. This avoids
waiting for a transcript that may only appear after the first user turn. Existing
and resumed sessions retain normal guarded delivery.

## Observation and identity

Status, the bar and delivery gates share runtime observations. Local Claude uses
launch callbacks and native transcripts; local Codex uses its held writer lock
and rollout metadata. Remote Codex requires app-server/thread evidence.

Runtime binding is distinct from sender authority. Cwd, inherited tmux variables,
display names and model labels do not prove identity. Native renames are convenience
aliases; canonical queue targets and hierarchy remain stable.

## Delivery

The sender receives a channel ID. The receiving machine's queue evaluates runtime,
draft and modal guards before writing to a pane. Per-pane and dispatch locks prevent
competing bp writers. Delivery records distinguish verified outcomes from uncertainty.
Unknown state remains unknown; empty screen content is not affirmative idle evidence.

A transcript-confirmed delivery is terminal even while the recipient is busy. It
does not authorize editing a current composer or cleaning it on a later pass.
Legacy pending cleanup records finalize without pane input. A remaining copy is
protected as a draft, so later deliveries wait until the composer is cleared.

Online sends share the queue path and persist attempt intent before input.
Recovery requires the original runtime/thread/pane binding. See
[message delivery](docs/message-delivery.md) for the scenario matrix and limits.

## Onboarding

`bp setup` installs shell integration. `bp onboard` prepares a private machine-local
workspace, creates or preserves the coordinator, and starts the chosen CLI with a
generic prompt. Existing real coordinators and user documents are preserved.
Onboarding does not grant operating-system privileges or bypass identity checks.

## Server operation

The optional daemon supervises configured jobs and dispatches queues. Updating the
CLI binary does not update an already-running daemon or local worker. Verify the
actual executable identity separately. Shared Codex app-servers and agent processes
have independent lifetimes and should not be restarted as part of routine bp updates.

Claude account switching (`bp account`, package `claudeacct`) keeps one slot per
stored login under `<stateDir>/claude-accounts/`: `accounts.json`, per-slot
`credentials.json` and `oauthAccount.json`, `auto-state.json` and a `.lock`
flock. Secret files are 0600 in 0700 directories and are written by temp file,
fsync and rename; replaced or removed copies are moved aside, never deleted. A
switch takes Claude Code's own locks in its order (the OAuth refresh lock, the
config-home lock, then the global-config lock), touches them while held and
never performs network calls under them. The live login is captured back into
its slot first, after an identity check, so tokens Claude Code rotated are not
lost. bp never refreshes the active account's token; Claude Code owns it. An
inactive slot is refreshed only when it expires within five minutes and the
rotated token is persisted before use. Only the `oauthAccount` key of the global
config is replaced. The daemon job `claude-account-auto` is off by default; when
enabled it polls at most one account per minute and switches with a threshold,
a 10-point hysteresis and a cooldown.

Account bindings run several accounts side by side. The agentbook field
`claudeAccount` holds an account email (or `default`, which stops inheritance);
an agent's effective binding is its own or its nearest ancestor's. Each bound
account has a profile, `<stateDir>/claude-accounts/profiles/<accountUuid>/`, a
Claude Code config home with a `.bp-account-profile` marker. Shared entries
(projects, sessions, settings.json, skills, history and similar) are symlinks
to the default home; credentials and `.claude.json` stay per profile. The
profile config is seeded once from the default global config without its
`oauthAccount`, and the login comes from `claude auth login` run inside the
profile, never from a slot's tokens, because refresh-token rotation would log
two homes sharing one lineage out of each other. Launch options are recomputed
on every open, resume, fleet restart and keepalive and prefix the Claude command
with `CLAUDE_CONFIG_DIR=<profile>`. A profile that is not logged in as its
account fails `bp open`; daemon keepalive logs and falls back to the default
login. Inside a profile, bp resolves the default home from the marker, so
switching and agents opened from a bound agent never inherit the profile.

Interactive remote terminals use a separate `remotes` registry. Local bp only
constructs the configured mosh or SSH transport; agent lookup and revival are
delegated to `bp attach` on the destination, so remote agentbook state is never
guessed locally. No password or transport credential is stored beyond an SSH
identity-file path.

Project-local `.blueprint/schema.yaml` files declare portable agent trees using
relative folders. `bp continue` validates and plans the complete tree before
confirmation, checks folder collisions, then opens parents before children.
Per-project trust is keyed by the absolute project folder and schema content
hash. Optional history is kept outside Git by default; local Claude/Codex
transcripts are imported into the native store for the moved folder, while
remote history is fetched over SSH without storing credentials in the schema.

## Optional P2P transport

A separate `bp p2p serve` worker owns the machine key, network host and outgoing
channel retries. It calls the receiver's existing queue guards; network waits do
not block local dispatch. Peer ID plus channel ID maps idempotently to a durable
queue receipt. Acceptance and verified delivery are separate states. The receiver
controls its peer/target allowlist and creates external provenance; a peer's
claimed agent name never grants local authority. See [P2P](docs/p2p.md).
