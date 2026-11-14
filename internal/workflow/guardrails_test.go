package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// toolDriver adds a scripted ActivityMeter to scriptedDriver. Tool events
// are synthetic: a tool name, an input label and an outcome.
type toolDriver struct {
	*scriptedDriver
	tmu     sync.Mutex
	events  map[string][]ToolEvent
	unknown bool
	err     error
	reads   int
}

func newToolDriver(driver *scriptedDriver) *toolDriver {
	return &toolDriver{scriptedDriver: driver, events: map[string][]ToolEvent{}}
}

func (d *toolDriver) Activity(_ context.Context, agent string, since time.Time) (Activity, bool, error) {
	d.tmu.Lock()
	defer d.tmu.Unlock()
	d.reads++
	if d.err != nil {
		return Activity{}, false, d.err
	}
	if d.unknown {
		return Activity{}, false, nil
	}
	var events []ToolEvent
	for _, event := range d.events[agent] {
		if !event.At.Before(since) {
			events = append(events, event)
		}
	}
	return Activity{Events: events}, true, nil
}

func (d *toolDriver) add(agent string, events ...ToolEvent) {
	d.tmu.Lock()
	d.events[agent] = append(d.events[agent], events...)
	d.tmu.Unlock()
}

func (d *toolDriver) readCount() int {
	d.tmu.Lock()
	defer d.tmu.Unlock()
	return d.reads
}

func toolCalls(at time.Time, count int, tool, input string, outcome ToolOutcome) []ToolEvent {
	events := make([]ToolEvent, count)
	for index := range events {
		events[index] = ToolEvent{At: at, ID: fmt.Sprintf("%s-%s-%d-%d", tool, input, at.UnixNano(), index), Tool: tool, Input: input, Outcome: outcome}
	}
	return events
}

func intPointer(value int) *int { return &value }

const failuresGuard = "guardrails:\n  tool_failures:\n    warn: 5\n    halt: 10\n    tool: Bash\n"

// guardedRun builds a one-agent run with a follow-up prompt and a tool
// driver whose first send of every unit runs the given calls.
func guardedRun(t *testing.T, yamlText string, keys []string, firstSend func(at time.Time) []ToolEvent, agents ...string) (*Store, Run, *toolDriver, *fakeClock) {
	t.Helper()
	if len(agents) == 0 {
		agents = []string{"agent-a"}
	}
	store, run, scripted, clock := approvalRun(t, yamlText, keys, agents...)
	driver := newToolDriver(scripted)
	sent := map[string]bool{}
	scripted.beforeSend = func(agent, key string) {
		if firstSend != nil && !sent[key] {
			sent[key] = true
			driver.add(agent, firstSend(clock.Now())...)
		}
	}
	return store, run, driver, clock
}

// busyFor keeps agent-a working for count observations after each send.
func busyFor(driver *toolDriver, count int) {
	var mu sync.Mutex
	left := 0
	previous := driver.scriptedDriver.beforeSend
	driver.scriptedDriver.beforeSend = func(agent, key string) {
		if previous != nil {
			previous(agent, key)
		}
		mu.Lock()
		left = count
		mu.Unlock()
	}
	driver.scriptedDriver.observe = func(string) (AgentState, bool) {
		mu.Lock()
		defer mu.Unlock()
		if left > 0 {
			left--
			return Working, true
		}
		return "", false
	}
}

