# Workflows

bp workflow runs a saved directory of prompt templates over unit records in a
project workdir. It assigns one unit at a time to each selected tmux agent,
checks delivery and agent activity, optionally compacts context, validates
outputs, and records timing data.

## Definition

A workflow directory contains workflow.yaml, prompt templates, and any
validator files named in validate.command. Paths in the definition are relative
and may not traverse outside that directory. YAML uses the strict schema:
unknown keys are errors. Templates use Go text/template with missingkey=error;
they receive only .Unit, .Key, .Round, .Problems, .Output, .Started, .Workdir,
and .Run.

Example settings (see docs/examples/workflow/workflow.yaml for the complete
generic example):

    name: sample-scoring
    version: 1
    units:
      file: records.json
      key: slug
    prompt:
      template: prompts/score.md
      followup: prompts/fix.md
    output:
      path: "results/{{.Unit.slug}}.json"
      format: json
      require_fresh: true
    validate:
      command: ["python3", "./check.py", "{{.Output}}"]
      timeout: 2m
      rounds: 3
      weak_after: 2
    compact:
      ctx_above: 150000
      every_units: 0
    timeouts:
      start: 3m
      unit: 40m
    notify: owner
    supervise:
      breaker:
        failures: 3
        open_for: 15m
      meltdown:
        failures: 5
        window: 30m
    budget:
      unit_tokens: 2000000
      run_tokens: 50000000
      cache_read_percent: 10
    approve:
      on: [weak, failed]

units.file is read from the run workdir. JSON is one array of objects, JSONL
is one object per nonblank line, and TXT is one nonblank line per unit. JSON
and JSONL require units.key; TXT uses the line itself as both key and
.Unit.key. Keys must be unique.

output.path is optional. If set, it is a workdir-relative template; format
json requires exactly one valid JSON value. format any accepts any file
content. require_fresh checks that the file was modified after the unit's
first send. Workflows that produce a set of files instead of one named file can
omit output.path; their validator can use .Started (RFC3339Nano, the first
send time) to inspect only files created for this unit.

Validators run as an argv array in the workdir, with stdin closed and a timeout.
They are not passed through a shell. Exit 0 accepts the unit. Exit 1 means
problems; up to 8 KiB of stdout is passed as .Problems to the follow-up prompt.
Exit 3 accepts a weak result only when the current round is at least
weak_after; otherwise it behaves like exit 1. Any other exit, timeout, crash,
missing output, stale output, or invalid JSON fails that unit without stopping
the run. When exit 1 exhausts the configured rounds, the result is weak only if
the final round meets weak_after.

A validator file shipped inside the definition directory must be referenced with a
path separator, for example `./check.py`, so it resolves in the run's snapshot copy
of the definition. A bare name with no separator runs from the project workdir
instead.

Control characters in rendered prompts are replaced with U+FFFD, except for
newlines and tabs. The replacement count is recorded with the unit transition.

## Save and run

    bp workflow check docs/examples/workflow --workdir /path/to/project
    bp workflow add docs/examples/workflow
    bp workflow list
    bp workflow show sample-scoring
    bp workflow start sample-scoring --workdir /path/to/project --agent worker-a --agent worker-b
    bp workflow status
    bp workflow times <run-id>
    bp workflow approvals <run-id>
    bp workflow approve <run-id> <unit>

start requires a verified caller who is the configured root or an ancestor of
every selected agent. The owner identity is stored in run.json; before each
unit, bp reloads the hierarchy and confirms the agent is still below that owner.
The daemon holds per-agent locks so concurrent runs cannot share an agent. If
the daemon is unavailable, start refuses the run. --dry-run validates the
workdir, authority, selected agents, and unit count without creating a run.

The daemon starts a separate _workflow-run process for each run. Unexpected
worker exits are retried with exponential backoff, up to six restarts. At daemon
startup, runs still marked running, stopping, pausing, or waiting are started
again. A paused run is never restarted, neither after a worker exit nor at
daemon startup; it waits for bp workflow resume. A run in awaiting-approval is
not restarted either; it waits for decisions (see Approval gates). A worker panic cannot unwind
into the daemon process. After six restarts, the run is marked error.

