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
daemon startup; it waits for bp workflow resume. A worker panic cannot unwind
into the daemon process. After six restarts, the run is marked error.

bp workflow stop <run-id> sets a stop request. The current unit is allowed to
finish its wait and validation; no input is sent to interrupt an agent. Resume
with bp workflow resume <run-id> [--retry-failed] [--run-tokens N];
--retry-failed also sends units whose latest state is failed, and --run-tokens
raises the run token ceiling (see Supervision and budgets). Sent, working, and
validating units are observed and validated on resume without sending their
prompt again. New unit keys are picked up on resume. Run directories are
retained. Stopping a paused run changes nothing; it is already at rest.

Run statuses: running; waiting: <reason> (an agent is blocked, or every agent's
breaker is open); stopping and pausing (a stop or a pause was requested and
in-flight units are finishing); stopped; paused (supervision ended the run,
with pause_reason); done; error.

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

## Timing and state

Runs live below <state-dir>/workflow-runs/<run-id>/. run.json contains the
workflow snapshot hash, a workflow directory copy, workdir, agents, owner,
status, and restart metadata. units.jsonl records append-only state transitions;
times.jsonl has one row per finished unit. Both logs use append writes plus
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
each open breaker's reopen time, and run_tokens=<spent>/<ceiling> when a run
budget is set.

bp wait <agent> --until idle|working [--confirm N] [--timeout 5m] [--json] is
the read-only wait primitive for external runners. Idle defaults to two
confirmations; working defaults to one. Exit status 0 means reached, 1 means
timed out, and 2 means the agent is closed or its runtime is unknown.

## Example files

docs/examples/workflow/ is a generic, fictional workflow. Copy its records.json
into the selected project workdir before starting it.
