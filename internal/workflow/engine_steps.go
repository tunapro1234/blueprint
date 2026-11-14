package workflow

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// StepRecord is a finished step as its step_done event carries it, and the
// parked step of a parked unit: the step's result, rendered output path and
// its own times row (the row a one-step unit would have written).
type StepRecord struct {
	Name    string `json:"name"`
	Attempt int    `json:"attempt"`
	Output  string `json:"output,omitempty"`
	// QueuedSeconds is how long the step waited, ready, for an agent of
	// its role; zero for the first step.
	QueuedSeconds float64 `json:"queued_s,omitempty"`
	// Role is the role of the agent that ran the attempt: the step's role,
	// or a fallback role.
	Role string `json:"role,omitempty"`
	// AttemptReason says why the attempt was dispatched (AttemptFirst and
	// friends). Records written before send-backs and fallbacks have none.
	AttemptReason string `json:"attempt_reason,omitempty"`
	// SentBackTo names the step a sent_back attempt sent the unit back to.
	SentBackTo string  `json:"sent_back_to,omitempty"`
	Row        TimeRow `json:"row"`
	// kept marks a step a --retry-failed restart kept from an earlier pass:
	// its results feed the templates, but it belongs to the earlier pass's
	// times row, not to the row of the pass that keeps it. It is derived
	// from the log, never stored.
	kept bool
}

// StepRow is one step of a unit in the steps array of its times.jsonl row.
type StepRow struct {
	Name            string       `json:"name"`
	Attempt         int          `json:"attempt"`
	Agent           string       `json:"agent"`
	Model           string       `json:"model,omitempty"`
	Result          string       `json:"result"`
	Reason          string       `json:"reason,omitempty"`
	Rounds          int          `json:"rounds"`
	Output          string       `json:"output,omitempty"`
	WorkSeconds     float64      `json:"work_s"`
	ValidateSeconds float64      `json:"validate_s"`
	WaitSeconds     float64      `json:"wait_s"`
	QueuedSeconds   float64      `json:"queued_s"`
	StartedAt       *time.Time   `json:"started_at,omitempty"`
	FinishedAt      time.Time    `json:"finished_at"`
	Tokens          *UnitTokens  `json:"tokens,omitempty"`
	Activity        *RowActivity `json:"activity,omitempty"`
	// Role, AttemptReason and SentBackTo copy the step record's fields.
	Role          string `json:"role,omitempty"`
	AttemptReason string `json:"attempt_reason,omitempty"`
	SentBackTo    string `json:"sent_back_to,omitempty"`
}

func stepRowOf(record StepRecord) StepRow {
	row := record.Row
	return StepRow{Name: record.Name, Attempt: record.Attempt, Agent: row.Agent, Model: row.Model, Result: row.Result, Reason: row.Reason, Rounds: row.Rounds, Output: record.Output,
		WorkSeconds: row.WorkSeconds, ValidateSeconds: row.ValidateSeconds, WaitSeconds: row.WaitSeconds, QueuedSeconds: record.QueuedSeconds,
		StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, Tokens: row.Tokens, Activity: row.Activity,
		Role: record.Role, AttemptReason: record.AttemptReason, SentBackTo: record.SentBackTo}
}

// SendBackRecord is carried by a sent_back event, and by the failed event of
// a unit whose step exhausted its send-backs (Exhausted set). Count is the
// number of send-backs by the step in this unit attempt, this one included.
type SendBackRecord struct {
	To        string `json:"to"`
	Count     int    `json:"count"`
	Max       int    `json:"max"`
	Problems  string `json:"problems,omitempty"`
	Exhausted bool   `json:"exhausted,omitempty"`
}

// stepRun is the step a unit's current work item runs. In a definition
// without steps multi is false and every other field is zero: no event or
// row gains a step field.
type stepRun struct {
	multi   bool
	index   int
	name    string
	attempt int
	// reason is the attempt reason; role the role of the agent running it.
	reason string
	role   string
	// pass holds the latest finished attempt of each step before this one
	// in the unit's current pass, in step order.
	pass []StepRecord
	// history holds every step attempt the unit finished in this unit
	// attempt (since its last terminal event), in log order; the unit's
	// row and its token budget sum them.
	history []StepRecord
	// previous is the previous attempt of this step (.Previous).
	previous StepAttempt
	// sendBacks counts the send-backs this step issued in this unit
	// attempt; tried lists the roles whose attempt of this step failed and
	// fell back in it.
	sendBacks int
	tried     []string
	// queued is how long the work item waited ready for an agent.
	queued float64
	// output is the step's rendered output path.
	output string
	// final, when set, is the unit-level result and reason the step ends
	// the unit with; otherwise the step's own result is the unit's.
	hasFinal    bool
	finalResult string
	finalReason string
}

// stepData is what templates see of the pipeline: the current step's name,
// the steps finished before it, the attempt and the previous attempt.
type stepData struct {
	name     string
	results  map[string]StepResult
	attempt  int
	previous StepAttempt
}

func (s stepRun) data() stepData {
	results := map[string]StepResult{}
	for _, record := range s.pass {
		results[record.Name] = StepResult{Output: record.Output, Result: record.Row.Result, Agent: record.Row.Agent, Rounds: record.Row.Rounds, Reason: record.Row.Reason}
	}
	return stepData{name: s.name, results: results, attempt: s.attempt, previous: s.previous}
}

func (m unitMetrics) stepData() stepData { return m.step.data() }

