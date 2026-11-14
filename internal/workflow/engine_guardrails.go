package workflow

import (
	"context"
	"fmt"
	"time"
)

// guardEvery is the shortest gap between two guardrail evaluations while the
// engine waits for a busy agent.
const guardEvery = time.Minute

// unitGuard is one unit's guardrail state while it runs. It is shared by the
// unit's rounds through unitMetrics and used only by its agent's goroutine.
type unitGuard struct {
	run   Run
	def   Definition
	unit  string
	agent string
	// since starts the activity window: the unit's first send, or the send
	// of a human-requested revision round.
	since time.Time
	// prior is the activity of the rounds before a revision round.
	prior *RowActivity
	// announced is the highest level announced per rule for this attempt,
	// rebuilt from units.jsonl so a restarted worker does not notify twice.
	announced map[string]GuardLevel
	lastPoll  time.Time
	// lastHalt is the explanation of the last known halt verdict. A halt is
	// sticky: if the activity becomes unknown before the boundary, the known
	// halt still ends the unit.
	lastHalt string
}

// ensureGuard attaches a guard to a unit's metrics once its window start is
// known. Without guardrails it does nothing.
func (e *Engine) ensureGuard(run Run, d Definition, unit, agent string, metrics *unitMetrics) error {
	if d.Guardrails == nil || metrics.guard != nil {
		return nil
	}
	events, _, err := e.Store.Events(run.ID)
	if err != nil {
		return err
	}
	guard := &unitGuard{run: run, def: d, unit: unit, agent: agent, since: metrics.tokenStart(), announced: announcedGuardrails(events, unit)}
	if metrics.revision != nil {
		guard.prior = metrics.revision.Row.Activity
	}
	metrics.guard = guard
	return nil
}

// activity reads the guard's window. An unknown answer records one
// guardrails_unknown event per agent per run.
func (e *Engine) activity(guard *unitGuard) (ToolTally, bool) {
	meter, ok := e.Driver.(ActivityMeter)
	if !ok {
		e.activityUnknown(guard, "the driver has no activity source")
		return ToolTally{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), meterTimeout)
	activity, known, err := meter.Activity(ctx, guard.agent, guard.since)
	cancel()
	switch {
	case err != nil:
		e.activityUnknown(guard, "activity unreadable: "+oneLine(err.Error()))
		return ToolTally{}, false
	case !known:
		e.activityUnknown(guard, "no tool activity source for this agent (guardrails read Claude Code transcripts only)")
		return ToolTally{}, false
	}
	return activity.Tally(), true
}

func (e *Engine) activityUnknown(guard *unitGuard, reason string) {
	if e.sup != nil && e.sup.markActivityUnknown(guard.agent) {
		_ = e.Store.AppendEvent(guard.run.ID, Event{At: e.now(), Agent: guard.agent, State: eventGuardrailsUnknown, Reason: reason + "; guardrails are not enforced for this agent while its activity is unknown"})
	}
}

func (g *unitGuard) priorCalls() int {
	if g.prior == nil {
		return 0
	}
	return g.prior.ToolCalls
}

// evaluate measures the window and judges it. known is false when the
// activity could not be read.
func (e *Engine) evaluate(guard *unitGuard) (ToolTally, []RuleVerdict, bool) {
	tally, known := e.activity(guard)
	if !known {
		return ToolTally{}, nil, false
	}
	verdicts := evaluateGuardrails(guard.def.Guardrails, tally, guard.priorCalls())
	if worst := worstVerdict(verdicts); worst.Level == GuardHalt {
		guard.lastHalt = worst.Explanation
	}
	return tally, verdicts, true
}

// guardPoll runs while the engine waits for the unit's agent, at most once per
// guardEvery. A rule newly reaching warn or halt is recorded and announced to
// the owner; nothing is ever sent to the agent.
func (e *Engine) guardPoll(ctx context.Context, guard *unitGuard) error {
	if guard == nil {
		return nil
	}
	now := e.now()
	if !guard.lastPoll.IsZero() && now.Sub(guard.lastPoll) < guardEvery {
		return nil
	}
	guard.lastPoll = now
	_, verdicts, known := e.evaluate(guard)
	if !known {
		return nil
	}
	return e.announceGuardrails(ctx, guard, verdicts, false)
}

// guardrailHalt runs at a round boundary, before a follow-up would be sent.
// It returns the unit's end reason when its verdict is halt.
func (e *Engine) guardrailHalt(ctx context.Context, guard *unitGuard) (string, error) {
	if guard == nil {
		return "", nil
	}
	_, verdicts, known := e.evaluate(guard)
	if known {
		if err := e.announceGuardrails(ctx, guard, verdicts, true); err != nil {
			return "", err
		}
	}
	if guard.lastHalt != "" {
		return GuardrailReason(guard.lastHalt), nil
	}
	return "", nil
}

// announceGuardrails records and notifies each rule that reached a level not
// yet announced for this attempt. A rule that jumps from ok to halt is
// announced once, as halt.
func (e *Engine) announceGuardrails(ctx context.Context, guard *unitGuard, verdicts []RuleVerdict, boundary bool) error {
	run, d := guard.run, guard.def
	for _, verdict := range verdicts {
		if verdict.Level <= guard.announced[verdict.Rule] {
			continue
		}
		state, label := eventGuardrailWarn, "warning"
		if verdict.Level == GuardHalt {
			state, label = eventGuardrailHalt, "halt"
		}
		record := &GuardrailRecord{Unit: guard.unit, Rule: verdict.Rule, Level: verdict.Level.String(), Value: verdict.Value, Limit: verdict.Limit}
		if err := e.Store.AppendEvent(run.ID, Event{At: e.now(), Agent: guard.agent, State: state, Reason: verdict.Explanation, Guardrail: record}); err != nil {
			return err
		}
		guard.announced[verdict.Rule] = verdict.Level
		message := fmt.Sprintf("workflow %s run %s: guardrail %s on unit %s (agent %s): %s (%s %s at %d)", run.Workflow, run.ID, label, guard.unit, guard.agent, verdict.Explanation, verdict.Rule, verdict.Level, verdict.Limit)
		switch {
		case verdict.Level == GuardHalt && boundary:
			message += ". The agent is idle; no follow-up is sent and the unit ends failed."
		case verdict.Level == GuardHalt:
			message += fmt.Sprintf(". bp never interrupts a working agent: %s keeps working, and the unit ends when it next goes idle (as failed, unless the validator accepts its output then). Step in yourself to stop it sooner.", guard.agent)
		}
		e.notifyOwner(ctx, run, d, message)
	}
	return nil
}

// rowActivity measures a finishing unit's activity for its times row. It
// does not announce: a unit that ends has nothing left to stop.
func (e *Engine) rowActivity(metrics unitMetrics) *RowActivity {
	guard := metrics.guard
	if guard == nil {
		if metrics.revision != nil {
			return metrics.revision.Row.Activity
		}
		return nil
	}
	tally, verdicts, known := e.evaluate(guard)
	if !known {
		return nil
	}
	row := &RowActivity{ToolCalls: tally.Calls() + guard.priorCalls(), LongestFailures: tally.LongestFailures("")}
	row.LongestRepeat, _ = tally.LongestRepeat("")
	if guard.prior != nil {
		row.LongestRepeat = max(row.LongestRepeat, guard.prior.LongestRepeat)
		row.LongestFailures = max(row.LongestFailures, guard.prior.LongestFailures)
	}
	if worst := worstVerdict(verdicts); worst.Level > GuardOK {
		row.Verdict, row.Rule = worst.Level.String(), worst.Rule
	}
	return row
}