bp workflow stop <run-id> sets a stop request. The current unit is allowed to
finish its wait and validation; no input is sent to interrupt an agent. Resume
with bp workflow resume <run-id> [--retry-failed] [--run-tokens N];
--retry-failed also sends units whose latest state is failed, and --run-tokens
raises the run token ceiling (see Supervision and budgets). Sent, working, and
validating units are observed and validated on resume without sending their
prompt again. New unit keys are picked up on resume. Run directories are
retained. Stopping a paused run changes nothing; it is already at rest.
Stopping a run in awaiting-approval changes nothing either and reports how many
units wait for a decision. --retry-failed also retries rejected units (a
rejection is a failed result), but only while the run is not done: once the
last parked unit is decided the run is done and cannot be resumed.

Run statuses: running; waiting: <reason> (an agent is blocked, or every agent's
breaker is open); stopping and pausing (a stop or a pause was requested and
in-flight units are finishing); stopped; paused (supervision ended the run,
with pause_reason); awaiting-approval (every unit is finished or parked and at
least one parked unit waits for a decision; see Approval gates); done; error.

Before a prompt is sent, an agent must be observed idle twice (five seconds
between polls). A blocked composer moves status to waiting: <agent> blocked
until the agent becomes available. The engine never presses a key to submit a
prompt. A queued but unverified message is not resent; it fails as not delivered
after the start timeout. A verified delivery that never starts also fails with
the delivery record and last observation in units.jsonl.

## Supervision and budgets

Two optional blocks keep a broken agent or a broken run from failing every
remaining unit one after another, and cap token spend. Without them a run
behaves exactly as before. All keys are validated by check and add: counts
and token values must be positive integers, durations must parse as positive
Go durations (15m, 2h), cache_read_percent must be 0 to 100, and a breaker or
meltdown block must set both of its keys.

    supervise:
      breaker:
        failures: 3      # consecutive failed units on one agent
        open_for: 15m    # rest period before one trial unit
      meltdown:
        failures: 5      # failed units across all agents ...
        window: 30m      # ... within this sliding window
    budget:
      unit_tokens: 2000000
      run_tokens: 50000000
      cache_read_percent: 10   # default 10

Breaker. Each agent has its own circuit breaker. After failures consecutive
units on that agent end failed (weak and done results reset the count), the
breaker opens: the agent takes no new unit while the other agents continue.
After open_for it is half-open and takes exactly one trial unit. A trial that
succeeds closes the breaker; a trial that fails opens it again for another
open_for. units.jsonl records breaker_open (with the reopen time),
breaker_half_open and breaker_closed events, and the owner is notified once
per opening. bp workflow status lists open and half-open breakers with their
reopen time. While every agent's breaker is open the run shows
"waiting: all agent breakers open; next trial <agent> at <time>". Breaker state
is not stored separately: it is rebuilt from units.jsonl, so a restarted worker
continues where the old one stopped, and a torn log line is skipped. Resume
resets every breaker.

Meltdown. When failures units end failed within window, counted across all
agents and starting no earlier than the run start or the last resume, the run
pauses. A pause works like a stop: no new unit is dispatched, in-flight units
finish their wait and validation, and the run ends with status paused and a
reason such as "meltdown: 5 units failed within 30m; last: <unit> on <agent>:
<reason>". The owner is notified once. bp workflow resume continues the run and
starts a new window.

Token budget. Every finished unit records its token usage in times.jsonl when
the usage is known, with or without a budget. The budget measure is
input + output + cache_write + cache_read * cache_read_percent / 100, so cheap
cache reads count at a fraction. unit_tokens is checked between rounds, before
a follow-up prompt: at the ceiling the unit ends the way an exhausted round
limit ends it (weak only if weak_after allows it, otherwise failed) with the
reason "unit token budget exhausted (spent X of Y)". run_tokens is checked
before each new unit is dispatched, counting finished units plus the spend of
units still in flight; at the ceiling the run pauses with the reason "run token
budget exhausted (spent X of Y)". A follow-up for a unit already in flight is
not blocked by run_tokens. bp workflow resume <run-id> --run-tokens N stores a
new ceiling in run.json; resuming without raising it pauses again before the
next unit, and resume says so.

