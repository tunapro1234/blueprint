# Module design: compaction-hooks

Status: implemented on `feat/compaction-hooks`. Off by default, like every
module that changes a harness's own configuration (principle 1): enabling
writes the artifacts below, `bp disable compaction-hooks` removes exactly
what enable added.

## Why a module

Claude Code already survives a context compaction: `cmd/bp/hook.go`'s
`SessionStart(source=compact|clear)` answer restates the bp identity
(`internal/identity.CompactionNote`) through `additionalContext`, and the
agent keeps the pane it was opened in. OpenCode and Hermes have no such hook
wired, so an agent that compacts under either harness forgets it is a bp
agent and stops answering `bp msg`. This module closes that gap for both,
reusing the exact same note text Claude already shows.

## Injection verification (read before trusting this doc; the facts are what
matter, not the design)

### OpenCode — real, verified against the installed SDK

- Detection: the plugin `event` hook receives `{ event }`; `Event` is a
  tagged union including `EventSessionCompacted = { type: "session.compacted",
  properties: { sessionID: string } }`
  (`@opencode-ai/sdk/dist/gen/types.gen.d.ts`, installed at
  `/root/.config/opencode/node_modules/@opencode-ai/sdk`). Fires after
  compaction completes.
- Injection: `PluginInput.client` is `ReturnType<typeof createOpencodeClient>`,
  whose `Session` class has `promptAsync` —
  `POST /session/{id}/prompt_async`, body `{ parts: [...], noReply?: boolean
  }`, 204 response (`@opencode-ai/sdk/dist/gen/sdk.gen.d.ts`,
  `SessionPromptAsyncData`). `noReply: true` adds the message to the session
  without starting a new agent turn — the plugin calls
  `client.session.promptAsync({ path: { id: sessionID }, body: { parts:
  [{ type: "text", text: note, synthetic: true }], noReply: true } })`.
- Plugin discovery directory: `~/.config/opencode/plugins/` (global) or
  `.opencode/plugins/` (project), confirmed against
  `https://opencode.ai/docs/plugins` and the `opencode plugin <module>`
  subcommand (that subcommand installs a published npm plugin and edits
  `opencode.json`'s `plugin` array; it does not fit a local, bp-owned file, so
  the module drops the file straight into the plugins directory instead).
- What the vendor docs do NOT show (and this doc does not claim): a worked
  example combining `session.compacted` with `promptAsync`. The contract
  above comes from the installed type definitions, which are closer to the
  shipped behavior than prose documentation.

### Hermes — real, verified against the installed shell-hooks doc and source

- Hermes has four hook systems; only two run for a CLI/TUI session bp opens:
  **plugin hooks** and **shell hooks** (`hooks.md`, "Event Hooks" table).
  **Gateway hooks**, including `session:compress` (which reports
  `old_session_id`/`in_place` directly), run "Gateway only" and never fire for
  a bp-opened CLI session — this is why the module does not use that event.
