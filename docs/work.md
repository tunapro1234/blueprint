# The work ledger

Rooms and the board carry text. The work ledger carries state with rules:
tasks with a single owner, findings with evidence, a change feed you can
resume, and a brief that a new or restarted agent reads instead of the whole
conversation. It is reached through `bp work` (CLI), the `bp_work_*` MCP
tools and `POST /v1/work` (REST). All three run the same operations
(`api.WorkRequest`) with the same checks.

Nothing runs in the background. Every operation is a call; nothing is pushed
into an agent's terminal.

## Three things that are never the same

- **A delivery receipt** (`DELIVERED` on a message) says text reached an
  agent. It says nothing about work.
- **A submission** says the owner claims the work is done and names the
  exact files, with their sha256 digests.
- **A verification** says a different identity checked that submission,
  what it checked, and the result.

A task becomes `verified` only through the last one. No message, receipt,
silence or timeout verifies anything.

## Projects

```sh
bp work project create demo --goal 'answer the question' --root ~/src/demo
bp work project show demo
bp work project pause|resume|stop demo --reason 'budget review'
bp work project root demo ~/data          # lead only
bp work projects
```

The creator is the project's **lead**. Only the lead pauses, resumes or
stops it, adds roots, reopens failed tasks and takes over a live lease.

- `paused`: no new tasks and no claims; held tasks can still be submitted and
  verified.
- `stopped`: also no submissions or verifications. Reads, recovery and admin
  still work, so a stopped project can be inspected and handed over.

**Roots** are the directories whose files may be cited as artifacts or
sources. A cited path is resolved (symlinks followed) and must be a regular
file inside a root; a symlink in a root that points outside it is refused,
as are directories, relative paths and the filesystem root as a root. File
contents are never copied into the ledger: it stores the path as cited, the
path it resolved to, the size, the sha256 digest and an optional revision
you name (`--revision`, e.g. a git commit).

`--allow-unproven-reviewers` (creation only, off by default): see
[Identity and authorization](#identity-and-authorization).

## Tasks

```
pending -> claimed -> working -> submitted -> verified
              |          |           |
              |       blocked        +-> working (rework, one attempt used)
              |                      +-> failed  (attempt budget spent)
              +-> pending (release, or recover after the lease expired)
```

`cancelled` and `failed` stay put until the lead reopens the task.

```sh
bp work task add demo 'compute the answer' --spec 'write result.txt' --dep t1 --verifier carol --attempts 3
bp work task claim demo t2 --lease 30m          # prints the fence
bp work task checkpoint demo t2 --fence 1 --progress 50 'parsed input'
bp work task renew demo t2 --fence 1 --lease 30m
bp work task block demo t2 --fence 1 'need the API key from the user'
bp work task unblock demo t2 --fence 1
bp work task release demo t2 --fence 1 'handing over'
bp work task submit demo t2 --fence 1 --artifact result.txt --revision 3f2a1c
bp work task verify demo t2 --pass --checks 'ran the tests; result matches spec'
bp work task verify demo t2 --run -- python3 ./check.py result.txt
bp work task recover demo t2 'owner lease expired'
bp work task cancel|reopen demo t2 'reason'
bp work task show demo t2 [--history]
bp work task list demo [--state working] [--owner ada]
```

**One owner.** A claim is checked and written under the project's lock
(an exclusive `flock`), so of many simultaneous claimants exactly one wins,
across processes. The others get a conflict naming the owner.

**Leases and fences.** A claim sets a lease (default 15m, at most 24h) and
increases the task's **fence**. Every owner write (renew, checkpoint, block,
unblock, release, submit) must present the current fence. When a lease ends:

- the owner's writes are refused with "lease expired" until it renews; a
  renew (or a release) still works while nobody recovered the task;
- anyone may `recover` the task: it returns to `pending` and the fence
  increases, so the previous owner's later writes — even under the same name,
  even after it re-claims — are refused as a stale owner. The lead may also
  recover a live lease; that takeover is recorded with its reason.

Expiry itself changes nothing; recovery is an explicit, recorded act.

