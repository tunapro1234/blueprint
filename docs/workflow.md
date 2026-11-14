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
.Run, .Attempt and .Previous (plus .Step and .Steps in a workflow with steps;
see Steps and roles). Without steps .Attempt is always 1 and .Previous is
empty.

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

Key reference. Every key of workflow.yaml, listed once, with its default;
the sections named in the last column explain it.

| key | default | meaning (section) |
|---|---|---|
| name | required | lowercase letters, digits and interior hyphens |
| version | required | schema version; only 1 |
| description | empty | free text printed by list and show |
| units.file | required | .json, .jsonl or .txt file in the run workdir |
| units.key | required for JSON and JSONL | field that names each unit |
| prompt.template | required | first prompt of a unit (or step) |
| prompt.followup | none | prompt of every later round; without it a unit has one round |
| output.path | none | workdir-relative output template |
| output.format | any | json or any |
| output.require_fresh | false | the output must be newer than the first send |
| validate.command | none (output check only) | validator argv; see the exit-code table |
| validate.timeout | 5m | validator timeout |
| validate.rounds | 1 | rounds per unit (per step attempt with steps) |
| validate.weak_after | rounds + 1 (never) | first round at which exits 1 and 3 may end weak |
| compact.ctx_above | 0 (off) | compact an agent whose context is at least this many tokens |
| compact.every_units | 0 (off) | compact an agent after this many units (work items with steps) |
| timeouts.start | 3m | delivery and start wait |
| timeouts.unit | 40m | work wait per round |
| notify | owner | owner or none: notify the run owner when the run ends |
| steps | none | 1 to 16 steps, each with name, agent, prompt, output, validate, send_back and fallback (Steps and roles) |
| steps[].send_back.to, .max | none; max 1 | validator exit 4 sends the unit back to an earlier step (Send-back) |
| steps[].fallback | none | ordered roles that retry a failed step (Fallback) |
| supervise.breaker.failures, .open_for | off | per-agent circuit breaker (Supervision and budgets) |
| supervise.meltdown.failures, .window | off | run-wide pause on repeated failures (Supervision and budgets) |
| budget.unit_tokens, .run_tokens | off | token ceilings (Supervision and budgets) |
| budget.cache_read_percent | 10 | weight of cache reads in the budget measure |
| approve.on | off | results that park for a decision (Approval gates) |
| guardrails.tool_calls.warn, .halt | off | tool calls per unit (Guardrails) |
| guardrails.repeated_tool_call.warn, .halt, .tool | off | identical calls in a row (Guardrails) |
| guardrails.tool_failures.warn, .halt, .tool | off | failed tool results in a row (Guardrails) |

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
They are not passed through a shell. The exit code decides the round:

| exit | meaning |
|---|---|
| 0 | accepted: the unit (or step) is done |
| 1 | problems: up to 8 KiB of stdout is passed as .Problems to the follow-up prompt and another round starts; when the rounds are used up (or there is no prompt.followup) the result is weak if the round is at least weak_after, otherwise failed |
| 3 | weak: accepted as weak when the round is at least weak_after; otherwise it behaves like exit 1 (and fails when the rounds are used up) |
| 4 | send back: on a step with send_back, up to 8 KiB of stdout becomes .Problems of the redo of the step send_back.to names (see Send-back); anywhere else it fails like any other code |
| other | failed, with stderr (or "validator exited with code N") as the reason |

Before the validator runs, the output is checked. Missing output, stale
output (require_fresh) or invalid JSON (format json) starts another round
with the error as .Problems, like exit 1 but never weak; at the last round,
or without prompt.followup, the unit fails. A validator that cannot run,
crashes or times out fails the unit at once. None of these stops the run.

A validator file shipped inside the definition directory must be referenced with a
path separator, for example `./check.py`, so it resolves in the run's snapshot copy
of the definition. A bare name with no separator runs from the project workdir
instead.

Control characters in rendered prompts are replaced with U+FFFD, except for
newlines and tabs. The replacement count is recorded with the unit transition.

## Steps and roles

A unit can go through several steps, each with its own prompt, output and
validator, run by agents of the step's role. A typical pipeline has a coder
write a change and a reviewer check it:

    steps:
      - name: code
        agent: coder
        prompt:
          template: prompts/code.md
          followup: prompts/code-fix.md
        output:
          path: "work/{{.Key}}.txt"
        validate:
          command: ["python3", "./check_code.py", "{{.Output}}"]
          rounds: 3
          weak_after: 2
      - name: review
        agent: reviewer
        prompt:
          template: prompts/review.md
          followup: prompts/review-fix.md
        output:
          path: "reviews/{{.Key}}.json"
          format: json
        validate:
          command: ["python3", "./check_review.py", "{{.Output}}", "{{.Steps.code.Output}}"]

