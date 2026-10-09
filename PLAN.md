# bp Rust port — plan

Status: approved by blueprint (2026-10-09). Source of truth for behavior: Go `bp`
at `origin/dev`. Branch for the port: `dev-rs` (not created yet; waits for the
history rewrite).

**Ported up to Go revision: b877eb4 (1.9.30, 2026-10-09)** — update this line after
each sync. blueprint sends the range to port after every `dev` release; for each
range, review `git log <old>..<new> -- cmd internal scripts` and port the changes
into the matching Rust modules (or note in §11 why one does not apply yet).

## 1. Goals and non-goals

Goals

- A Rust `bp` that is a drop-in replacement: same commands, flags, exit codes,
  stdout/stderr contracts, on-disk formats, lock files and tmux behavior.
- Go and Rust `bp` can run **on the same machine at the same time** during the
  migration (shared queue, agentbook, state dir, locks) without corrupting each
  other.
- Parity is proven by the existing tests, not by inspection: the Python E2E suite
  (`scripts/test_local_cli.py`) runs unchanged against both binaries.

Non-goals (for the port itself)

- No behavior redesign. Bugs found during the port are fixed in Go first (or
  recorded), so the two implementations do not diverge silently.
- No changes to the external pieces bp supervises (WhatsApp `bridge.js`,
  `/srv/monitor` scripts, usage Python scripts).

## 2. Size of the job

| Area | Go src lines | Go test lines |
| --- | ---: | ---: |
| `cmd/bp` | 16.3k | 12.8k |
| `internal/tmux` | 5.9k | 6.7k |
| `internal/book` | 4.2k | 2.9k |
| `internal/workflow` | 3.2k | 1.0k |
| `internal/claudeacct` | 3.0k | 1.4k |
| `internal/msgq` | 2.3k | 3.4k |
| `internal/tokens` | 2.1k | 0.5k |
| `internal/daemon` | 1.7k | 0.7k |
| `internal/cache` | 1.5k | 1.4k |
| `internal/fed` / `internal/p2p` | 1.5k / 1.3k | 0.6k / 0.7k |
| other 18 packages | ~6k | ~5k |
| **total** | **~57k** | **~48k** |

Plus `scripts/test_local_cli.py`: 1.9k lines, 60 real-tmux E2E tests.

## 3. Repository layout on `dev-rs`

`dev-rs` branches from the new `dev`, so the Go tree comes along. Keep it in place
until cutover: it is the reference implementation, the oracle for golden data and
the second target of the E2E suite. A Cargo workspace lives next to it at the
repo root:

```
Cargo.toml              # [workspace], shared lints/profile, resolver = "3"
rust-toolchain.toml     # pinned stable toolchain
crates/
  bp-core/              # shared foundations (no tmux, no network)
  bp-tmux/              # screen parsing + tmux client + delivery keystrokes
  bp-book/              # agentbook, transcripts/runtime binding, codex RPC
  bp-msgq/              # durable queue, offline spool, dispatch
  bp-release/           # buildinfo, signed manifests, self-update
  bp-accounts/          # Claude account store / switching
  bp-tokens/            # token collector + usage history
  bp-daemon/            # service loops, workflow engine, fed, wa, ntfy, dashboard
  bp-p2p/               # libp2p transport (last)
  bp-cli/               # the `bp` binary: arg parsing, commands, shell integration
  bp-testkit/           # dev-only: fake tmux runner, fake /proc, golden loaders
testdata/golden/        # data exported from Go (see §6.3)
tools/goldenexport/     # small Go program that dumps Go results as golden JSON
cmd/ internal/ go.mod   # existing Go tree, removed at cutover
```

Why this many crates and not more: crate boundaries follow the dependency graph
(core ← tmux ← book ← msgq ← cli) so incremental builds stay fast on this box and
the heavy dependencies (libp2p, tokio, reqwest) do not get compiled for every
change to screen parsing. Small Go packages become modules, not crates.

`.gitignore` additions: `/target/`, `/.claude/` (host-only settings pin), any
`*.profraw`. Never commit binaries or build output.

The repository is set up with `git init` + `git fetch` in `/srv/blueprint-rs`
(a separate clone, not a worktree), because the directory already holds the
host-only `.claude/` pin and this file.

## 4. Package → module mapping