// stamp adds the step and attempt to a unit event of a workflow with steps.
func (m unitMetrics) stamp(event Event) Event {
	if m.step.multi {
		event.Step, event.Attempt, event.AttemptReason = m.step.name, m.step.attempt, m.step.reason
	}
	return event
}

func (m *unitMetrics) setFinal(result, reason string) {
	m.step.hasFinal, m.step.finalResult, m.step.finalReason = true, result, reason
}

// earlierWeighted is the budget measure of the step attempts the unit
// finished before the current one in this unit attempt (send-backs, reruns
// and fallbacks included); unknown attempts count zero. Steps kept from an
// earlier unit attempt were spent by it and do not count, as a retried unit
// without steps starts with a fresh unit budget.
func (s stepRun) earlierWeighted() int64 {
	var total int64
	for _, record := range s.history {
		if record.Row.Tokens != nil {
			total += record.Row.Tokens.Weighted
		}
	}
	return total
}

// stepRow returns the parked step's own row: the step record of a parked
// unit of a workflow with steps, else the unit row.
func (p ParkRecord) stepRow() TimeRow {
	if p.StepRecord != nil {
		return p.StepRecord.Row
	}
	return p.Row
}

// unitProgress is what the log says about one unit's pass through the steps.
type unitProgress struct {
	// pass is the latest finished (done or weak) attempt of each step of the
	// unit's current pass, in step order. After a terminal event it is the
	// pass that ended there, which a --retry-failed restart at a later step
	// continues. Steps finished before a terminal event are marked kept:
	// that event's row holds them.
	pass []StepRecord
	// attempts is the highest attempt seen per step.
	attempts map[string]int
	// history is every step attempt finished (step_done, sent_back,
	// step_failed) since the unit's last terminal event, in log order.
	history []StepRecord
	// finished is every ended attempt per step, any unit attempt, in log
	// order (terminal and parked attempts included).
	finished map[string][]StepRecord
	// sendBacks counts sent_back events per sending step and tried lists
	// the roles of step_failed attempts per step, both since the unit's
	// last terminal event.
	sendBacks map[string]int
	tried     map[string][]string
}

func progressOf(d Definition, events []Event, key string) unitProgress {
	progress := unitProgress{attempts: map[string]int{}, finished: map[string][]StepRecord{}, sendBacks: map[string]int{}, tried: map[string][]string{}}
	terminated := false
	for _, event := range events {
		eventKey := event.Key
		if eventKey == "" {
			eventKey = event.Unit
		}
		if eventKey != key {
			continue
		}
		if event.Step != "" && event.Attempt > progress.attempts[event.Step] {
			progress.attempts[event.Step] = event.Attempt
		}
		progress.noteFinished(event)
		if terminalState(event.State) {
			// The terminal event's row holds this pass's steps.
			for index := range progress.pass {
				progress.pass[index].kept = true
			}
			progress.history = nil
			progress.sendBacks, progress.tried = map[string]int{}, map[string][]string{}
			terminated = true
			continue
		}
		if event.Step != "" && terminated {
			// A new pass after a terminal event keeps the steps before the
			// step it restarts at.
			progress.pass = recordsBefore(d, progress.pass, d.StepIndex(event.Step))
			terminated = false
		}
		if !betweenSteps(event.State) || event.StepRecord == nil {
			continue
		}
		record := *event.StepRecord
		progress.history = append(progress.history, record)
		switch event.State {
		case StateStepDone:
			progress.pass = withRecord(d, progress.pass, record)
		case StateSentBack:
			progress.sendBacks[record.Name]++
		case StateStepFailed:
			progress.tried[record.Name] = append(progress.tried[record.Name], record.Role)
		}
	}
	return progress
}

// noteFinished records an attempt an event ends: a finished step, a parked
// step or a terminal event. A later event about the same attempt (a human
// decision) replaces the earlier one and keeps its output.
func (p *unitProgress) noteFinished(event Event) {
	if event.Step == "" {
		return
	}
	var record StepRecord
	switch {
	case event.StepRecord != nil && (betweenSteps(event.State) || terminalState(event.State)):
		record = *event.StepRecord
	case event.Park != nil && event.Park.StepRecord != nil && parkedState(event.State):
		record = *event.Park.StepRecord
	case terminalState(event.State):
		// A decision's terminal event carries no record.
		record = StepRecord{Name: event.Step, Attempt: event.Attempt, Row: TimeRow{Agent: event.Agent, Result: event.State, Reason: event.Reason}}
	default:
		return
	}
	list := p.finished[record.Name]
	for index := range list {
		if list[index].Attempt == record.Attempt {
			if record.Output == "" {
				record.Output = list[index].Output
			}
			list[index] = record
			return
		}
	}
	p.finished[record.Name] = append(list, record)
}

// previousAttempt is the latest ended attempt of step before attempt.
func (p unitProgress) previousAttempt(step string, attempt int) StepAttempt {
	var best *StepRecord
	for index, record := range p.finished[step] {
		if record.Attempt < attempt && (best == nil || record.Attempt > best.Attempt) {
			best = &p.finished[step][index]
		}
	}
	if best == nil {
		return StepAttempt{}
	}
	return StepAttempt{Agent: best.Row.Agent, Result: best.Row.Result, Reason: best.Row.Reason, Output: best.Output}
}

