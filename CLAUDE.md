# CLAUDE.md — working on bp

Guidance for AI agents (and people) changing this repository. Details live in
the linked docs; this page holds what you must keep in mind on every change.

## Language: always English

Everything that goes into this repository, or to other agents, is written in
English:

- commit messages, branch names, pull request titles and descriptions, issues
  and review comments;
- code comments, identifiers, log lines, error messages and help text;
- documentation, including new files under `docs/`;
- `bp` messages to other agents.

Reply to a person in the language they write to you. Only the artifacts above
are always English.

## What bp is

bp (long form "Blueprint") lets your agents find each other, message each
other and work as a team, whatever they run in and wherever they run
([docs/direction.md](docs/direction.md), agreed with the owner).

- It is a communication and hierarchy layer for AI agents: Claude Code, Codex,
  Hermes, OpenCode, or any script that can make an HTTP request. It is **not**
  a harness: it never calls a model and never uses a provider's credentials
  outside that provider's own client.
- The core promise: a message waits for the lull between an agent's turns,
  never lands on a human's half-typed draft, and its receipt is honest
  (`DELIVERED` only with evidence, otherwise unconfirmed).
- Agents reach bp through the `bp` CLI, an MCP server (`bp mcp`) and a local
  HTTP API with A2A-shaped bodies (`bp serve --api`). Nothing listens by
  default.
- Installing bp adds only its binary, its own state under `~/.blueprint` and a
  short skill note for Claude Code and Codex. Everything that changes the
  user's environment is an opt-in module.
- Machines connect over P2P (libp2p, expose lists, your own relay). It is a
  multiplier, not the headline.

Where it is going: [docs/vision.md](docs/vision.md). Who works on what:
[docs/workplan-2026-10.md](docs/workplan-2026-10.md).

## Repository map

- `cmd/bp` — the CLI. `main.go` dispatches commands; each command area has its
  own file (`api.go`, `modules.go`, `uninstall.go`, `p2p.go`, `hook.go`, ...).
- Delivery core:
  - `internal/msgq` — the durable queue: ordering, retries, claims, delivery
    records and the message log.
  - `internal/tmux` — pane and process observation, composer and busy/draft
    guards, and the per-harness screen adapters.
  - `internal/harness` — the one catalog of what bp knows about each agent
    CLI. `docs/harnesses.json` is generated from it.
  - `internal/delivery` — wires guard framing into the delivery path.
  - `internal/messagetext` — keeps message data from becoming terminal input
    commands.
  - `internal/pending` — the offline spool for agents that are not running.
- Identity and safety:
  - `internal/identity` — who is sending, and what authority that carries.
  - `internal/guard` — untrusted framing (`guard.Frame`), scans, redaction,
    taint tracking and the tool-call tripwire.
  - `internal/audit` — the owner-readable log of security-relevant decisions.
- Open surface: `internal/api` (REST `/v1`, A2A, MCP, rooms, board, remote
  gateway; loopback only, token or peer-credential auth).
- Machines: `internal/p2p` (libp2p transport, peers, expose lists, limits) and
  `internal/fed` (the older HTTP hub-and-client federation).
- State and config: `internal/book` (the agent registry and hierarchy),
  `internal/config`, `internal/safefile` (edits to files bp does not own, such
  as rc files and harness configs), `internal/modules` (the module registry,
  `enable`/`disable` and the undo journal).
- Release: `internal/release` (signed release verification for install and
  update) and `internal/buildinfo`.
- Harness specifics: `internal/codexrpc` (Codex app-server thread state),
  `internal/codexauth`, `internal/cache` (context and turn observations),
  `internal/bpskill` (the agent hint and skill files).
- Maintainer modules and tools: `internal/claudeacct` (Claude accounts),
  `internal/wa` (WhatsApp), `internal/dashboard`, `internal/monitorcli`,
  `internal/usagecli`, `internal/tokens`, `internal/ntfy`. They are not part of
  the public product surface; see "Ask the owner first" below.
- Also: `internal/workflow`, `internal/worktree`, `internal/projectschema`,
  `internal/daemon`, `internal/lowprio`, `internal/compositor`,
  `internal/windowmap`.
- `scripts/` — the Python CLI suite (`test_local_cli.py`, real tmux and fake
  agent CLIs), the release script and helpers.
- `site/` — the website (see "Website"). `npm/` — the npm wrapper.
  `docs/` — see "Docs map".

## Build and test

Go is pinned by the `go` line of [go.mod](go.mod) (1.27.2). With the default
`GOTOOLCHAIN=auto` an older local Go downloads it.