| Go package | Rust location | Notes |
| --- | --- | --- |
| *(new)* | `bp-core::gojson` | Go-compatible JSON encoder: sorted map keys, `\u003c \u003e \u0026 \u2028 \u2029` escaping, Go float formatting, zero `time.Time` as `"0001-01-01T00:00:00Z"`, RFC3339Nano with trimmed zeros, Go `Duration` strings |
| *(new)* | `bp-core::fs` | atomic temp+fsync+rename, dir fsync, hard-link publish, `flock(2)` RAII guards with non-blocking poll |
| `config` | `bp-core::config` | strict YAML (unknown keys, duplicates, multi-doc rejected), lenient JSON fallback, home resolution, `UpdateRemote` comment-preserving edit |
| `messagetext` | `bp-core::text` | pure; port first, fuzz against Go |
| `identity` | `bp-core::identity` | `/proc` walker with injectable root; `Sessioner` trait for tmux |
| `lowprio`, `ntfy`, `bpskill`, `projectschema`, `worktree` | `bp-core::{lowprio,…}` / `bp-daemon` | small |
| `tmux` (screen funcs) | `bp-tmux::screen::{ansi,region,composer,harness::{claude,codex,hermes,opencode}}` | pure functions; the riskiest regex code |
| `tmux` (client) | `bp-tmux::{client,snapshot,send,clear,open,launch_args,panelock,proc,transcripts,env}` | `Runner` trait (`argv, stdin → bytes`) + `Clock` trait |
| `compositor`, `windowmap` | `bp-tmux::{compositor,windowmap}` | Hyprland/Sway via `Command` |
| `book` | `bp-book::{model,fleet,mutate,archive,adopt,runtime,turn,witness,title}` | |
| `cache` | `bp-book::transcript` | Claude/Codex readers, rollout index |
| `codexrpc`, `codexauth` | `bp-book::{codexrpc,codexauth}` | WebSocket over Unix socket (`tungstenite`) + stdio |
| `msgq` | `bp-msgq::{record,store,locks,enqueue,idempotent,dispatch,status_text}` | sync code, no async |
| `pending` | `bp-msgq::spool` | in-place rewrite under flock, never rename |
| `buildinfo`, `release` | `bp-release` | `build.rs` stamps revision + dirty flag; Ed25519 (`ed25519-dalek`) |
| `claudeacct` | `bp-accounts` | touches the live Claude login; port late |
| `tokens`, `usagecli` | `bp-tokens` | |
| `daemon`, `workflow`, `fed`, `wa`, `dashboard`, `monitorcli` | `bp-daemon::*` | tokio only here |
| `p2p` | `bp-p2p` | rust-libp2p |
| `cmd/bp` | `bp-cli` | hand-rolled per-command parser (see §5.4) |

## 5. Compatibility rules (apply everywhere)

These come from reading the Go code; each is a way the port can look right and
still break coexistence or behavior.

### 5.1 Files and locks

- Locks are **`flock(2)` only** (`rustix::fs::flock`). fcntl/OFD locks do not
  exclude flock holders on Linux. Same lock file paths, same modes, same lock
  order (`.dispatch.lock` before any `.panelock/*`; agentbooks in sorted order).