// stepRunAt builds the stepRun of one attempt of step index from the unit's
// progress.
func stepRunAt(d Definition, progress unitProgress, index, attempt int, reason, role string) stepRun {
	name := d.Steps[index].Name
	return stepRun{multi: true, index: index, name: name, attempt: attempt, reason: reason, role: role,
		pass: recordsBefore(d, progress.pass, index), history: progress.history,
		previous: progress.previousAttempt(name, attempt), sendBacks: progress.sendBacks[name], tried: progress.tried[name]}
}

func recordsBefore(d Definition, records []StepRecord, index int) []StepRecord {
	var kept []StepRecord
	for _, record := range records {
		if position := d.StepIndex(record.Name); position >= 0 && position < index {
			kept = append(kept, record)
		}
	}
	return kept
}

// withRecord adds a finished step to a pass, replacing an earlier record of
// the same step, and keeps the pass in step order.
func withRecord(d Definition, records []StepRecord, record StepRecord) []StepRecord {
	var result []StepRecord
	for _, existing := range records {
		if existing.Name != record.Name {
			result = append(result, existing)
		}
	}
	result = append(result, record)
	sort.SliceStable(result, func(i, j int) bool { return d.StepIndex(result[i].Name) < d.StepIndex(result[j].Name) })
	return result
}

// aggregateUnitRow builds the times row of a unit of a workflow with steps
// from the step attempts of the unit attempt it closes (history, then the
// current attempt): the top-level fields describe that unit attempt and
// steps lists each of its step attempts, send-backs, reruns and fallbacks
// included. Steps a --retry-failed restart kept belong to the earlier
// row and are not in history, so no step attempt appears in two rows; the
// result and reason are the unit's.
func aggregateUnitRow(history []StepRecord, current StepRecord, result, reason string) TimeRow {
	records := append(append([]StepRecord(nil), history...), current)
	first, last := records[0].Row, current.Row
	row := TimeRow{Run: last.Run, Workflow: last.Workflow, Unit: last.Unit, Agent: last.Agent, Model: last.Model, Result: result, Reason: reason, FinishedAt: last.FinishedAt,
		QueuedAt: first.QueuedAt, DeliveredAt: first.DeliveredAt, StartedAt: first.StartedAt, IdleGapSeconds: first.IdleGapSeconds, CtxStart: first.CtxStart, CtxEnd: last.CtxEnd}
	for _, record := range records {
		part := record.Row
		row.Rounds += part.Rounds
		row.RoundSeconds = append(row.RoundSeconds, part.RoundSeconds...)
		row.WaitSeconds += part.WaitSeconds
		row.WorkSeconds += part.WorkSeconds
		row.ValidateSeconds += part.ValidateSeconds
		row.CompactSeconds += part.CompactSeconds
		if part.CompactResult != "" {
			row.CompactResult = part.CompactResult
		}
		if row.CtxBefore == nil {
			row.CtxBefore, row.CompactCtxBefore = part.CtxBefore, part.CompactCtxBefore
		}
		if row.CtxAfter == nil {
			row.CtxAfter, row.CompactCtxAfter = part.CtxAfter, part.CompactCtxAfter
		}
		row.Steps = append(row.Steps, stepRowOf(record))
	}
	row.Tokens = sumStepTokens(records)
	row.Activity = sumStepActivity(records)
	row.Approval = mergeStepApprovals(records)
	return row
}

// sumStepTokens adds the steps' tokens. It is nil when any step's usage was
// unknown. The sum has no interval of its own: readers settle each step.
func sumStepTokens(records []StepRecord) *UnitTokens {
	total := &UnitTokens{}
	for _, record := range records {
		tokens := record.Row.Tokens
		if tokens == nil {
			return nil
		}
		total.Input += tokens.Input
		total.Output += tokens.Output
		total.CacheWrite += tokens.CacheWrite
		total.CacheRead += tokens.CacheRead
		total.Weighted += tokens.Weighted
	}
	return total
}

func sumStepTokenRows(steps []StepRow) *UnitTokens {
	total := &UnitTokens{}
	for _, step := range steps {
		if step.Tokens == nil {
			return nil
		}
		total.Input += step.Tokens.Input
		total.Output += step.Tokens.Output
		total.CacheWrite += step.Tokens.CacheWrite
		total.CacheRead += step.Tokens.CacheRead
		total.Weighted += step.Tokens.Weighted
	}
	return total
}

func sumStepActivity(records []StepRecord) *RowActivity {
	var total *RowActivity
	worst := GuardOK
	for _, record := range records {
		activity := record.Row.Activity
		if activity == nil {
			continue
		}
		if total == nil {
			total = &RowActivity{}
		}
		total.ToolCalls += activity.ToolCalls
		total.LongestRepeat = max(total.LongestRepeat, activity.LongestRepeat)
		total.LongestFailures = max(total.LongestFailures, activity.LongestFailures)
		if level := parseGuardLevel(activity.Verdict); level > worst {
			worst, total.Verdict, total.Rule = level, activity.Verdict, activity.Rule
		}
	}
	return total
}

func mergeStepApprovals(records []StepRecord) *RowApproval {
	var merged *RowApproval
	for _, record := range records {
		approval := record.Row.Approval
		if approval == nil {
			continue
		}
		if merged == nil {
			merged = &RowApproval{}
		}
		merged.WaitSeconds += approval.WaitSeconds
		merged.Revisions += approval.Revisions
		if approval.Proposed != "" {
			merged.Proposed = approval.Proposed
		}
		if approval.Decision != "" {
			merged.Decision, merged.By, merged.Note, merged.DecidedAt = approval.Decision, approval.By, approval.Note, approval.DecidedAt
		}
	}
	return merged
}