func TestGuardrailEvaluation(t *testing.T) {
	at := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	call := func(tool, input string, outcome ToolOutcome) ToolEvent {
		return ToolEvent{At: at, Tool: tool, Input: input, Outcome: outcome}
	}
	tally := func(events ...ToolEvent) ToolTally { return Activity{Events: events}.Tally() }

	t.Run("identical streak ends at any other call", func(t *testing.T) {
		got := tally(call("Bash", "a", ToolSucceeded), call("Bash", "a", ToolSucceeded), call("Read", "x", ToolSucceeded), call("Bash", "a", ToolSucceeded), call("Bash", "a", ToolSucceeded), call("Bash", "a", ToolSucceeded), call("Bash", "b", ToolSucceeded))
		if longest, tool := got.LongestRepeat(""); longest != 3 || tool != "Bash" {
			t.Fatalf("longest repeat %d %s", longest, tool)
		}
		if longest, _ := got.LongestRepeat("Read"); longest != 1 {
			t.Fatalf("Read repeat %d", longest)
		}
		if got.Calls() != 7 {
			t.Fatalf("calls %d", got.Calls())
		}
	})
	t.Run("failure streak skips pending calls and resets on success", func(t *testing.T) {
		got := tally(call("Bash", "a", ToolFailed), call("Bash", "b", ToolPending), call("Bash", "c", ToolFailed), call("Bash", "d", ToolSucceeded), call("Bash", "e", ToolFailed))
		if got.LongestFailures("") != 2 {
			t.Fatalf("longest failures %d", got.LongestFailures(""))
		}
	})
	t.Run("a tool filter counts only that tool's results", func(t *testing.T) {
		got := tally(call("Bash", "a", ToolFailed), call("Read", "x", ToolSucceeded), call("Bash", "b", ToolFailed), call("Edit", "y", ToolFailed), call("Read", "z", ToolSucceeded), call("Bash", "c", ToolFailed))
		if got.LongestFailures("Bash") != 3 || got.LongestFailures("") != 2 || got.LongestFailures("Edit") != 1 {
			t.Fatalf("filtered failures Bash=%d any=%d Edit=%d", got.LongestFailures("Bash"), got.LongestFailures(""), got.LongestFailures("Edit"))
		}
	})
	t.Run("streaks are the longest seen, not the current one", func(t *testing.T) {
		events := toolCalls(at, 6, "Bash", "same", ToolFailed)
		events = append(events, call("Bash", "fixed", ToolSucceeded))
		got := tally(events...)
		spec := &GuardrailSpec{ToolFailures: &StreakRule{Warn: intPointer(3), Halt: intPointer(6)}}
		if verdict := worstVerdict(evaluateGuardrails(spec, got, 0)); verdict.Level != GuardHalt || verdict.Explanation != "6 tool calls failed in a row" {
			t.Fatalf("verdict %#v", verdict)
		}
	})
	t.Run("earlier tally folds exactly", func(t *testing.T) {
		events := append(toolCalls(at, 4, "Bash", "same", ToolFailed), toolCalls(at, 3, "Bash", "same", ToolFailed)...)
		var earlier ToolTally
		for _, event := range events[:4] {
			earlier.Add(event)
		}
		folded := Activity{Earlier: earlier, Events: events[4:]}.Tally()
		whole := tally(events...)
		if folded.Calls() != whole.Calls() || folded.LongestFailures("") != 7 || folded.LongestFailures("Bash") != 7 {
			t.Fatalf("folded %d/%d whole %d", folded.Calls(), folded.LongestFailures(""), whole.Calls())
		}
		if longest, _ := folded.LongestRepeat(""); longest != 7 {
			t.Fatalf("folded repeat %d", longest)
		}
		// The fold must not share maps with the earlier tally.
		folded.Add(call("Bash", "same", ToolFailed))
		if earlier.LongestFailures("Bash") != 4 {
			t.Fatalf("clone shares state: %d", earlier.LongestFailures("Bash"))
		}
	})
	t.Run("levels, worst verdict and explanations", func(t *testing.T) {
		spec := &GuardrailSpec{
			ToolCalls:        &CountRule{Warn: intPointer(5), Halt: intPointer(20)},
			RepeatedToolCall: &StreakRule{Halt: intPointer(4), Tool: "Read"},
			ToolFailures:     &StreakRule{Warn: intPointer(2)},
		}
		events := append(toolCalls(at, 4, "Read", "same", ToolSucceeded), call("Bash", "x", ToolFailed), call("Bash", "y", ToolFailed))
		verdicts := evaluateGuardrails(spec, tally(events...), 3)
		want := []struct {
			rule        string
			level       GuardLevel
			value       int
			limit       int
			explanation string
		}{
			{RuleToolFailures, GuardWarn, 2, 2, "2 tool calls failed in a row"},
			{RuleRepeatedToolCall, GuardHalt, 4, 4, "the same Read call was made 4 times in a row"},
			{RuleToolCalls, GuardWarn, 9, 5, "the unit made 9 tool calls"},
		}
		if len(verdicts) != len(want) {
			t.Fatalf("verdicts %#v", verdicts)
		}
		for index, verdict := range verdicts {
			w := want[index]
			if verdict.Rule != w.rule || verdict.Level != w.level || verdict.Value != w.value || verdict.Limit != w.limit || verdict.Explanation != w.explanation {
				t.Fatalf("verdict %d = %#v, want %#v", index, verdict, w)
			}
		}
		if worst := worstVerdict(verdicts); worst.Rule != RuleRepeatedToolCall || GuardrailReason(worst.Explanation) != "guardrail: the same Read call was made 4 times in a row" {
			t.Fatalf("worst %#v", worst)
		}
		ok := evaluateGuardrails(spec, tally(call("Read", "a", ToolSucceeded)), 0)
		if worst := worstVerdict(ok); worst.Level != GuardOK {
			t.Fatalf("one call judged %#v", worst)
		}
		if GuardWarn.String() != "warn" || GuardHalt.String() != "halt" || parseGuardLevel("halt") != GuardHalt || parseGuardLevel("bogus") != GuardOK {
			t.Fatal("level names")
		}
	})
	t.Run("ties keep the earlier rule", func(t *testing.T) {
		spec := &GuardrailSpec{RepeatedToolCall: &StreakRule{Halt: intPointer(3)}, ToolFailures: &StreakRule{Halt: intPointer(3)}}
		worst := worstVerdict(evaluateGuardrails(spec, tally(toolCalls(at, 3, "Bash", "same", ToolFailed)...), 0))
		if worst.Rule != RuleToolFailures {
			t.Fatalf("tie went to %s", worst.Rule)
		}
	})
}