steps is optional. It replaces the top-level prompt, output and validate keys:
a definition sets either steps or those keys, never both (check names the
keys that clash). It lists 1 to 16 steps. Each step has:

- name: required, unique, lowercase letters, digits, '-' and '_', starting
  with a letter or digit.
- agent: the role that runs the step, same characters as a name; a step
  without it has the role default.
- prompt, output, validate: exactly the rules of the top-level keys
  described above, for this step only. timeout, rounds and weak_after apply
  per step attempt.
- send_back: optional, {to, max}. Validator exit 4 sends the unit back to
  the earlier step to names (see Send-back).
- fallback: optional, an ordered list of roles that retry the step when it
  fails (see Fallback).

compact, timeouts, notify, supervise, budget, approve and guardrails stay at
the top level and apply to every step. A definition without steps behaves,
and records its runs, exactly as before.

Templates of a step (prompt, follow-up, output.path, validator arguments)
also receive .Step, the current step's name, and .Steps, the steps of this
unit that already finished, by name: .Steps.code.Output (the step's rendered
output path, empty without output.path), .Result (done or weak), .Agent,
.Rounds and .Reason. A template may only refer to steps that finish before
its own step; check and add reject a reference to a later step, to the step
itself or to an unknown step, and to .Steps in a definition without steps.
.Steps holds the latest finished attempt of each earlier step. .Started is
the current step attempt's first send.

.Attempt is the step attempt, starting at 1: how many times this step has
started for the unit, counting reruns, send-back redos, fallbacks and
--retry-failed restarts (approve --edit revision rounds are rounds of the
attempt they revise, not attempts). .Previous is the step's latest ended
attempt before this one, empty on attempt 1: .Previous.Agent, .Previous.Result
(done, weak, failed or sent_back), .Previous.Reason and .Previous.Output.
check renders every step's prompt and follow-up once as attempt 1 and once as
attempt 2 with a sample .Previous, so a misspelled field fails also inside
{{if gt .Attempt 1}}; any other field is an error.

Roles. bp workflow start --agent <name> gives an agent the role default;
--agent <role>=<name> gives it that role. Each agent has exactly one role,
every role a step uses (its agent role or a fallback role) needs at least one
agent, and every agent's role must be used by a step, where a fallback role
counts; start (and --dry-run) refuses anything else with the role named.
run.json records the roles of a run with steps. Each time a worker starts on a
run (start, resume, a daemon restart) it checks the roles again before any
work: a step role or fallback role without an agent in run.json (a run edited
by hand, or created by an older bp) ends the run with status error and "run
<id> has no agent for role <role>, which step <name> uses", instead of leaving
units waiting for that role.

    bp workflow start code-review --workdir /path/to/project \
      --agent coder=worker-a --agent coder=worker-b --agent reviewer=worker-c

Scheduling. A work item is one attempt of one step of one unit. Steps run
in order; a step that ends done or weak hands the unit to the next step, a
step that is sent back (exit 4) hands it to an earlier step, a failed step
is retried on a fallback role if one is left, and otherwise a failed step
ends the unit failed and later steps never run. When a step ended weak the
unit's result is weak, with the reason "step <name> weak: ...". A free agent
takes, in this order:

1. the unit it handed to itself: when the next step has its role, the agent
   that finished a step continues the unit at once (no other agent can take
   it in between), unless a stop or pause was requested or its breaker is
   not closed;
2. a revision of its own (approve --edit);
3. a send-back redo pinned to it (see Send-back);
4. a rerun of a step it ran last for that unit;
5. the oldest other item waiting for its role: a next step, a rerun the
   preferred agent cannot take now, a redo whose pinned agent left the run,
   a fallback attempt for a role it has, or a --retry-failed restart;
6. a new unit, when its role runs the first step.

An agent with nothing to take waits and polls as long as some unit can still
reach a step it serves (a step of its role, or a step that lists its role as
fallback, counting the steps a send-back can return to), and stops when none
can (every remaining unit is finished, parked or past every such step) or on a
stop or pause request. Compaction never happens between two steps of one unit
on the same agent, nor before a send-back redo on its pinned agent, also after
a restart; compact.every_units counts work items per agent. When every worker
has stopped but units still wait between steps or for a revision, the workers
start again; a pass that appended nothing to units.jsonl is followed by one
poll interval of waiting first, so the worker never loops hot. timeouts.start
and timeouts.unit apply to each step attempt. A stop or pause takes effect at
step boundaries: the step in flight finishes, the unit waits between steps,
and resume continues it with its next step. --limit counts units.

