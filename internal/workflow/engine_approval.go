package workflow

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// parkUnit ends a judged unit in awaiting_approval instead of a result. The
// event carries the row the unit would have written, so a decision can
// finish it later without the worker. The agent is released exactly as after
// a finished unit.
//
// In a workflow with steps, proposed is the parked step's own result and the
// unit-level proposal is metrics.step's final result when set; the park
// keeps the step's record so a revision continues that step.
func (e *Engine) parkUnit(ctx context.Context, run Run, d Definition, unit Unit, agent string, metrics unitMetrics, proposed, reason string, round int, lastFinished *time.Time) error {
	unitProposed := proposed
	if metrics.step.hasFinal {
		unitProposed = metrics.step.finalResult
	}
	metrics.approval.Proposed = unitProposed
	event, row, record := e.unitOutcome(run, unit, agent, metrics, proposed, reason, round, nil, *lastFinished)
	output, _ := renderOutput(d, unit, run.Workdir, run.ID, metrics.started.Format(time.RFC3339Nano), metrics.stepData())
	event.State = StateAwaitingApproval
	event.Park = &ParkRecord{Proposed: unitProposed, Output: output, Started: metrics.started, Row: row, StepRecord: record}
	if err := e.Store.AppendEvent(run.ID, event); err != nil {
		return err
	}
	if err := e.countAgentUnit(run.ID, agent); err != nil {
		return err
	}
	*lastFinished = row.FinishedAt
	e.afterPark(row)
	if err := e.noteParked(ctx, run, d); err != nil {
		return err
	}
	return e.setCurrent(run.ID, agent, "")
}

// seedParkNotice restores the parked-unit notification throttle from the log.
func (e *Engine) seedParkNotice(events []Event) {
	e.parkMu.Lock()
	defer e.parkMu.Unlock()
	e.parkNotice = time.Time{}
	for _, event := range events {
		if event.State == eventApprovalNotice && event.At.After(e.parkNotice) {
			e.parkNotice = event.At
		}
	}
}

// noteParked notifies the owner when a unit parks, at most once per
// approvalNoticeEvery, with the number of units waiting.
func (e *Engine) noteParked(ctx context.Context, run Run, d Definition) error {
	e.parkMu.Lock()
	defer e.parkMu.Unlock()
	now := e.now()
	if !e.parkNotice.IsZero() && now.Sub(e.parkNotice) < approvalNoticeEvery {
		return nil
	}
	events, _, err := e.Store.Events(run.ID)
	if err != nil {
		return err
	}
	count := 0
	for _, event := range latestEvents(events) {
		if parkedState(event.State) {
			count++
		}
	}
	message := fmt.Sprintf("%d units await approval", count)
	if err := e.Store.AppendEvent(run.ID, Event{At: now, State: eventApprovalNotice, Reason: message}); err != nil {
		return err
	}
	e.parkNotice = now
	e.notifyOwner(ctx, run, d, fmt.Sprintf("workflow %s run %s: %s; list with bp workflow approvals %s", run.Workflow, run.ID, message, run.ID))
	return nil
}

// revisionsFor lists the revisions waiting for agent.
func (e *Engine) revisionsFor(runID, agent string, units map[string]Unit) ([]Event, error) {
	events, _, err := e.Store.Events(runID)
	if err != nil {
		return nil, err
	}
	return pendingRevisions(events, agent, units), nil
}

// nextRevision claims and runs the oldest revision waiting for agent. It
// reports whether one was claimed.
func (e *Engine) nextRevision(ctx context.Context, run Run, d Definition, agent string, units map[string]Unit, lastFinished *time.Time, completed *atomic.Int64) (bool, error) {
	waiting, err := e.revisionsFor(run.ID, agent, units)
	if err != nil {
		return false, err
	}
	for _, candidate := range waiting {
		claim, ok, err := e.Store.claimRevision(run.ID, candidate.Key, agent)
		if err != nil {
			return false, err
		}
		if !ok {
			continue
		}
		return true, e.processRevision(ctx, run, d, agent, units[candidate.Key], claim, lastFinished, completed)
	}
	return false, nil
}