func TestGuardrailDefinitionValidation(t *testing.T) {
	for _, test := range []struct {
		name, block, want string
	}{
		{"empty block", "guardrails: {}\n", "guardrails needs at least one of tool_calls, repeated_tool_call, tool_failures"},
		{"rule without levels", "guardrails:\n  tool_calls: {}\n", "guardrails.tool_calls needs warn or halt"},
		{"zero warn", "guardrails:\n  tool_calls:\n    warn: 0\n", "guardrails.tool_calls.warn must be a positive integer, got 0"},
		{"negative halt", "guardrails:\n  tool_failures:\n    halt: -2\n", "guardrails.tool_failures.halt must be a positive integer, got -2"},
		{"warn not below halt", "guardrails:\n  repeated_tool_call:\n    warn: 5\n    halt: 5\n", "guardrails.repeated_tool_call.warn (5) must be below halt (5)"},
		{"tool with spaces", "guardrails:\n  tool_failures:\n    halt: 3\n    tool: \" Bash\"\n", "guardrails.tool_failures.tool must be a tool name"},
		{"unknown rule", "guardrails:\n  tool_loops:\n    halt: 3\n", "tool_loops"},
		{"unknown key", "guardrails:\n  tool_calls:\n    halt: 3\n    stop: 4\n", "stop"},
		{"tool on a count rule", "guardrails:\n  tool_calls:\n    halt: 3\n    tool: Bash\n", "tool"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseDefinitionYAML(baseYAML(test.block))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %v, want %q", err, test.want)
			}
		})
	}
	t.Run("valid rules round-trip", func(t *testing.T) {
		d, err := parseDefinitionYAML(baseYAML("guardrails:\n  tool_calls:\n    warn: 100\n  repeated_tool_call:\n    halt: 8\n    tool: mcp__server__fetch\n  tool_failures:\n    warn: 3\n    halt: 6\n"))
		if err != nil {
			t.Fatal(err)
		}
		g := d.Guardrails
		if g == nil || g.ToolCalls == nil || *g.ToolCalls.Warn != 100 || g.ToolCalls.Halt != nil || *g.RepeatedToolCall.Halt != 8 || g.RepeatedToolCall.Tool != "mcp__server__fetch" || *g.ToolFailures.Warn != 3 || *g.ToolFailures.Halt != 6 {
			t.Fatalf("parsed %#v", g)
		}
		encoded, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		var back Definition
		if err := json.Unmarshal(encoded, &back); err != nil || back.Guardrails == nil || *back.Guardrails.ToolFailures.Halt != 6 || back.Guardrails.RepeatedToolCall.Tool != "mcp__server__fetch" {
			t.Fatalf("json round trip %v %#v", err, back.Guardrails)
		}
	})
	t.Run("without the key nothing changes", func(t *testing.T) {
		d, err := parseDefinitionYAML(baseYAML(""))
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(d)
		if d.Guardrails != nil || strings.Contains(string(encoded), "guardrails") {
			t.Fatalf("definition without guardrails: %s", encoded)
		}
	})
}

func TestGuardrailWarnWhileBusyAnnouncesOnce(t *testing.T) {
	store, run, driver, clock := guardedRun(t, budgetYAML(failuresGuard, 3, 3), []string{"u1"}, func(at time.Time) []ToolEvent {
		return toolCalls(at, 6, "Bash", "same", ToolFailed)
	})
	busyFor(driver, 200)
	notes := &notifications{}
	engine := supervisedEngine(store, driver, clock, notes)
	engine.Validator = alwaysCode(0)
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	warns := eventsWithState(t, store, run.ID, eventGuardrailWarn)
	if len(warns) != 1 || warns[0].Guardrail == nil || warns[0].Guardrail.Unit != "u1" || warns[0].Guardrail.Rule != RuleToolFailures || warns[0].Guardrail.Value != 6 || warns[0].Guardrail.Limit != 5 || warns[0].Reason != "tool Bash failed 6 times in a row" {
		t.Fatalf("warn events %#v", warns)
	}
	if notes.count("guardrail warning on unit u1") != 1 || notes.count("guardrail") != 1 {
		t.Fatalf("notifications %q", notes.messages)
	}
	row := timesByUnit(t, store, run.ID)["u1"]
	if row.Result != "done" || row.Activity == nil || row.Activity.ToolCalls != 6 || row.Activity.LongestFailures != 6 || row.Activity.LongestRepeat != 6 || row.Activity.Verdict != "warn" || row.Activity.Rule != RuleToolFailures {
		t.Fatalf("row %#v activity %#v", row, row.Activity)
	}
}