Token counts come from bp's token collector (bp tokens), the same data
bp tokens reports: the agent's own transcript plus the subagents it spawned,
attributed by the agent's name. Limits:

- The collector runs every five minutes, so when a unit finishes the last
  minutes of its usage may not be collected yet. The row written at finish
  keeps what was known then, with as_of (how far the collector had got) and
  since (the start of the measured interval). A row is final once as_of is at
  or after finished_at. Rows that are not final are re-measured over their
  own interval whenever they are read: by the run budget before every new
  unit, and by bp workflow times, the run_tokens figure of bp workflow status
  and the resume note. times.jsonl itself is never rewritten. bp workflow
  times prints "tokens not final yet: N units" while some rows still lag. Raw
  usage is kept about 14 days; after that a row keeps its recorded figure.
- Both ceilings are soft. unit_tokens is checked against the lagging figure
  between rounds, and run_tokens counts in-flight units with the lagging
  figure, so either can be overshot by roughly one collection interval of
  spend (plus the round or units already running). Run spend catches up with
  finished units at the next admission.
- Only harnesses whose usage the collector reads under the agent's name are
  metered: Claude Code and Codex agents in a tmux pane. codex-remote agents
  (threads of a shared app-server) and any other harness are unknown. An agent
  the store has no record of at all, from the day before the unit's start
  through its end, is unknown too: the collector attributes its transcript to
  another label (a session without the agent's title, a folder shared with
  another agent, or an agentbook the collector does not read), and zero would
  be wrong. When the collector has not run in the last 15 minutes usage is
  also unknown. For an unknown agent the budget is not enforced, units.jsonl
  records one tokens_unknown event per agent per run, and the unit row has no
  tokens; such a row is never filled in later. bp workflow check warns about
  this when a budget is set.
- A unit's tokens are everything the agent spent between the unit's first send
  and its end, including any work the agent did in that time that was not part
  of the unit.

## Approval gates

The optional approve key holds chosen results for a human decision:

    approve:
      on: [weak, failed]

on lists one or more of done, weak and failed, without duplicates. Without the
key nothing changes. A unit whose result is listed parks instead of finishing:
units.jsonl gets an awaiting_approval event with the proposed result, the
reason, the round, the agent, the rendered output path and the metrics its
times row would have had, and the agent goes on with its next unit. A parked
unit has no times.jsonl row until it is decided.

Only results the validator (or the output check) judged after the agent
finished a turn can park: validator exits, validator crash or timeout,
missing, stale or invalid output, exhausted rounds, and an exhausted
unit_tokens budget. A unit that was not delivered, never started, whose
delivery is unknown after a restart, that hit the unit timeout, or whose
prompt, follow-up or output path could not be rendered fails directly, since
there is nothing to judge.

Parked units are neutral for supervision: they neither count as failures for
the breaker and meltdown nor reset the breaker's count, and human decisions are
ignored by both as well. Their tokens count for the run budget. The owner is
notified when units park, at most once every ten minutes with the number
waiting, and once more when the run has nothing left but decisions. When every
unit is finished or parked and at least one is parked, the run ends with status
awaiting-approval. Neither the worker supervisor nor daemon startup restarts
it; bp workflow status shows awaiting_approval=N.

    bp workflow approvals <run-id> [--json]
    bp workflow approve <run-id> (<unit>... | --all) [--note <text>]
    bp workflow reject <run-id> (<unit>... | --all) [--reason <text>]
    bp workflow approve <run-id> <unit> --edit <text>

approvals lists parked units with agent, proposed result, round, park time,
output and reason. approve finishes a unit: a proposed done stays done, a
proposed weak or failed becomes weak. reject finishes it as failed with reason
"rejected: <text>". Each decision writes the terminal event and the unit's
times row with the parked metrics plus approval: proposed, decision
(approved or rejected), by, note, decided_at and wait_s (time from parking to
the decision). finished_at stays the time the unit parked. "--" ends the
options, for unit keys that start with a dash.

