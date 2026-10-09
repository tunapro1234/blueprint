# bp work plan, October 2026

This plan turns [direction.md](direction.md) into code. Each workstream has
one owner agent, one branch and one worktree. `blueprint` reviews and merges
into `dev`; nobody else pushes.

## Workstreams

| Id | Owner | Branch | Scope |
|---|---|---|---|
| W1 | blueprint | dev | P2P safety: `internal/audit` (audit.jsonl), `messages.jsonl`, untrusted framing of inbound messages, per-peer rate limit, loop cap, lookup limited to exposed agents |
| W2 | bp-modules | feat/modules | module registry, `bp enable/disable <module>`, zero-change installer, `bp uninstall`, upgrade migration that keeps existing installs exactly as they are |
| W3 | bp-api | feat/api | local HTTP API and MCP server in the A2A shape; rooms (pub/sub) and a shared board so agents can work as one group; remote MCP gateway for web chat apps (ChatGPT, Claude.ai) behind auth, off by default |
| W4 | bp-term | feat/term | terminal layer interface; tmux backend over one control-mode connection (fast `bp status`); built-in pty backend |
| W5 | bp-compat | feat/compat | survey every agent bp can talk to; compatibility matrix; per-harness adapters for delivery, busy and turn boundaries, compaction, resume; conformance tests |
| W6 | bp-guard | feat/guard | threat model; `internal/guard` (framing, injection scan, outbound redaction, capability policy); reviews every change that touches inbound data |
| W7 | bp-redteam | feat/redteam | attacks against a sandboxed dev build only; LLM-in-the-loop tests with canary files that show whether an agent tries to reach what it was not given |
| - | bp-site, bp-ui, bp-strategy | site/dev, ui/dev | as briefed earlier |

## Shared contracts

- **Audit log:** `internal/audit`. `audit.Append(root, audit.Event{...})`
  writes one JSON line to `<state>/audit.jsonl`. Every security-relevant
  decision is logged: message accepted, rejected, held, delivered; peer
  connect and reject; expose list change; module enable and disable; loop
  cap and rate limit hits; guard findings.
- **Messages log:** `<msgq root>/messages.jsonl`, one line per final message
  state, written by msgq (`msgq.LogEntry`); `bp messages reindex` adds
  older `done/` records. Readers never parse `done/`.
- **Inbound framing:** text that crosses a trust boundary (P2P, HTTP from a
  non-local caller, remote MCP, rooms fed by peers) reaches an agent only
  through `guard.Frame`. The frame names the source, says the content is
  untrusted data and not instructions from the owner, and cannot be closed
  from inside the body.
- **Modules:** `internal/modules` owns the registry. A feature that changes
  the user's environment (tmux options, shell rc, daemon jobs, credentials)
  checks `modules.Enabled(name)` first.
- **Delivery:** `internal/term` owns writing to and reading from agent
  terminals. New code never calls tmux directly.

## Rules for every owner

- Work only in your worktree. Commit on your branch; do not push; do not
  merge other branches except `origin/dev`.
- Never touch the live install: `/srv/blueprint/state`, `/srv/blueprint/config.json`,
  `/usr/local/bin/bp`, `blueprint.service`, real peers, or other agents.
  Tests use temporary roots, `env -u TMUX -u TMUX_PANE` and `tmux -S <tmp socket>`.
- Heavy work: check `sensors | grep "Package id 0"` is below 80C first. Run
  Go builds and tests with `nice -n 10` and hold `/run/lock/bp-gobuild.lock`
  (`flock /run/lock/bp-gobuild.lock nice -n 10 go test ./...`). Browsers hold
  `/run/lock/agir-tarayici.lock`.
- No outbound mail, no WhatsApp, no messages to people. No new listening
  port outside tests; tests bind 127.0.0.1 port 0.
- Write code, docs, identifiers and bp messages in English.
- When a step is done: commit, then `bp msg blueprint` with the branch, the
  commit, what changed and the test commands you ran.
