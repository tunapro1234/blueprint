# Changelog

All notable changes to bp are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and bp uses
[semantic versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Changes on `dev` since the 1.9.34 release.

### Added

- Backup Claude accounts: `bp account role N normal|backup` and
  `bp account priority N P` (lower first); `bp account list` and the
  keepalive plan show the role. Auto switching ignores backups while the
  active normal account has room. `claudeAccounts.backup.switchOrder`
  picks when a backup is used: `after-normals` (default; only when every
  normal account is used up) or `interleave` (A1 B1 A2 B2 ...). On a
  backup, auto switching returns to a normal account once a used-up one
  has reset. `backup.stagger: interleave` (default) alternates normal and
  backup keepalive windows; `backup.keepAliveBackups: false` leaves backups
  out of keepalive.
- `bp remote --off [--wait <duration>] [<agent>...]` turns Remote Control off
  in Claude sessions (every live one when no name is given). It opens the
  /remote-control menu, selects "Disconnect this session" and confirms the
  "Remote Control disconnected." line. A session that was not connected is
  connected by the command itself, so bp sends it a second time and
  disconnects it again. A working agent or a composer holding text is never
  touched: it is retried every 20 seconds until `--wait` (default 10m) runs
  out and is then reported as still busy.
- Config `remoteControlOnOpen` (default `false`).

### Changed

- `bp open`, resume and daemon relaunches no longer connect Claude sessions to
  Remote Control. A resumed session that reconnects on its own (its banner
  reads `· /rc`) is disconnected. Set `remoteControlOnOpen: true` for the old
  behaviour (/remote-control and /rename at startup). `bp doctor` reports a
  dropped Remote Control only when that setting is on or the agent is named.
- The Claude account auto switch polls the active account every minute while
  its burn rate would carry it past its limit before the next regular poll.

## [1.9.34] - 2026-10-15

### Fixed

- `bp hop` no longer refuses a working directory that contains a file whose
  name merely includes `..` as a substring (for example the `do..ts.txt`
  fixture shipped inside some npm packages). The bundle path check rejected any
  path containing `..` anywhere; it now rejects only a real `..` path
  component, so genuine traversal is still refused while ordinary filenames
  pack and extract unchanged. This affected both the single-agent and
  recursive bundle paths and could block hopping a whole `node_modules` tree.

## [1.9.33] - 2026-10-15

### Added

- `bp hop <agent> --to <peer>` moves an agent to another machine over P2P: its
  conversation transcript and, by default, its working-directory files travel in
  a single gzip-tar bundle streamed over the existing authenticated libp2p
  connection. The default is a move — the bundle is delivered for the peer
  owner's review, then the source agent is stopped and archived with its files
  kept (never deleted). Flags cover the other cases: `--copy` leaves the source
  running, `--no-files` carries only the conversation, `--delete-source` removes
  the source files (explicit opt-in), `--raw` includes `.git/` and gitignored
  files, and `--dry-run` shows the bundle without sending it. Moving an agent in
  the middle of a turn is refused. The transfer is a new ingress: a peer may
  hop an agent onto a machine only when that machine opts it in with `allowHop`
  on the peer (off by default; connectivity and exposure never grant it), each
  bundle is capped (`hopMaxBytes`, default 2 GiB), and an incoming bundle is
  quarantined with a pending record — nothing is materialized or launched until
  the owner reviews it with `bp hop list` and runs `bp hop accept <id>`
  (`bp hop reject <id>` discards it). Accept re-validates every bundle entry
  (fail-closed extraction, path-traversal and symlink guards, size cap), places
  the files and transcript, and resumes the agent. See [p2p.md](docs/p2p.md).

- `bp hop <root> --to <peer> --recursive` moves a whole hierarchy — an agent and
  every descendant under it — as one bundle that preserves the parent/child
  structure, so an entire fleet migrates to another machine in a single
  transfer. One recursive bundle carries at most a thousand agents
  (`MaxHopAgents`), a bound checked on both ends. On accept, every agent's files
  and transcript are rebuilt and the agents are registered in the agentbook as
  closed, revivable entries — **nothing is launched**, so accepting a migrated
  fleet never starts dozens of sessions at once; revive one later with
  `bp open <name> <dir> --resume`. `bp hop accept <id> --parent <name>` attaches
  the subtree's root under a local agent, and `--resume` opts into launching the
  whole subtree parent-first right after accept. A single-agent hop still
  auto-resumes on accept as before.

- The token counter now includes subagent and workflow transcripts. Usage from
  `<session>/subagents/*.jsonl` and `<session>/subagents/workflows/wf_*/agent-*.jsonl`
  was never read, so a busy day's subagent work — often larger than the main
  agents' own — went uncounted. Each record is attributed to the main agent that
  owns the session and tagged with a `kind` (`subagent` or `workflow`, with the
  workflow id), so `bp tokens` shows a main agent's own usage and the work it
  farmed out to subagents separately. Records are deduped by request id (then
  message id), so a subagent turn that also appears in the main transcript as a
  sidechain is counted once. `bp tokens backfill` runs the one-time full scan of
  the previously-unseen transcripts and refuses to run during the 20:00–22:59
  Europe/Istanbul heat window unless given `--force`. The scan counts only
  transcripts within the 14-day raw-retention window, so a day whose raw tier
  has already aged out is never recomputed from subagent-only records and its
  retained daily and hourly totals stay intact.

## [1.9.32] - 2026-10-11

### Changed

- Automatic account switching with `claudeAccounts.switchPrefer: "soonest-reset"`
  (the default) now keeps the active account on whichever usable account resets
  soonest, switching proactively as the order changes rather than only when the
  active account hits its threshold. Usage lands on the five-hour window that is
  about to refresh, and the active account rotates roughly every (5h / number of
  accounts). A short reset-time hysteresis and the existing cooldown keep two
  near-simultaneous resets from flapping. `switchPrefer: "room"` is unchanged.

### Fixed

- A Claude agent whose turn was left structurally open — its close never
  recorded because the session was interrupted, Remote Control dropped, or the
  signed-in account changed mid-turn — no longer has its message queue blocked
  indefinitely. Once the open turn is older than the 15-minute ceiling the live
  pane decides: if it shows no spinner, no dialog and no half-typed draft, the
  agent is idle and deliverable. This matches what the `TurnOpen` gate already
  concluded for the same case; a turn genuinely in progress still shows a
  spinner and is never treated as idle.

## [1.9.31] - 2026-10-10

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

- The `monitor` module no longer raises a false "cannot find its session file"
  alarm for a live Claude agent whose large transcript is momentarily
  unreadable (compaction, a concurrent write, fd pressure under load). The
  session-file check is debounced across two consecutive hourly sweeps, so a
  genuine misconfiguration still alarms one beat later while a transient read
  blip is ignored.
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
[1.9.31]: https://github.com/tunapro1234/blueprint/compare/7c404da...dev