// processRevision sends one follow-up round with the human's text as
// .Problems to the agent that produced the unit, regardless of
// validate.rounds. It never compacts first: the revision relies on the
// conversation context. Afterwards the validator decides as usual.
func (e *Engine) processRevision(ctx context.Context, run Run, d Definition, agent string, unit Unit, claim Event, lastFinished *time.Time, completed *atomic.Int64) error {
	if e.Authority != nil {
		if err := e.Authority(run.Owner, agent); err != nil {
			return fmt.Errorf("authority lost before revision of unit %q on %s: %w", unit.Key, agent, err)
		}
	}
	if stop, err := e.stopRequested(run.ID); err != nil || stop {
		return err
	}
	if err := e.setCurrent(run.ID, agent, unit.Key); err != nil {
		return err
	}
	defer func() { _ = e.setCurrent(run.ID, agent, "") }()
	idle, err := e.waitIdle(ctx, run.ID, agent, 0)
	if errors.Is(err, errStopBoundary) {
		return nil
	}
	if err != nil {
		return err
	}
	park := *claim.Park
	step, err := e.revisionStep(run, d, unit.Key, claim)
	if err != nil {
		return err
	}
	d = d.forStep(step.index)
	metrics := metricsFromPark(park, idle)
	metrics.step = step
	metrics.tokenSince = e.now()
	metrics.prior = e.priorTokens(agent, park.stepRow())
	e.markInflight(agent, metrics.tokenSince)
	round := max(1, park.stepRow().Rounds) + 1
	started := metrics.started.Format(time.RFC3339Nano)
	output, err := renderOutput(d, unit, run.Workdir, run.ID, started, metrics.stepData())
	if err != nil {
		return e.unjudged(ctx, run, d, unit, agent, metrics, err.Error(), round-1, nil, completed, lastFinished)
	}
	metrics.step.output = output
	text := ""
	if claim.Decision != nil {
		text = claim.Decision.Text
	}
	prompt, controls, err := renderPrompt(e.Store.SnapshotDir(run.ID), d, unit, run, round, text, output, started, metrics.stepData())
	if err != nil {
		return e.unjudged(ctx, run, d, unit, agent, metrics, "render follow-up: "+err.Error(), round-1, nil, completed, lastFinished)
	}
	metrics.controls += controls
	if err := e.appendEvent(run.ID, metrics.stamp(Event{Unit: unit.Key, Key: unit.Key, Agent: agent, State: "sending", Round: round, ControlReplaced: controls, LastObservation: idle})); err != nil {
		return err
	}
	return e.sendAndWork(ctx, run, d, unit, agent, metrics, round, prompt, output, completed, lastFinished)
}

// revisionStep is the step a revision continues: the parked step, with its
// attempt (a revision is not a new attempt).
func (e *Engine) revisionStep(run Run, d Definition, key string, claim Event) (stepRun, error) {
	if !d.MultiStep() {
		return stepRun{}, nil
	}
	index := d.StepIndex(claim.Step)
	if index < 0 {
		return stepRun{}, fmt.Errorf("unit %q was parked at unknown step %q", key, claim.Step)
	}
	events, _, err := e.Store.Events(run.ID)
	if err != nil {
		return stepRun{}, err
	}
	progress := progressOf(d, events, key)
	reason, role := claim.AttemptReason, ""
	if record := claim.Park.StepRecord; record != nil {
		reason, role = record.AttemptReason, record.Role
	}
	if role == "" {
		role = run.RoleOf(claim.Agent)
	}
	return stepRunAt(d, progress, index, max(1, claim.Attempt), reason, role), nil
}