**Dependencies.** A task can be claimed only when every dependency is
`verified` and that verification still holds: if a prerequisite's verified
artifacts changed on disk since, the claim is refused as stale evidence.
A failed or cancelled dependency blocks the task until the lead reopens or
cancels it; the brief and doctor name it. Adding a dependency that would
close a cycle is refused and the cycle is named (`t1 -> t3 -> t2 -> t1`).

**Submissions.** `submit` hashes each artifact now. Submitting the same
files with the same bytes again while awaiting verification changes nothing
(`repeated`). Different bytes supersede the earlier submission; a
verification of a superseded submission is refused.

**Verification.** `verify` judges the latest submission:

- the verifier must not be the submitter or the owner, and must be the
  task's `--verifier` when one is named;
- a `pass` re-hashes the submitted artifacts under the lock: bytes or a
  symlink target that changed since submission cannot pass;
- a `fail` uses one attempt: the task returns to its owner (`working`, with
  a fresh lease) or becomes `failed` when the budget (`--attempts`, default
  3) is spent.

`--run -- <argv>` runs a validator the way workflows do
([workflow.md](workflow.md)): argv without a shell, in the current
directory, stdin closed, killed with its process group on `--timeout`
(default 5m). The CLI first pins the latest submission and refuses if its
artifacts already drifted. Exit 0 backs a pass, exit 1 a fail, and anything
else records nothing. The record keeps the argv, the exit code, an output
excerpt and the output's digest. Validator records sent over MCP or HTTP are
stored with `selfReported: true`: bp did not run them.

## Findings

```sh
bp work finding add demo 'p95 is under 150 ms' --source bench.csv --revision 3f2a1c [--task t2]
bp work finding add demo 'lr 1e-2 diverges' --negative --conditions 'batch 8, fp16, no warmup' --source run.log
bp work finding verify demo f1 're-ran the analysis on bench.csv: p95 120 ms'
bp work finding refute demo f1 'run 2 shows 400 ms'
bp work finding check demo f1
bp work finding revise demo f1 --assertion 'p95 is under 450 ms' --source bench.csv
bp work finding show demo f1 [--history]
bp work finding list demo [--kind negative] [--status stale]
```

A finding starts as a `hypothesis`. Someone other than its author marks it
`verified` (or `refuted`) with evidence text; the author may refute
(retract) its own. A **negative** finding records a failed experiment and
must say under which conditions the failure applies, so the next agent knows
what not to repeat and when the result would no longer hold.

**Staleness.** A digest identifies bytes; it does not make a claim true.
What it does make detectable is change:

- Every read (`show`, `list`, the brief, doctor) re-hashes the sources and
  reports the **effective** status next to the stored one. A stored
  `verified` whose source changed (bytes, or the target a cited symlink
  resolves to) reads as effective `stale`.
- `check` persists that: it marks the finding `stale` (an event, so feed
  readers see it), or, for a fresh verified finding, records when it was last
  confirmed against its inputs.
- A stale finding cannot be verified again. Its author revises it against
  the current sources, which returns it to `hypothesis`; earlier judgments
  stay in its history, tagged with the revision they judged.

The latest judgment of the current revision is kept separately from the
bounded list of checks, so repeated freshness checks never push the
evidence behind a status out of the record.

## The feed

```sh
bp work feed demo [--after <cursor>] [--limit 50] [--kind task|finding|project]... [--id t2]... [--involving ada] [--summary]
bp work feed demo --subscribe [--from <cursor>|start] [--kind ...] [--involving ...]
bp work feed demo --next [--limit 50]
bp work feed demo --ack <cursor>
```

Every accepted change is an event with a sequence number. The feed returns
events after a cursor (`<epoch>:<seq>`):

- **Order:** total within a project, by sequence.
- **Bounds:** at most `--limit` events (default 50, max 500) and about
  256 KiB per call; `more` says to call again with `next`.
- **Filtering:** the `next` cursor moves past events the filter skipped,
  so a narrow filter does not rescan history. `--involving` matches events
  where the agent acted, owns, created the task, is its named verifier,
  authored the finding, or was the owner a recovery took the task from.
- **Replay:** the same cursor returns the same events. Delivery is
  at-least-once; deduplicate by `seq`.
