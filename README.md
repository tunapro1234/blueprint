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

A composer that is not empty no longer automatically means "someone is typing".
bp first reads the WHOLE composer box and compares it with the message it is
about to send and with the records already queued for that target. Text that is
exactly one of them is bp's OWN paste whose Enter never registered: it is
finished with Enter and reported as a normal delivery (this is what used to
deadlock the queue behind itself — one message sat for four days). A damaged
version of one of them is cleared with Ctrl-u and pasted again, never submitted,
because pressing Enter on it delivers a silently truncated message. Anything else
is someone's own text and is left completely untouched, exactly as before. bp
never sends Escape into a pane — it would cancel a running turn. Before the queue
re-pastes anything it also checks the target's transcript, so a message that
already arrived some other way is closed instead of delivered twice; when that
same text is still hanging in the composer, bp clears it (with Ctrl-u, never
Enter) and says so, because the closed record would otherwise be the last thing
able to recognise it.

### Machine-readable outcome and --force-busy (plumbing only)

Every `bp msg` run ends with one STABLE line for scripts:
`RESULT=delivered|queued|unverified|duplicate` plus ` CHANNEL=<id>` when a queue
record exists. The keys and verdict words never change; new information arrives
as new keys. Errors keep signalling through the exit code.

`bp msg --force-busy <name> <message>` delivers even to a WORKING pane (the
message lands in Claude Code's own input queue). It is reserved for plumbing —
the sender identity must be certain and one of `root`, `bp`, `wa`, `whatsapp`.
**The WhatsApp bridge's identity is `whatsapp` (bridge.js pins
`AGENT=whatsapp`); do NOT change the bridge to any other value** — earlier
instruction text saying "AGENT=wa" was written before the allowlist was fixed
and is void (ada, 2026-08-21). Force skips only the busy gate: a composer
holding anyone's text, a typing human, an expired login and a shell pane all
still refuse, and on a working screen the delivery is verified by the
transcript witness, never claimed from the pane.

A message that waits says WHY it waits. `bp q` prints a `!` line under it and
`bp qstat` names the cause instead of a bare "is still busy" — `composer'da
okunamayan bir paste var (chip)` (only a human can clear that one),
`composer'da yabanci metin var`, `pane calisiyor`, or the concrete delivery
failure (an expired login). `bp msg` prints the same reason immediately under its
queue line. The reason is refreshed on every dispatch pass and cleared when the
pane frees up, so a stuck target is one glance away from being fixed instead of
days away from being noticed.

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

`bp open <name> <dir> --hermes` launches the Hermes Agent TUI instead of
claude. A Hermes pane reports `python` as its command, so bp recognises it by
command AND screen together; delivery is fully supported with one hard rule —
a WORKING Hermes pane refuses even `--force-busy`, because in Hermes
submitting text mid-turn CANCELS the running turn (the pane itself says
`msg=interrupt` while busy). Messages to a busy Hermes queue normally and go
in when the turn ends. Hermes keeps no Claude-style transcript, so delivery
is screen-verified (`unverified` outcomes are possible, as with Codex).

`bp open <name> <dir> --codex --no-sandbox` opens a codex agent with BOTH
sandboxes off: ours (the session is created with `CODEX_BWRAPPED=1`, which
disables the bwrap wrapper) and codex's own
(`--dangerously-bypass-approvals-and-sandbox`). It is opt-in and changes nothing
for any other agent. The reason it exists is that the default is not merely
stricter but broken in some directories: codex's sandbox binds `.git` read-only
inside the writable root, and where there is no `.git` — `/srv`,
`/srv/kavram/.agents` — bwrap tries to mkdir it and dies, leaving an agent that
cannot run a single command. After opening, bp re-reads the pane and WARNS if
the command still says `bwrap`, which means the environment never reached the
launch line. What the flag buys is a shell with no confinement at all, so the
agent's own brief has to carry the boundary the sandbox no longer does.

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
