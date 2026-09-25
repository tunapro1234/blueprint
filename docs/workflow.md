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
      command: ["python3", "check.py", "{{.Output}}"]
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
startup, runs still marked running, stopping, or waiting are started again. A
worker panic cannot unwind into the daemon process. After six restarts, the run
is marked error.

bp workflow stop <run-id> sets a stop request. The current unit is allowed to
finish its wait and validation; no input is sent to interrupt an agent. Resume
with bp workflow resume <run-id>; --retry-failed also sends units whose latest
state is failed. Sent, working, and validating units are observed and validated
on resume without sending their prompt again. New unit keys are picked up on
resume. Run directories are retained.

Before a prompt is sent, an agent must be observed idle twice (five seconds
between polls). A blocked composer moves status to waiting: <agent> blocked
until the agent becomes available. The engine never presses a key to submit a
prompt. A queued but unverified message is not resent; it fails as not delivered
after the start timeout. A verified delivery that never starts also fails with
the delivery record and last observation in units.jsonl.

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
and per-model summaries, total compact time, and the five slowest units. A
workflow name aggregates all of its saved runs.

bp wait <agent> --until idle|working [--confirm N] [--timeout 5m] [--json] is
the read-only wait primitive for external runners. Idle defaults to two
confirmations; working defaults to one. Exit status 0 means reached, 1 means
timed out, and 2 means the agent is closed or its runtime is unknown.

## Example files

docs/examples/workflow/ is a generic, fictional workflow. Copy its records.json
into the selected project workdir before starting it.