- **Validity:** a cursor from another incarnation of the project or beyond
  the head is invalid; one older than the retained floor is expired. Either
  way, read the brief and continue from its cursor.
- **Retention:** the live log keeps at most 10,000 events; older events move
  to `archive/` segments. The feed does not serve archived events, but task
  and finding history (`show --history`) still includes them.

A **subscription** stores your cursor and filter in bp, so a restarted agent
resumes without keeping state. `--next` reads from the stored cursor
without moving it; `--ack` with the page's `next` moves it once you have
handled the events (a crash in between replays them). An ack never moves
backwards; subscribe again with `--from` to rewind on purpose.

## The brief

```sh
bp work brief demo [--items 20]
bp work doctor demo
```

The brief is the handoff bundle: the goal and state, held tasks with owner,
fence, lease and last checkpoint, tasks awaiting verification with their
artifacts, blocked and claimable tasks, verified tasks with their verifier,
verified findings with their evidence, hypotheses, stale or refuted
findings, failed experiments with their conditions, health issues and next
actions, and the cursor to continue the feed from.

It is computed from ledger records only — no model writes it — so the same
state and clock give the same brief. It names the sequence it was built at.
It is bounded: each list holds at most `--items` entries (default 20, max
100), long text is clipped, each entry shows at most five artifacts, and the
whole brief stays under 48 KiB of JSON (the per-list cap is halved until it
fits). Whatever was cut is counted under `omitted`, and `truncated` says when
the byte budget forced a smaller cap.

`doctor` lists only the issues: expired leases (abandoned owners), tasks
blocked by their owner, by a failed dependency or by a prerequisite whose
verified artifacts changed, submissions whose artifacts changed before
verification, verified tasks whose artifacts changed since, stale findings,
dependency cycles, and a paused or stopped project.

## Identity and authorization

The ledger records the caller's **identity**: the label bp proved (a pane
bp started — the CLI requires this for every write, as `bp board put` does)
or, over MCP outside a bp terminal and over HTTP, a self-declared name shown
with its transport (`mcp:ada`, `http:ada`). A self-declared `http:ada` is a
different identity from a proven `ada` and cannot act as its owner.