func TestGuardrailHaltWhileBusyNotifiesOnceAndSendsNothing(t *testing.T) {
	store, run, driver, clock := guardedRun(t, budgetYAML(failuresGuard, 3, 3), []string{"u1", "u2"}, func(at time.Time) []ToolEvent {
		return toolCalls(at, 12, "Bash", "same", ToolFailed)
	})
	busyFor(driver, 200)
	var marks []GuardrailMark
	var sendsWhileBusy int
	observe := driver.scriptedDriver.observe
	driver.scriptedDriver.observe = func(agent string) (AgentState, bool) {
		state, ok := observe(agent)
		if ok && state == Working && len(eventsWithState(t, store, run.ID, eventGuardrailHalt)) == 1 && marks == nil {
			status, err := store.Status(run.ID)
			if err != nil {
				t.Error(err)
			}
			marks = status.Agents[0].Guardrails
			sendsWhileBusy = len(driver.sendsFor("agent-a"))
		}
		return state, ok
	}
	notes := &notifications{}
	engine := supervisedEngine(store, driver, clock, notes)
	engine.Validator = alwaysCode(1)
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	if len(marks) != 1 || marks[0].Rule != RuleToolFailures || marks[0].Level != "halt" || sendsWhileBusy != 1 {
		t.Fatalf("status while busy: marks=%#v sends=%d", marks, sendsWhileBusy)
	}
	halts := eventsWithState(t, store, run.ID, eventGuardrailHalt)
	if len(halts) != 2 || halts[0].Guardrail.Unit != "u1" || halts[1].Guardrail.Unit != "u2" || len(eventsWithState(t, store, run.ID, eventGuardrailWarn)) != 0 {
		t.Fatalf("halt events %#v", halts)
	}
	if notes.count("guardrail halt on unit u1") != 1 || notes.count("never interrupts a working agent: agent-a keeps working") != 2 || notes.count("Step in yourself") != 2 {
		t.Fatalf("notifications %q", notes.messages)
	}
	// One send per unit: the halt stopped every follow-up.
	sends := driver.sendsFor("agent-a")
	if len(sends) != 2 || sends[0] != "u1" || sends[1] != "u2" {
		t.Fatalf("sends %q", sends)
	}
	for _, key := range []string{"u1", "u2"} {
		row := timesByUnit(t, store, run.ID)[key]
		if row.Result != "failed" || row.Reason != "guardrail: tool Bash failed 12 times in a row" || row.Rounds != 1 || row.Activity == nil || row.Activity.Verdict != "halt" || row.Activity.Rule != RuleToolFailures {
			t.Fatalf("%s row %#v", key, row)
		}
	}
	status, err := store.Status(run.ID)
	if err != nil || len(status.Agents[0].Guardrails) != 0 {
		t.Fatalf("finished units keep no mark: %#v %v", status.Agents, err)
	}
}

func TestGuardrailHaltAtBoundary(t *testing.T) {
	store, run, driver, clock := guardedRun(t, budgetYAML(failuresGuard, 3, 3), []string{"u1"}, nil)
	notes := &notifications{}
	engine := supervisedEngine(store, driver, clock, notes)
	// The calls appear only after the agent went idle (the last poll saw
	// none), so the boundary check is the first to see the halt.
	engine.Validator = func(context.Context, string, []string, time.Duration) (int, string, string, error) {
		driver.add("agent-a", toolCalls(clock.Now(), 10, "Bash", "same", ToolFailed)...)
		return 1, "still wrong", "", nil
	}
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	if notes.count("guardrail halt on unit u1") != 1 || notes.count("The agent is idle; no follow-up is sent and the unit ends failed.") != 1 {
		t.Fatalf("notifications %q", notes.messages)
	}
	row := timesByUnit(t, store, run.ID)["u1"]
	if len(driver.sendsFor("agent-a")) != 1 || row.Result != "failed" || row.Reason != "guardrail: tool Bash failed 10 times in a row" {
		t.Fatalf("sends=%d row %#v", len(driver.sendsFor("agent-a")), row)
	}
}

func TestGuardrailHaltKeepsAcceptedOutput(t *testing.T) {
	for _, test := range []struct {
		name string
		code int
		want string
	}{
		{"done", 0, "done"},
		{"weak by weak_after", 3, "weak"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, run, driver, clock := guardedRun(t, budgetYAML(failuresGuard, 3, 1), []string{"u1"}, func(at time.Time) []ToolEvent {
				return toolCalls(at, 12, "Bash", "same", ToolFailed)
			})
			engine := supervisedEngine(store, driver, clock, &notifications{})
			engine.Validator = alwaysCode(test.code)
			if err := engine.Run(context.Background(), run.ID, false); err != nil {
				t.Fatal(err)
			}
			row := timesByUnit(t, store, run.ID)["u1"]
			if row.Result != test.want || strings.HasPrefix(row.Reason, "guardrail:") || row.Activity == nil || row.Activity.Verdict != "halt" {
				t.Fatalf("row %#v activity %#v", row, row.Activity)
			}
		})
	}
}