```sh
make build    # go build ./... and ./bp
make check    # go test, go vet, govulncheck (make vuln) and the build
GOOS=darwin GOARCH=arm64 go build ./...   # run both before pushing anything
GOOS=darwin GOARCH=amd64 go vet ./...     # that touches syscalls or processes
```

CI ([check.yml](.github/workflows/check.yml)) runs three jobs on Linux:
`test` (Go tests, vet, race on `internal/p2p` and `internal/msgq`, the Python
CLI suite, the npm tests), `darwin` (cross-compile and vet) and `vuln`
(govulncheck). Full instructions: [CONTRIBUTING.md](CONTRIBUTING.md).

- Every behavior change needs a test. Delivery changes need a regression test,
  ideally in `scripts/test_local_cli.py` against a real, private tmux server.
- After changing a harness descriptor, regenerate the matrix:
  `BP_UPDATE_MATRIX=1 go test ./internal/harness -run TestMatrixIsGenerated`.
- On macOS, some Go tests fail for environmental reasons (long socket paths,
  `/var` vs `/private/var`, no `/proc`). Compare against a run without your
  change before you call a failure new.

## Safety rules for agents

People run live bp agents on the machines where you work. Never:

- run the installed `bp` from `PATH` while testing; build your own and call it
  by absolute path;
- read or write `~/.blueprint`, `/etc/blueprint/home`, `/srv/blueprint` or any
  real `BP_HOME`, or touch the default tmux server;
- send messages to real agents or people, push to `stable`, cut or sign a
  release, or change GitHub settings.

Always isolate manual runs:

```sh
env -u TMUX -u TMUX_PANE -u AGENT -u CODEX_THREAD_ID -u BP_SESSION \
  HOME=/tmp/bp-dev/home BP_HOME=/tmp/bp-dev/home/.blueprint \
  TMUX_TMPDIR=/tmp/bp-dev/tmux BP_NO_UPDATE_CHECK=1 \
  PATH=/tmp/bp-dev:/usr/bin:/bin /tmp/bp-dev/bp help
```

Keep the temporary directory short: a tmux socket path must fit in about 100
bytes. A pane created by `tmux new-session` or `new-window` inherits the
requesting client's `PATH`, so give every tmux command the same isolated
environment. Never commit secrets, tokens, transcripts, personal data or
release binaries.

## Engineering principles

From [direction.md](docs/direction.md):

