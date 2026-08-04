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

## Talking to agents

`bp msg <name> <message>` delivers to the agent's tmux session. A target that is
working or has half-typed input is never interrupted: the message is queued and
the printed channel id follows it with `bp qstat`. An agent that is not open at
all keeps its messages until it opens. Read what an agent says with `bp peek
<name>`, and see the fleet with `bp status` or `bp tree` (`closed` / `idle` /
`working` / `dead` — `dead` means the session is up but its pane no longer runs
an agent). `bp status --json` prints the same fleet as one JSON object, with
context tokens, conversation age, and model where they are known.

Delivery is reported honestly, in three outcomes. `sent` means the composer was
seen holding the message and then seen to clear. `GONDERILEMEDI: … kuyruga
alindi` means bp proved it did not land (the pane's login expired, or the
composer held someone else's text where the paste should be) and queued it for
another attempt. `gonderildi ama DOGRULANAMADI` means the keystrokes went in but
nothing confirmed them: bp exits non-zero and does NOT queue a retry, because
the message may well have arrived — look with `bp peek <name>` before resending.

`bp` stamps every message with `[<sender>] `, taken from the tmux session the
command runs in; `AGENT` is honoured only outside tmux. Never write your own
`[name]` prefix — a hand-written one just shows up nested inside the real
envelope.

Slash commands (`/compact`, `/goal`, ...) are delivered bare, since a prefix
would break the command, so authority is checked instead: only the fleet root or
an ancestor of the target may send one, and sideways or upward is refused.
`/goal <text>` is rewritten to `/goal [<sender>] <text>` so the target records
who set the goal; a bare `/goal` passes through untouched.

`bp compact` looks for agents worth compacting — an open, not busy `claude` pane
whose last human turn is older than `--idle-hours` (24) and whose context is
above `--min-ctx` (200000 tokens) — and prints one decision row per agent. It
sends nothing; `--apply` is what sends. Under `--apply` every pane is read again
right before its `/compact`, and a target that has started working is skipped
rather than queued. `--all` sweeps every descendant of the sender instead of the
policy set.

The fleet lives in one agentbook, `/srv/server-main/agentbook.json` (override
with `AGENTBOOK`). `bp open` takes `--parent <name>` and `--role <text>` to
write that entry correctly instead of guessing from the folder path; the parent
is validated before anything is opened, and both flags also correct an
already-open agent's entry.

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

Mirror the monitor dashboard in a terminal with `bp monitor`. Detailed views
are available as `bp monitor usage|cost|agents|projects|services|radar`. The
CLI reads `/srv/monitor/site/data.json` when available and otherwise uses the
configured `DASH_URL` (default `https://monitor.tunapro.xyz`).

Install from the product site:

```sh
curl -fsSL https://bp.tunapro.xyz/install.sh | bash
```

On the server, the installer links `/srv/blueprint/bp` into `/usr/local/bin`.
On a laptop, it downloads the matching release into `~/.local/bin` and creates
`~/.config/bp/config`. Connect with `bp con <agent-name>`; run `bp con` to list
available sessions. `bp img` uploads a local clipboard image over SSH and copies
the resulting server path back to the local clipboard.

The npm wrapper package is prepared in `npm/`. It intentionally is not
published by the release build; see `npm/README.md` for the manual publish step.
