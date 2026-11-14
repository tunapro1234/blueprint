package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// meterTimeout bounds one token-meter read so a slow source cannot hold a unit.
const meterTimeout = 10 * time.Second

func (e *Engine) notifyOwner(ctx context.Context, run Run, d Definition, message string) {
	if e.Notify == nil || (d.Notify != "" && d.Notify != "owner") {
		return
	}
	_ = e.Notify(ctx, run.Owner, message)
}

func (e *Engine) markInflight(agent string, started time.Time) {
	if e.sup != nil {
		e.sup.setInflight(agent, started)
	}
}

// announcePendingOpenings records and notifies a breaker opening that the log
// shows but no breaker_open event confirms, e.g. after a worker crash.
func (e *Engine) announcePendingOpenings(ctx context.Context, run Run, d Definition) error {
	if e.sup == nil || e.sup.breaker == nil {
		return nil
	}
	for agent, b := range e.sup.pendingOpenings() {
		if err := e.announceOpen(ctx, run, d, agent, b.consecutive, b.lastFailure.Add(e.sup.breaker.openFor), b.lastUnit, b.lastReason); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) announceOpen(ctx context.Context, run Run, d Definition, agent string, consecutive int, until time.Time, unit, reason string) error {
	text := fmt.Sprintf("%d consecutive failed units; no new unit until %s; last: %s: %s", consecutive, until.UTC().Format(time.RFC3339), unit, oneLine(reason))
	if err := e.Store.AppendEvent(run.ID, Event{At: e.now(), Agent: agent, State: eventBreakerOpen, Reason: text, Until: timePointer(until.UTC())}); err != nil {
		return err
	}
	e.notifyOwner(ctx, run, d, fmt.Sprintf("workflow %s run %s: breaker open for %s after %s", run.Workflow, run.ID, agent, text))
	return nil
}

// breakerWait holds a worker whose agent's breaker is open. It returns true
// after one poll so the caller re-checks the queue and stop requests.
func (e *Engine) breakerWait(ctx context.Context, run Run, agent string) (bool, error) {
	if e.sup == nil || e.sup.breaker == nil {
		return false, nil
	}
	now := e.now()
	allowed, halfOpen, announce, until := e.sup.gate(agent, now)
	if allowed {
		if halfOpen && announce {
			if err := e.Store.AppendEvent(run.ID, Event{At: now, Agent: agent, State: eventBreakerHalfOpen, Reason: "open_for elapsed; one trial unit", Until: timePointer(until.UTC())}); err != nil {
				return false, err
			}
		}
		return false, e.clearBreakerWaiting(run, agent)
	}
	agents, label := run.Agents, ""
	if e.steps != nil {
		// With steps, a role whose agents are all open has no live path:
		// the status names the role.
		role := run.RoleOf(agent)
		agents, label = agentsOfRole(run, role), " for role "+role
	}
	if all, next, at := e.sup.allOpen(agents, now); all {
		status := fmt.Sprintf("%s%s; next trial %s at %s", statusBreakersWaiting, label, next, at.UTC().Format(time.RFC3339))
		e.mu.Lock()
		current := ""
		if e.active != nil {
			current = e.active.Status
		}
		e.mu.Unlock()
		if current != status {
			if err := e.setRunStatus(run.ID, status, ""); err != nil {
				return false, err
			}
		}
	}
	delay := e.interval()
	if remaining := until.Sub(now); remaining > 0 && remaining < delay {
		delay = remaining
	}
	if err := e.sleep(ctx, delay); err != nil {
		return false, err
	}
	return true, nil
}

// agentsOfRole lists the run's agents of one role.
func agentsOfRole(run Run, role string) []string {
	var agents []string
	for _, agent := range run.Agents {
		if run.RoleOf(agent) == role {
			agents = append(agents, agent)
		}
	}
	return agents
}

// clearBreakerWaiting clears the all-breakers-open status once agent may
// work. With steps only the status naming agent's role is cleared.
func (e *Engine) clearBreakerWaiting(active Run, agent string) error {
	runID := active.ID
	prefix := statusBreakersWaiting
	if e.steps != nil {
		prefix += " for role " + active.RoleOf(agent) + ";"
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active != nil && e.active.ID == runID && !strings.HasPrefix(e.active.Status, prefix) {
		return nil
	}
	run, err := e.Store.UpdateRun(runID, func(current *Run) error {
		if strings.HasPrefix(current.Status, prefix) {
			current.Status, current.LastError = "running", ""
		}
		return nil
	})
	if err != nil {
		return err
	}
	if e.active != nil && e.active.ID == runID {
		e.active = &run
	}
	return nil
}

// meter reads the agent's token use since a time. Unknown or failed reads
// return false; with a budget configured the first one per agent is recorded.
// measureTokens asks the driver's meter for the agent's usage in
// [since, until); a zero until means now. reason explains an unknown answer.
func (e *Engine) measureTokens(agent string, since, until time.Time) (TokenUse, bool, string) {
	if since.IsZero() {
		return TokenUse{}, false, "tokens unknown: no unit start"
	}
	meter, ok := e.Driver.(TokenMeter)
	if !ok {
		return TokenUse{}, false, "tokens unknown: the driver has no token meter"
	}
	ctx, cancel := context.WithTimeout(context.Background(), meterTimeout)
	use, known, err := meter.Tokens(ctx, agent, since, until)
	cancel()
	switch {
	case err != nil:
		return TokenUse{}, false, "tokens unknown: " + oneLine(err.Error())
	case known:
		return use, true, ""
	default:
		return TokenUse{}, false, "tokens unknown: no usage data for this agent"
	}
}

// meter measures for a unit decision or row and records the first unknown
// answer per agent when a budget is configured.
func (e *Engine) meter(runID, agent string, since, until time.Time) (TokenUse, bool) {
	use, known, reason := e.measureTokens(agent, since, until)
	if known {
		return use, true
	}
	if since.IsZero() {
		return TokenUse{}, false
	}
	if e.sup != nil && e.sup.budgeted() && e.sup.markUnknown(agent) {
		_ = e.Store.AppendEvent(runID, Event{At: e.now(), Agent: agent, State: eventTokensUnknown, Reason: reason + "; budget not enforced for this agent"})
	}
	return TokenUse{}, false
}

func (e *Engine) percent() int {
	if e.sup != nil {
		return e.sup.percent
	}
	return DefaultCacheReadPercent
}

// unitTokens measures a finished unit over [started, finished) for
// times.jsonl, budget or not. The row keeps what the source had collected at
// finish; readers settle it later (SettleTokens) without rewriting the row.
func (e *Engine) unitTokens(run Run, agent string, started, finished time.Time) *UnitTokens {
	use, known := e.meter(run.ID, agent, started, finished)
	if !known {
		return nil
	}
	return unitTokensFrom(use, e.percent(), started)
}

// unitBudgetExhausted is checked at a round boundary, before a follow-up is
// sent. It returns the unit's end reason when the unit ceiling is spent.
func (e *Engine) unitBudgetExhausted(_ context.Context, run Run, agent string, metrics unitMetrics) string {
	if e.sup == nil || e.sup.unitCeiling <= 0 {
		return ""
	}
	use, known := e.meter(run.ID, agent, metrics.tokenStart(), time.Time{})
	if !known {
		return ""
	}
	spent := use.Weighted(e.sup.percent)
	if metrics.prior != nil {
		spent += metrics.prior.Weighted
	}
	// unit_tokens covers the whole unit: the steps it finished before this
	// one, on whichever agents ran them.
	spent += metrics.step.earlierWeighted()
	if spent >= e.sup.unitCeiling {
		return UnitBudgetReason(spent, e.sup.unitCeiling)
	}
	return ""
}

// admitRunBudget runs before every new unit dispatch (never before a
// follow-up). Spend is the finished units plus the in-flight units so far.
// Finished units whose tokens were not final at finish (the source lags) are
// re-measured over their own interval first, until the source has collected
// past their finish.
func (e *Engine) admitRunBudget(ctx context.Context, run Run, d Definition) (bool, error) {
	if e.sup == nil || e.sup.runCeiling <= 0 {
		return true, nil
	}
	for index, unit := range e.sup.unsettledUnits() {
		if unit.since.IsZero() {
			continue
		}
		if use, known, _ := e.measureTokens(unit.agent, unit.since, unit.until); known {
			e.sup.remeasured(index, use.Weighted(e.sup.percent)+unit.prior, !use.AsOf.IsZero() && !use.AsOf.Before(unit.until))
		}
	}
	spent := e.sup.finishedSpend()
	for agent, started := range e.sup.inflightUnits() {
		if use, known := e.meter(run.ID, agent, started, time.Time{}); known {
			spent += use.Weighted(e.sup.percent)
		}
	}
	if spent < e.sup.runCeiling {
		return true, nil
	}
	return false, e.requestPause(ctx, run, d, RunBudgetReason(spent, e.sup.runCeiling))
}

// afterPark feeds a parked unit's spend to the run budget. A parked unit is
// neither a success nor a failure for breakers and meltdown.
func (e *Engine) afterPark(row TimeRow) {
	if e.sup == nil {
		return
	}
	e.sup.setInflight(row.Agent, time.Time{})
	e.sup.addSpent(row, true)
}

// afterFinish feeds a written terminal event to the breaker, meltdown and
// spend accounting.
func (e *Engine) afterFinish(run Run, row TimeRow) error {
	if e.sup == nil {
		return nil
	}
	unit, agent, result, reason, finished := row.Unit, row.Agent, row.Result, row.Reason, row.FinishedAt
	e.sup.setInflight(agent, time.Time{})
	e.sup.addSpent(row, false)
	if e.sup.breaker == nil && e.sup.meltdown == nil {
		return nil
	}
	d := e.sup.def
	ctx := context.Background()
	outcome := e.sup.recordFinish(agent, unit, result, reason, finished)
	if outcome.opened {
		if err := e.announceOpen(ctx, run, d, agent, outcome.consecutive, outcome.until, unit, reason); err != nil {
			return err
		}
	}
	if outcome.closed {
		if err := e.Store.AppendEvent(run.ID, Event{At: e.now(), Agent: agent, State: eventBreakerClosed, Reason: "trial unit " + unit + " finished " + result}); err != nil {
			return err
		}
	}
	if outcome.meltdown != "" {
		return e.requestPause(ctx, run, d, outcome.meltdown)
	}
	return nil
}

// requestPause asks workers to stop at the next unit boundary and end the run
// as paused. Only the first request records an event and notifies the owner.
func (e *Engine) requestPause(ctx context.Context, run Run, d Definition, reason string) error {
	transitioned := false
	e.mu.Lock()
	updated, err := e.Store.UpdateRun(run.ID, func(current *Run) error {
		if current.PauseRequested || current.Status == "paused" {
			return nil
		}
		current.PauseRequested, current.PauseReason, current.Status = true, reason, "pausing"
		transitioned = true
		return nil
	})
	if err == nil && e.active != nil && e.active.ID == run.ID {
		e.active = &updated
	}
	e.mu.Unlock()
	if err != nil || !transitioned {
		return err
	}
	if err := e.Store.AppendEvent(run.ID, Event{At: e.now(), State: eventRunPaused, Reason: reason}); err != nil {
		return err
	}
	e.notifyOwner(ctx, run, d, fmt.Sprintf("workflow %s run %s paused: %s; continue with: bp workflow resume %s", run.Workflow, run.ID, reason, run.ID))
	return nil
}
