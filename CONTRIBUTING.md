# Contributing to bp

Thanks for helping. bp moves text between AI agents and types into terminals
that people are using, so the bar for changes is correctness first: a message
that waits is fine, a message that lands in the wrong place is not.

## Ground rules

- Open pull requests against `dev`. All work lands there through review by the
  maintainer; releases are built from `dev`, and `stable`, the default branch
  on GitHub, tracks released versions.
- Code, comments, docs, identifiers and commit messages are in English.
- For anything larger than a fix, open an issue first and describe the
  problem, so we can agree on the approach before you write the code.
  [docs/direction.md](docs/direction.md) explains what bp is and is not.
- Keep installs non-invasive: installing bp adds only its own files and the
  agent skill note. A feature that changes the user's environment (shell
  startup files, tmux options, daemon jobs, credentials) belongs to a module
  and checks `modules.Enabled(name)` first, and `bp disable` and
  `bp uninstall` must be able to undo it.
- Changes that touch inbound text, delivery, identity or the API get a second
  look against the [threat model](docs/security/threat-model.md). Say in the
  pull request which trust boundary your change crosses, if any.

## Development setup

You need:

- Go, at the version on the `go` line of [go.mod](go.mod) (currently 1.27.2).
  CI installs exactly that version and checks it. With the default
  `GOTOOLCHAIN=auto`, an older local Go downloads it for you; with
  `GOTOOLCHAIN=local` you need Go 1.27.2 or newer installed.
- tmux, bash and zsh for the terminal tests.
- Python 3.11 or newer for the CLI test suite.
- Node.js 22 for the npm package tests (the package itself supports Node 16+).
- OpenSSL with Ed25519 for the installer tests.

```sh
git clone https://github.com/tunapro1234/blueprint.git
cd blueprint
git switch dev
make build        # go build ./... and ./bp
make check        # go test, go vet, govulncheck (make vuln) and the build
```

## Running tests safely

bp talks to tmux and to agent CLIs, and you probably have real agents running
on your machine. The test suites are built not to touch them; keep it that way
when you run bp by hand.

CI ([check.yml](.github/workflows/check.yml)) runs three jobs on Linux, so a
failing test step cannot skip the other two:

```sh
# test: also checks that CI's Go is the version go.mod selects
sh -n install.sh
go test ./...
go vet ./...
go test -race ./internal/p2p ./internal/msgq
python3 -m unittest scripts.test_local_cli scripts.test_publish_release scripts.test_runtime_usage scripts.test_migrate_codex_book
npm --prefix npm test

# darwin: macOS binaries are checked by cross-compiling
GOOS=darwin GOARCH=arm64 go build ./...
GOOS=darwin GOARCH=amd64 go vet ./...

# vuln: govulncheck, pinned in the Makefile
make vuln
```

- `scripts/test_local_cli.py` builds its own `bp`, gives every test a
  temporary `HOME` and `BP_HOME`, puts a `tmux` wrapper first on `PATH` that
  runs `tmux -S <temp>/tmux.sock` (a private server), and replaces `claude`,
  `codex`, `opencode` and `hermes` with fake CLIs. It never uses your tmux
  server and spends no model quota. Unset `TMUX` and `TMUX_PANE` when you run
  it from inside tmux.
- Go tests in `cmd/bp` pin `BP_HOME` to a temporary directory. New tests must
  do the same: no test may read `~/.blueprint`, `/etc/blueprint/home` or the
  default tmux server.
- `docs/harnesses.json` is generated from `internal/harness`; after changing a
  descriptor, run `BP_UPDATE_MATRIX=1 go test ./internal/harness -run TestMatrixIsGenerated`.

To try a build by hand, isolate it the same way:

```sh
go build -o /tmp/bp-dev/bp ./cmd/bp
env -u TMUX -u TMUX_PANE -u AGENT -u CODEX_THREAD_ID -u BP_SESSION \
  HOME=/tmp/bp-dev/home BP_HOME=/tmp/bp-dev/home/.blueprint \
  TMUX_TMPDIR=/tmp/bp-dev/tmux BP_NO_UPDATE_CHECK=1 \
  PATH=/tmp/bp-dev:/usr/bin:/bin /tmp/bp-dev/bp help
```

Two tmux details cause most accidents:

- A tmux socket path must fit in about 100 bytes, so keep the temporary
  directory short.
- A pane created by `tmux new-session` or `new-window` gets the `PATH` of the
  tmux client that asked for it. Run every tmux command with the same isolated
  environment, or a pane may find your installed `bp` instead of the one you
  are testing.

Integration tests run on Linux with real tmux and fake CLIs; macOS builds are
cross-compiled and vetted in the `darwin` job. If you touch syscalls or
process inspection, run the two `GOOS=darwin` commands above before you push;
code that differs per OS goes in `_linux.go` / `_darwin.go` files, as in
`internal/api`'s peer-credential check.

## Pull requests

- Keep each pull request to one change, with tests for the behavior you add or
  fix. Delivery changes need a regression test, ideally in
  `scripts/test_local_cli.py` with a real tmux server.
- Write commit messages as in the history: a short imperative subject with a
  scope where it helps (`fix(p2p): …`, `feat(cli): …`, `docs: …`), and a body
  that explains why.
- Update the docs your change affects: the README, [docs/usage.md](docs/usage.md),
  [docs/configuration.md](docs/configuration.md) or [docs/api.md](docs/api.md),
  and [CHANGELOG.md](CHANGELOG.md) under "Unreleased" for anything users notice.
- Never commit secrets, tokens, transcripts or private conversation content,
  including in test fixtures.

## Reporting bugs

Use the [bug report form](https://github.com/tunapro1234/blueprint/issues/new/choose).
For a session problem, include the bp version, `bp doctor --agent <name> --json`
output and the relevant channel ID, and distinguish automatic delivery from a
manual Enter. Remove tokens, private keys, transcripts and private
conversation content first.

Security problems go through [SECURITY.md](SECURITY.md), not public issues.

## License

bp is licensed under the [GNU General Public License v3.0 only](LICENSE). By
submitting a contribution you agree that it is licensed under the same terms.
