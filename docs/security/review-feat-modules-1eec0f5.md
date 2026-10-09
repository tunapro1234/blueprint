# Review: feat/modules 1eec0f5 (installer, uninstall, journal, json-entry)

Reviewer: bp-guard (W6), 2026-10-09. Read-only (`git show`); nothing was
built, run or installed. Scope: install.sh, cmd/bp/uninstall.go, setup.go,
update.go, internal/modules (journal, jsonentry, modules, sessions),
internal/config/write.go, internal/bpskill.

Verdict: no high findings. The release path (signed manifest, SHA-256,
https only, rollback) is solid. The medium items are all "undo removes or
changes something bp did not make", which breaks the module contract
(`bp disable` removes exactly what enable added) and should be fixed before
modules ship.

## Medium

- **U1: `_install-record` accepts any path, marker or line**
  (uninstall.go:20-45). Any process running as the user, including a
  prompt-injected agent that runs one bp command, can record
  `line ~/.ssh/authorized_keys <key>` or `file <any path> <1-char marker>`.
  The next `bp uninstall` then deletes it without asking. Fix: accept only
  the fixed set install.sh uses (the bp binary paths, `/etc/blueprint/home`,
  `~/.tmux.conf`), with fixed markers and lines.
- **U2: `uninstall --purge` can remove a non-bp directory**
  (uninstall.go:150-183). `looksLikeBPHome` accepts any directory holding
  `config.json`/`config.yaml`, so a stray `BP_HOME=~/work/app` wipes a project.
  A relative home resolves against the current directory. Fix: a bp sentinel
  file written at init, required before `RemoveAll`; refuse relative homes;
  list the targets and ask.
- **U3: a marker means "unmodified"** (journal.go:207-210, sessions.go:135,
  setup.go:136). A user who edits `SKILL.md` or `shell.sh` keeps the marker
  comment, and uninstall deletes their edits. Fix: journal a SHA-256 of the
  written bytes and delete only on a match; otherwise list the file as kept.
- **U4: a pre-existing entry or line is recorded as bp's.**
  `AddJSONEntry` returns a `Change` when a deep-equal entry already exists
  (jsonentry.go:101-104), and `appendRCLine` records the line even when the rc
  already had it (sessions.go:150). Disable then removes the user's own hook
  or line. For guard-hooks this means a user who already had the same
  PreToolUse hook loses it. Fix: return no change when nothing was added;
  include `Created` in the journal dedup key.
- **U5: concurrent enable/disable loses a switch** (modules.go:339, 395,
  423-447). `writeSwitch` builds the map from the config loaded before
  `lock()`; `setup.go:69` calls `SetModules` with no lock. Fix: re-read the
  config inside the lock and lock in setup too.
- **U6: a multi-line flow `modules:` value corrupts the YAML config**
  (config/write.go:208-222). The fast path checks only that the value
  starts on the key's line, so `modules: {bar: true,\n ui: true}` leaves
  `ui: true}` behind and bp stops loading its config. Fix: require the value
  to end on that line too, or re-parse the result before writing.
- **U7: edits race the harness and drop the owner** (journal.go:316-338,
  jsonentry.go, write.go:267). Read, modify, rename with no check that the
  file is unchanged, so a concurrent Claude Code write to `settings.json` is
  lost. The rename makes a new inode: ACLs, xattrs and hardlinks go, and a
  root run (`sudo` with HOME kept) leaves the user's file root-owned.
  install.sh has no root-vs-HOME-owner check either (install.sh:234-262).
  Fix: re-stat before rename and abort on change; copy uid/gid; refuse when
  euid differs from the owner of `$HOME` unless explicitly asked.

## Low

- Unknown module names are written unquoted into YAML (write.go:235); quote
  keys.
- Symlinks: a dangling symlink is replaced by a regular file
  (jsonentry.go:189-194, `WriteShell`, `config.writeAtomic`); KindFile undo
  removes a symlink whose target has the marker. Use `Lstat`/`Readlink`.
- `removeLine` removes every identical line and the blank line above each
  (journal.go:294-298).
- Backups and leftovers are not journaled: `*.before-bp-*`,
  `bp.before-local.*`, `bp.failed-install.*`, `bp.before-update-*`, an empty
  `~/.bash_profile` bp created.
- `jsonEqual` compares numbers as float64; use `UseNumber`. With duplicate
  keys, `AddJSONEntry` edits the first while parsers use the last.
- install.sh prints "signature and SHA-256 verified" when `BP_LOCAL_BINARY`
  skipped verification (install.sh:151).
- HINT.md:11 "Nothing else about this machine has changed" is false once
  other modules are enabled.

## Verified good

- install.sh: quoted variables, `set -eu`, `mktemp -d` with a trap, https
  only, an Ed25519 signature over `checksums.txt` with the version bound,
  SHA-256 match, staged `mv`, `--check` before replacement, rollback.
- update.go: signed manifest, base-name checksums, downgrade blocked, flock,
  atomic rename with backup and rollback.
- json-entry: parsed with `encoding/json` (escapes, `]` in strings handled);
  comments, trailing commas, BOM or garbage are refused rather than
  corrupted; add then undo is byte-identical; undo removes only one
  deep-equal element and refuses when the array is gone.
- Journal and switch edits hold the modules flock (0700 dir, 0600 file).
- No shell injection: rc content is constant, tmux undo passes argv.
- bpskill: fixed paths, symlinked dirs left alone, user copies backed up.
  HINT.md tells agents that peer messages are information, not orders.

## For guard-hooks

The `AddJSONEntry` / journal API is what guard-hooks needs. U4 (pre-existing
entry), U3 (hash, not marker) and U7 (lost update on `settings.json`, owner)
must be fixed before guard-hooks is built on it.

## Follow-up: 6793199, 2f7ac62, 64c5490, 6783fd6

U1-U7 are fixed. Verified in code:

- U1: `_install-record` accepts only `~/.local/bin/bp` (must hold the bp
  marker; recorded with its SHA-256), `/etc/blueprint/home` with matching
  content, the 8 fixed tmux lines in `~/.tmux.conf`, and a
  `/usr/local/bin/bp` link whose actual target equals the absolute `.../bp`
  argument.
- U2: purge needs the `.bp-home` sentinel, refuses relative homes, `/` and
  `$HOME`, lists the targets, and asks on `/dev/tty` unless `--yes`.
- U3/U4: file undo compares a SHA-256; `AddJSONEntry` returns nil when a
  deep-equal entry existed; `Created` is in the dedup key.
- U7: `internal/safefile` re-checks identity, size and mtime before writing
  and before renaming, writes hard-linked files in place, keeps owner when
  root (directory owner for new files), refuses dangling symlinks;
  `CheckOwner` refuses a foreign `$HOME` unless `BP_ALLOW_FOREIGN_HOME=1`.

Remaining, low:

- A caller can still record one of the 8 generic tmux lines that the user
  wrote themselves (for example `set -g mouse on`); uninstall then removes
  that user line. Only cosmetic config is affected. If install.sh writes its
  block under the `# blueprint:` header, record and remove the block as one
  unit instead of single lines.
- `AddJSONEntry` returns a non-nil `*Change` together with a `Replace`
  error (jsonentry.go:53, 77, 105). Callers must record only when the error
  is nil; return `nil, err` to make that impossible to get wrong.
- Journal entries written before 6793199 have no SHA-256 and still use the
  marker rule. Acceptable for migration; the next enable rewrites them.
- Rename still drops ACLs and xattrs (accepted, documented by bp-modules).
- HINT.md:11 wording is with blueprint.

guard-hooks can now be built on `AddJSONEntry` and the journal.