- Same write patterns: queue enqueue = temp + `link(2)` publish; updates = temp +
  rename + dir fsync; offline spool = **in-place** truncate/write under flock on
  the same inode (a rename would break other processes' locks).
- JSON written by Rust goes through `bp-core::gojson` where Go's byte shape is
  observable (agentbook, account store, `jobs.json`, version JSON). Records only
  read semantically (queue) can use serde, with `skip_serializing_if` matching
  every `omitempty`.
- JSON read by Rust must be as lenient as Go: case-insensitive key match where Go
  structs rely on it, last duplicate wins, invalid UTF-8 → U+FFFD, unknown fields
  ignored **but preserved** on rewrite (`#[serde(flatten)] extra`) so a Go/Rust
  version skew never drops fields.
- Legacy keys/values keep working (`durum`/`bitis`, `iletildi`, …).

### 5.2 Text and screen parsing

- Go regexp `\s \d \b` are ASCII; Rust `regex` defaults to Unicode. Use
  `(?-u:\s)` or explicit classes; NBSP must not become whitespace by accident.
- Split rows with `split('\n')`, never `.lines()` (it drops the last empty row and
  `\r`, shifting row indexes).
- `len([]rune)` → `chars().count()`, not `len()`. Keep Go's `composerRuneWidth`
  table; do not substitute `unicode-width`.
- Operator/status strings are copied verbatim (`delivered (…)`, `not delivered`,
  `unconfirmed`, `RESULT=…`); other code and scripts match on their prefixes.

### 5.3 Process contract

- Same exit codes (`wait`: 1/2, `p2p lookup`: 1/2, `account auto --once`: 0/2/3,
  default 1 with `ERROR: <msg>` on stderr), same `bp help` text (install.sh checks
  the order of lines), byte-identical `~/.config/bp/shell.sh` (doctor compares it).
- `bp msg` last line stays `RESULT=<delivered|queued|unverified|duplicate>[ CHANNEL=<id>]`.
- `status --json` keeps `schema_version: 3` and omits unknown numbers.
- `version --json` keeps all fields; `revision`/`source_modified` come from
  `build.rs`; asset names keep Go arch names (`amd64`, `arm64`).
- `current_exe()` self-references (tmux `#()` bar jobs, `_session`, `_observe`,
  `_local-worker`, `_workflow-run` children) mean a command and the internal
  commands it spawns must be ported **together**. No “delegate unported
  subcommands to the Go binary” shim: it would mix implementations inside one
  session.

### 5.4 CLI parsing

Go parses by hand and the E2E suite depends on the quirks (native flags passed
through after the harness word in `run`, `-h` only before the first positional in
`msg`, `wa send` treating `-5` as text, `update` switching to fleet mode on any
other flag, “may only be specified once” errors). clap would fight this. Port the
parser as a small cursor-based helper per command, with table tests copied from
`main_test.go`.

### 5.5 Workflow template subset

Go uses `text/template` with `missingkey=error` for prompt files, `output.path`
and `validate.command` arguments. The Rust port supports a documented subset and
rejects anything outside it at `workflow add`/`check` time with a clear error:

- literal text, and actions `{{ <chain> }}` where `<chain>` is a field chain on
  the root data: `.Unit .Key .Round .Problems .Output .Started .Workdir .Run`,
  with further `.name` steps indexing into maps (`{{.Unit.slug}}`);
- trim markers `{{-` / `-}}` and `{{/* comments */}}`;
- a missing key is an error (same as `missingkey=error`);
- values print like Go `fmt` `%v` (notably JSON numbers are float64: `1e+06`,
  maps print as `map[k:v]`), checked by goldens from Go.

Not supported: pipelines, functions, `if`/`range`/`with`/`define`/`template`,
variables. Host scan (2026-10-09, read-only): the configured state dir
`/srv/blueprint/state` has no `workflows/` or `workflow-runs/`, so there are no
saved workflows or run snapshots on this host. The only bp workflow files
found are `docs/examples/workflow` (repo + two worktrees); they use only
`{{.Key}}`, `{{.Output}}`, `{{.Problems}}`, `{{.Unit.<field>}}`, so they fit the
subset. (`/srv/outpost/workflows` and the `.claude/workflows` folders are other
tools' JavaScript workflows, not bp.)

## 6. Testing strategy — same suite, two binaries

### 6.1 E2E suite against both binaries

`scripts/test_local_cli.py` is black-box (private tmux socket, fake CLIs, temp
`HOME`/`BP_HOME`) and already the best parity oracle. Changes needed, all small
and made on `dev-rs` (then upstreamed to `dev` so the file stays shared):

1. `setUpClass`: if `BP_TEST_BINARY` is set, use that path instead of
   `go build ./cmd/bp`; coverage flags/`GOCOVERDIR` only apply to the Go build.
2. The fake TUI (`FAKE_TUI`) is Go source built by the suite. Keep building it
   with Go at first (it is a test fixture, not product). Later replace it with a
   tiny Rust bin in `bp-testkit` (or Python + `pty`) so CI can drop Go entirely.
3. `@skipUnless(... "go")` becomes “tmux and (Go or `BP_TEST_BINARY`)”.
4. Expected-failure list for the Rust run: `scripts/rs_e2e_pending.txt` lists test
   names not yet expected to pass. A test runner wrapper marks those `expectedFailure`
   when `BP_TEST_IMPL=rust`; an **unexpected pass fails the run**, which forces the
   list to shrink as features land. The port is done when the list is empty.

Make targets:

```
make e2e-go    # python3 -m unittest scripts.test_local_cli  (Go binary)
make e2e-rs    # cargo build --release -p bp-cli && BP_TEST_IMPL=rust BP_TEST_BINARY=target/release/bp python3 -m unittest scripts.test_local_cli
```

CI (`check.yml`) gets a Rust job: `cargo fmt --check`, `cargo clippy -D warnings`,
`cargo test --workspace`, `make e2e-rs`; the Go job keeps running unchanged.

### 6.2 Unit tests ported with the code

Go tests are table-driven or scenario tests with inline fixtures (≈700 tests).
Each ported module brings its tests along, translated mechanically:

- `internal/tmux` screens (incl. byte-exact live Hermes captures) and the
  `sendHarness` mutation logs (exact `send-keys`/`paste-buffer` sequences) are the
  spec for `bp-tmux`.
- `msgq_test.go` (fake target, 24-goroutine lock contention, crash re-exec) → same
  with threads and a re-exec’d test binary.
- `identity` fake `/proc` trees → `bp-testkit::FakeProc`.
- `cmd/bp` tests that use shell-script fake `tmux` binaries keep doing so: the
  Rust tmux client execs `tmux` by argv, so the same fakes work.

### 6.3 Golden data exported from Go

`tools/goldenexport` (Go, inside the module so it can import `internal/…`) runs
the Go functions over corpora and writes `testdata/golden/*.json`:

- screen predicates: every inline pane fixture × (`Busy`, `Typing`,
  `ComposerBlockReason`, `classifyPaste`, harness detection, …);
- transcript readers over `internal/book/testdata`, `internal/cache/testdata` and
  scrubbed real transcripts;
- `messagetext` over a fuzz corpus; `gojson` encoder output over sample structs;
- config load results over valid/invalid config files.

Rust tests assert equality. Regenerating goldens is one command; a diff in the
goldens is a review signal that Go behavior changed and Rust must follow.

### 6.4 Cross-implementation tests (coexistence)

- Go enqueues → Rust dispatches, and the other way round, over one temp queue root.
- Go and Rust processes contend on the same `.dispatch.lock` / `.panelock` /
  agentbook lock; assert mutual exclusion.
- Agentbook round trip: Go writes → Rust mutates → Go reads, byte-compare where
  Go's encoder shape is expected.
- Read-only parse of the live queue `done/` corpus and live agentbooks (copied to
  a temp dir, never in place).
- P2P: Go node ↔ Rust node, direct and through the relay, before any P2P cutover.

## 7. Port order

Each phase ends with its unit tests green, its E2E tests removed from
`rs_e2e_pending.txt`, and a short note in this file.

**Phase 0: scaffolding**
Workspace, toolchain pin, `.gitignore`, `build.rs` (revision, dirty flag),
CI job, `BP_TEST_BINARY` support in the E2E suite, `rs_e2e_pending.txt` with all
60 tests, `tools/goldenexport` skeleton.

**Phase 1: foundations → `bp version`, `help`, `config path|check`**
`bp-core::{gojson, fs, config, text, color}`, `bp-release::buildinfo`.
Cheap, and every later phase depends on it.

**Phase 2: read-only observation → `status [--json]`, `tree`, `peek`, `book`, `whoami`**
`bp-tmux::screen` (all harness recognizers, composer box readers, verdicts) with
goldens; tmux client read paths + snapshot; `bp-book` model/fleet (read),
transcript readers, turn phase, codexauth, codexrpc (read), runtime binding;
`identity`. Still nothing writes to panes, so this is safe to dogfood next to the
Go binary.

**Phase 3: delivery → `msg`, `q`, `qstat`, `qcancel`, `announce`, `wait`**
`bp-msgq` (record/store/locks/enqueue/idempotent, spool), `bp-tmux::{send, clear,
panelock}`, `dispatch` state machine. Highest-risk logic: ported last within the
phase, verified by mutation-log goldens and the delivery E2E tests (busy/draft
wait, torn paste, wrapped submit, concurrent retries, transcript-confirmed close).

**Phase 4: lifecycle → `run`, `open`, `attach`, `close`, `setup`, `onboard`, `bar`, `name`, `archive`/`restore`, `rename`, `reparent`, `keep`/`release`, `doctor`**
Together with internal `_session`, `_open-session`, `_local-worker`, `_observe`
(they self-reference the binary). Book mutations, shell integration, exit.json,
resume/owner routing. This phase covers most of the 60 E2E tests.

**Phase 5: service → `daemon`, `update`, `workflow`, `tokens`, `usage`, `account`**
`bp-daemon` loops (tokio, `spawn_blocking` around sync core), `jobs.json`,
workflow store/supervisor/engine (Go `text/template` compatibility: either `gtmpl`
or a defined subset that refuses anything else, checked against saved workflows),
`bp-release` self-update, `bp-tokens`, `bp-accounts` last. Template subset: §5.5.

**Phase 6: long tail → `fed`, `p2p` (after the first cutover), `wa`, `dash`, `monitor`, `windows`/`focus`/`color`, `remote`/`shell`/`con`/`img`, `compact`, `schema`/`continue`/`history`, fleet `update`**
P2P only after the Go↔Rust interop test passes. Notes: the rust-libp2p mDNS
service name is fixed (`_p2p._udp.local`), so Go's `_blueprint._udp` discovery
does not interoperate; use explicit addresses during migration. Relay circuit
limit must be raised to Go's 2 MiB. Strict JSON field parity (Go rejects unknown
fields).

**Phase 7: first cutover** (P2P not required: Go `bp p2p serve` may keep running
beside the Rust CLI, which talks to it only through `p2p/control.sock`)
- Shadow run on this host under a different path (e.g. `~/.local/bin/bp-rs`),
  never replacing `/usr/local/bin/bp` or touching `blueprint.service` without
  explicit approval.
- Release pipeline: `publish-release.py` builds with Cargo for the same four asset
  names; macOS targets need `cargo-zigbuild` (or macOS CI runners). Same manifest,
  same Ed25519 key, `install.sh` and npm package unchanged.
- Remove the Go tree after one release cycle with Rust as the published binary.

## 8. Risks

| Risk | Mitigation |
| --- | --- |
| Regex/Unicode/row-splitting differences silently change busy/draft detection | goldens from Go over every fixture; ASCII classes; review checklist (§5.2) |
| Delivery state machine order/timing (at-most-once guarantee) | port verbatim with mutation-log goldens; delivery E2E tests; no refactoring during the port |
| Go JSON byte shape and lenient decoding | `bp-core::gojson` + round-trip goldens; `extra` field preservation |
| Lock incompatibility (fcntl vs flock) | one lock helper in `bp-core::fs`; cross-implementation contention test |
| Go `text/template` in saved workflows | subset + golden render of every saved workflow before Phase 5 ships |
| libp2p interop gaps (mDNS, WebTransport/WebRTC, relay limits) | port last; interop test; keep Go `bp p2p serve` running meanwhile (CLI talks to it only via `control.sock`) |
| Two implementations drift while Go `dev` keeps moving | track the Go revision each module was ported from (header comment); periodic `git log <rev>..dev -- internal/<pkg>` review |
| Thermal limits on this host during builds | check `sensors \| grep "Package id 0"` < 80 °C before heavy builds; `CARGO_BUILD_JOBS=4`, `nice`; release LTO builds only in CI |
| macOS cross-compilation | `cargo-zigbuild` or GitHub macOS runners |

## 9. Toolchain and host notes

- Rust: stable toolchain installed for root with rustup (`~/.cargo`, `~/.rustup`;
  rustc/cargo 1.99.0, default profile with clippy and rustfmt), approved by
  blueprint 2026-10-09. No other system packages without asking.
- Builds: `nice -n 10`, `CARGO_BUILD_JOBS=4`, and check
  `sensors | grep "Package id 0"` is below 80 °C first.
- Host Go is 1.22.2; the module needs 1.25.7 and relies on Go toolchain
  auto-download. Golden export and the Go E2E run inherit that.
- Proposed crates: `serde`, `serde_json` (`raw_value`, `float_roundtrip`),
  `serde_norway` (strict YAML), `rustix` (flock, linkat, statx), `tempfile`,
  `regex`, `thiserror`, `sha2`, `hex`, `base64`, `ed25519-dalek` (pkcs8/pem),
  `flate2`, `chrono`, `tungstenite`; service layer only: `tokio`,
  `tokio-util`, `axum`/`hyper`, `reqwest` (rustls), `libp2p` + `libp2p-stream`;
  dev: `insta`, `proptest`.

## 10. Decisions (blueprint, 2026-10-09)

1. The Go tree stays in `dev-rs` until cutover.
2. P2P is not required for the first cutover; Go `bp p2p serve` may run beside a
   Rust CLI.
3. A documented subset of `text/template` is fine (§5.5); all templates in use
   on this host fit it.
4. Go is acceptable as a test-only dependency (fake TUI, golden export, Go E2E run).
5. blueprint-rs owns the Go→Rust sync and keeps the "Ported up to" line at the
   top of this file current; blueprint sends the range after each `dev` release.

## 11. Sync log

| Date | Go range | Notes |
| --- | --- | --- |
| 2026-10-09 | baseline 1543440 (1.9.29) | plan written from this revision (was 078cd5b before the history rewrite) |
| 2026-10-09 | 1543440..b877eb4 (1.9.30) | only `claudeacct` (per-account usage limits, staggered keepalive), `config`, `daemon/claudeaccounts` and `cmd/bp/account`; all Phase 5, nothing ported yet, so no Rust change. Release binaries are no longer tracked |