func TestGuardrailFailureCountsForBreakerAndMeltdown(t *testing.T) {
	halting := func(at time.Time) []ToolEvent { return toolCalls(at, 12, "Bash", "same", ToolFailed) }
	t.Run("breaker", func(t *testing.T) {
		// agent-b has no tool calls: it runs its rounds out and fails too,
		// so the run ends without waiting for a breaker to reopen.
		store, run, driver, clock := guardedRun(t, budgetYAML(failuresGuard+"supervise:\n  breaker:\n    failures: 1\n    open_for: 240h\n", 3, 3), []string{"u1", "u2"}, nil, "agent-a", "agent-b")
		driver.scriptedDriver.beforeSend = func(agent, key string) {
			if agent == "agent-a" {
				driver.add(agent, halting(clock.Now())...)
			}
		}
		engine := supervisedEngine(store, driver, clock, &notifications{})
		engine.Validator = alwaysCode(1)
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		opened := eventsWithState(t, store, run.ID, eventBreakerOpen)
		var openedA int
		for _, event := range opened {
			if event.Agent == "agent-a" {
				openedA++
			}
		}
		rows := timesByUnit(t, store, run.ID)
		var haltedRow TimeRow
		for _, row := range rows {
			if row.Agent == "agent-a" {
				haltedRow = row
			}
		}
		if openedA != 1 || len(driver.sendsFor("agent-a")) != 1 || !strings.HasPrefix(haltedRow.Reason, "guardrail: ") {
			t.Fatalf("breaker events %#v sends %q", opened, driver.sendsFor("agent-a"))
		}
	})
	t.Run("meltdown", func(t *testing.T) {
		store, run, driver, clock := guardedRun(t, budgetYAML(failuresGuard+"supervise:\n  meltdown:\n    failures: 1\n    window: 30m\n", 3, 3), []string{"u1", "u2"}, halting)
		engine := supervisedEngine(store, driver, clock, &notifications{})
		engine.Validator = alwaysCode(1)
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		current, err := store.LoadRun(run.ID)
		if err != nil || current.Status != "paused" || !strings.Contains(current.PauseReason, "guardrail: tool Bash failed 12 times in a row") {
			t.Fatalf("run %s pause %q err %v", current.Status, current.PauseReason, err)
		}
	})
}

func TestGuardrailFailureParks(t *testing.T) {
	store, run, driver, clock := guardedRun(t, approveYAML("failed", 3, 3, failuresGuard), []string{"u1"}, func(at time.Time) []ToolEvent {
		return toolCalls(at, 12, "Bash", "same", ToolFailed)
	})
	engine := supervisedEngine(store, driver, clock, &notifications{})
	engine.Validator = alwaysCode(1)
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	parked := latestState(t, store, run.ID)["u1"]
	if parked.State != StateAwaitingApproval || parked.Park == nil || parked.Park.Proposed != "failed" || parked.Reason != "guardrail: tool Bash failed 12 times in a row" {
		t.Fatalf("parked %#v", parked)
	}
	if activity := parked.Park.Row.Activity; activity == nil || activity.Verdict != "halt" || activity.ToolCalls != 12 {
		t.Fatalf("park row activity %#v", activity)
	}
	if len(driver.sendsFor("agent-a")) != 1 || len(timesByUnit(t, store, run.ID)) != 0 {
		t.Fatalf("parking sent a follow-up or wrote a row")
	}
}