// unitOutcome is outcome plus, in a workflow with steps, the record of the
// step that ends here. The event and row then describe the whole unit with
// the unit-level result (the step's own unless metrics.step.final is set).
func (e *Engine) unitOutcome(run Run, unit Unit, agent string, metrics unitMetrics, result, reason string, round int, delivery *Delivery, lastFinished time.Time) (Event, TimeRow, *StepRecord) {
	event, row := e.stepOutcome(run, unit, agent, metrics, result, reason, round, delivery, lastFinished)
	if !metrics.step.multi {
		return event, row, nil
	}
	event = metrics.stamp(event)
	record := &StepRecord{Name: metrics.step.name, Attempt: metrics.step.attempt, Output: metrics.step.output, QueuedSeconds: metrics.step.queued,
		Role: metrics.step.role, AttemptReason: metrics.step.reason, Row: row}
	unitResult, unitReason := result, reason
	if metrics.step.hasFinal {
		unitResult, unitReason = metrics.step.finalResult, metrics.step.finalReason
	}
	event.State, event.Reason = unitResult, unitReason
	event.SendBack = metrics.sendBack
	return event, aggregateUnitRow(metrics.step.history, *record, unitResult, unitReason), record
}

// judgedStep is judged for a workflow with steps. A failed step falls back
// to the step's next untried fallback role; with none left it ends the unit
// failed (or parks it when approve.on lists failed). A done or weak step
// hands the unit to its next step; after the last step the unit result is
// weak when any step was weak, else done, and parks when approve.on lists
// it.
func (e *Engine) judgedStep(ctx context.Context, run Run, d Definition, unit Unit, agent string, metrics unitMetrics, result, reason string, round int, completed *atomic.Int64, lastFinished *time.Time) error {
	if result == "failed" {
		if role := fallbackRole(d, metrics); role != "" {
			return e.stepFailed(ctx, run, d, unit, agent, metrics, reason, round, nil, lastFinished, role)
		}
		if d.Approves("failed") {
			return e.parkUnit(ctx, run, d, unit, agent, metrics, result, reason, round, lastFinished)
		}
		return e.finishUnit(run, unit, agent, metrics, result, reason, round, nil, completed, lastFinished)
	}
	if metrics.step.index < len(d.Steps)-1 {
		return e.stepDone(ctx, run, d, unit, agent, metrics, result, reason, round, lastFinished)
	}
	unitResult, unitReason := result, reason
	if result == "done" {
		for _, record := range metrics.step.pass {
			if record.Row.Result == "weak" {
				unitResult, unitReason = "weak", "step "+record.Name+" weak"
				if text := oneLine(record.Row.Reason); text != "" {
					unitReason += ": " + text
				}
			}
		}
	}
	metrics.setFinal(unitResult, unitReason)
	if d.Approves(unitResult) {
		return e.parkUnit(ctx, run, d, unit, agent, metrics, result, reason, round, lastFinished)
	}
	return e.finishUnit(run, unit, agent, metrics, result, reason, round, nil, completed, lastFinished)
}

// stepDone records a step that finished done or weak before the last step.
// The unit is not finished: it waits for an agent of the next step's role.
// For the agent the step was a finished work item and, for its breaker, a
// success.
func (e *Engine) stepDone(ctx context.Context, run Run, d Definition, unit Unit, agent string, metrics unitMetrics, result, reason string, round int, lastFinished *time.Time) error {
	event, _, record := e.unitOutcome(run, unit, agent, metrics, result, reason, round, nil, *lastFinished)
	event.State, event.Reason, event.StepRecord = StateStepDone, reason, record
	if err := e.Store.AppendEvent(run.ID, event); err != nil {
		return err
	}
	if err := e.countAgentUnit(run.ID, agent); err != nil {
		return err
	}
	*lastFinished = record.Row.FinishedAt
	if err := e.afterStep(ctx, run, d, unit.Key, *record); err != nil {
		return err
	}
	if next := metrics.step.index + 1; next < len(d.Steps) && d.Steps[next].Role() == run.RoleOf(agent) && e.steps != nil {
		e.steps.setHandoff(agent, unit.Key)
	}
	return e.setCurrent(run.ID, agent, "")
}

// sendBack records a step whose validator exited 4 with send-backs left:
// the attempt ends sent_back and the unit goes back to the step send_back.to
// names, whose redo gets problems as .Problems. For the agent the attempt
// was a finished work item and, for its breaker, a success.
func (e *Engine) sendBack(ctx context.Context, run Run, d Definition, unit Unit, agent string, metrics unitMetrics, problems string, round int, lastFinished *time.Time) error {
	spec := *d.Steps[metrics.step.index].SendBack
	reason := strings.TrimSpace(problems)
	event, _, record := e.unitOutcome(run, unit, agent, metrics, StateSentBack, reason, round, nil, *lastFinished)
	record.SentBackTo = spec.To
	event.State, event.StepRecord = StateSentBack, record
	event.Reason = "sent back to step " + spec.To
	if text := oneLine(reason); text != "" {
		event.Reason += ": " + text
	}
	event.SendBack = &SendBackRecord{To: spec.To, Count: metrics.step.sendBacks + 1, Max: spec.Limit(), Problems: problems}
	if err := e.Store.AppendEvent(run.ID, event); err != nil {
		return err
	}
	if err := e.countAgentUnit(run.ID, agent); err != nil {
		return err
	}
	*lastFinished = record.Row.FinishedAt
	if err := e.afterStep(ctx, run, d, unit.Key, *record); err != nil {
		return err
	}
	return e.setCurrent(run.ID, agent, "")
}

