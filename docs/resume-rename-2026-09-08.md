# Local resume ownership and Codex native names — 2026-09-08

Source: `60d047a5d2645dada785cf411aaa88f1cc393ccb` (dev).
Linux amd64 SHA-256: `a30093e3b9b5ecd512b99fdf9477a03c794878a621154aa36d067e4c252eb769`.

The local launcher previously allocated a random bp name on every `claude -c`,
allowing several panes to resume the same native transcript. Resume now pins the
selected UUID, serializes ownership lookup and tmux creation, attaches to one
existing owner, reuses a closed owner's name, and rejects multiple live owners.
Existing duplicate panes and transcript files are retained. See configuration.md
for boundaries: BP_HOME/tmux scope, explicit resume UUID, and native TUI switching.
Physical cwd registration fixes new symlink-based folder discrepancies.

Codex names now come from its session_index.jsonl using the already bound exact
thread ID. The same canonical-name and authority separation used by Claude applies.
Native names do not repair missing runtime bindings or grant sender authority.

Validation: go test ./... and go vet ./... passed. Nineteen isolated real-tmux
fake-CLI tests passed, including concurrent resume, existing live ownership,
closed-record reuse, symlink continue, and duplicate-owner refusal. A twentieth
isolated test passed for Codex native rename, alias-based color changes, and
rejection of a server-main display name. These consume no model quota.

Published all four Linux/macOS builds; downloaded and checked all four binaries,
checksums.txt and install.sh against the candidates. Host CLI and actual Blueprint
daemon executable match the SHA above. All existing tmux pane PIDs are unchanged.
Only blueprint.service was restarted; shared Codex and other agents were untouched.

Evidence and retained backups: dist/resume-rename-20260908/{rollout.json,
public-verification.json,backup-20260908T114158Z}. The user's laptop and its existing
four advice panes were not accessed or modified; updating the laptop remains
necessary before its next launch uses this code.
