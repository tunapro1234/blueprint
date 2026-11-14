package workflow

import (
	"context"
	"fmt"
	"sort"
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
	Row           TimeRow `json:"row"`
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
}

func stepRowOf(record StepRecord) StepRow {
	row := record.Row
	return StepRow{Name: record.Name, Attempt: record.Attempt, Agent: row.Agent, Model: row.Model, Result: row.Result, Reason: row.Reason, Rounds: row.Rounds, Output: record.Output,
		WorkSeconds: row.WorkSeconds, ValidateSeconds: row.ValidateSeconds, WaitSeconds: row.WaitSeconds, QueuedSeconds: record.QueuedSeconds,
		StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, Tokens: row.Tokens, Activity: row.Activity}
}

// stepRun is the step a unit's current work item runs. In a definition
// without steps multi is false and every other field is zero: no event or
// row gains a step field.
type stepRun struct {
	multi   bool
	index   int
	name    string
	attempt int
	// pass holds the steps this unit already finished in its current pass,
	// in step order.
	pass []StepRecord
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

// stepData is what templates see of the pipeline: the current step's name and
// the steps finished before it.
type stepData struct {
	name    string
	results map[string]StepResult
}

func (s stepRun) data() stepData {
	results := map[string]StepResult{}
	for _, record := range s.pass {
		results[record.Name] = StepResult{Output: record.Output, Result: record.Row.Result, Agent: record.Row.Agent, Rounds: record.Row.Rounds, Reason: record.Row.Reason}
	}
	return stepData{name: s.name, results: results}
}

func (m unitMetrics) stepData() stepData { return m.step.data() }

// stamp adds the step and attempt to a unit event of a workflow with steps.
func (m unitMetrics) stamp(event Event) Event {
	if m.step.multi {
		event.Step, event.Attempt = m.step.name, m.step.attempt
	}
	return event
}

func (m *unitMetrics) setFinal(result, reason string) {
	m.step.hasFinal, m.step.finalResult, m.step.finalReason = true, result, reason
}

// earlierWeighted is the budget measure of the steps the unit finished
// before the current one in this pass; unknown steps count zero. Steps kept
// from an earlier pass were spent by that pass and do not count, as a
// retried unit without steps starts with a fresh unit budget.
func (s stepRun) earlierWeighted() int64 {
	var total int64
	for _, record := range s.pass {
		if !record.kept && record.Row.Tokens != nil {
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
	// pass is the finished steps of the unit's current pass, in step order.
	// After a terminal event it is the pass that ended there, which a
	// --retry-failed restart at a later step continues. Steps finished
	// before a terminal event are marked kept: that event's row holds them.
	pass []StepRecord
	// attempts is the highest attempt seen per step.
	attempts map[string]int
}

func progressOf(d Definition, events []Event, key string) unitProgress {
	progress := unitProgress{attempts: map[string]int{}}
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
		if terminalState(event.State) {
			// The terminal event's row holds this pass's steps.
			for index := range progress.pass {
				progress.pass[index].kept = true
			}
			terminated = true
			continue
		}
		if event.Step != "" && terminated {
			// A new pass after a terminal event keeps the steps before the
			// step it restarts at.
			progress.pass = recordsBefore(d, progress.pass, d.StepIndex(event.Step))
			terminated = false
		}
		if event.State == StateStepDone && event.StepRecord != nil {
			progress.pass = withRecord(d, progress.pass, *event.StepRecord)
		}
	}
	return progress
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
// from the steps of the pass it closes: the top-level fields describe that
// pass and steps lists each of its step attempts. Steps a --retry-failed
// restart kept belong to the earlier pass's row and are left out, so no step
// attempt appears in two rows; the result and reason are the unit's.
func aggregateUnitRow(pass []StepRecord, current StepRecord, result, reason string) TimeRow {
	var records []StepRecord
	for _, record := range pass {
		if !record.kept {
			records = append(records, record)
		}
	}
	records = append(records, current)
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
	record := &StepRecord{Name: metrics.step.name, Attempt: metrics.step.attempt, Output: metrics.step.output, QueuedSeconds: metrics.step.queued, Row: row}
	unitResult, unitReason := result, reason
	if metrics.step.hasFinal {
		unitResult, unitReason = metrics.step.finalResult, metrics.step.finalReason
	}
	event.State, event.Reason = unitResult, unitReason
	return event, aggregateUnitRow(metrics.step.pass, *record, unitResult, unitReason), record
}

// judgedStep is judged for a workflow with steps. A failed step ends the
// unit failed (or parks it when approve.on lists failed). A done or weak
// step hands the unit to its next step; after the last step the unit result
// is weak when any step was weak, else done, and parks when approve.on lists
// it.
func (e *Engine) judgedStep(ctx context.Context, run Run, d Definition, unit Unit, agent string, metrics unitMetrics, result, reason string, round int, completed *atomic.Int64, lastFinished *time.Time) error {
	if result == "failed" {
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

// afterStep feeds a finished, non-final step to the spend accounting and the
// agent's breaker (a success).
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
	outcome := e.sup.recordFinish(row.Agent, key, row.Result, row.Reason, row.FinishedAt)
	if outcome.closed {
		return e.Store.AppendEvent(run.ID, Event{At: e.now(), Agent: row.Agent, State: eventBreakerClosed, Reason: "trial unit " + key + " finished step " + record.Name + " " + row.Result})
	}
	return nil
}

// stepFor resolves the step a work item runs, its attempt and the unit's
// earlier steps in this pass.
func (e *Engine) stepFor(run Run, d Definition, task unitTask, fresh bool) (stepRun, error) {
	if !d.MultiStep() {
		return stepRun{}, nil
	}
	events, _, err := e.Store.Events(run.ID)
	if err != nil {
		return stepRun{}, err
	}
	progress := progressOf(d, events, task.unit.Key)
	index, attempt := task.step, 0
	if fresh {
		attempt = progress.attempts[d.Steps[index].Name] + 1
	} else {
		index = d.StepIndex(task.previous.Step)
		if index < 0 {
			return stepRun{}, fmt.Errorf("unit %q is at unknown step %q", task.unit.Key, task.previous.Step)
		}
		attempt = max(1, task.previous.Attempt)
	}
	return stepRun{multi: true, index: index, name: d.Steps[index].Name, attempt: attempt, pass: recordsBefore(d, progress.pass, index), queued: task.queued}, nil
}

// stepScheduler hands out the work items of a workflow with steps: new
// units (their first step), continuations (a unit whose step finished and
// whose next step waits for an agent of its role) and --retry-failed restarts
// at a later step. Continuations are read from units.jsonl, so a decision
// made while the run works (an approval that continues a unit) is seen.
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

func (s *stepScheduler) takeHandoff(agent string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := s.handoff[agent]
	delete(s.handoff, agent)
	return key
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

// stepCandidate is a ready continuation or restart.
type stepCandidate struct {
	key     string
	step    int
	readyAt time.Time
	seed    bool
	latest  Event
}

// readyFor lists the continuations and restarts an agent of role may take,
// oldest first.
func (e *Engine) readyFor(run Run, d Definition, role string, units map[string]Unit, latest map[string]Event) []stepCandidate {
	s := e.steps
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []stepCandidate
	for key := range units {
		if s.claimed[key] {
			continue
		}
		event, ok := latest[key]
		if seed, seeded := s.seeds[key]; seeded {
			if d.Steps[seed.step].Role() == role {
				result = append(result, stepCandidate{key: key, step: seed.step, readyAt: seed.readyAt, seed: true, latest: event})
			}
			continue
		}
		if !ok || event.State != StateStepDone {
			continue
		}
		next := d.StepIndex(event.Step) + 1
		if next <= 0 || next >= len(d.Steps) || d.Steps[next].Role() != role {
			continue
		}
		result = append(result, stepCandidate{key: key, step: next, readyAt: event.At, latest: event})
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].readyAt.Equal(result[j].readyAt) {
			return result[i].readyAt.Before(result[j].readyAt)
		}
		return result[i].key < result[j].key
	})
	return result
}

// newUnitsFor reports whether a new unit waits for an agent of role.
func (e *Engine) newUnitsFor(d Definition, role string) bool {
	s := e.steps
	s.mu.Lock()
	defer s.mu.Unlock()
	return d.Steps[0].Role() == role && s.next < len(s.pending)
}

// liveFor reports whether an agent of role may still get work: some unit
// that is not finished or parked can still reach a step of that role. Units
// whose current step is claimed or running count from the step after it.
func (e *Engine) liveFor(run Run, d Definition, role string, units map[string]Unit, latest map[string]Event) bool {
	s := e.steps
	s.mu.Lock()
	defer s.mu.Unlock()
	reaches := func(from int) bool {
		for index := max(0, from); index < len(d.Steps); index++ {
			if d.Steps[index].Role() == role {
				return true
			}
		}
		return false
	}
	if s.next < len(s.pending) && reaches(0) {
		return true
	}
	for key := range units {
		event, ok := latest[key]
		seed, seeded := s.seeds[key]
		from := -1
		switch {
		case seeded:
			from = seed.step
		case !ok:
			if s.claimed[key] {
				from = 0
			}
		case terminalState(event.State) || parkedState(event.State):
		case revisionState(event.State):
			if event.Park != nil && contains(run.Agents, event.Agent) {
				from = d.StepIndex(event.Step) + 1
			}
		default:
			// Between steps (step_done) or a step in flight.
			from = d.StepIndex(event.Step) + 1
		}
		if from >= 0 && reaches(from) {
			return true
		}
	}
	return false
}

// claimWork claims the oldest ready continuation or restart for role, else a
// new unit when role runs the first step. The claim is checked against a
// fresh read of the log, so a unit another worker advanced meanwhile is not
// run twice.
func (e *Engine) claimWork(run Run, d Definition, role string, units map[string]Unit) (unitTask, bool, error) {
	events, _, err := e.Store.Events(run.ID)
	if err != nil {
		return unitTask{}, false, err
	}
	for _, candidate := range e.readyFor(run, d, role, units, latestEvents(events)) {
		task, ok, err := e.claimCandidate(run, d, candidate, units)
		if err != nil || ok {
			return task, ok, err
		}
	}
	s := e.steps
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.Steps[0].Role() != role || s.next >= len(s.pending) {
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
	return unitTask{unit: units[candidate.key], previous: current, hasPrevious: ok, step: candidate.step, queued: queued}, true, nil
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
// agent, the oldest ready continuation for its role, a new unit when its
// role runs the first step. With nothing to take it polls while some unit can
// still reach a step of its role, and returns when none can, or on a stop or
// pause request.
func (e *Engine) runStepWorkers(ctx context.Context, run Run, d Definition, pending []unitTask, recovery map[string][]unitTask, seeds map[string]stepSeed, units map[string]Unit, completed *atomic.Int64) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sched := &stepScheduler{pending: pending, seeds: seeds, claimed: map[string]bool{}, handoff: map[string]string{}}
	for _, tasks := range recovery {
		for _, task := range tasks {
			sched.claimed[task.unit.Key] = true
		}
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
			lastFinished := time.Time{}
			for _, task := range recovery[agent] {
				err := e.processTask(ctx, run, d, agent, task, &lastFinished, completed)
				sched.release(task.unit.Key)
				if err != nil {
					fail(err)
					return
				}
			}
			for {
				mu.Lock()
				stopped := firstErr != nil || ctx.Err() != nil
				mu.Unlock()
				if stopped {
					return
				}
				if key := sched.takeHandoff(agent); key != "" {
					task, ok, err := e.stickyTask(run, d, agent, key, units)
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
						err = e.processTask(ctx, run, d, agent, task, &lastFinished, completed)
						sched.release(key)
						if err != nil {
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
				latest := latestEvents(events)
				ready := len(e.readyFor(run, d, role, units, latest)) > 0 || e.newUnitsFor(d, role)
				if !ready && !revisionsWaiting {
					if !e.liveFor(run, d, role, units, latest) {
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
					if _, err := e.nextRevision(ctx, run, d, agent, units, &lastFinished, completed); err != nil {
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
				task, ok, err := e.claimWork(run, d, role, units)
				if err != nil {
					fail(err)
					return
				}
				if !ok {
					continue
				}
				err = e.processTask(ctx, run, d, agent, task, &lastFinished, completed)
				sched.release(task.unit.Key)
				if err != nil {
					fail(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	return firstErr
}
