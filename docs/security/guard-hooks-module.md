# Module design: guard-hooks

Status: design by W6 (bp-guard) for the W2 registry (`internal/modules` on
feat/modules). Off by default. Owner of the per-harness adapters: W5
(bp-compat); this page defines what guard needs from them.

## Why a module

Reach detection needs to see an agent's tool calls. Installing a hook edits a
harness's settings, which changes the user's environment, so by principle 1
it is opt-in and `bp disable guard-hooks` removes exactly what enable added.

## Registry entry

```go
{
    Name:        "guard-hooks",
    Description: "tool-call tripwire: alert when an agent that just received outside text touches secrets or canary files",
    Owns:        []string{"guardHooks"},
    Conflict:    guardHooksConflicts, // refuse if the harness settings file is not valid JSON or is managed by policy (managed-settings.json)
    Apply:       applyGuardHooks,
    Notes:       "alerts appear in bp audit --kind guard.reach; mode: bp config set guardHooks.mode observe|ask",
}
```

Config (`guardHooks`): `mode` = `observe` (default) or `ask`; `canaries` =
extra path patterns (W7 supplies the defaults).

## What Apply changes (Claude Code)

One entry in `~/.claude/settings.json`:

```json
{"hooks": {"PreToolUse": [{"matcher": "Read|Bash|Grep|Glob|Edit|Write|WebFetch",
  "hooks": [{"type": "command", "command": "bp guard hook claude", "timeout": 5}]}]}}
```

The journal needs one new change kind, proposed for W2:

- `json-entry`: `Path`, `Option` = JSON pointer of the array
  (`/hooks/PreToolUse`), `Value` = the exact entry added. Undo removes only an
  entry deep-equal to `Value`; the rest of the file, including the user's own
  hooks, is untouched. If the user edited our entry, undo leaves it and
  prints a note.

Other harnesses: W5's adapter decides. Where no pre-tool hook exists (Codex
today), guard reads tool calls from the transcript after the fact; nothing
is installed, so the module only enables the transcript check.

## `bp guard hook claude` (runtime)

1. Read the hook JSON from stdin (`session_id`, `tool_name`, `tool_input`,
   `cwd`). Flatten `tool_input` string values.
2. `guard.Sensitive(tool, input)` and the canary patterns. No match: exit 0
   with no output. This is the hot path and must stay under ~20 ms: no tmux
   calls, no network.
3. Resolve the agent name with bp's normal sender probes (pane env, session
   id); unknown stays unknown.
4. Taint is read from disk, not memory: the agent is tainted when
   `<msgq>/messages.jsonl` has a delivered record to it with a sender label
   starting `external:` (or a non-nil `Origin` once the log carries it) in the
   last 30 minutes. Reads only the file tail.
5. Write `guard.reach.secret` (alert) through `guard.AuditSink` when tainted,
   or when a canary path matched (always alert). Untainted access to an
   ordinary secret path writes nothing: that is normal work.
6. Mode `ask` and tainted: print
   `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"bp guard: this agent received outside text from <peer> <n> min ago; confirm access to <path>"}}`
   so the human decides. Never `deny`, never block an untainted agent, and any
   internal error exits 0 (a broken guard must not break the agent).

## What guard needs from W5 (bp-compat)

A per-harness `ToolCall{Agent, Harness, Tool, Input string; Time time.Time}`
source: hook payload where the harness has a pre-tool hook, transcript tail
otherwise. guard consumes it; it does not install anything itself.

## Tests

- Enable then disable leaves `settings.json` byte-identical when the user had
  other hooks before and after.
- Hook latency on a miss under 20 ms with a 10 MB `messages.jsonl`.
- Tainted read of a canary or `.credentials.json` writes one alert; `ask`
  mode prints the decision JSON; untainted ordinary access writes nothing.
- Malformed stdin, missing state dir, unreadable log: exit 0, no output.