### Send-back

A reviewing step can send the unit back to an earlier step instead of
failing it:

    steps:
      - name: code
        agent: coder
        prompt:
          template: prompts/code.md
          followup: prompts/code-fix.md
        ...
      - name: review
        agent: reviewer
        send_back:
          to: code    # an earlier step with prompt.followup
          max: 2      # send-backs per unit attempt; default 1
        ...

Rules, checked by check and add: send_back is not allowed on the first step;
to is required and must name a step that runs before this one and has a
prompt.followup; max must be a positive integer (default 1).

When the step's validator exits 4, the attempt ends with the non-terminal
result sent_back and up to 8 KiB of the validator's stdout becomes the
problems text. The round counter does not matter: exit 4 ends the attempt in
any round. Then:

- The redo of the step to names runs as a new attempt (reason send_back),
  through that step's prompt.followup with the problems as .Problems, on the
  agent that ran the latest finished attempt of that step. Its round counter
  starts at 1 again, and it is not preceded by a compaction.
- While that agent's breaker is open the redo waits for it; the half-open
  trial may take it. If the agent is no longer in the run (run.json edited),
  any agent of the step's role takes the redo with the main prompt.template,
  still with .Problems set.
- Every step after the redo runs again in order, up to and including the step
  that sent the unit back (reason rerun). Each rerun prefers the agent of the
  step's role that ran it last: other agents leave it to that agent while it
  is free and its breaker allows work.
- max counts the send-backs of one step per unit attempt; --retry-failed
  starts a new unit attempt and resets it. When a step exits 4 with no
  send-back left, the unit fails with the reason "sent back <max> times; last:
  <the problems on one line, at most 300 bytes>" through the normal failure
  path: it parks when approve.on lists failed and counts for meltdown, but for
  no agent's breaker, and it never falls back. Approving such a parked unit
  accepts the step's output as weak: the unit continues with the next step, or
  finishes weak at the last step.
- Without send_back, exit 4 is an ordinary failure, as in a workflow without
  steps.

The sent_back attempt counts as a finished, successful work item for its
agent's breaker. The unit token budget covers every attempt of the unit
attempt, so many loops can end the unit at unit_tokens; before a send-back
the budget is checked, and a spent budget ends the unit failed (without
fallback) instead of sending it back. Guardrail windows stay per step
attempt: the redo and each rerun start a new window.

### Fallback

A step can name roles that retry it when it fails:

    steps:
      - name: code
        agent: coder
        fallback: [senior, lead]
        ...

Rules: each entry is a valid role name, differs from the step's own role
(the role default for a step without agent) and appears once. start needs at
least one agent for every fallback role, and an agent whose role is only a
fallback role is valid.

A failure that would end the unit retries the step from scratch, as a new
attempt (reason fallback) through the main prompt.template, on the next role
of the list that this unit has not tried for the step yet; .Previous names
the failed attempt. Only a failure on the last role of the list (an
exhausted ladder) ends the unit failed or parks it. The triggers are: not
delivered, never started, delivery unknown after a restart, unit timeout,
missing, stale or invalid output, validator failures (an exit that fails,
a validator timeout or crash), exhausted rounds and a guardrail halt. These
never fall back: an exhausted unit token budget, a send-back with no
send-back left, a prompt, follow-up or output path that cannot be rendered,
a human reject and any approve --edit revision round (a revision's failure
parks the unit again as before).

The failed attempt is written as a non-terminal step_failed event naming the
fallback role. It counts for its agent's breaker; meltdown counts only units
that finally end failed, so a rescued unit never counts. When every agent of
the fallback role has an open breaker, the item waits for that role and the
run shows "waiting: all agent breakers open for role <role>; next trial
<agent> at <time>" while other agents keep working. A fallback agent that
finishes the step continues the unit at once when the next step has its
role (sticky continuation), and a later send-back to the step is pinned to
the fallback agent, since it ran the step last. --retry-failed restarts the
step on its own role with a fresh ladder.