func TestGuardrailRevisionRound(t *testing.T) {
	t.Run("streak rules start over at the revision send", func(t *testing.T) {
		store, run, driver, clock := guardedRun(t, approveYAML("weak, failed", 3, 3, failuresGuard), []string{"u1"}, func(at time.Time) []ToolEvent {
			return toolCalls(at, 12, "Bash", "same", ToolFailed)
		})
		notes := &notifications{}
		engine := supervisedEngine(store, driver, clock, notes)
		engine.Validator = alwaysCode(1)
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		clock.Advance(time.Minute)
		if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionRevise, Units: []string{"u1"}, Text: "try another way", By: "owner"}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Resume(run.ID, ResumeOptions{}); err != nil {
			t.Fatal(err)
		}
		// The revision round makes six calls that fail: warn, not halt.
		revisionCalls := false
		driver.scriptedDriver.beforeSend = func(agent, key string) {
			if !revisionCalls {
				revisionCalls = true
				driver.add(agent, toolCalls(clock.Now(), 6, "Bash", "other", ToolFailed)...)
			}
		}
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		sends := driver.sendsFor("agent-a")
		// First round, the revision, then a normal follow-up the old streak
		// no longer blocks. Round 3 exhausts validate.rounds.
		if len(sends) != 3 || sends[1] != "fix [try another way] u1" || sends[2] != "fix [still wrong] u1" {
			t.Fatalf("sends %q", sends)
		}
		parked := latestState(t, store, run.ID)["u1"]
		if parked.State != StateAwaitingApproval || parked.Park.Proposed != "weak" || parked.Reason != "still wrong" || parked.Round != 3 {
			t.Fatalf("parked after revision %#v", parked)
		}
		activity := parked.Park.Row.Activity
		// Totals cover all rounds; the verdict is the revision window's.
		if activity == nil || activity.ToolCalls != 18 || activity.LongestFailures != 12 || activity.Verdict != "warn" || activity.Rule != RuleToolFailures {
			t.Fatalf("activity after revision %#v", activity)
		}
		// The streak warning in the revision round is a new announcement.
		if notes.count("guardrail warning on unit u1") != 1 || notes.count("guardrail halt on unit u1") != 1 {
			t.Fatalf("notifications %q", notes.messages)
		}
	})
	t.Run("tool_calls keeps counting earlier rounds", func(t *testing.T) {
		store, run, driver, clock := guardedRun(t, approveYAML("failed", 3, 3, "guardrails:\n  tool_calls:\n    halt: 10\n"), []string{"u1"}, func(at time.Time) []ToolEvent {
			return toolCalls(at, 12, "Read", "file", ToolSucceeded)
		})
		notes := &notifications{}
		engine := supervisedEngine(store, driver, clock, notes)
		engine.Validator = alwaysCode(1)
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionRevise, Units: []string{"u1"}, Text: "finish it", By: "owner"}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Resume(run.ID, ResumeOptions{}); err != nil {
			t.Fatal(err)
		}
		driver.scriptedDriver.beforeSend = func(agent, key string) {
			driver.add(agent, toolCalls(clock.Now(), 2, "Read", "other", ToolSucceeded)...)
		}
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		sends := driver.sendsFor("agent-a")
		if len(sends) != 2 || sends[1] != "fix [finish it] u1" {
			t.Fatalf("the revision must be sent once and nothing after it: %q", sends)
		}
		parked := latestState(t, store, run.ID)["u1"]
		if parked.State != StateAwaitingApproval || parked.Reason != "guardrail: the unit made 14 tool calls" || parked.Park.Row.Activity.ToolCalls != 14 {
			t.Fatalf("parked after revision %#v", parked)
		}
		if notes.count("guardrail halt on unit u1") != 1 {
			t.Fatalf("tool_calls halt announced again: %q", notes.messages)
		}
	})
}

func TestGuardrailUnknownActivity(t *testing.T) {
	t.Run("meter says unknown", func(t *testing.T) {
		store, run, driver, clock := guardedRun(t, budgetYAML(failuresGuard, 2, 2), []string{"u1", "u2"}, nil)
		driver.unknown = true
		notes := &notifications{}
		engine := supervisedEngine(store, driver, clock, notes)
		engine.Validator = alwaysCode(1)
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		unknown := eventsWithState(t, store, run.ID, eventGuardrailsUnknown)
		if len(unknown) != 1 || unknown[0].Agent != "agent-a" || !strings.Contains(unknown[0].Reason, "Claude Code transcripts only") || !strings.Contains(unknown[0].Reason, "not enforced") {
			t.Fatalf("unknown events %#v", unknown)
		}
		rows := timesByUnit(t, store, run.ID)
		if len(driver.sendsFor("agent-a")) != 4 || rows["u1"].Activity != nil || rows["u1"].Rounds != 2 || notes.count("guardrail") != 0 {
			t.Fatalf("unknown activity changed the run: sends=%d rows=%#v notes=%q", len(driver.sendsFor("agent-a")), rows, notes.messages)
		}
	})
	t.Run("meter error", func(t *testing.T) {
		store, run, driver, clock := guardedRun(t, budgetYAML(failuresGuard, 1, 1), []string{"u1"}, nil)
		driver.err = errors.New("transcript\nunreadable")
		engine := supervisedEngine(store, driver, clock, nil)
		engine.Validator = alwaysCode(0)
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		unknown := eventsWithState(t, store, run.ID, eventGuardrailsUnknown)
		if len(unknown) != 1 || !strings.Contains(unknown[0].Reason, "activity unreadable: transcript unreadable") {
			t.Fatalf("unknown events %#v", unknown)
		}
	})
	t.Run("driver without a meter", func(t *testing.T) {
		store, run, scripted, clock := approvalRun(t, budgetYAML(failuresGuard, 1, 1), []string{"u1", "u2"}, "agent-a")
		engine := supervisedEngine(store, scripted, clock, nil)
		engine.Validator = alwaysCode(0)
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		unknown := eventsWithState(t, store, run.ID, eventGuardrailsUnknown)
		if len(unknown) != 1 || !strings.Contains(unknown[0].Reason, "the driver has no activity source") {
			t.Fatalf("unknown events %#v", unknown)
		}
	})
	t.Run("restarted worker does not record it again", func(t *testing.T) {
		store, run, driver, clock := guardedRun(t, budgetYAML(failuresGuard, 1, 1), []string{"u1"}, nil)
		driver.unknown = true
		if err := store.AppendEvent(run.ID, Event{At: clock.Now(), Agent: "agent-a", State: eventGuardrailsUnknown, Reason: "earlier worker"}); err != nil {
			t.Fatal(err)
		}
		engine := supervisedEngine(store, driver, clock, nil)
		engine.Validator = alwaysCode(0)
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		if unknown := eventsWithState(t, store, run.ID, eventGuardrailsUnknown); len(unknown) != 1 {
			t.Fatalf("unknown events %#v", unknown)
		}
	})
}

