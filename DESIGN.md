# Architecture

Blueprint is a Go CLI with local tmux integration and an optional server daemon.
Machine paths, agentbooks and optional services come from configuration.

## Main components

| Component | Responsibility |
| --- | --- |
| `cmd/bp` | CLI, local startup, shell integration, onboarding, status rendering |
| `internal/config` | YAML/JSON loading, defaults and validation |
| `internal/book` | Agent registry, hierarchy, native metadata and runtime binding |
| `internal/identity` | Sender evidence and authority checks |
| `internal/tmux` | Pane/process observation, guarded input and harness adapters |
| `internal/msgq` | Durable local queue, ordering, retries and delivery records |
| `internal/cache` | Native context, turn and cache observations |
| `internal/codexrpc` | Shared Codex app-server observations |
| `internal/daemon` | Optional server scheduling and queue dispatch |
| `internal/fed` | Existing HTTP hub/client federation |

## Local startup

Shell wrappers preserve normal CLI arguments and aliases. Interactive launches
outside tmux enter a named session whose foreground process becomes the native
CLI. Exiting the process closes the pane. A local worker retries pending messages
while the CLI is alive. Batch commands and existing tmux sessions retain native
behavior.

Claude continue/resume pins a native conversation UUID before creating a pane.
Owner lookup and creation are serialized. An existing owner is attached; multiple
live owners produce an error. Physical cwd paths prevent symlink aliases from
creating mismatched registry folders. This does not lock processes outside bp or
native in-TUI conversation switching.

## Observation and identity

Status, the bar and delivery gates share runtime observations. Local Claude uses
launch callbacks and native transcripts; local Codex uses its held writer lock
and rollout metadata. Remote Codex requires app-server/thread evidence.

Runtime binding is distinct from sender authority. Cwd, inherited tmux variables,
display names and model labels do not prove identity. Native renames are convenience
aliases; canonical queue targets and hierarchy remain stable.

## Delivery

The sender receives a channel ID. The receiving machine's queue evaluates runtime,
draft and modal guards before writing to a pane. Per-pane and dispatch locks prevent
competing bp writers. Delivery records distinguish verified outcomes from uncertainty.
Unknown state remains unknown; empty screen content is not affirmative idle evidence.

## Onboarding

`bp setup` installs shell integration. `bp onboard` prepares a private machine-local
workspace, creates or preserves the coordinator, and starts the chosen CLI with a
generic prompt. Existing real coordinators and user documents are preserved.
Onboarding does not grant operating-system privileges or bypass identity checks.

## Server operation

The optional daemon supervises configured jobs and dispatches queues. Updating the
CLI binary does not update an already-running daemon or local worker. Verify the
actual executable identity separately. Shared Codex app-servers and agent processes
have independent lifetimes and should not be restarted as part of routine bp updates.