Records. Every unit event in units.jsonl of a run with steps carries step and
attempt (how many times this step ran for the unit) and, on a step's events,
attempt_reason: first, rerun, send_back, fallback or retry. A step that
finishes and hands on writes a step_done event with the step's record; a step
sent back writes sent_back with send_back {to, count, max, problems}; a failed
step that falls back writes step_failed with fallback (the next role). None of
the three is terminal. The unit's terminal event (or awaiting_approval)
carries the step it ended at and the step's record, and an exhausted send-back
adds send_back with exhausted true. Resume reads the log: a unit between steps
continues with its next step, a sent_back unit with its redo (still pinned), a
step_failed unit with its fallback attempt, a step in flight is observed and
validated without being sent again (a redo or fallback in flight too), and
--retry-failed restarts a failed unit at the step that failed, keeping the
results and outputs of the steps before it. A torn last line is skipped, so a
crash while appending loses at most the event being written. times.jsonl has
one row per unit attempt, written when the unit ends (or is decided): its
result and reason are the unit's, its other top-level fields describe the step
attempts of that attempt (rounds and work, wait and validation seconds summed
over them, round_s concatenated, start and queue times of the first, agent,
model and finish of the last, tokens summed), and steps lists each of them
with name, attempt, role (the role the attempt ran as), attempt_reason, agent,
model, result (done, weak, failed or sent_back), reason, sent_back_to (on a
sent_back attempt), rounds, output, work_s, validate_s, wait_s, queued_s (how
long the unit waited, ready, for its agent; 0 when a new unit starts and for a
--retry-failed restart), started_at, finished_at, tokens and activity. Rows
written before role, attempt_reason and sent_back_to existed still load. A
retried unit has one row per attempt: the row of a --retry-failed restart
lists only the steps run since the restart, and the steps it kept stay in the
earlier row, so no step attempt appears in two rows. Parking, approve and
--edit write no extra row; the unit's single row is written when it ends. bp
workflow times counts every row, one per unit attempt, as it does for retried
units without steps. Rows without steps (runs without steps, older runs) still
load.

bp workflow status names each agent's role and step, with the attempt when
it is above 1, for example "worker-c[reviewer]:unit-7@review#2 (3m2s)", and
counts the units ready for each step (between steps, sent back to it or
falling back), with the redos pinned to an agent, for example
ready=[code=3 (2 for worker-a), review=4]. bp workflow times adds one line
per step: "step review: units=N failed=F median_work=... p90_work=...
median_queue=..." where units counts step attempts; for a step with loops
"step code loops: sent_back=S redo=R rerun=N fallback=F rescued=K" (attempts
that sent the unit back, send-back redos, reruns, fallback attempts and the
fallback attempts that ended done or weak); and when some unit was sent back
"send-backs: units=U/N median_per_unit=M" (rows with at least one send-back,
of all rows, and the median number of send-backs among them). bp workflow
show prints the steps and roles with "send_back: to=code max=2 (validator
exit 4)" and "fallback: senior, lead" lines, and check prints "steps: code
(role coder, fallback senior, lead) -> review (role reviewer, send_back to
code max 2)".

See docs/examples/workflow-steps/ for a complete generic example.

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
In a workflow with steps a stop or pause acts at step boundaries and
--retry-failed restarts a unit at its failed step (see Steps and roles).

Run statuses: running; waiting: <reason> (an agent is blocked, every agent's
breaker is open, or every agent of a role that units wait for is open);
stopping and pausing (a stop or a pause was requested and in-flight units are
finishing); stopped; paused (supervision ended the run, with pause_reason);
awaiting-approval (every unit is finished or parked and at least one parked
unit waits for a decision; see Approval gates); done; error.

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

With steps, the breaker counts work items: a step attempt that ends failed
counts against the agent that ran it only, also when the step then falls back
to another role (step_failed); an attempt that finishes done or weak (and
hands the unit on) or is sent back (sent_back) resets that agent's count. A
failure from an exhausted send-back counts for no agent's breaker. A half-open
agent's trial is its next work item, whatever the step, a send-back redo
pinned to it included. When every agent of one role is open, units wait for
that role and the run shows "waiting: all agent breakers open for role <role>;
next trial <agent> at <time>" while agents of other roles keep working.

Meltdown. When failures units end failed within window, counted across all
agents and starting no earlier than the run start or the last resume, the run
pauses. A pause works like a stop: no new unit is dispatched, in-flight units
finish their wait and validation, and the run ends with status paused and a
reason such as "meltdown: 5 units failed within 30m; last: <unit> on <agent>:
<reason>". The owner is notified once. bp workflow resume continues the run
and starts a new window. With steps, meltdown counts units that end failed; a
finished step, a send-back and a failed step that falls back are neither
failures nor successes for it. A unit that fails after its send-backs ran out,
or after the last fallback role failed, counts once.

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