// fallbackRole is the role a failed attempt hands its step to: the first
// fallback role of the step not yet tried for it in this unit attempt. It is
// empty for a revision round and for failures that do not fall back (unit
// budget, send-back exhaustion, a template that does not render).
func fallbackRole(d Definition, metrics unitMetrics) string {
	if !metrics.step.multi || metrics.revision != nil || metrics.noFallback {
		return ""
	}
	tried := map[string]bool{metrics.step.role: true}
	for _, role := range metrics.step.tried {
		tried[role] = true
	}
	for _, role := range d.Steps[metrics.step.index].Fallback {
		if !tried[role] {
			return role
		}
	}
	return ""
}

// stepFailed records a failed attempt whose step falls back to role: the
// unit is not finished, the step starts again from scratch on an agent of
// that role. The failure counts for the agent's breaker; meltdown counts
// only units that end failed.
func (e *Engine) stepFailed(ctx context.Context, run Run, d Definition, unit Unit, agent string, metrics unitMetrics, reason string, round int, delivery *Delivery, lastFinished *time.Time, role string) error {
	event, _, record := e.unitOutcome(run, unit, agent, metrics, "failed", reason, round, delivery, *lastFinished)
	event.State, event.Reason, event.StepRecord, event.Fallback = StateStepFailed, reason, record, role
	if err := e.Store.AppendEvent(run.ID, event); err != nil {
		return err
	}
	if err := e.countAgentUnit(run.ID, agent); err != nil {
		return err
	}
	*lastFinished = record.Row.FinishedAt
	if err := e.afterStep(ctx, run, d, unit.Key, *record); err != nil {
		return err
	}
	return e.setCurrent(run.ID, agent, "")
}

// afterStep feeds a step attempt that did not end the unit to the spend
// accounting and the agent's breaker: a success unless it failed (a
// fallback), which counts for the breaker but not for meltdown.
func (e *Engine) afterStep(ctx context.Context, run Run, d Definition, key string, record StepRecord) error {
	if e.sup == nil {
		return nil
	}
	row := record.Row
	e.sup.setInflight(row.Agent, time.Time{})
	e.sup.addSpent(TimeRow{Run: run.ID, Unit: key, Agent: row.Agent, FinishedAt: row.FinishedAt, Steps: []StepRow{stepRowOf(record)}}, false)
	if e.sup.breaker == nil {
		return nil
	}
	outcome := e.sup.recordOutcome(row.Agent, key, row.Result, row.Reason, row.FinishedAt, true, false)
	if outcome.opened {
		return e.announceOpen(ctx, run, d, row.Agent, outcome.consecutive, outcome.until, key, row.Reason)
	}
	if outcome.closed {
		return e.Store.AppendEvent(run.ID, Event{At: e.now(), Agent: row.Agent, State: eventBreakerClosed, Reason: "trial unit " + key + " finished step " + record.Name + " " + row.Result})
	}
	return nil
}

// stepFor resolves the step a work item runs, its attempt and reason, and
// the unit's earlier steps and attempts.
func (e *Engine) stepFor(run Run, d Definition, task unitTask, fresh bool, agent string) (stepRun, error) {
	if !d.MultiStep() {
		return stepRun{}, nil
	}
	events, _, err := e.Store.Events(run.ID)
	if err != nil {
		return stepRun{}, err
	}
	progress := progressOf(d, events, task.unit.Key)
	index, attempt, reason := task.step, 0, task.reason
	if fresh {
		attempt = progress.attempts[d.Steps[index].Name] + 1
		if reason == "" {
			switch {
			case task.hasPrevious && terminalState(task.previous.State):
				reason = AttemptRetry
			case attempt == 1:
				reason = AttemptFirst
			default:
				reason = AttemptRerun
			}
		}
	} else {
		index = d.StepIndex(task.previous.Step)
		if index < 0 {
			return stepRun{}, fmt.Errorf("unit %q is at unknown step %q", task.unit.Key, task.previous.Step)
		}
		attempt, reason = max(1, task.previous.Attempt), task.previous.AttemptReason
	}
	step := stepRunAt(d, progress, index, attempt, reason, run.RoleOf(agent))
	step.queued = task.queued
	return step, nil
}

// stepScheduler hands out the work items of a workflow with steps: new
// units (their first step), continuations (a unit whose step finished and
// whose next step waits for an agent of its role), send-back redos, fallback
// attempts and --retry-failed restarts at a later step. Everything but new
// units and restarts is read from units.jsonl, so a decision made while the
// run works (an approval that continues a unit) is seen.
type stepScheduler struct {
	mu      sync.Mutex
	pending []unitTask
	next    int
	// seeds are failed units that --retry-failed restarts at a later step.
	seeds map[string]stepSeed
	// claimed holds the units a worker is processing.
	claimed map[string]bool
	// handoff is the unit an agent continues at once with its next step
	// (sticky continuation).
	handoff map[string]string
	// busy marks the agents processing a work item.
	busy map[string]bool
}

type stepSeed struct {
	step    int
	readyAt time.Time
}

func (s *stepScheduler) setHandoff(agent, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handoff[agent] = key
}

func (s *stepScheduler) handoffOf(agent string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handoff[agent]
}

func (s *stepScheduler) dropHandoff(agent string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.handoff, agent)
}

