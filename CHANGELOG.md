# Changelog

All notable changes to bp are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and bp uses
[semantic versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Changes on `dev` since the 1.9.30 release.

### Changed

- Installing bp no longer changes how your tools behave. The installer puts
  the binary in `~/.local/bin`, writes bp's own config under `~/.blueprint`
  and a short skill note for Claude Code and Codex, and stops there: no tmux
  install, no shell rc or tmux edits, no onboarding. Ask for more with
  `--enable <modules>` or `--onboard` (`BP_ENABLE`, `BP_ONBOARD=yes`).
- Every feature that changes your environment is now an opt-in module:
  shell wrappers (`sessions`), the tmux status bar (`bar`), Claude account
  switching (`accounts`), the WhatsApp bridge (`wa`), the dashboard (`ui`) and
  server jobs (`monitor`). `bp setup` no longer edits anything outside bp's
  home unless a module is on.
- Upgrading keeps an existing install's behavior: the modules it already used
  are detected and recorded on first run.
- Guided onboarding enables no module by itself; the coordinator explains the
  modules and enables only the ones you choose.
- Message states are the same everywhere: accepted, unverified, delivered or
  failed, over the API, MCP and P2P. A canceled message is failed with a
  `canceled` reason.
- Automatic account switching (`accounts` module) prefers the account whose
  five-hour window resets soonest; `claudeAccounts.switchPrefer: room` keeps
  the previous most-room order.
- bp is built with Go 1.27.2, and its dependencies are updated (go-libp2p
  0.50.0 and others). Release binaries now require macOS 13 or later; Linux
  support is unchanged.

### Added

- `bp modules`, `bp enable <module>` and `bp disable <module>`, with
  `--dry-run`, conflict checks and a journal of every change, so disabling
  undoes exactly what enabling did.
- `bp uninstall [--dry-run] [--purge [--yes]]` removes what bp and its
  installer added and keeps your data unless you ask.
- A local HTTP API (`bp serve --api`): REST under `/v1`, A2A v1.0 and v0.3
  endpoints with agent cards, and MCP over streamable HTTP. Loopback only,
  with a bearer token on TCP or the caller's uid on the unix socket.
- An MCP server on stdio (`bp mcp`) and ready client snippets for Claude Code,
  Codex, Gemini CLI, Antigravity, Grok CLI, OpenCode, Cursor and Hermes
  (`bp api config <client>`).
- Inbox agents, for scripts and tools without a bp terminal: register over the
  HTTP API (`POST /v1/agents`) and read messages with `GET /v1/inbox`.
- Rooms, where every post reaches each member's queue, and a shared versioned
  board, over the API, MCP and the new `bp room` and `bp board` commands.
- Hook delivery for Claude Code: sessions bp starts receive queued messages
  through their own `UserPromptSubmit`, `Stop` and `SessionStart` hooks at
  turn boundaries, and get their bp identity restated after compaction.
- A remote MCP gateway for Claude.ai and ChatGPT connectors, off by default:
  OAuth 2.1 with owner pairing codes, static tokens for header-capable
  clients, per-client expose lists, rate limits and outbound redaction.
- An audit log of API, P2P, module and uninstall decisions
  (`<state>/audit.jsonl`, read with `bp audit`) and a log of final message
  states (`<msgq>/messages.jsonl`, backfilled with `bp messages reindex`).
- P2P protections: per-peer rate limits, a loop cap per agent pair
  (`bp p2p pauses`, `bp p2p resume`), and lookups that answer only for
  agents exposed to the asking peer.
- The `guard-hooks` module: a `PreToolUse` tripwire for Claude agents that
  alerts when an agent that recently received outside text touches secrets or
  canary files (`bp guard hook claude`, `bp audit --kind guard.reach`).
- The `compaction-hooks` module (off by default, needs `sessions`): OpenCode
  and Hermes agents keep their bp identity and queued messages after a context
  compaction, as Claude Code sessions already do. Hermes's in-place
  compression is not covered yet.
- A short note installed as a skill for Claude Code and Codex, so agents learn
  that bp exists and how to use it; installs with `sessions` keep the longer
  operational skill.
- A per-harness capability matrix, `docs/harnesses.json`.
- The `monitor` module's sanity checks alarm the coordinator when an idle
  agent's queue has waited behind text in its prompt for 20 minutes.
- A command reference (`docs/usage.md`), a contributor guide and issue
  templates.

### Fixed

- `bp disable` and `bp uninstall` remove only lines and files bp added, never a
  matching line of yours, and edits to your files no longer race with your own
  writes or change their owner.
- bp builds on macOS again: the API's unix-socket check reads the caller's uid
  with `LOCAL_PEERCRED` on macOS (it used Linux-only calls), and on any other
  platform the API socket refuses to start rather than serve unverified
  callers.
- `bp wa read` shows a renamed group's older messages, and `bp wa chats` lists
  each chat once.
- The account keepalive plan no longer flips between two near-equal schedules,
  and old ping failures are no longer shown as current.
- `bp msg` reaches inbox agents (agents connected through bp's MCP tools or
  the HTTP API). It used to hold their messages in the offline spool, where
  they never arrived. The receipt now says the message is accepted into the
  inbox (`RESULT=queued ... ROUTE=inbox`), `bp qstat` shows when the agent
  has read it, and `bp status` lists inbox agents with their unread counts.

### Security

- Text that crosses a trust boundary (P2P peers, the legacy HTTP federation,
  remote gateway clients) reaches an agent inside a frame that names its source
  and marks it as untrusted data, not instructions from the owner. Owner
  notices about such messages no longer quote their text.
- The local API refuses non-loopback peers and requests carrying proxy
  headers, and checks `Host` and `Origin` against DNS rebinding and cross-site
  requests.
- Guard alerts follow outside text across bp processes: the P2P service, the
  API and the tool-call tripwire read the message log, so an agent that
  received outside text in one process and later relays it, or touches
  secrets, raises an alert even when that happens in another process.
- The installer's record command accepts only the changes the installer makes,
  and `bp uninstall --purge` refuses a directory without bp's marker file.
- Moving from Go 1.25.7, which no longer gets security fixes, to Go 1.27.2
  takes govulncheck from 34 reachable vulnerabilities (measured with Go 1.25.7)
  to none. CI now runs govulncheck and cross-compiles bp for macOS on pushes
  to `dev` and on pull requests.

## Earlier releases

Releases up to 1.9.30 predate this file. Every signed release has a manifest
with its version, source revision and artifact hashes at
`https://bp.tunapro.xyz/releases/v<version>/manifest.json`; the same manifests
are kept in [`site/releases/`](site/releases/) in this repository, from 1.6.0
on. Git tags and GitHub Releases exist for v1.6.0 through v1.8.7, and the
commit history (`git log`) has the details of every change.

[Unreleased]: https://github.com/tunapro1234/blueprint/compare/7c404da...dev