With steps, unit_tokens covers the whole unit attempt: the steps it already
finished plus the current one, so a later step can end the unit at the
ceiling. Send-back redos, reruns and fallback attempts are part of the unit
attempt and count too; unit_tokens is also checked before a send-back, and at
the ceiling the unit ends failed instead of going back, and does not fall
back. After a --retry-failed restart the steps kept from the earlier attempt
do not count, as a retried unit without steps starts with a fresh budget. The
run_tokens admission happens before every work item's first prompt, a new
unit, the next step of a unit (including an agent continuing its own unit), a
send-back redo, a rerun or a fallback attempt, but not before follow-ups or
revisions.

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
there is nothing to judge. With steps, a failure with a fallback role left is
retried on that role first; the unit parks or fails, as above, only when the
ladder is exhausted (see Fallback).

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

With steps, a unit parks when its last step finishes with a result approve.on
lists, or when any step ends failed and on lists failed; a weak or done step
before the last one always hands on. approvals shows the step
("step=review"). Approving the last step finishes the unit as above.
Approving a failed step before the last one accepts that step as weak: the
unit continues with its next step (its result will be weak) and approve
prints "approved <unit> (<agent>) at step <step> as weak; continues with step
<next>"; without a running worker, bp workflow resume continues it. reject
ends the unit failed at that step. approve --edit sends the revision to the
agent of the parked step through that step's prompt.followup (the step needs
one); after the round the step finishes, parks again or fails as usual, and a
finished step hands the unit on.

Send-backs and fallbacks. Neither a sent_back attempt nor a failed attempt
that falls back parks: approve.on sees only the unit's final result. A unit
parks after its fallback ladder is exhausted (the last fallback attempt's
agent is the parked agent) or after its send-backs ran out, with the reason
"sent back <max> times; last: ...". Approving the latter accepts the output
as weak and continues forward like any approved failed step. A revision
round never falls back, and a validator exit 4 in a revision round follows
the send-back rules: with send-backs left the unit goes back, otherwise it
parks again. A human reject never falls back.

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

Steps. With steps the window is one step attempt: it starts at the step's
first send, so each step of a unit is measured on its own and announced levels
start over at the next step, and also at a send-back redo, a rerun and a
fallback attempt. A halt is a fallback trigger: when the step lists a fallback
role left to try, the step is retried there instead of ending the unit.
guardrail_warn and guardrail_halt events and their notices name the step. A
halt with no fallback left ends the unit failed at that step (or parks it when
approve.on lists failed). Each steps entry of the times row carries that
step's activity; the unit-level activity adds the tool calls of all steps and
keeps the longest streaks and the worst verdict.

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
is decided; a unit with steps gets one row, with a steps array, when it ends). Both logs use append writes plus
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
that reached a guardrail level. With steps it also shows each agent's role and
current step (with #<attempt> above 1) and ready=[<step>=N ...], with the
redos pinned to an agent in parentheses (see Steps and roles).

Event states in units.jsonl. A unit's events: sending, sent, working and
validating while a round is in flight; step_done, sent_back and step_failed
between the steps of a unit (not terminal); awaiting_approval, revise and
revising while it waits for a decision or a revision round; done, weak and
failed when it ends (terminal; a decision writes the terminal event with
decision set). Agent and run events: compact_pending, compact_effective and
compact_ineffective; breaker_open, breaker_half_open and breaker_closed;
run_paused (meltdown or run budget); tokens_unknown; guardrail_warn,
guardrail_halt and guardrails_unknown; approval_notice (an owner
notification about parked units).

bp wait <agent> --until idle|working [--confirm N] [--timeout 5m] [--json] is
the read-only wait primitive for external runners. Idle defaults to two
confirmations; working defaults to one. Exit status 0 means reached, 1 means
timed out, and 2 means the agent is closed or its runtime is unknown.

## Example files

docs/examples/workflow/ is a generic, fictional workflow. Copy its records.json
into the selected project workdir before starting it.

docs/examples/workflow-steps/ is a generic two-step pipeline: a coder writes a
short note per record and a reviewer checks it. A "revise" verdict makes
check_review.py exit 4, which sends the note back to its coder (send_back, at
most twice); a commented fallback line shows how to add a senior role. Copy
its records.json into the workdir and start it with --agent coder=<name>
--agent reviewer=<name>.
