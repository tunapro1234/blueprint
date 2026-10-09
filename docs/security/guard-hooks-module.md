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
extra path patterns (W7 supplies the defaults). Apply turns them into the
hook command line (`--ask`, `--canary <path>` per pattern), so the hook never
reads bp's config on the fast path.

## What Apply changes (Claude Code)

Decision (9 Oct, after blueprint found the tripwire dormant): nothing in the
user's own settings changes. When the module is on, `prepareLocalObservation`
(cmd/bp/local_observation.go) adds one PreToolUse group to the per-agent
`--settings` layer that already carries bp's other Claude hooks. Disabling
needs no undo journal. Only agents opened or restarted after enabling are
covered, and `bp guard status` must say so. The group, with the absolute bp
path in place of `bp`:

```json
{"hooks": {"PreToolUse": [{"matcher": "Read|Bash|Grep|Glob|Edit|Write|WebFetch",
  "hooks": [{"type": "command", "command": "bp guard hook claude [--ask] [--canary <path>]...", "timeout": 5}]}]}}
```

The earlier plan to edit `~/.claude/settings.json` through the journal is
dropped. `json-entry` landed in W2 for other modules but guard does not use
it. The original proposal, kept for the record:

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
   or `guard.reach.canary` (alert) when a canary path matched, tainted or
   not. Untainted access to an
   ordinary secret path writes nothing: that is normal work.
6. Mode `ask` and tainted: print
   `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"bp guard: this agent received outside text from <peer> <n> min ago; confirm access to <path>"}}`
   so the human decides. Never `deny`, never block an untainted agent, and any
   internal error exits 0 (a broken guard must not break the agent).

## What guard needs from W5 (bp-compat)

A per-harness `ToolCall{Agent, Harness, Tool, Input string; Time time.Time}`
source: hook payload where the harness has a pre-tool hook, transcript tail
otherwise. guard consumes it; it does not install anything itself.

## Status

Runtime implemented on feat/guard: `internal/guard/hook.go` (`ParseHook`,
`Flatten`, `MatchCanary`, `TaintFrom`, `HookConfig.Match`/`Evaluate`) and
`cmd/bp/guard_hook.go` (`bp guard hook claude`, run before normal startup;
config and identity load only after a match).

Module wired: `guard-hooks` is in the registry
(`internal/modules/modules.go`). It is off by default and owns
`guardHooks {mode, canaries}`, validated in `internal/config`. `Apply` is
nil. When the module is on, `prepareLocalObservation` adds one PreToolUse
group (`guardHookGroup` in `cmd/bp/guard_hook.go`) to the per-agent settings
layer. The layer exists only while `localObservation` is on.
`guardHooksConflicts` refuses to enable the module when the hook would never
run: the sessions module is off (plain `claude` launches never pass through
bp), `localObservation` is off, the managed settings set `disableAllHooks` or
`allowManagedHooksOnly`, or the user settings set `disableAllHooks`.
`Detect` turns the module on only for a config that already has
`guardHooks`; the legacy server fixture asserts it stays off. Enabling it on
the live install is the owner's call: `bp enable guard-hooks`, then reopen
the agents.

## Tests

- Enable then disable leaves `settings.json` byte-identical when the user had
  other hooks before and after.
- Hook latency on a miss under 20 ms with a 10 MB `messages.jsonl`.
- Tainted read of a canary or `.credentials.json` writes one alert; `ask`
  mode prints the decision JSON; untainted ordinary access writes nothing.
- Malformed stdin, missing state dir, unreadable log: exit 0, no output.

## Implementation plan (W2 registry conventions from bp-modules, 9 Oct)

- Add a `GuardHooks = "guard-hooks"` const next to the other module names.
  `Owns: []string{"guardHooks"}`, matching the JSON name of the config key.
- `Apply` stays nil. The PreToolUse group is generated into bp's own
  per-agent settings layer at launch, gated on
  `modules.EnabledIn(cfg, "guard-hooks")`. Disabling then drops the group at
  the next launch.
- `Conflict` (read-only): Claude merges hook arrays across settings layers,
  so a user PreToolUse hook is never shadowed. The real conflicts are setups
  where the hook would silently never run (see Status).
- `Notes`: "applies to agents opened from now on; running agents keep their
  settings until reopened; alerts: bp audit --kind guard.reach".
- `Detect`: enable the module only when `cfg.GuardHooks` is already set.
  Never tie it to `cfg.Legacy`, so the owner server keeps its six modules.
- Tests: `TestFreshConfigEnablesNothing` and
  `TestSetupChangesNothingOutsideBPHome` stay green, and the legacy fixture
  asserts guard-hooks is off. Enabling and disabling the module is already
  audited by `module.enable` and `module.disable`.
