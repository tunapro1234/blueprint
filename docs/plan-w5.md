# W5 plan: harness compatibility (bp-compat)

Owner: bp-compat. Branch: `feat/compat`. Base: dev `de916f9`.
Goal: bp works well with every agent people use, and the behaviors all agents
share are solid and tested.

## Deliverables

1. **Survey** — `docs/harnesses.md` (prose, sourced) and
   `docs/harnesses.json` (machine-readable matrix, one object per harness,
   fixed field set, `"unknown"` instead of guesses). Covers the CLI harnesses
   (Claude Code, Codex, Gemini, Qwen, OpenCode, Hermes, OpenClaw, Antigravity,
   Cursor, Copilot, Amp, Goose, Aider, Grok, Cline/Roo, Kiro, Kimi, Warp),
   hosted agents (Devin, Jules, Codex cloud, Copilot agent, Cursor background,
   Claude Code web), chat apps (ChatGPT, Claude.ai, Gemini, Grok) and bots
   (Telegram, Discord, Slack). Raw gathering by parallel Haiku subagents;
   every fact is checked by me against the source or the local binary before
   it lands in the matrix.
2. **Common behaviors** — `docs/behaviors.md`: the contract every harness
   adapter must meet, with an id per behavior:
   - B1 receive at a turn boundary; B2 never interrupt a busy turn;
   - B3 confirm delivery (verified / unverified, never silent);
   - B4 survive compaction (pending messages and identity kept);
   - B5 resume after restart (same identity, same session);
   - B6 report context and usage; B7 rename and title.
   Per harness: which delivery path from direction.md applies
   (terminal / push / hooks / pull), in priority order.
3. **Adapters** — new package `internal/harness`: one `Descriptor` per
   harness holding the data that changes with TUI versions (process names,
   busy and idle signatures, composer glyphs, transcript roots and record
   markers, compaction markers, resume/name flags, hook and MCP config paths,
   supported delivery paths). Code that today hard-codes these literals
   (mostly `internal/tmux`, `internal/cache`, `internal/book`) reads them from
   the descriptor, so a new TUI version is one edit. Moves happen in small,
   behavior-preserving commits with the existing tests as the guard.
   - **Claude Code hook path:** `bp hook claude <event>` handles
     `UserPromptSubmit` (adds queued messages as `additionalContext`) and
     `Stop` (returns `decision: block` with the queued messages as the reason,
     guarded by `stop_hook_active` and a per-turn cap). Messages are claimed
     from msgq under the record lock, so the tmux dispatcher and the hook can
     never deliver the same record twice; the hook delivery is recorded as
     verified with path `hook`. Installed through the `--settings` layer bp
     already passes to Claude sessions it launches (no change to the user's
     own settings); a user-level install is an explicit opt-in later.
   - **Compaction:** pending messages live in msgq and the spool, so they are
     already on disk; the work is (a) treat "compacting" as busy for every
     harness, (b) re-inject identity after compaction (Claude `SessionStart`
     with `source=compact`; Codex/Hermes/OpenCode via their compaction
     markers in the transcript, delivered as a short identity note at the
     next turn boundary), (c) keep the transcript witness working across a
     compaction boundary so in-flight messages are not re-pasted or lost.
4. **Conformance tests** — `internal/harness/conformance`: a table of
   behaviors × harnesses. Each harness gets a fake TUI (small Go program that
   renders the real busy/idle/composer screens recorded from the real CLI and
   writes a transcript in the real format) run under `tmux -S <tmp>`. Real
   CLIs run the same table behind a build tag / env flag when installed and
   logged in. Free CLIs get installed on this host; logins that need the
   owner's accounts are listed for blueprint.

## Order

1. Plan (this file) → blueprint.
2. Survey fan-out (running) + code map of current harness knowledge.
3. `internal/harness` descriptors for Claude, Codex, Hermes, OpenCode with
   the literals moved in; no behavior change.
4. Claude hook path + compaction identity re-injection + tests.
5. `docs/harnesses.md`, `docs/harnesses.json`, `docs/behaviors.md`.
6. Fake TUIs and the conformance table; real-CLI runs where available.
7. Descriptors for the next tier (Gemini, Qwen, Cursor, Copilot, Goose,
   Aider, Amp, Kimi, Antigravity, OpenClaw) as data + pull/MCP hints.

## Coordination

- **bp-term (`internal/term`):** descriptors are data only; the terminal
  layer reads busy/composer signatures from `internal/harness` instead of
  owning them. I will not add new direct tmux calls; any capture/paste I need
  goes through whatever `internal/term` exposes, and until it lands I touch
  `internal/tmux` only to replace literals with descriptor lookups.
- **bp-api (MCP/HTTP):** the matrix records each harness's MCP config path
  and format; bp-api owns the MCP server, I own writing its entry into each
  harness's config (behind the modules check) and the pull-path tests.
- **bp-guard:** messages injected by hooks pass through `guard.Frame` once it
  exists; until then the hook output uses the same envelope as terminal
  delivery.

## Rules kept

No push; no live install (`/srv/blueprint/state`, `config.json`,
`/usr/local/bin/bp`, `blueprint.service`); tests on temp roots with
`env -u TMUX -u TMUX_PANE` and `tmux -S`; heat check below 80C and
`flock /run/lock/bp-gotest.lock nice -n 10` for Go builds and tests; English
only; each finished step is committed and reported with `bp msg blueprint`.
