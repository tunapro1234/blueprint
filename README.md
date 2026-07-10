# blueprint (`bp`)

`bp` combines the server's agent/tmux, message queue, usage-policy, and WhatsApp
client operations in one standalone Go binary. `bp daemon` is a supervisor that
schedules the existing Python/Node jobs without rewriting their business logic.

Build and verify:

```sh
make check
```

The binary is written to `/srv/blueprint/bp`.

Run `bp help` for a command summary. `blueprint.service` is at the repository
root; it is not installed or enabled under `/etc` until cutover.

Use managed Git worktrees when agents need isolated branches in the same
repository:

```sh
bp worktree add /srv/project shop
bp worktree list /srv/project
bp open shop-agent /srv/project --worktree shop
bp worktree rm /srv/project shop
```

Managed worktrees live at `<repo>/.worktrees/<topic>` on `<topic>/dev`.
Removal refuses uncommitted changes unless `--force` is supplied.

Install from the product site:

```sh
curl -fsSL https://bp.trasumanar.ai/install.sh | bash
```

On the server, the installer links `/srv/blueprint/bp` into `/usr/local/bin`.
On a laptop, it downloads the matching release into `~/.local/bin` and creates
`~/.config/bp/config`. Connect with `bp con <agent-name>`; run `bp con` to list
available sessions. `bp img` uploads a local clipboard image over SSH and copies
the resulting server path back to the local clipboard.

The npm wrapper package is prepared in `npm/`. It intentionally is not
published by the release build; see `npm/README.md` for the manual publish step.