approve --edit records a revise event: the agent that produced the unit gets
one more follow-up round with the text as .Problems, regardless of
validate.rounds; the workflow needs prompt.followup. The round is not preceded
by a compaction and does not wait for run budget admission. A running worker
sends it at that agent's next unit boundary, ahead of new units; if the run has
no worker, bp workflow resume <run-id> delivers it. While the agent's breaker is
open, or the agent has left the run, the revision waits and approvals shows
"revision pending on <agent>". After the round the validator decides as usual,
so the unit finishes or parks again; if the round cannot be delivered or
judged, the unit parks again with its earlier proposal. Its row carries
approval.revisions and the sum of the parked wait times, and its tokens cover
the earlier rounds plus the revision round, not the time it spent parked.

A revision that is pending (requested, not yet claimed by a worker) can be
cancelled: approve or reject the unit by name and the decision applies to the
earlier parked proposal (approve gives done or weak as proposed, reject gives
failed), and the worker never sends the revision. --all does not cover units
with a pending revision; they must be named. A cancelled revision request
still counts in approval.revisions, and approval.wait_s covers the original
park and the time the request was pending. Once a worker has claimed the
revision (state revising: its round is about to be sent or running), the unit
cannot be decided until the round ends and the unit parks again or finishes.
The claim and the decision take the same lock, so exactly one of them wins.
approvals marks such rows "revision pending on <agent>" (with the hint that
approve or reject cancels it) or "revision running on <agent>".

Decisions require a verified caller who is the run's owner, the configured
root, or an ancestor of the owner. They are serialized with each other and with
the worker by a decisions.lock file in the run directory, so two concurrent
decisions on one unit write one terminal event and one row; the second is
refused because the unit is no longer awaiting approval. A unit list is
checked as a whole before anything is written. A decision made while a worker
runs is honored when that worker finishes the run. The last decision on an
awaiting-approval run with nothing left to run sets status done without a
notification. bp workflow resume on an awaiting-approval run with no pending
unit or revision only prints that the run waits for decisions.

bp workflow times reports approvals: approved, rejected and revised counts and
the median decision wait, plus the number of units still awaiting approval.

## Guardrails

The optional guardrails key watches what an agent does while it works on a
unit, not only what it produces. It counts the agent's tool calls and their
results and stops a unit that is clearly going in circles:

    guardrails:
      tool_calls:            # tool calls in one unit
        warn: 150
        halt: 400
      repeated_tool_call:    # the same call (tool and input) in a row
        warn: 5
        halt: 10
        tool: Bash           # optional: only this tool counts
      tool_failures:         # failed tool results in a row
        warn: 5
        halt: 12
        tool: Bash           # optional: only this tool's results count

Each rule is optional, but the block needs at least one. A rule needs warn,
halt or both; both are positive integers and warn must be below halt. tool is
an exact tool name as the harness reports it (Bash, Read, Edit, an MCP tool's
full name). Unknown keys are rejected. Without the key nothing is read and
nothing changes.

What is measured. The window of a unit starts at its first send and covers
every round, so follow-ups count too. Only the agent's own conversation counts:
tool calls made inside subagents it spawned do not.

- tool_calls is the number of tool calls in the window.
- repeated_tool_call is the longest run of consecutive calls with the same tool
  and the same input (compared by a hash of the input with its keys sorted).
  Any other call in between ends the run; with tool set only runs of that tool
  count.
- tool_failures is the longest run of consecutive tool results the harness
  marked as errors. A successful result ends the run; a call whose result has
  not arrived (still running, or interrupted) neither extends nor ends it.
  With tool set only that tool's results count and other tools in between do
  not end the run.

The streak rules use the longest run seen in the window, not the current one:
an agent that failed twelve times in a row and then recovered still reached
halt. Each rule's level is ok, warn or halt, and the unit's verdict is the
worst of them.

Only Claude Code agents are measured. bp reads the transcript it already binds
to the agent for status and delivery (the same file, never a second guess)
and reads only what was appended since the last check; a session switch
(/clear, a resume into a new session) is noticed and the new file is read by
timestamp from the window start. Codex, Hermes, opencode and other harnesses,
and a Claude agent whose transcript cannot be bound, have unknown activity:
units.jsonl records one guardrails_unknown event per agent per run, and the
unit runs without guardrails while its activity stays unknown. bp workflow
check prints a warning about this whenever guardrails are set.