func (s *stepScheduler) claim(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed[key] {
		return false
	}
	s.claimed[key] = true
	return true
}

func (s *stepScheduler) release(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.claimed, key)
}

func (s *stepScheduler) setBusy(agent string, busy bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if busy {
		s.busy[agent] = true
	} else {
		delete(s.busy, agent)
	}
}

// stepCandidate is a ready work item of a unit already in the run.
type stepCandidate struct {
	key     string
	step    int
	readyAt time.Time
	seed    bool
	latest  Event
	// role is the role whose agents may take the item; pinned, when set,
	// is the only agent that may (a send-back redo).
	role   string
	pinned string
	// prefer is the agent that ran the step last for the unit: a rerun
	// waits for it while it is free.
	prefer string
	// reason is the attempt reason (empty: stepFor decides); problems is
	// the send-back's text for .Problems; followup sends the attempt's
	// first prompt through the follow-up template.
	reason   string
	problems string
	followup bool
	rank     int
}

// lastStepAgents maps unit key and step name to the agent of the step's
// latest ended attempt for that unit.
func lastStepAgents(events []Event) map[string]map[string]string {
	agents := map[string]map[string]string{}
	for _, event := range events {
		if event.Step == "" || event.Agent == "" || event.Decision != nil {
			continue
		}
		if !betweenSteps(event.State) && !terminalState(event.State) && !parkedState(event.State) {
			continue
		}
		key := event.Key
		if key == "" {
			key = event.Unit
		}
		if agents[key] == nil {
			agents[key] = map[string]string{}
		}
		agents[key][event.Step] = event.Agent
	}
	return agents
}

// readyItems lists the ready work items of units already in the run:
// continuations, send-back redos, fallback attempts and restarts.
func (e *Engine) readyItems(run Run, d Definition, units map[string]Unit, events []Event) []stepCandidate {
	latest := latestEvents(events)
	last := lastStepAgents(events)
	s := e.steps
	s.mu.Lock()
	defer s.mu.Unlock()
	// A unit an agent handed to itself (sticky continuation) is not
	// offered to the others: between the release of its finished step and
	// the agent's next loop it would otherwise be free to claim.
	handed := map[string]bool{}
	for _, key := range s.handoff {
		handed[key] = true
	}
	var result []stepCandidate
	for key := range units {
		if s.claimed[key] || handed[key] {
			continue
		}
		event, ok := latest[key]
		if seed, seeded := s.seeds[key]; seeded {
			result = append(result, stepCandidate{key: key, step: seed.step, readyAt: seed.readyAt, seed: true, latest: event, role: d.Steps[seed.step].Role(), reason: AttemptRetry})
			continue
		}
		if !ok {
			continue
		}
		candidate := stepCandidate{key: key, readyAt: event.At, latest: event}
		switch event.State {
		case StateStepDone:
			next := d.StepIndex(event.Step) + 1
			if next <= 0 || next >= len(d.Steps) {
				continue
			}
			candidate.step, candidate.role = next, d.Steps[next].Role()
			if agent := last[key][d.Steps[next].Name]; agent != "" && contains(run.Agents, agent) && run.RoleOf(agent) == candidate.role {
				candidate.prefer = agent
			}
		case StateSentBack:
			if event.SendBack == nil {
				continue
			}
			target := d.StepIndex(event.SendBack.To)
			if target < 0 {
				continue
			}
			candidate.step, candidate.role, candidate.reason, candidate.problems = target, d.Steps[target].Role(), AttemptSendBack, event.SendBack.Problems
			if agent := last[key][event.SendBack.To]; agent != "" && contains(run.Agents, agent) {
				// The redo goes to the agent that ran the step last, through
				// the follow-up template; an agent no longer in the run is
				// replaced by any agent of the step's role and the main
				// template.
				candidate.pinned, candidate.followup = agent, true
			}
		case StateStepFailed:
			index := d.StepIndex(event.Step)
			if index < 0 || event.Fallback == "" {
				continue
			}
			candidate.step, candidate.role, candidate.reason = index, event.Fallback, AttemptFallback
		default:
			continue
		}
		result = append(result, candidate)
	}
	return result
}

// readyFor lists the ready items agent may take, in the order it takes
// them: items pinned to it, reruns of steps it ran last, then the others
// oldest first. A rerun preferring another agent is left to that agent
// while it is free and its breaker allows work.
func (e *Engine) readyFor(run Run, d Definition, agent string, units map[string]Unit, events []Event) []stepCandidate {
	role := run.RoleOf(agent)
	now := e.now()
	var result []stepCandidate
	for _, candidate := range e.readyItems(run, d, units, events) {
		switch {
		case candidate.pinned != "":
			if candidate.pinned != agent {
				continue
			}
			candidate.rank = 0
		case candidate.role != role:
			continue
		case candidate.prefer == agent:
			candidate.rank = 1
		case candidate.prefer != "" && e.preferredFree(candidate.prefer, now):
			continue
		default:
			candidate.rank = 2
		}
		result = append(result, candidate)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].rank != result[j].rank {
			return result[i].rank < result[j].rank
		}
		if !result[i].readyAt.Equal(result[j].readyAt) {
			return result[i].readyAt.Before(result[j].readyAt)
		}
		return result[i].key < result[j].key
	})
	return result
}

// preferredFree reports whether agent is idle in this run and its breaker
// lets it take work now.
func (e *Engine) preferredFree(agent string, now time.Time) bool {
	s := e.steps
	s.mu.Lock()
	busy := s.busy[agent]
	s.mu.Unlock()
	if busy {
		return false
	}
	return e.sup == nil || !e.sup.blocked(agent, now)
}