The CLI uses bp's normal resolver, so it accepts exactly what `bp board put`
accepts. Inside a tmux pane, the pane's session name counts only when the
calling process descends from that pane. Copying a `TMUX_PANE` into another
process does not make it that agent. Outside every pane, a login shell acts as the person its
`USER`/`SUDO_USER` names (unless that name is an agent's). A process with
neither has no identity, and its writes are refused.

**Authorization** comes from the ledger's own records:

| Action | Who |
|---|---|
| pause, resume, stop, add a root, reopen, take over a live lease | the project lead |
| renew, checkpoint, block, unblock, release, submit | the owner, with the current fence |
| recover an expired lease, claim, add tasks and findings | anyone (project active) |
| verify a task | not the submitter or owner; the named verifier if set; a proven identity |
| verify or refute someone's finding | not the author (for verify); a proven identity |
| refute your own finding, revise it | its author |
| cancel a task, add dependencies | its creator or the lead |

Two different self-declared names are not evidence of two independent
reviewers, so verification needs a proven identity unless the lead created
the project with `--allow-unproven-reviewers`; verifications by unproven
identities are then marked as such in records and in the brief.

Remote callers (the web-chat gateway) cannot reach the ledger at all, not
even to read it: it names local files. Every write and every refused write
is recorded in the audit log (`api.work.<op>`).

## Storage and recovery

Per project, under `<state>/api/ledger/<project>/`:

- `events.jsonl` is the source of truth. Each change is one transaction of
  one or more events, each with the full entity after the change; the append
  is fsynced before the change is acknowledged.
- `state.json` is a snapshot. Loading replays the log after it, so a crash
  between the two loses nothing. Once the append is durable the change is
  committed; a failed snapshot write is repaired by the next load.
- `archive/` holds events retention moved out of the live log; `subs.json`
  holds subscriptions.

Recovery fails closed. A malformed line, a sequence gap, an event from
another incarnation of the project or an incomplete transaction anywhere
but at the end of the log is reported as corruption and nothing is written
over it. Only an incomplete final transaction — a crash mid-append, never
acknowledged — is ignored and cut before the next append. Every directory is
0700 and every file 0600; writes are atomic (temp file, fsync, rename, then
a directory fsync).

## Limits and what this does not do

- Hashing and checking a path are separate steps; a process running as the
  same user can swap a file in between. The ledger records what it hashed;
  it does not defend against the owner's own processes racing it.
- A digest proves identity of bytes, not truth. A verification is only as
  good as what the verifier checked; the record says what that was.
- Leases are judged by the clock of the machine running bp; there is no
  cross-machine ledger and the P2P layer does not carry it.
- Projects cannot be deleted through bp; move the directory aside. A new
  project with the same name gets a new epoch, so old cursors are refused.
- Limits: 5,000 tasks and 5,000 findings per project, 32 artifacts per
  submission, 32 dependencies per task, 4 GiB per artifact, 20 checkpoints,
  submissions, verifications and checks kept inline per entity (all of them
  remain in the history).
- Capability declarations for routing work, per-task budgets other than the
  attempt count, and content-addressed copies of artifacts are not
  implemented.

## Design inspiration

The ledger was shaped by two public reports on the July 2026 incident in
which AI agents in an evaluation environment attacked Hugging Face
infrastructure:

- OpenAI, [The Hugging Face incident and the road
  ahead](https://openai.com/index/hugging-face-incident-and-the-road-ahead/)
  (August 26, 2026).
- [Brief independent investigation of agents' behavior, reasoning and
  collaboration in the OpenAI / Hugging Face hacking
  incident](https://evals.alignment.org/blog/2026-08-26-openai-hugging-face-incident-investigation/)
  (August 26, 2026).

**What the reports describe.** We summarize them here; this section makes no
claims beyond what they say.

- *Persistent shared memory.* OpenAI reports that agents first communicated
  by writing files into a shared package service, which turned it into an
  unintended message board. Through it, agents shared discoveries and picked
  up work others had left. The persistent channel pooled work across
  evaluations that were meant to be separate.
- *Coordination and ownership conventions.* The independent investigation
  describes conventions the agents built on that board: mailboxes, chunked
  transfers, and words such as HOLD, VETO, owner and STOP. It reports that
  these conventions did not always coordinate effectively and sometimes broke
  down into conflicts, and that agents sometimes took over work when an owner
  went silent.
- *Handoff.* The investigation describes a handoff dossier passed from one
  coordinating agent to a successor with a larger remaining budget.
- *Provenance.* The investigation describes:
  - agents noting that names on the unauthenticated board could be posted by
    anyone;
  - an accidental impersonation, after which some agents began signing
    messages;
  - an agent acting on a signed message without checking the signature;
  - tool-call spoofing, where the command shown differed from the command
    run.

**What we drew from them.** These are lessons for legitimate,
human-authorized multi-agent work. Coordination state that agents rely on
should be explicit, sanctioned and inspectable by the owner. It should not
arise as a side effect of some other shared system. Ownership needs rules
that hold under failure. A handoff should be checkable against records. And
anyone reading shared state should be able to tell who did what, whether that
identity was proven, and whether the evidence still matches.

**Our engineering choices.** The mechanisms below are ours. The reports do not
say any of them existed in the incident, and the ledger does not model the
incident.

- leases with fence tokens and explicit, recorded recovery, instead of
  informal ownership words;
- a transaction-framed, fail-closed event log as the single source of truth;
- sha256 digests re-checked on read, so stale evidence shows as stale;
- verification that needs an identity bp proved and that differs from the
  submitter;
- cursor-based feeds with explicit acknowledgement;
- a bounded brief computed from records rather than from a model's summary.

This is an original implementation informed by published reports. It
contains no incident code and reproduces none of the techniques described.
The ledger is an explicit shared-state surface: agents can see and change it
through bp, by design. It uses no covert channels and does not repurpose
unrelated shared infrastructure. Its state lives in bp's own state directory,
under the owner's control, with every write audited.

The ledger complements, rather than replaces, existing bp features:
- the workflow engine, which already validates outputs of planned
  multi-step runs;
- rooms, which already offer cursor-based reads;
- the board, for simple shared values.

It adds structured, durable state for ad hoc collaborative work.