func TestGuardrailPollsAtMostOncePerMinute(t *testing.T) {
	store, run, driver, clock := guardedRun(t, budgetYAML(failuresGuard, 1, 1), []string{"u1"}, nil)
	// About ten minutes of polling at one observation per second.
	busyFor(driver, 600)
	engine := supervisedEngine(store, driver, clock, nil)
	var readsAtBoundary int
	engine.Validator = func(context.Context, string, []string, time.Duration) (int, string, string, error) {
		readsAtBoundary = driver.readCount()
		return 0, "", "", nil
	}
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	if readsAtBoundary < 10 || readsAtBoundary > 13 {
		t.Fatalf("activity read %d times in ten busy minutes", readsAtBoundary)
	}
}

func TestGuardrailRestartDoesNotNotifyAgain(t *testing.T) {
	store, run, driver, clock := guardedRun(t, budgetYAML(failuresGuard, 3, 3), []string{"u1"}, nil)
	// The previous worker sent u1, announced the halt while the agent worked
	// and died. The transcript still shows the failing calls.
	at := clock.Now()
	driver.add("agent-a", toolCalls(at, 11, "Bash", "same", ToolFailed)...)
	for _, event := range []Event{
		{At: at, Unit: "u1", Key: "u1", Agent: "agent-a", State: "sending", Round: 1},
		{At: at, Unit: "u1", Key: "u1", Agent: "agent-a", State: "sent", Round: 1, DeliveryID: "q1"},
		{At: at, Unit: "u1", Key: "u1", Agent: "agent-a", State: "working", Round: 1, DeliveryID: "q1"},
		{At: at, Agent: "agent-a", State: eventGuardrailHalt, Reason: "tool Bash failed 11 times in a row", Guardrail: &GuardrailRecord{Unit: "u1", Rule: RuleToolFailures, Level: "halt", Value: 11, Limit: 10}},
	} {
		if err := store.AppendEvent(run.ID, event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.UpdateRun(run.ID, func(current *Run) error {
		current.Status = "running"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	notes := &notifications{}
	engine := supervisedEngine(store, driver, clock, notes)
	engine.Validator = alwaysCode(1)
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	if notes.count("guardrail") != 0 || len(eventsWithState(t, store, run.ID, eventGuardrailHalt)) != 1 {
		t.Fatalf("restart announced again: %q", notes.messages)
	}
	row := timesByUnit(t, store, run.ID)["u1"]
	if len(driver.sendsFor("agent-a")) != 0 || row.Result != "failed" || row.Reason != "guardrail: tool Bash failed 11 times in a row" {
		t.Fatalf("sends=%q row %#v", driver.sendsFor("agent-a"), row)
	}
}

func TestGuardrailsCheckedBeforeUnitBudget(t *testing.T) {
	yamlText := budgetYAML(failuresGuard+"budget:\n  unit_tokens: 100000\n", 3, 3)
	store, run, scripted, clock := meteredRun(t, yamlText, []string{"u1"}, "agent-a")
	driver := newToolDriver(scripted)
	scripted.beforeSend = func(agent, key string) {
		driver.add(agent, toolCalls(clock.Now(), 10, "Bash", "same", ToolFailed)...)
	}
	engine := supervisedEngine(store, driver, clock, &notifications{})
	engine.Validator = alwaysCode(1)
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	row := timesByUnit(t, store, run.ID)["u1"]
	if row.Result != "failed" || row.Reason != "guardrail: tool Bash failed 10 times in a row" || len(scripted.sendsFor("agent-a")) != 1 {
		t.Fatalf("row %#v", row)
	}
}

func TestGuardrailsAbsentKeepRowsUnchanged(t *testing.T) {
	store, run, scripted, clock := approvalRun(t, budgetYAML("", 2, 2), []string{"u1"}, "agent-a")
	driver := newToolDriver(scripted)
	driver.add("agent-a", toolCalls(clock.Now(), 50, "Bash", "same", ToolFailed)...)
	engine := supervisedEngine(store, driver, clock, nil)
	engine.Validator = alwaysCode(0)
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	row := timesByUnit(t, store, run.ID)["u1"]
	if row.Activity != nil || driver.readCount() != 0 || len(eventsWithState(t, store, run.ID, eventGuardrailsUnknown)) != 0 {
		t.Fatalf("activity read without guardrails: reads=%d row=%#v", driver.readCount(), row.Activity)
	}
	raw, err := os.ReadFile(filepath.Join(store.RunPath(run.ID), "times.jsonl"))
	if err != nil || strings.Contains(string(raw), "activity") {
		t.Fatalf("times.jsonl gained an activity field: %s %v", raw, err)
	}
}

func TestActivityRowsLoadAndSummarize(t *testing.T) {
	clock := newFakeClock()
	store, run := makeRun(t, clock, baseYAML(""), unitsJSON("u1"), "{{.Key}}", "", "agent-a")
	lines := []string{
		// An old row without activity.
		`{"run":"` + run.ID + `","workflow":"sample","unit":"old","agent":"agent-a","result":"done","rounds":1,"work_s":10,"finished_at":"2026-09-25T10:00:00Z"}`,
		`{"run":"` + run.ID + `","workflow":"sample","unit":"a","agent":"agent-a","result":"done","rounds":1,"work_s":10,"finished_at":"2026-09-25T10:01:00Z","activity":{"tool_calls":10,"longest_repeat":2,"longest_failures":1}}`,
		`{"run":"` + run.ID + `","workflow":"sample","unit":"b","agent":"agent-a","result":"done","rounds":1,"work_s":10,"finished_at":"2026-09-25T10:02:00Z","activity":{"tool_calls":20,"longest_repeat":5,"longest_failures":1,"verdict":"warn","rule":"repeated_tool_call"}}`,
		`{"run":"` + run.ID + `","workflow":"sample","unit":"c","agent":"agent-a","result":"failed","reason":"guardrail: 12 tool calls failed in a row","rounds":1,"work_s":10,"finished_at":"2026-09-25T10:03:00Z","activity":{"tool_calls":40,"longest_repeat":1,"longest_failures":12,"verdict":"halt","rule":"tool_failures"}}`,
		`{"run":"` + run.ID + `","workflow":"sample","unit":"d","agent":"agent-a","result":"failed","reason":"guardrail: the unit made 400 tool calls","rounds":2,"work_s":10,"finished_at":"2026-09-25T10:04:00Z","activity":{"tool_calls":400,"longest_repeat":1,"longest_failures":0,"verdict":"halt","rule":"tool_calls"}}`,
	}
	if err := os.WriteFile(filepath.Join(store.RunPath(run.ID), "times.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rows, torn, err := store.Times(run.ID)
	if err != nil || torn || len(rows) != 5 || rows[0].Activity != nil || rows[3].Activity.LongestFailures != 12 {
		t.Fatalf("rows %#v torn=%t err=%v", rows, torn, err)
	}
	stats := SummarizeTimes(rows).Activity
	if stats == nil || stats.Units != 4 || stats.MedianCalls != 30 || stats.Warned != 1 || stats.Halted != 2 || stats.HaltsByRule[RuleToolFailures] != 1 || stats.HaltsByRule[RuleToolCalls] != 1 {
		t.Fatalf("activity stats %#v", stats)
	}
	if stats.P90Calls < 40 || stats.P90Calls > 400 {
		t.Fatalf("p90 %v", stats.P90Calls)
	}
	if SummarizeTimes(rows[:1]).Activity != nil {
		t.Fatal("rows without activity must not produce activity stats")
	}
}

func TestGuardrailMarksFollowTheCurrentAttempt(t *testing.T) {
	at := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	mark := func(unit, rule, level string) Event {
		return Event{At: at, Agent: "agent-a", State: "guardrail_" + level, Guardrail: &GuardrailRecord{Unit: unit, Rule: rule, Level: level}}
	}
	events := []Event{
		{Key: "u1", State: "working"},
		mark("u1", RuleToolCalls, "warn"),
		mark("u1", RuleToolFailures, "warn"),
		mark("u2", RuleToolFailures, "halt"),
		mark("u1", RuleToolFailures, "halt"),
	}
	marks := guardrailMarks(events, "u1")
	if len(marks) != 2 || marks[0] != (GuardrailMark{RuleToolFailures, "halt"}) || marks[1] != (GuardrailMark{RuleToolCalls, "warn"}) {
		t.Fatalf("marks %#v", marks)
	}
	// A revision keeps tool_calls and forgets the streak rules.
	revised := append(append([]Event(nil), events...), Event{Key: "u1", State: StateRevising})
	if marks := guardrailMarks(revised, "u1"); len(marks) != 1 || marks[0].Rule != RuleToolCalls {
		t.Fatalf("marks after revising %#v", marks)
	}
	// A finished attempt forgets everything (a retry starts clean).
	retried := append(append([]Event(nil), events...), Event{Key: "u1", State: "failed"})
	if marks := guardrailMarks(retried, "u1"); len(marks) != 0 {
		t.Fatalf("marks after the attempt ended %#v", marks)
	}
}
