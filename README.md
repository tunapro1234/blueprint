# Blueprint (`bp`)

Run CLI agents in tmux with guarded messaging, model/context indicators and
configurable colors. Works with local laptop sessions and configured servers.

## Install

Install and sign in to your preferred agent CLI first, then:

```sh
curl -fsSL https://github.com/tunapro1234/blueprint/releases/latest/download/install.sh | sh -s -- --local
```

On first interactive installation, bp asks which CLI to use and opens a `main`
coordinator with a generic onboarding prompt. It learns the machine's setup through
a small, scoped inspection and helps configure bp. Non-interactive installation
prints the next command instead of starting an agent. Reinstallation and updates
preserve existing agents and do not automatically start onboarding; use `bp onboard`
when you explicitly want to configure the coordinator.

```sh
bp onboard                         # choose a CLI and start/attach the coordinator
bp onboard --cli codex --prepare    # prepare only; no agent or model request
bp book --json                     # inspect the actual coordinator and agent records
```

Install tmux first with your package manager. The installer only installs missing
dependencies when you explicitly pass `--yes`.

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

`claude --resume` (or `-r`) and `codex resume` open the CLI's own interactive
resume screen inside tmux. Search, selection, cancellation and named-session
lookup belong to the native CLI; bp does not replace the picker or read its keys.
The temporary bp session is observed after selection, using Claude's callbacks
or Codex's writer lock. Until then its conversation is unknown and messages wait.

For explicit UUIDs, `claude -c` and `codex resume --last`, bp can attach to an
existing verified owner before starting another CLI. This pre-launch routing does
not replace a native picker. If Codex exits with its specific active-writer
resume error, bp can attach that client to one kernel-verified existing tmux
writer. It never kills that writer or removes its lock. Other native errors are
preserved in a private `exit.json` and printed after tmux exits; `bp doctor --agent <name> --json` locates them. Shared app-server connections retain `--remote`.
Native Claude
and Codex `/rename` changes update the display name without changing authority.

Archive a closed registration without deleting its conversation:

```sh
bp archive work
bp archive --list --json
bp restore work
```

Archived records stay in their original agentbook with an `archivedAt` timestamp;
they leave active BP lists but retain their name, launch metadata and native
conversation files. Restore makes the record available again without launching a
CLI. Native resume pickers are not modified. Exit the agent first; a coordinator,
an agent with children, or an agent with pending messages cannot be archived.
Remote Codex threads must also be confirmed unloaded. Restore an archived parent
before its children. `bp open` and named local launches require an explicit
restore instead of silently reusing an archived name.

## Configure

Settings live in `~/.blueprint/config.yaml`, or under `BP_HOME`. Shell integration
lives in `~/.config/bp/shell.sh`. Native transcripts stay in their CLI directories.
Setup preserves existing configuration, aliases and records. It installs the
bundled `blueprint` skill for Codex and Claude, respecting `CODEX_HOME` and
`CLAUDE_CONFIG_DIR`. User-owned skills are preserved; changed managed copies are
backed up before an update.

For local session problems, run `bp doctor --agent <canonical-name> --json`.
It checks runtime binding, conflicting live registrations, stale resume names and
bar commands, without changing sessions or records. Closed historical entries do
not block the current session. A live thread conflict blocks message delivery,
but keeps model/context visible as a shared thread snapshot. `bp rename` updates
local resume claims and both bar commands; `--no-retitle` preserves native input
and leaves the native transcript title unchanged.

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
skips automatic onboarding during installation:

```sh
curl -fsSL https://github.com/tunapro1234/blueprint/releases/latest/download/install.sh | BP_ONBOARD=skip sh -s -- --local
```

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
on the installer side of the pipe, for example:

```sh
curl -fsSL https://github.com/tunapro1234/blueprint/releases/latest/download/install.sh | BP_VERSION=1.6.0 sh -s -- --local
```

The updater verifies the signed
manifest and binary checksum, keeps a backup, and restores the previous binary if
setup fails. Running agents are not restarted. Server installations use an explicit
host rollout so CLI and daemon versions are verified together.

Local agent startup can show a cached update notice. `updateCheck: false` in YAML
disables its daily background check. No model prompt is sent. Disabling shell
integration preserves aliases, agent records and open sessions; open a new terminal
to use native commands again.

## Disable or remove

Run `bp setup --disable` before uninstalling, then open a new terminal. Npm users
can run `npm uninstall -g @tunapro/blueprint`. Shell-installer users can remove
`~/.local/bin/bp` after checking `command -v bp`. The disabled shell source line
is harmless; remove the line ending `# bp local agents` from your shell startup
file if you want it gone. Agent records and configuration under `~/.blueprint`,
and native CLI transcripts, are kept. If bp is missing, its shell wrappers fall
back to the native CLI.

Installer and updater output names the previous binary backup. To roll back a
shell installation, copy that backup over `~/.local/bin/bp`, then run `bp setup`.
Use npm to change versions of an npm-managed installation.

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

Server-only integrations are optional. [P2P messaging](docs/p2p.md) is opt-in and uses
libp2p with your own rendezvous/relay and explicit peer permissions.


Report bugs and feature requests in [GitHub Issues](https://github.com/tunapro1234/blueprint/issues).
For a session problem, include the BP version, `bp doctor --agent <name> --json`
output and relevant channel ID; distinguish automatic delivery from a manual Enter.

## License

Blueprint is licensed under the [GNU General Public License v3.0](LICENSE)
(SPDX: `GPL-3.0-only`).
