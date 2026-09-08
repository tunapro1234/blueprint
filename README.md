# Blueprint (`bp`)

Run CLI agents in tmux with guarded messaging, model/context indicators and
configurable colors. Works with local laptop sessions and configured servers.

## Install

Install and sign in to your preferred agent CLI first, then:

```sh
curl -fsSL https://bp.tunapro.xyz/install.sh | sh -s -- --local
```

On first interactive installation, bp asks which CLI to use and opens a `main`
coordinator with a generic onboarding prompt. It learns the machine's setup through
a small, scoped inspection and helps configure bp. Non-interactive installation
prints the next command instead of starting an agent.

```sh
bp onboard                         # choose a CLI and start/attach the coordinator
bp onboard --cli codex --prepare    # prepare only; no agent or model request
bp book --json                     # inspect the actual coordinator and agent records
```

Linux and macOS, amd64/arm64, Bash/Zsh are supported build targets. Linux integration
is tested with real tmux and fake CLIs; macOS builds are cross-compiled.

## Use

After opening a new terminal, your usual `codex`, `claude`, `opencode` and `hermes`
commands start through bp. Existing aliases and their arguments are preserved;
existing custom shell functions take precedence. Batch commands, pipes and help
retain native behavior. Exiting the CLI closes its pane; detaching keeps it alive.

```sh
bp run --name work codex
bp status
bp msg work "Please review the change"
bp qstat <channel-id>
bp peek work
bp color work purple
```

Messages wait when the target is working, its state is uncertain, or the user is
typing. A transport acknowledgement alone is not proof of agent delivery.

`claude -c` reattaches to an existing bp owner of the conversation. If old duplicate
owners exist, bp reports them without choosing or closing a pane. Native Claude
and Codex `/rename` changes update the display name without changing authority.

## Configure

Settings live in `~/.blueprint/config.yaml`, or under `BP_HOME`. Shell integration
lives in `~/.config/bp/shell.sh`. Native transcripts stay in their CLI directories.
Setup preserves existing configuration, aliases and records.

```sh
bp setup                   # install/refresh shell integration
bp config path
bp config check
```

`bp onboard` prepares `$BP_HOME/main/ONBOARDING.md`. The coordinator creates tailored
`MACHINE.md` guidance there. Existing files and real coordinators are preserved.
Optional desktop integration is proposed, not installed automatically. Native
model, effort and permission settings are not overridden.

For another CLI, specify how it accepts a prompt:

```sh
bp onboard --cli custom -- /path/to/agent --prompt '{prompt}'
```

Custom CLIs can run in tmux; structured activity and automatic delivery require a
supported harness. Run interactive onboarding outside tmux. `BP_ONBOARD=skip`
skips automatic onboarding during installation.

## Updates and diagnostics

```sh
bp version --json
bp update --check
bp update
bp doctor --json
bp setup --disable
```

Releases are versioned and signed. The installer requires OpenSSL with Ed25519
support; macOS can use `brew install openssl@3`. Pin installation with `BP_VERSION`
(e.g. `BP_VERSION=1.6.0 sh install.sh --local`). The updater verifies the signed
manifest and binary checksum, keeps a backup, and restores the previous binary if
setup fails. Running agents are not restarted. Server installations use an explicit
host rollout so CLI and daemon versions are verified together.

Local agent startup can show a cached update notice. `updateCheck: false` in YAML
disables its daily background check. No model prompt is sent. Disabling shell
integration preserves aliases, agent records and open sessions; open a new terminal
to use native commands again.

## Develop

```sh
make check
python3 -m unittest scripts.test_local_cli
```

- [Configuration and platform limits](docs/configuration.md)
- [Architecture](DESIGN.md)
- [Runtime JSON contract](docs/runtime-status.md)
- [Security boundaries](SECURITY.md)
- [Build and release](docs/local-release.md)

Server-only integrations are optional. P2P/libp2p is not part of the current release.