// newUnitsFor reports whether a new unit waits for an agent of role.
func (e *Engine) newUnitsFor(d Definition, role string) bool {
	s := e.steps
	s.mu.Lock()
	defer s.mu.Unlock()
	return d.Steps[0].Role() == role && s.next < len(s.pending)
}

// sendBackFloor is the lowest step a unit at step from (or later) can still
// reach: send-backs of reachable steps lower it.
func sendBackFloor(d Definition, from int) int {
	for changed := true; changed; {
		changed = false
		for index := max(0, from); index < len(d.Steps); index++ {
			if spec := d.Steps[index].SendBack; spec != nil {
				if target := d.StepIndex(spec.To); target >= 0 && target < from {
					from, changed = target, true
				}
			}
		}
	}
	return from
}

// liveFor reports whether agent may still get work: some unit that is not
// finished or parked can still reach a step it serves (its role is the
// step's role or one of the step's fallback roles), counting send-backs.
// A unit whose current step is claimed or running counts from the step
// after it, plus that step's own fallbacks and send-back.
func (e *Engine) liveFor(run Run, d Definition, agent string, units map[string]Unit, latest map[string]Event) bool {
	role := run.RoleOf(agent)
	s := e.steps
	s.mu.Lock()
	defer s.mu.Unlock()
	reaches := func(from int) bool {
		for index := sendBackFloor(d, from); index < len(d.Steps); index++ {
			if servesStep(d.Steps[index], role) {
				return true
			}
		}
		return false
	}
	// after reports whether a unit whose step index is running or just
	// ended can reach a step the agent serves.
	after := func(index int) bool {
		if index < 0 || index >= len(d.Steps) {
			return false
		}
		from := index + 1
		if spec := d.Steps[index].SendBack; spec != nil {
			if target := d.StepIndex(spec.To); target >= 0 {
				from = min(from, target)
			}
		}
		return reaches(from) || (servesStep(d.Steps[index], role) && d.Steps[index].Role() != role)
	}
	if s.next < len(s.pending) && reaches(0) {
		return true
	}
	for key := range units {
		event, ok := latest[key]
		seed, seeded := s.seeds[key]
		switch {
		case seeded:
			if reaches(seed.step) {
				return true
			}
		case !ok:
			if s.claimed[key] && reaches(0) {
				return true
			}
		case s.claimed[key] && terminalState(event.State):
			// A --retry-failed restart claimed by a worker that has not
			// logged its first event yet: it restarts at the failed step.
			if reaches(max(0, d.StepIndex(event.Step))) {
				return true
			}
		case terminalState(event.State) || parkedState(event.State):
		case revisionState(event.State):
			if event.Park != nil && contains(run.Agents, event.Agent) && after(d.StepIndex(event.Step)) {
				return true
			}
		case event.State == StateSentBack:
			if event.SendBack != nil && reaches(d.StepIndex(event.SendBack.To)) {
				return true
			}
		case event.State == StateStepFailed:
			if index := d.StepIndex(event.Step); event.Fallback == role || after(index) {
				return true
			}
		default:
			// Between steps (step_done) or a step in flight.
			if after(d.StepIndex(event.Step)) {
				return true
			}
		}
	}
	return false
}

// claimWork claims the first ready item for agent (see readyFor), else a
// new unit when its role runs the first step. The claim is checked against
// a fresh read of the log, so a unit another worker advanced meanwhile is
// not run twice.
func (e *Engine) claimWork(run Run, d Definition, agent string, units map[string]Unit) (unitTask, bool, error) {
	events, _, err := e.Store.Events(run.ID)
	if err != nil {
		return unitTask{}, false, err
	}
	for _, candidate := range e.readyFor(run, d, agent, units, events) {
		task, ok, err := e.claimCandidate(run, d, candidate, units)
		if err != nil || ok {
			return task, ok, err
		}
	}
	s := e.steps
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.Steps[0].Role() != run.RoleOf(agent) || s.next >= len(s.pending) {
		return unitTask{}, false, nil
	}
	task := s.pending[s.next]
	s.next++
	s.claimed[task.unit.Key] = true
	return task, true, nil
}

func (e *Engine) claimCandidate(run Run, d Definition, candidate stepCandidate, units map[string]Unit) (unitTask, bool, error) {
	s := e.steps
	if !s.claim(candidate.key) {
		return unitTask{}, false, nil
	}
	events, _, err := e.Store.Events(run.ID)
	if err != nil {
		s.release(candidate.key)
		return unitTask{}, false, err
	}
	current, ok := latestEvents(events)[candidate.key]
	if ok != (candidate.latest.Key != "") || current.State != candidate.latest.State || current.Step != candidate.latest.Step || !current.At.Equal(candidate.latest.At) {
		s.release(candidate.key)
		return unitTask{}, false, nil
	}
	if candidate.seed {
		s.mu.Lock()
		delete(s.seeds, candidate.key)
		s.mu.Unlock()
	}
	queued := 0.0
	if !candidate.seed {
		queued = max(0, e.now().Sub(candidate.readyAt).Seconds())
	}
	return unitTask{unit: units[candidate.key], previous: current, hasPrevious: ok, step: candidate.step, queued: queued,
		reason: candidate.reason, problems: candidate.problems, followup: candidate.followup, pinned: candidate.pinned != ""}, true, nil
}

