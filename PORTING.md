# Porting conventions (Go → Rust)

Read `PLAN.md` first (§5 compatibility rules are binding). This file is the
working agreement for everyone writing Rust in `crates/`.

## Source of truth

- The Go code in `cmd/` and `internal/` at the revision named on the "Ported up
  to" line of `PLAN.md` defines behavior. Port the code, not the comments or
  docs, when they disagree.
- Do not change Go code. The only shared files the port edits are the E2E suite
  (`scripts/test_local_cli.py`), `scripts/rs_e2e_pending.txt`, `Makefile`, CI and
  `.gitignore`.
- Every Rust source file starts with a header naming its Go origin:
  `//! Port of internal/msgq/msgq.go (dispatch half).`

## Code

- English everywhere: identifiers, comments, docs, messages. User-visible
  strings are copied from Go **verbatim** (they are already English).
- Edition 2024, the pinned toolchain, `cargo fmt`, and no clippy warnings
  (`cargo clippy --workspace --all-targets -- -D warnings`). `unsafe` is denied;
  use `rustix` for syscalls.
- Synchronous code everywhere except `bp-daemon` and `bp-p2p` (tokio).
- Errors: a `thiserror` enum per crate where callers branch on the kind (e.g.
  `is_busy()` must also hold for `Dialog`/`PaneLocked`, as Go's `errors.Is`
  chains do). `bp-cli` may use `anyhow` internally but maps to Go's exit codes
  and `ERROR: <msg>` lines.
- Test seams are traits (`Runner` for tmux/argv exec, `Clock`, `ProcFs` root,
  env lookup closures) instead of Go's package-level function variables.
- Keep the Go file/function structure recognizable (similar names in
  snake_case) so later syncs can diff Go changes against the Rust module.
- Allowed dependencies without asking: serde, serde_json, serde_norway, rustix,
  tempfile, regex, thiserror, anyhow, sha2, hex, base64, ed25519-dalek, flate2,
  chrono, tungstenite, libc-free crates in general; service layer: tokio,
  tokio-util, axum/hyper, reqwest (rustls), libp2p. Dev: insta, proptest. Add
  them with `cargo add` so versions are current; prefer `default-features =
  false` when it keeps compile times down.

## Tests

- Port the Go tests of a module together with the module. Table tests stay
  tables; inline fixtures are copied verbatim (raw strings, `\u{…}` escapes for
  invisible characters such as NBSP).
- When behavior is easier to pin by running Go, add a small exporter under
  `tools/goldenexport/<area>/` (Go, `package main`, imports `blueprint/internal/…`)
  that writes `testdata/golden/<area>/*.json`; Rust tests read those files.
  Commit both the exporter and the generated JSON.
- Cross-implementation tests (Go and Rust touching the same files/locks) live in
  `crates/<crate>/tests/` and may build Go helpers with `go build` into a temp dir;
  skip them when `go` is missing.

## Builds on this host

- Always build through `scripts/rs-cargo` (waits until the CPU package is below
  78 °C, runs `nice -n 10` with `CARGO_BUILD_JOBS=4`). Example:
  `scripts/rs-cargo test -p bp-tmux`.
- Several people/agents share one `target/`; cargo serializes builds on its lock.
  Test the crate you work on (`-p`), not the whole workspace, while iterating.
- Never commit `target/` or any binary.

## Parity

- `make e2e-rs` runs the real-tmux suite against `target/release/bp`.
  Remove a test from `scripts/rs_e2e_pending.txt` as soon as it passes.
- The port is complete when the pending list is empty, `make rs-check` passes,
  and every Go command in `cmd/bp` has a Rust implementation.