// metricsFromPark continues a parked unit's metrics in its revision round.
// In a workflow with steps they are the parked step's metrics.
func metricsFromPark(park ParkRecord, last Observation) unitMetrics {
	row := park.stepRow()
	metrics := unitMetrics{started: park.Started, model: row.Model, last: last, work: row.WorkSeconds, validate: row.ValidateSeconds, wait: row.WaitSeconds,
		roundWork: append([]float64(nil), row.RoundSeconds...), compact: row.CompactSeconds, compactResult: row.CompactResult,
		compactBefore: row.CompactCtxBefore, compactAfter: row.CompactCtxAfter, ctxStart: row.CtxStart, revision: &park}
	if metrics.model == "" {
		metrics.model = last.Model
	}
	if row.QueuedAt != nil {
		metrics.queuedAt = *row.QueuedAt
	}
	if row.DeliveredAt != nil {
		metrics.deliveredAt = *row.DeliveredAt
	}
	if row.StartedAt != nil {
		metrics.startedAt = *row.StartedAt
	}
	if row.Approval != nil {
		metrics.approval = RowApproval{WaitSeconds: row.Approval.WaitSeconds, Revisions: row.Approval.Revisions}
	}
	return metrics
}

// seedRevision restores revision metrics when a restarted worker resumes a
// unit that is in its revision round.
func (e *Engine) seedRevision(run Run, agent, key string, metrics *unitMetrics) error {
	events, _, err := e.Store.Events(run.ID)
	if err != nil {
		return err
	}
	var claim *Event
	var sentAt time.Time
	for _, event := range events {
		if event.Key != key {
			continue
		}
		switch {
		case terminalState(event.State) || parkedState(event.State) || betweenSteps(event.State):
			claim, sentAt = nil, time.Time{}
		case event.State == StateRevising && event.Park != nil:
			copied := event
			claim, sentAt = &copied, time.Time{}
		case event.State == "sending" && claim != nil && sentAt.IsZero():
			sentAt = event.At
		}
	}
	if claim == nil {
		return nil
	}
	seeded := metricsFromPark(*claim.Park, metrics.last)
	seeded.channel = metrics.channel
	seeded.step = metrics.step
	seeded.tokenSince = sentAt
	if seeded.tokenSince.IsZero() {
		seeded.tokenSince = claim.At
	}
	seeded.prior = e.priorTokens(agent, claim.Park.stepRow())
	*metrics = seeded
	return nil
}

// priorTokens freezes the parked rounds' usage for a revision. The parked
// interval is re-measured first so usage the collector had not seen at park
// time is included; a lower re-measure keeps the recorded figure. A parked
// row without tokens was unknown and stays unknown (nil).
func (e *Engine) priorTokens(agent string, row TimeRow) *TokenCounts {
	if row.Tokens == nil {
		return nil
	}
	recorded := countsOf(row.Tokens)
	if row.Tokens.Since == nil {
		return recorded
	}
	use, known, _ := e.measureTokens(agent, *row.Tokens.Since, row.FinishedAt)
	if !known {
		return recorded
	}
	remeasured := tokensWithPrior(use, e.percent(), *row.Tokens.Since, row.Tokens.Prior)
	if remeasured.Weighted < recorded.Weighted {
		return recorded
	}
	return countsOf(remeasured)
}

// rowTokens measures a unit's tokens for its row. A revised unit adds the
// frozen usage of its earlier rounds to the revision interval, so the time it
// spent parked (when the agent worked on other units) is not counted.
func (e *Engine) rowTokens(run Run, agent string, metrics unitMetrics, finished time.Time) *UnitTokens {
	if metrics.revision == nil {
		return e.unitTokens(run, agent, metrics.started, finished)
	}
	if metrics.prior == nil {
		return nil
	}
	use, known := e.meter(run.ID, agent, metrics.tokenStart(), finished)
	if !known {
		return nil
	}
	return tokensWithPrior(use, e.percent(), metrics.tokenStart(), metrics.prior)
}