// stickyTask claims the continuation an agent handed to itself. It declines
// (the unit stays ready for any agent of the role) on a stop or pause
// request or when the agent's breaker does not allow work.
func (e *Engine) stickyTask(run Run, d Definition, agent, key string, units map[string]Unit) (unitTask, bool, error) {
	if stop, err := e.stopRequested(run.ID); err != nil || stop {
		return unitTask{}, false, err
	}
	if e.sup != nil && e.sup.breaker != nil {
		if allowed, halfOpen, _, _ := e.sup.gate(agent, e.now()); !allowed || halfOpen {
			return unitTask{}, false, nil
		}
	}
	events, _, err := e.Store.Events(run.ID)
	if err != nil {
		return unitTask{}, false, err
	}
	event, ok := latestEvents(events)[key]
	if !ok || event.State != StateStepDone {
		return unitTask{}, false, nil
	}
	next := d.StepIndex(event.Step) + 1
	if next <= 0 || next >= len(d.Steps) || d.Steps[next].Role() != run.RoleOf(agent) {
		return unitTask{}, false, nil
	}
	task, claimed, err := e.claimCandidate(run, d, stepCandidate{key: key, step: next, readyAt: event.At, latest: event}, units)
	task.sticky = claimed
	return task, claimed, err
}

// runStepWorkers runs one worker per agent for a workflow with steps. A
// worker takes, in order: its handed-off continuation, a revision for its
// agent, the ready items readyFor lists for it, a new unit when its role
// runs the first step. With nothing to take it polls while some unit can
// still reach a step it serves, and returns when none can, or on a stop or
// pause request.
func (e *Engine) runStepWorkers(ctx context.Context, run Run, d Definition, pending []unitTask, recovery map[string][]unitTask, seeds map[string]stepSeed, units map[string]Unit, completed *atomic.Int64) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sched := &stepScheduler{pending: pending, seeds: seeds, claimed: map[string]bool{}, handoff: map[string]string{}, busy: map[string]bool{}}
	for _, tasks := range recovery {
		for _, task := range tasks {
			sched.claimed[task.unit.Key] = true
		}
	}
	for agent := range recovery {
		sched.busy[agent] = true
	}
	e.steps = sched
	defer func() { e.steps = nil }()
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	for _, agent := range run.Agents {
		agent := agent
		role := run.RoleOf(agent)
		wg.Add(1)
		go func() {
			defer wg.Done()
			fail := func(err error) { setFirstErr(&mu, &firstErr, cancel, err) }
			process := func(task unitTask, lastFinished *time.Time) error {
				sched.setBusy(agent, true)
				defer sched.setBusy(agent, false)
				defer sched.release(task.unit.Key)
				return e.processTask(ctx, run, d, agent, task, lastFinished, completed)
			}
			lastFinished := time.Time{}
			for _, task := range recovery[agent] {
				if err := process(task, &lastFinished); err != nil {
					fail(err)
					return
				}
			}
			sched.setBusy(agent, false)
			for {
				mu.Lock()
				stopped := firstErr != nil || ctx.Err() != nil
				mu.Unlock()
				if stopped {
					return
				}
				if key := sched.handoffOf(agent); key != "" {
					// The handoff stays in place until the sticky claim is
					// decided, so no other agent claims the unit meanwhile.
					task, ok, err := e.stickyTask(run, d, agent, key, units)
					sched.dropHandoff(agent)
					if err != nil {
						fail(err)
						return
					}
					if ok {
						admitted, err := e.admitRunBudget(ctx, run, d)
						if err != nil || !admitted {
							sched.release(key)
							if err != nil {
								fail(err)
							}
							return
						}
						if err := process(task, &lastFinished); err != nil {
							fail(err)
							return
						}
						continue
					}
				}
				revisionsWaiting := false
				if d.Approve != nil {
					waiting, err := e.revisionsFor(run.ID, agent, units)
					if err != nil {
						fail(err)
						return
					}
					revisionsWaiting = len(waiting) > 0
				}
				events, _, err := e.Store.Events(run.ID)
				if err != nil {
					fail(err)
					return
				}
				ready := len(e.readyFor(run, d, agent, units, events)) > 0 || e.newUnitsFor(d, role)
				if !ready && !revisionsWaiting {
					if !e.liveFor(run, d, agent, units, latestEvents(events)) {
						return
					}
					if stop, err := e.stopRequested(run.ID); err != nil {
						fail(err)
						return
					} else if stop {
						return
					}
					if err := e.sleep(ctx, e.interval()); err != nil {
						fail(err)
						return
					}
					continue
				}
				if stop, err := e.stopRequested(run.ID); err != nil {
					fail(err)
					return
				} else if stop {
					return
				}
				if wait, err := e.breakerWait(ctx, run, agent); err != nil {
					fail(err)
					return
				} else if wait {
					continue
				}
				if revisionsWaiting {
					sched.setBusy(agent, true)
					_, err := e.nextRevision(ctx, run, d, agent, units, &lastFinished, completed)
					sched.setBusy(agent, false)
					if err != nil {
						fail(err)
						return
					}
					continue
				}
				if admitted, err := e.admitRunBudget(ctx, run, d); err != nil {
					fail(err)
					return
				} else if !admitted {
					return
				}
				task, ok, err := e.claimWork(run, d, agent, units)
				if err != nil {
					fail(err)
					return
				}
				if !ok {
					continue
				}
				if err := process(task, &lastFinished); err != nil {
					fail(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	return firstErr
}