- The only event that can inject LLM context from a shell hook is
  `pre_llm_call` (`hooks.md`, "Shell Hooks" comparison table: "Can inject LLM
  context: Yes (`pre_llm_call`)"). Its JSON wire payload has `extra.
  is_first_turn` and `extra.parent_session_id`
  (`hooks.md` "Shipped plugin-hook catalog", `pre_llm_call` row).
- Detection: "Compression-triggered session splitting via parent_session_id
  chains" (`hermes_state.py:12`); `hermes_state.py:5172`,
  "`parent_session_id` is set (compression fork, delegate/subagent ...)". So
  the first turn (`is_first_turn: true`) of a session that already has a
  `parent_session_id` is the signature of "this session was just forked by a
  compression". There is no such signal for **in-place** compression (same
  session id, no fork): that case is not covered — see "Known gap" below.
- Injection: `pre_llm_call`'s stdout contract is `{"context": "..."}`,
  appended to the user message (`hooks.md`, "JSON wire protocol" and
  "pre_llm_call" sections). Any other output, or none, is a silent no-op.
- Shell hooks need first-use consent per `(event, command)` pair
  (`~/.hermes/shell-hooks-allowlist.json`). The module does not pre-populate
  that file; the user accepts the prompt the first time Hermes runs the hook,
  same as any other shell hook they add by hand.

### Codex — out of scope

Per the 2026-10-07 decision, no new Codex work; `internal/harness/core.go`
already records Codex's `BIdentityAfterReset` as `Planned`, unchanged by this
module.

## Known gap

Hermes in-place compression (no session-id rotation) leaves no signal a shell
hook can observe, so that path is not covered.
`internal/harness/core.go` records `BIdentityAfterReset` as `Partial` for both
OpenCode and Hermes, not `Supported`: the module is opt-in and off by
default, and this gap is real for Hermes.

## What the module installs

`CompactionHooks = "compaction-hooks"` (`internal/modules/modules.go`), owns
`compactionHooks`, `Conflict` is `compactionHooksConflicts`
(`internal/modules/compaction.go`): refuses when the `sessions` module is
off, because the hook resolves its caller through the same pane-based
identity `bp _hook claude` already uses — a plain `opencode` or `hermes`
process started outside bp would call the hook as nobody.

`Apply` (`applyCompactionHooks`) writes, every time the module is enabled
(refreshing bytes bp still owns; never touching a file the user has since
edited — the same `KindFile` SHA-256 check every other module's whole-file
writes use):

- `~/.config/opencode/plugins/bp-compaction.js` — the plugin above.
- `~/.hermes/agent-hooks/bp-compaction.sh` — `exec <bp> _hook hermes`,
  forwarding the hook's stdin straight to bp.
- One entry in `~/.hermes/config.yaml`'s `hooks.pre_llm_call` list
  (`{command: <script path>, timeout: 5}`), added with a new journal
  primitive, `internal/modules/yamlentry.go` (`KindYAMLEntry`): the YAML
  equivalent of `KindJSONEntry` for an array a harness's own hook config
  owns. It edits a parsed `yaml.Node` tree rather than preserving bytes
  verbatim (YAML does not admit the same precise byte-splicing JSON does),
  so comments elsewhere in the file survive but exact formatting of the
  touched nodes may not; undo compares the decoded entry, not the bytes.

Both artifacts are written regardless of whether that harness is installed on
this host: an unread plugin file or an unused hook entry is inert.

## `bp _hook opencode` / `bp _hook hermes` (runtime)

Both are new cases in the existing `bp _hook <harness>` dispatch
(`cmd/bp/hook.go`, `hookCommand`), next to `bp _hook claude`. All three share
`identity.CompactionNote` and `claimForHook` (queued messages, framed through
the same untrusted-input seam as every other delivery path).

- `bp _hook opencode`: no gating — the plugin only calls it already knowing
  `session.compacted` just fired. Answers `{"note": "<text>"}` on stdout.
- `bp _hook hermes`: reads the shell hook's JSON stdin itself and only
  answers when `hook_event_name == "pre_llm_call"` and `extra.is_first_turn
  && extra.parent_session_id != ""` (the compression-fork signature above).
  Every other turn, and any input that fails to parse, answers with nothing.
  Answers `{"context": "<text>"}` on stdout — Hermes's own `pre_llm_call`
  contract.

A broken hook (bp missing, malformed JSON, a rejected HTTP request) must
never break the agent: every failure path in the plugin, the shell script and
the Go handlers is swallowed.

## Tests

- `internal/identity/compaction_test.go`: the note carries the marker, the
  agent name and the `bp msg`/`bp status` instructions.
- `internal/modules/yamlentry_test.go`: add/idempotent-add/undo round trip,
  creating a missing file or a missing key, and refusing to undo an entry the
  user has since replaced.
- `internal/modules/compaction_test.go`: off by default and never
  auto-detected; conflicts without `sessions`; enable writes all three
  artifacts (plugin content, executable hook script, config.yaml entry) and
  is idempotent; disable removes them (including the whole `config.yaml` when
  bp created it and nothing else is left) while leaving a user-edited plugin
  or a user's own hooks and keys in place.
- `cmd/bp/compaction_hook_test.go`: `bp _hook opencode` returns the note plus
  queued messages, framed when external; `bp _hook hermes` injects only on
  the first turn of a session with a `parent_session_id`, and is a silent
  no-op for every other turn, the wrong event, or malformed input.

No test drives a real OpenCode or Hermes compaction end to end (that needs a
live provider call under `/run/lock/agir-tarayici.lock`-class constraints,
out of scope for this change); what is tested is everything on bp's side of
the contract documented above.
