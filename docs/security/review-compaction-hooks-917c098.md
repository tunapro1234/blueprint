# Review: compaction-hooks module (917c098)

Scope: `internal/modules/yamlentry.go`, `internal/modules/compaction.go`,
and the `bp _hook opencode|hermes` handlers in `cmd/bp/hook.go`, all on dev.
The module is opt-in and off by default. Nothing is enabled anywhere.

## Medium (fixed on feat/guard)

- **M1: a dry run writes `~/.hermes/config.yaml`.**
  `applyCompactionHooks` called `AddYAMLEntry` even with a dry-run journal.
  The dry-run path also skips `CheckOwner` and the modules lock.
  `bp enable compaction-hooks --dry-run` therefore:
  - added a `hooks.pre_llm_call` entry, but did not journal it;
  - pointed that entry at a script the dry run never wrote.

  A later real enable found the entry already present and returned a nil
  change, so `disable` never removed it. This is the U4 class: bp's entry
  ends up looking like the user's. Fix: a dry run only records the planned
  entry. Test: `TestCompactionHooksDryRunWritesNothing`, which failed before
  the fix.
- **M2: a multi-document YAML file loses every document after `---`.**
  `yaml.Unmarshal` reads only the first document, and the re-encode writes
  only that one. This is the U6 class. Fix: `decodeOneYAMLDocument` refuses a
  stream with more than one document, both in add and in undo. Test:
  `TestYAMLEntryRefusesMultiDocument`; a probe showed `b: 2` dropped before
  the fix.
- **M3: enable overwrites a user's hook script or plugin.**
  `writeOwnedFile` replaced the file unconditionally, although its comment
  says it "never touches a file the user has since edited". Two consequences:
  - A re-enable lost the user's edits.
  - A file bp never wrote was replaced, and the next undo then deleted it,
    because the hash now matched.

  This is the U3/U4 class. Fix: overwrite only when the file already holds
  the new bytes, or the bytes this journal last recorded for the path;
  otherwise refuse with an error. Test: `TestCompactionHooksKeepsUserFiles`.

## Medium (pre-existing, not compaction-hooks; fixed on feat/guard after ee7fe19)

- **M4: `bp _hook <harness> --agent X` bypasses the verified identity**
  (cmd/bp/hook.go:57-63, from 8df48d9). `hookAgent` accepts only a certain
  pane label ("a guessed one would hand another agent's messages to this
  session"), but `--agent` overrides it. Any process running as the user
  can run `bp _hook opencode --agent server-main`. That includes a
  prompt-injected agent with a single shell call. The call claims
  server-main's queued messages as `delivered_hook`, so server-main never
  sees them: silent suppression and redirection of another agent's inbox.
  No production caller passes `--agent`: the settings hooks, the OpenCode
  plugin and the Hermes script all omit it.

  Fix: the flag is gone. The hook always uses `hookAgent` (a certain pane
  label), and tests go through `runHook` or the `resolveSender` seam.
  `TestHookIgnoresAgentArgument` failed on the old code: the unverified
  caller received "secret for worker" and the record was claimed.

## Low (open)

- **The whole Hermes config is re-encoded.** Indentation becomes 2 spaces,
  and quoting or flow styles may change. Enable followed by disable is not
  byte-identical, unlike json-entry. There is also no check that the output
  re-parses to the input plus one element. Add one before `Replace`; that is
  the U6 rule bp applied to its own config.
- **Merge keys are not followed.** If the user's `hooks` comes in via a
  `<<:` merge key, bp adds a sibling `hooks` that shadows the merged map.
  Rare. Detect the merge key and refuse.
- **The `command` value is an unquoted path.** If `$HOME` contains a space
  and Hermes splits the command, the hook just does not run (fail-safe).
- **`jsonEqual` compares numbers as float64.** Already known from the
  modules review.

## Verified good

- **Framing:** `claimForHook` calls `wireFraming` and uses `Wire()` for
  OpenCode and Hermes as well. External records reach those harnesses framed,
  exactly as terminal delivery would deliver them. This closes the Wire()
  check for these two harnesses; Codex is not covered here.
- **Identity:** it comes only from a certain pane label (M4 removed the
  `--agent` override).
- **Hermes hook:**
  - It answers only `pre_llm_call` on a first turn that has a parent session.
  - Stdin is capped at 2 MiB.
  - The bp path is single-quoted in the script.
- **OpenCode plugin:**
  - The bp path is JSON-encoded into the plugin.
  - It uses `execFileSync` (no shell) with a 5 s timeout.
  - It injects with `noReply`.
  - Every failure is swallowed.
- **Undo:**
  - It removes one deep-equal element and refuses on drift
    (`errModified`).
  - It deletes the file only when bp created it and only an empty skeleton
    is left.
- **Existing entries:** an identical pre-existing entry gives a nil change,
  so it is never journaled as bp's (U4).
- **Concurrency and ownership:** writes go through `safefile`, which handles
  concurrent change, owner, mode and hard links (U7).
- **Conflict:** with sessions off, enabling is refused.

## Tests

The following ran under the build lock and were green:

- `go test -race ./internal/modules/ ./internal/safefile/`
- `go test ./cmd/bp/ -run 'Module|Compaction|Enable|Disable|Uninstall|Hook'`
- `go vet ./internal/modules/ ./cmd/bp/`