1. **Installing bp changes nothing.** A feature that touches the user's
   environment (shell startup files, tmux options, daemon jobs, credentials,
   other tools' configs) is a module: it checks `modules.Enabled(name)`, writes
   through `internal/safefile`, journals each change, and `bp disable` and
   `bp uninstall` undo exactly that.
2. **Agents learn about bp; users don't have to.** Teach through the least
   invasive channel each harness offers: MCP, a skill, a one-line hint.
3. **Open to every agent.** CLI, MCP and plain HTTP, with no SDK required.
4. **Light and fast.** No background work the user did not ask for; status
   reads cached state.
5. **Structures are examples, not rules.** bp never decides what agents work
   on.
6. **Humans can always see and steer.** Expose lists, the audit log, loop caps
   and untrusted framing are not optional.

How that translates into code:

7. **Never claim what you cannot prove.** `delivered` needs evidence; unknown
   stays unknown in receipts, identity labels, status output and docs. Never
   retype a paste that bp could not confirm.
8. **Respect the turn.** Never type into a busy agent or over text bp did not
   write. Forcing past a busy agent is reserved for the root coordinator and
   bp's own infrastructure, and never applies to a Hermes turn.
9. **Fail closed.** An unrecognized screen, an unreadable credential or an
   unsupported platform means wait or refuse, never "assume empty" or "assume
   allowed".
10. **Outside text is untrusted.** Text that crosses a trust boundary reaches an
    agent only through `guard.Frame`. Authority comes from proof (a pane bp
    started, a Codex thread), never from a cwd, an environment variable or a
    display name. Changes to delivery, identity, inbound text or the API are
    checked against the [threat model](docs/security/threat-model.md).
11. **Use the front door.** Prefer each harness's official integration points
    (hooks, native queues, MCP, A2A) over reading the screen; screen parsing
    is the fallback, covered by conformance tests.
12. **State survives crashes and rollbacks.** Write atomically (temp file and
    rename), check every write error, and keep state readable by the previous
    release, so a rollback never strands a user.
13. **Portable by construction.** macOS and Linux are both first-class. Code
    that differs per OS goes in `_linux.go` / `_darwin.go` files with a
    fail-closed fallback; never assume `/proc`.
14. **Private by default.** Messages and logs stay on the machine, there is no
    telemetry, and secrets are redacted before they reach logs or other agents.
15. **Small core, earned surface.** A new module or surface needs an owner,
    tests, docs, a removal path and, if it carries outside text, a
    threat-model entry.
16. **Errors help the next step.** Say what happened and what to do; keep exit
    codes and `--json` output stable for scripts.

## Workflow

- Branch from `dev` and open pull requests against `dev`. `stable`, the GitHub
  default branch, tracks released versions; releases are built from `dev` and
  signed by the owner.
- Branch names: lowercase English with a type prefix (`feat/...`, `fix/...`,
  `docs/...`). Commit messages: a short imperative subject with a scope where
  it helps (`fix(p2p): ...`, `api: ...`, `docs: ...`) and a body that explains
  why.
- One change per pull request, with its tests and docs. Update the README,
  [docs/usage.md](docs/usage.md), [docs/api.md](docs/api.md) or
  [docs/configuration.md](docs/configuration.md) as needed, and
  [CHANGELOG.md](CHANGELOG.md) under "Unreleased" for anything users notice.
- After a pull request is merged, delete its branch locally and on GitHub.
- Anything larger than a fix starts with an issue that describes the problem.
- `AGENTS.md` is host-local on purpose (see `.gitignore`); keep this file
  generic and public.

## Ask the owner first

Do not extend these without the owner's go-ahead:

- the `accounts` module (`internal/claudeacct`): storing, refreshing or
  rotating Claude subscription credentials can conflict with Anthropic's
  published policy for third-party tools (see
  [vision.md §10.3](docs/vision.md));
- maintainer-only modules and jobs (`wa`, `ui`, `monitor`) and anything that
  contacts the owner's servers;
- the release pipeline, signing keys and the website's live paths;
- protocol IDs and wire formats (P2P, federation, API), which older peers
  depend on.

## Website

`bp.tunapro.xyz` serves the `site/` directory of a `dev` checkout, so a merge
into `dev` changes the live site once the server pulls.

- `site/index.html` is the live homepage. `site/next/` is the new landing page,
  previewed at `/next/` until the release that ships `dev`; how it was built
  and how to promote it: [docs/brand/README.md](docs/brand/README.md).
- `site/install.sh`, `site/checksums.txt`, `site/latest.version` and
  `site/releases/` belong to the release script. Never edit them by hand.
- Every command and output shown on the site or in the README must exist in
  the release it describes. Capture outputs from a real, isolated run.

## Docs map

- Product: [direction.md](docs/direction.md), [vision.md](docs/vision.md),
  [launch/product-hunt.md](docs/launch/product-hunt.md)
- Usage and reference: [README](README.md), [usage.md](docs/usage.md),
  [api.md](docs/api.md), [configuration.md](docs/configuration.md),
  [p2p.md](docs/p2p.md), [workflow.md](docs/workflow.md),
  [windows.md](docs/windows.md)
- Internals: [DESIGN.md](DESIGN.md),
  [message-delivery.md](docs/message-delivery.md),
  [runtime-status.md](docs/runtime-status.md),
  [harnesses.json](docs/harnesses.json),
  [local-release.md](docs/local-release.md)
- Security: [SECURITY.md](SECURITY.md),
  [threat model](docs/security/threat-model.md) and the reviews in
  `docs/security/`
- Process: [CONTRIBUTING.md](CONTRIBUTING.md), [CHANGELOG.md](CHANGELOG.md),
  [workplan-2026-10.md](docs/workplan-2026-10.md) and the `plan-w*.md` files

## Known pitfalls

- **tmux with no server** prints `error connecting to <socket>`, not only
  `no server running`. Treat both, and a missing tmux binary, as "no sessions".
- **No UTF-8 locale** (cron, launchd, some ssh and agent shells): tmux prints
  the `\t` separators of `-F` formats as `_`. Run tmux with `-u` or use a
  printable separator.
- **macOS process inspection:** there is no `/proc`, and `lsof` does not report
  flock locks. Codex 0.16x keeps its thread writer lock in an app-server daemon
  child, not in the TUI process.
- **macOS paths:** unix socket paths are limited to about 104 bytes and the
  default `TMPDIR` is long; `/var` is a symlink to `/private/var`.
- **Shell quoting:** pass `bp msg` text in single quotes. Inside double quotes
  the sender's shell expands `$(...)` and backticks before bp sees the text.
- **Toolchain:** with `GOTOOLCHAIN=local` and a Go older than 1.27.2 the build
  refuses to start; install the pinned version instead of lowering go.mod.