bp never interrupts a working agent. While the engine waits for a busy agent
it checks the rules at most once a minute. A rule that newly reaches warn or
halt writes a guardrail_warn or guardrail_halt event to units.jsonl (with the
rule, level, value and limit) and notifies the owner, once per level per rule
for the unit. A halt notice says that the agent keeps working and that the
unit ends at its next idle boundary, so the owner can step in sooner by hand.

Boundaries. When the agent goes idle the validator runs as usual. An accepted
result (done, or weak by weak_after) finishes the unit normally even at halt.
Before a follow-up round would be sent, guardrails are checked first, then the
unit token budget: at halt no follow-up is sent and the unit ends failed with
the reason "guardrail: <explanation>", for example "guardrail: tool Bash failed
12 times in a row". This goes through the normal finish path: it counts for
the breaker and meltdown, and it parks instead when approve.on lists failed.
A halt reached while the agent was busy stays in force even if the activity
cannot be read at the boundary.

Revisions. approve --edit on a unit that a guardrail stopped still sends its
one revision round. tool_calls keeps counting from the earlier rounds, so a
unit already at its tool_calls halt gets no further follow-up after the
revision round. The streak rules start over at the revision's send: only the
revision round's calls count for them, and they are announced again if they
reach a level again.

Restarts. A restarted worker rebuilds which levels were already announced
from units.jsonl, so it does not notify twice, and recomputes the verdict from
the transcript.

Records. With guardrails set, each times.jsonl row whose activity was known
carries activity: tool_calls, longest_repeat, longest_failures, and verdict
plus rule when the unit ended at warn or halt (the row of a parked unit too).
These are measured over all tools, whatever the rules' tool filters. Rows
without the field (older runs, unknown activity) still load. bp workflow
status marks an in-flight unit that reached a level, for example
"a1:unit-7 (12m3s) [guardrail tool_failures halt]". bp workflow times adds
"activity: units=N/M median_tool_calls=X p90_tool_calls=Y warned=W halted=H"
and, when units halted, "guardrail halts: <rule>=N ...". bp workflow show
prints the rules.

## Timing and state

Runs live below <state-dir>/workflow-runs/<run-id>/. run.json contains the
workflow snapshot hash, a workflow directory copy, workdir, agents, owner,
status, and restart metadata. units.jsonl records append-only state transitions;
times.jsonl has one row per finished unit (a parked unit gets its row when it
is decided). Both logs use append writes plus
fsync. An incomplete or malformed record is skipped and surfaced as torn_log
by bp workflow status.

Each timing row includes run, workflow, unit, agent, observed model, result,
reason, rounds and per-round work seconds, queued/delivered/started/finished
timestamps, delivery wait, work, validation and compact seconds, idle gap, and
context values when known. Compact rows include before/after context and whether
the context shrink was ineffective. bp workflow times <run-id|workflow-name>
reports totals, result counts, median and p90 work time, round counts, per-agent
and per-model summaries, total compact time, and the five slowest units. When
rows carry token usage it also reports total tokens (raw and budget-weighted,
with the input, output, cache-write and cache-read split), median and p90
tokens per unit, tokens per agent and per model, and the five most expensive
units, all settled as described in Supervision and budgets. A workflow name aggregates all of its saved runs.

bp workflow status shows pause_reason for a paused run, breakers=[...] with
each open breaker's reopen time, run_tokens=<spent>/<ceiling> when a run
budget is set, and a [guardrail <rule> <level>] mark on an in-flight unit
that reached a guardrail level.

bp wait <agent> --until idle|working [--confirm N] [--timeout 5m] [--json] is
the read-only wait primitive for external runners. Idle defaults to two
confirmations; working defaults to one. Exit status 0 means reached, 1 means
timed out, and 2 means the agent is closed or its runtime is unknown.

## Example files

docs/examples/workflow/ is a generic, fictional workflow. Copy its records.json
into the selected project workdir before starting it.
