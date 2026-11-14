package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func approveYAML(on string, rounds, weakAfter int, extra string) string {
	return budgetYAML("approve:\n  on: ["+on+"]\n"+extra, rounds, weakAfter)
}

// approvalRun creates a run whose follow-up prompt carries .Problems and ends
// with the unit key, so scriptedDriver and keyValidator can tell units apart.
func approvalRun(t *testing.T, yamlText string, keys []string, agents ...string) (*Store, Run, *scriptedDriver, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	store, run := makeRun(t, clock, yamlText, unitsJSON(keys...), "{{.Key}}", "fix [{{.Problems}}] {{.Key}}", agents...)
	return store, run, newScriptedDriver(clock), clock
}

// keyValidator answers per unit. It reads the unit from the last prompt sent
// to agent, so it is only exact for one agent at a time.
func keyValidator(driver *scriptedDriver, agent string, code func(key string, call int) int) func(context.Context, string, []string, time.Duration) (int, string, string, error) {
	var mu sync.Mutex
	calls := map[string]int{}
	return func(context.Context, string, []string, time.Duration) (int, string, string, error) {
		sends := driver.sendsFor(agent)
		key := promptKey(sends[len(sends)-1])
		mu.Lock()
		calls[key]++
		call := calls[key]
		mu.Unlock()
		return code(key, call), "problems in " + key, "", nil
	}
}

func latestState(t *testing.T, store *Store, runID string) map[string]Event {
	t.Helper()
	events, _, err := store.Events(runID)
	if err != nil {
		t.Fatal(err)
	}
	return latestEvents(events)
}

func runStatus(t *testing.T, store *Store, runID string) string {
	t.Helper()
	run, err := store.LoadRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	return run.Status
}

func TestApprovalParksOnlyListedResults(t *testing.T) {
	for _, test := range []struct {
		name     string
		on       string
		code     int
		parked   bool
		proposed string
	}{
		{name: "done listed", on: "done", code: 0, parked: true, proposed: "done"},
		{name: "done not listed", on: "weak, failed", code: 0, proposed: "done"},
		{name: "weak listed", on: "weak", code: 3, parked: true, proposed: "weak"},
		{name: "weak not listed", on: "done", code: 3, proposed: "weak"},
		{name: "failed listed", on: "failed", code: 2, parked: true, proposed: "failed"},
		{name: "failed not listed", on: "done, weak", code: 2, proposed: "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, run, driver, clock := approvalRun(t, approveYAML(test.on, 1, 1, ""), []string{"u1", "u2"}, "agent-a")
			notes := &notifications{}
			engine := supervisedEngine(store, driver, clock, notes)
			engine.Validator = keyValidator(driver, "agent-a", func(key string, _ int) int {
				if key == "u1" {
					return test.code
				}
				return 0
			})
			if err := engine.Run(context.Background(), run.ID, false); err != nil {
				t.Fatal(err)
			}
			// The agent took u2 right after u1, parked or not.
			if sends := driver.sendsFor("agent-a"); len(sends) != 2 || promptKey(sends[1]) != "u2" {
				t.Fatalf("agent did not take the next unit: %v", sends)
			}
			latest := latestState(t, store, run.ID)
			rows := timesByUnit(t, store, run.ID)
			if !test.parked {
				if latest["u1"].State != test.proposed || rows["u1"].Result != test.proposed || rows["u1"].Approval != nil {
					t.Fatalf("unlisted result must finish: event=%#v row=%#v", latest["u1"], rows["u1"])
				}
				return
			}
			parked := latest["u1"]
			if parked.State != StateAwaitingApproval || parked.Park == nil || parked.Park.Proposed != test.proposed || parked.Agent != "agent-a" || parked.Round != 1 {
				t.Fatalf("u1 did not park with its proposal: %#v", parked)
			}
			if row := parked.Park.Row; row.Result != test.proposed || row.Rounds != 1 || row.StartedAt == nil || row.WorkSeconds <= 0 || row.Approval == nil || row.Approval.Proposed != test.proposed {
				t.Fatalf("parked row lacks its metrics: %#v", row)
			}
			if _, ok := rows["u1"]; ok {
				t.Fatalf("a parked unit must not have a times row yet: %#v", rows["u1"])
			}
			if status := runStatus(t, store, run.ID); status != StatusAwaitingApproval {
				t.Fatalf("run status %s, want %s", status, StatusAwaitingApproval)
			}
			if notes.count("; list with bp workflow approvals") != 1 || notes.count("is waiting for decisions") != 1 || len(notes.messages) != 2 {
				t.Fatalf("notifications: %v", notes.messages)
			}
		})
	}
}

func TestApprovalUnjudgedFailuresDoNotPark(t *testing.T) {
	t.Run("delivery failure", func(t *testing.T) {
		store, run, driver, clock := approvalRun(t, approveYAML("failed", 1, 1, ""), []string{"u1", "u2"}, "agent-a")
		driver.failKeys["u1"] = true
		engine := supervisedEngine(store, driver, clock, nil)
		engine.Validator = alwaysCode(0)
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		rows := timesByUnit(t, store, run.ID)
		if rows["u1"].Result != "failed" || !strings.Contains(rows["u1"].Reason, "not delivered") || rows["u2"].Result != "done" || runStatus(t, store, run.ID) != "done" {
			t.Fatalf("a delivery failure must fail without parking: %#v status=%s", rows, runStatus(t, store, run.ID))
		}
	})
	t.Run("never started", func(t *testing.T) {
		store, run, driver, clock := approvalRun(t, approveYAML("failed", 1, 1, "timeouts:\n  start: 2s\n"), []string{"u1"}, "agent-a")
		driver.ignoreWork = true
		engine := supervisedEngine(store, driver, clock, nil)
		engine.Validator = alwaysCode(0)
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		if rows := timesByUnit(t, store, run.ID); rows["u1"].Result != "failed" || !strings.Contains(rows["u1"].Reason, "never started") {
			t.Fatalf("never started must fail without parking: %#v", rows)
		}
	})
	t.Run("validator failure parks", func(t *testing.T) {
		store, run, driver, clock := approvalRun(t, approveYAML("failed", 1, 1, ""), []string{"u1"}, "agent-a")
		engine := supervisedEngine(store, driver, clock, nil)
		engine.Validator = func(context.Context, string, []string, time.Duration) (int, string, string, error) {
			return -1, "", "", fmt.Errorf("validator crashed")
		}
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		if parked := latestState(t, store, run.ID)["u1"]; parked.State != StateAwaitingApproval || !strings.Contains(parked.Reason, "validator failed") {
			t.Fatalf("a judged failure must park: %#v", parked)
		}
	})
}

func TestApprovalUnitBudgetFailureParks(t *testing.T) {
	yamlText := approveYAML("failed", 3, 3, "budget:\n  unit_tokens: 100000\n")
	store, run, driver, clock := approvalRun(t, yamlText, []string{"u1"}, "agent-a")
	driver.metered, driver.perSend = true, TokenUse{Input: 200000}
	engine := supervisedEngine(store, driver, clock, nil)
	engine.Validator = alwaysCode(1)
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	parked := latestState(t, store, run.ID)["u1"]
	if parked.State != StateAwaitingApproval || parked.Park.Proposed != "failed" || !strings.Contains(parked.Reason, "unit token budget exhausted") || parked.Park.Row.Tokens == nil {
		t.Fatalf("budget failure did not park with tokens: %#v", parked)
	}
	// Parked spend counts for the run budget although no row is written.
	if spent, _, err := store.RunSpend(run.ID); err != nil || spent != 200000 {
		t.Fatalf("RunSpend=%d err=%v", spent, err)
	}
}

func TestParkedUnitsAreNeutralForBreakerAndMeltdown(t *testing.T) {
	yamlText := approveYAML("failed", 1, 1, "supervise:\n  breaker:\n    failures: 2\n    open_for: 240h\n  meltdown:\n    failures: 2\n    window: 30m\n")
	keys := []string{"u1", "u2", "u3", "u4"}
	store, run, driver, clock := approvalRun(t, yamlText, keys, "agent-a")
	notes := &notifications{}
	engine := supervisedEngine(store, driver, clock, notes)
	engine.Validator = alwaysCode(2)
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	if len(driver.sendsFor("agent-a")) != 4 || len(eventsWithState(t, store, run.ID, eventBreakerOpen)) != 0 || len(eventsWithState(t, store, run.ID, eventRunPaused)) != 0 {
		t.Fatalf("parked units moved breaker or meltdown: sends=%d notes=%v", len(driver.sendsFor("agent-a")), notes.messages)
	}
	if status := runStatus(t, store, run.ID); status != StatusAwaitingApproval {
		t.Fatalf("status %s", status)
	}
	// Human rejections do not count either.
	if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionReject, All: true, Text: "wrong", By: "owner"}); err != nil {
		t.Fatal(err)
	}
	events, _, _ := store.Events(run.ID)
	if breakers := deriveBreakers(events, run.SupervisionStart()); breakers["agent-a"] != nil && breakers["agent-a"].consecutive != 0 {
		t.Fatalf("rejections counted for the breaker: %#v", breakers["agent-a"])
	}
	current, _ := store.LoadRun(run.ID)
	definition, _ := ReadDefinition(store.SnapshotDir(run.ID))
	if sup := newSupervision(definition, current, events, nil); len(sup.failures) != 0 {
		t.Fatalf("rejections counted for meltdown: %v", sup.failures)
	}
	status, err := store.Status(run.ID)
	if err != nil || len(status.Breakers) != 0 || status.Counts.Failed != 4 || status.Status != "done" {
		t.Fatalf("status after rejecting all: %#v err=%v", status, err)
	}
	for _, row := range timesByUnit(t, store, run.ID) {
		if row.Result != "failed" || row.Reason != "rejected: wrong" || row.Approval == nil || row.Approval.Decision != "rejected" {
			t.Fatalf("rejected row: %#v", row)
		}
	}
}

func TestApprovalNotificationThrottle(t *testing.T) {
	t.Run("a burst notifies once", func(t *testing.T) {
		store, run, driver, clock := approvalRun(t, approveYAML("done", 1, 1, ""), []string{"u1", "u2", "u3"}, "agent-a")
		notes := &notifications{}
		engine := supervisedEngine(store, driver, clock, notes)
		engine.Validator = alwaysCode(0)
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		if notes.count(": 1 units await approval") != 1 || notes.count("; list with bp workflow approvals") != 1 || notes.count("is waiting for decisions: 3 units await approval") != 1 || len(notes.messages) != 2 {
			t.Fatalf("notifications: %v", notes.messages)
		}
		if len(eventsWithState(t, store, run.ID, eventApprovalNotice)) != 1 {
			t.Fatal("the notice was not recorded")
		}
	})
	t.Run("one notice per ten minutes while units keep parking", func(t *testing.T) {
		store, run, driver, clock := approvalRun(t, approveYAML("done", 1, 1, ""), []string{"u1", "u2", "u3", "u4"}, "agent-a")
		driver.beforeSend = func(_, key string) {
			if key == "u3" {
				clock.Advance(11 * time.Minute)
			}
		}
		notes := &notifications{}
		engine := supervisedEngine(store, driver, clock, notes)
		engine.Validator = alwaysCode(0)
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		if notes.count(": 1 units await approval;") != 1 || notes.count(": 3 units await approval;") != 1 || notes.count("; list with bp workflow approvals") != 2 {
			t.Fatalf("notifications: %v", notes.messages)
		}
	})
	t.Run("a restarted worker keeps the throttle", func(t *testing.T) {
		store, run, driver, clock := approvalRun(t, approveYAML("done", 1, 1, ""), []string{"u1", "u2"}, "agent-a")
		if err := store.AppendEvent(run.ID, Event{At: clock.Now(), State: eventApprovalNotice, Reason: "1 units await approval"}); err != nil {
			t.Fatal(err)
		}
		notes := &notifications{}
		engine := supervisedEngine(store, driver, clock, notes)
		engine.Validator = alwaysCode(0)
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		if notes.count("; list with bp workflow approvals") != 0 || notes.count("waiting for decisions") != 1 {
			t.Fatalf("notifications: %v", notes.messages)
		}
	})
}

func TestSupervisorDoesNotRestartAwaitingApprovalRun(t *testing.T) {
	clock := newFakeClock()
	store, run := makeRun(t, clock, baseYAML(""), unitsJSON("u1"), "{{.Key}}", "", "agent-a")
	attempts := 0
	launch := func(_ context.Context, id string) (ChildWait, error) {
		attempts++
		return func() error {
			_, err := store.UpdateRun(id, func(current *Run) error {
				current.Status = StatusAwaitingApproval
				return nil
			})
			if err != nil {
				return err
			}
			return os.ErrClosed
		}, nil
	}
	supervisor := NewSupervisor(store, store.StateDir, launch, nil)
	supervisor.Delay = func(context.Context, time.Duration) error { return nil }
	if err := supervisor.Start(run.ID); err != nil {
		t.Fatal(err)
	}
	waitSupervisor(t, supervisor, run.ID)
	if attempts != 1 {
		t.Fatalf("supervisor restarted an awaiting-approval run %d times", attempts-1)
	}
	if err := supervisor.Start(run.ID); err == nil || !strings.Contains(err.Error(), "not resumable") {
		t.Fatalf("awaiting-approval run must not start, got %v", err)
	}
	if err := NewSupervisor(store, store.StateDir, launch, nil).restore(); err != nil || attempts != 1 {
		t.Fatalf("daemon startup restored an awaiting-approval run: attempts=%d err=%v", attempts, err)
	}
	if stopped, err := store.RequestStop(run.ID); err != nil || stopped.Status != StatusAwaitingApproval || stopped.StopRequested {
		t.Fatalf("stop changed an awaiting-approval run: %#v err=%v", stopped, err)
	}
}

// parkedRun runs units that all park: u1 proposes done, u2 weak, u3 failed.
func parkedRun(t *testing.T) (*Store, Run, *fakeClock) {
	t.Helper()
	store, run, driver, clock := approvalRun(t, approveYAML("done, weak, failed", 1, 1, ""), []string{"u1", "u2", "u3"}, "agent-a")
	engine := supervisedEngine(store, driver, clock, nil)
	engine.Validator = keyValidator(driver, "agent-a", func(key string, _ int) int {
		return map[string]int{"u1": 0, "u2": 3, "u3": 2}[key]
	})
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	if status := runStatus(t, store, run.ID); status != StatusAwaitingApproval {
		t.Fatalf("status %s", status)
	}
	return store, run, clock
}

func TestApproveRejectDecisions(t *testing.T) {
	t.Run("approve maps proposals and the last decision ends the run", func(t *testing.T) {
		store, run, clock := parkedRun(t)
		approvals, err := store.Approvals(run.ID)
		if err != nil || len(approvals) != 3 || approvals[0].Unit != "u1" || approvals[1].Proposed != "weak" || approvals[2].Reason != "validator exited with code 2" {
			t.Fatalf("approvals %#v err=%v", approvals, err)
		}
		clock.Advance(5 * time.Minute)
		report, err := store.Decide(run.ID, DecideOptions{Action: DecisionApprove, Units: []string{"u1", "u2"}, Text: "looks right", By: "boss"})
		if err != nil || len(report.Outcomes) != 2 || report.Finished || report.Left.Parked != 1 || report.Run.Status != StatusAwaitingApproval {
			t.Fatalf("first decision: %#v err=%v", report, err)
		}
		report, err = store.Decide(run.ID, DecideOptions{Action: DecisionApprove, Units: []string{"u3"}, By: "boss"})
		if err != nil || !report.Finished || report.Run.Status != "done" {
			t.Fatalf("last decision did not finish the run: %#v err=%v", report, err)
		}
		rows, _, _ := store.Times(run.ID)
		if len(rows) != 3 {
			t.Fatalf("times rows %#v", rows)
		}
		want := map[string]string{"u1": "done", "u2": "weak", "u3": "weak"}
		for _, row := range rows {
			if row.Result != want[row.Unit] || row.Approval == nil || row.Approval.Decision != "approved" || row.Approval.By != "boss" || row.Approval.WaitSeconds < 300 || row.Approval.DecidedAt == nil || row.WorkSeconds <= 0 || row.Rounds != 1 {
				t.Fatalf("decided row %#v approval=%#v", row, row.Approval)
			}
			if row.Unit == "u1" && row.Approval.Note != "looks right" {
				t.Fatalf("note not recorded: %#v", row.Approval)
			}
		}
		latest := latestState(t, store, run.ID)
		if event := latest["u3"]; event.State != "weak" || event.Decision == nil || event.Decision.Action != DecisionApprove || event.Decision.By != "boss" {
			t.Fatalf("terminal decision event %#v", event)
		}
		// Idempotency: a decided unit is no longer parked.
		if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionReject, Units: []string{"u1"}, By: "boss"}); err == nil || !strings.Contains(err.Error(), `unit "u1" is not awaiting approval (state done)`) {
			t.Fatalf("second decision: %v", err)
		}
		if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionApprove, All: true, By: "boss"}); err == nil || !strings.Contains(err.Error(), "no unit") {
			t.Fatalf("--all with nothing parked: %v", err)
		}
		stats := SummarizeTimes(rows)
		if stats.Approvals == nil || stats.Approvals.Approved != 3 || stats.Approvals.Rejected != 0 || stats.Approvals.MedianWait < 300 || stats.Count != 3 {
			t.Fatalf("approval stats %#v", stats.Approvals)
		}
	})
	t.Run("reject all with a reason", func(t *testing.T) {
		store, run, _ := parkedRun(t)
		report, err := store.Decide(run.ID, DecideOptions{Action: DecisionReject, All: true, Text: "wrong scale", By: "boss"})
		if err != nil || len(report.Outcomes) != 3 || !report.Finished {
			t.Fatalf("reject --all: %#v err=%v", report, err)
		}
		for _, row := range timesByUnit(t, store, run.ID) {
			if row.Result != "failed" || row.Reason != "rejected: wrong scale" || row.Approval.Decision != "rejected" {
				t.Fatalf("rejected row %#v", row)
			}
		}
	})
	t.Run("one bad unit name writes nothing", func(t *testing.T) {
		store, run, _ := parkedRun(t)
		if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionApprove, Units: []string{"u1"}, By: "boss"}); err != nil {
			t.Fatal(err)
		}
		_, err := store.Decide(run.ID, DecideOptions{Action: DecisionApprove, Units: []string{"u2", "u1"}, By: "boss"})
		if err == nil || !strings.Contains(err.Error(), "state done") {
			t.Fatalf("mixed decision: %v", err)
		}
		if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionApprove, Units: []string{"nope"}, By: "boss"}); err == nil || !strings.Contains(err.Error(), "has no state") {
			t.Fatalf("unknown unit: %v", err)
		}
		if latest := latestState(t, store, run.ID); latest["u2"].State != StateAwaitingApproval {
			t.Fatalf("u2 was decided by a failed call: %#v", latest["u2"])
		}
	})
	t.Run("options are checked", func(t *testing.T) {
		store, run, _ := parkedRun(t)
		for _, options := range []DecideOptions{
			{Action: DecisionApprove, By: "boss"},
			{Action: DecisionApprove, All: true, Units: []string{"u1"}, By: "boss"},
			{Action: DecisionRevise, Units: []string{"u1", "u2"}, Text: "x", By: "boss"},
			{Action: DecisionRevise, Units: []string{"u1"}, By: "boss"},
			{Action: "maybe", Units: []string{"u1"}},
		} {
			if _, err := store.Decide(run.ID, options); err == nil {
				t.Fatalf("options %#v were accepted", options)
			}
		}
	})
}

func TestConcurrentDecisionsWriteOneTerminalEvent(t *testing.T) {
	store, run, _ := parkedRun(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for index, action := range []string{DecisionApprove, DecisionReject} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A separate Store is a separate process: only the file lock
			// serializes them.
			other := NewStore(store.StateDir)
			_, errs[index] = other.Decide(run.ID, DecideOptions{Action: action, Units: []string{"u1"}, By: "boss"})
		}()
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("exactly one decision must win: %v", errs)
	}
	events, _, _ := store.Events(run.ID)
	terminal := 0
	for _, event := range events {
		if event.Key == "u1" && terminalState(event.State) {
			terminal++
		}
	}
	rows, _, _ := store.Times(run.ID)
	if terminal != 1 || len(rows) != 1 {
		t.Fatalf("terminal events=%d rows=%d", terminal, len(rows))
	}
}

func TestDecisionWhileWorkerAliveIsHonoredAtRunEnd(t *testing.T) {
	store, run, driver, clock := approvalRun(t, approveYAML("weak", 1, 1, ""), []string{"u1", "u2"}, "agent-a")
	decided := false
	driver.beforeSend = func(_, key string) {
		if key == "u2" {
			if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionApprove, Units: []string{"u1"}, By: "owner"}); err != nil {
				t.Error(err)
			}
			decided = true
		}
	}
	notes := &notifications{}
	engine := supervisedEngine(store, driver, clock, notes)
	engine.Validator = keyValidator(driver, "agent-a", func(key string, _ int) int {
		if key == "u1" {
			return 3
		}
		return 0
	})
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	rows := timesByUnit(t, store, run.ID)
	if !decided || runStatus(t, store, run.ID) != "done" || rows["u1"].Result != "weak" || rows["u1"].Approval == nil || rows["u2"].Result != "done" || len(rows) != 2 {
		t.Fatalf("decision during the run was lost: status=%s rows=%#v", runStatus(t, store, run.ID), rows)
	}
	if notes.count("run "+run.ID+" done") != 1 || notes.count("waiting for decisions") != 0 {
		t.Fatalf("notifications: %v", notes.messages)
	}
}

func TestRevisionGoesToSameAgentAheadOfNewUnits(t *testing.T) {
	store, run, driver, clock := approvalRun(t, approveYAML("weak", 1, 1, ""), []string{"u1", "u2", "u3"}, "agent-a")
	driver.beforeSend = func(_, key string) {
		if key == "u2" && !strings.HasPrefix(driver.sendsFor("agent-a")[len(driver.sendsFor("agent-a"))-1], "fix") {
			clock.Advance(time.Minute)
			report, err := store.Decide(run.ID, DecideOptions{Action: DecisionRevise, Units: []string{"u1"}, Text: "use metric units", By: "owner"})
			if err != nil || report.Outcomes[0].Result != StateRevise {
				t.Errorf("revise: %#v %v", report, err)
			}
		}
	}
	engine := supervisedEngine(store, driver, clock, nil)
	engine.Validator = keyValidator(driver, "agent-a", func(key string, call int) int {
		if key == "u1" && call == 1 {
			return 3
		}
		return 0
	})
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	sends := driver.sendsFor("agent-a")
	if len(sends) != 4 || promptKey(sends[0]) != "u1" || promptKey(sends[1]) != "u2" || sends[2] != "fix [use metric units] u1" || promptKey(sends[3]) != "u3" {
		t.Fatalf("revision was not sent to the same agent ahead of new units: %q", sends)
	}
	rows := timesByUnit(t, store, run.ID)
	row := rows["u1"]
	if row.Result != "done" || row.Rounds != 2 || len(row.RoundSeconds) != 2 || row.Approval == nil || row.Approval.Revisions != 1 || row.Approval.WaitSeconds < 60 || row.Approval.Decision != "" || len(rows) != 3 {
		t.Fatalf("revised unit row %#v approval=%#v", row, row.Approval)
	}
	if runStatus(t, store, run.ID) != "done" {
		t.Fatalf("status %s", runStatus(t, store, run.ID))
	}
	stats := SummarizeTimes([]TimeRow{row})
	if stats.Approvals == nil || stats.Approvals.Revised != 1 || stats.Approvals.Approved != 0 {
		t.Fatalf("stats %#v", stats.Approvals)
	}
}

func TestRevisionAfterRoundsExhaustedAndReparks(t *testing.T) {
	store, run, driver, clock := approvalRun(t, approveYAML("weak", 2, 2, ""), []string{"u1"}, "agent-a")
	engine := supervisedEngine(store, driver, clock, nil)
	engine.Validator = alwaysCode(1)
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	if parked := latestState(t, store, run.ID)["u1"]; parked.State != StateAwaitingApproval || parked.Round != 2 || parked.Park.Proposed != "weak" {
		t.Fatalf("u1 did not park after two rounds: %#v", parked)
	}
	if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionRevise, Units: []string{"u1"}, Text: "round the scores", By: "owner"}); err != nil {
		t.Fatal(err)
	}
	approvals, _ := store.Approvals(run.ID)
	if len(approvals) != 1 || approvals[0].State != StateRevise || approvals[0].RevisionBy != "owner" {
		t.Fatalf("approvals while the revision is pending: %#v", approvals)
	}
	if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionRevise, Units: []string{"u1"}, Text: "more", By: "owner"}); err == nil || !strings.Contains(err.Error(), "already has a revision pending on agent-a") {
		t.Fatalf("a second revision request: %v", err)
	}
	if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionApprove, All: true, By: "owner"}); err == nil || !strings.Contains(err.Error(), "--all does not cover the 1 units with a pending revision") {
		t.Fatalf("--all must not cancel a pending revision: %v", err)
	}
	// No worker runs: the revision waits for resume.
	if _, err := store.Resume(run.ID, ResumeOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	sends := driver.sendsFor("agent-a")
	if len(sends) != 3 || sends[2] != "fix [round the scores] u1" {
		t.Fatalf("the exhausted unit did not get its revision round: %q", sends)
	}
	parked := latestState(t, store, run.ID)["u1"]
	if parked.State != StateAwaitingApproval || parked.Round != 3 || parked.Park.Row.Approval.Revisions != 1 || runStatus(t, store, run.ID) != StatusAwaitingApproval {
		t.Fatalf("u1 did not park again after the revision: %#v status=%s", parked, runStatus(t, store, run.ID))
	}
	if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionApprove, Units: []string{"u1"}, By: "owner"}); err != nil {
		t.Fatal(err)
	}
	row := timesByUnit(t, store, run.ID)["u1"]
	if row.Result != "weak" || row.Rounds != 3 || row.Approval.Revisions != 1 || row.Approval.Decision != "approved" || runStatus(t, store, run.ID) != "done" {
		t.Fatalf("final row %#v approval=%#v", row, row.Approval)
	}

	t.Run("a revision that is not delivered parks the earlier proposal again", func(t *testing.T) {
		store, run, driver, clock := approvalRun(t, approveYAML("weak", 1, 1, ""), []string{"u1"}, "agent-a")
		engine := supervisedEngine(store, driver, clock, nil)
		engine.Validator = alwaysCode(3)
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionRevise, Units: []string{"u1"}, Text: "again", By: "owner"}); err != nil {
			t.Fatal(err)
		}
		driver.failKeys["u1"] = true
		if _, err := store.Resume(run.ID, ResumeOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		parked := latestState(t, store, run.ID)["u1"]
		if parked.State != StateAwaitingApproval || parked.Park.Proposed != "weak" || !strings.Contains(parked.Reason, "revision round failed: not delivered") || len(timesByUnit(t, store, run.ID)) != 0 {
			t.Fatalf("failed revision: %#v", parked)
		}
	})
}

func TestRevisionWithTwoAgentsUsesTheProducingAgent(t *testing.T) {
	store, run, driver, clock := approvalRun(t, approveYAML("weak", 1, 1, ""), []string{"u1", "u2"}, "agent-a", "agent-b")
	// u1 was produced and parked by agent-b in an earlier worker.
	at := clock.Now()
	row := TimeRow{Run: run.ID, Workflow: run.Workflow, Unit: "u1", Agent: "agent-b", Result: "weak", Reason: "unit exhausted", Rounds: 1, FinishedAt: at, WorkSeconds: 5, Approval: &RowApproval{Proposed: "weak"}}
	if err := store.AppendEvent(run.ID, Event{At: at, Unit: "u1", Key: "u1", Agent: "agent-b", State: StateAwaitingApproval, Round: 1, Reason: "unit exhausted", Park: &ParkRecord{Proposed: "weak", Started: at.Add(-time.Minute), Row: row}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionRevise, Units: []string{"u1"}, Text: "be precise", By: "owner"}); err != nil {
		t.Fatal(err)
	}
	engine := supervisedEngine(store, driver, clock, nil)
	engine.Validator = alwaysCode(0)
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	for _, send := range driver.sendsFor("agent-a") {
		if promptKey(send) == "u1" {
			t.Fatalf("the revision went to another agent: %q", driver.sendsFor("agent-a"))
		}
	}
	found := false
	for _, send := range driver.sendsFor("agent-b") {
		found = found || send == "fix [be precise] u1"
	}
	result := timesByUnit(t, store, run.ID)["u1"]
	if !found || result.Agent != "agent-b" || result.Result != "done" || result.Rounds != 2 || result.WorkSeconds <= 5 || runStatus(t, store, run.ID) != "done" {
		t.Fatalf("revision on agent-b: sends=%q row=%#v", driver.sendsFor("agent-b"), result)
	}
}

func TestApprovalRestartSafety(t *testing.T) {
	t.Run("parked units are not sent again", func(t *testing.T) {
		store, run, driver, clock := approvalRun(t, approveYAML("done", 1, 1, ""), []string{"u1", "u2"}, "agent-a")
		engine := supervisedEngine(store, driver, clock, nil)
		engine.Validator = alwaysCode(0)
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		// A worker that died after parking is restarted by the supervisor.
		if _, err := store.UpdateRun(run.ID, func(current *Run) error { current.Status = "running"; return nil }); err != nil {
			t.Fatal(err)
		}
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		if len(driver.sendsFor("agent-a")) != 2 || runStatus(t, store, run.ID) != StatusAwaitingApproval {
			t.Fatalf("restart resent parked units: sends=%d status=%s", len(driver.sendsFor("agent-a")), runStatus(t, store, run.ID))
		}
		current, _ := store.LoadRun(run.ID)
		if current.AgentUnits["agent-a"] != 2 {
			t.Fatalf("agent unit count after restart: %v", current.AgentUnits)
		}
	})
	t.Run("a claimed revision runs once after a restart", func(t *testing.T) {
		store, run, driver, clock := approvalRun(t, approveYAML("weak", 1, 1, ""), []string{"u1"}, "agent-a")
		engine := supervisedEngine(store, driver, clock, nil)
		engine.Validator = alwaysCode(3)
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionRevise, Units: []string{"u1"}, Text: "fix it", By: "owner"}); err != nil {
			t.Fatal(err)
		}
		// The previous worker claimed the revision and died before sending.
		if _, ok, err := store.claimRevision(run.ID, "u1", "agent-a"); err != nil || !ok {
			t.Fatalf("claim ok=%t err=%v", ok, err)
		}
		if _, err := store.Resume(run.ID, ResumeOptions{}); err != nil {
			t.Fatal(err)
		}
		engine.Validator = alwaysCode(0)
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		sends := driver.sendsFor("agent-a")
		if len(sends) != 2 || sends[1] != "fix [fix it] u1" || timesByUnit(t, store, run.ID)["u1"].Result != "done" {
			t.Fatalf("claimed revision after restart: %q", sends)
		}
	})
	t.Run("a revision round in flight is validated without resending", func(t *testing.T) {
		store, run, driver, clock := approvalRun(t, approveYAML("weak", 1, 1, ""), []string{"u1"}, "agent-a")
		engine := supervisedEngine(store, driver, clock, nil)
		engine.Validator = alwaysCode(3)
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionRevise, Units: []string{"u1"}, Text: "fix it", By: "owner"}); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := store.claimRevision(run.ID, "u1", "agent-a"); err != nil || !ok {
			t.Fatalf("claim ok=%t err=%v", ok, err)
		}
		at := clock.Now()
		for _, state := range []string{"sending", "sent", "working"} {
			if err := store.AppendEvent(run.ID, Event{At: at, Unit: "u1", Key: "u1", Agent: "agent-a", State: state, Round: 2, DeliveryID: "q1"}); err != nil {
				t.Fatal(err)
			}
		}
		// A torn line from the crash must not break recovery.
		file, err := os.OpenFile(filepath.Join(store.RunPath(run.ID), "units.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = file.WriteString(`{"at":"2026-09-25T10:00:00Z","key":"u1","state":"vali`)
		_ = file.Close()
		if _, err := store.Resume(run.ID, ResumeOptions{}); err != nil {
			t.Fatal(err)
		}
		engine.Validator = alwaysCode(0)
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		row := timesByUnit(t, store, run.ID)["u1"]
		if len(driver.sendsFor("agent-a")) != 1 || row.Result != "done" || row.Rounds != 2 || row.Approval == nil || row.Approval.Revisions != 1 || row.WorkSeconds <= 0 {
			t.Fatalf("in-flight revision after restart: sends=%d row=%#v", len(driver.sendsFor("agent-a")), row)
		}
	})
	t.Run("a torn log line is tolerated by approvals and decisions", func(t *testing.T) {
		store, run, _ := parkedRun(t)
		file, err := os.OpenFile(filepath.Join(store.RunPath(run.ID), "units.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = file.WriteString(`{"at":"2026-09-25T10:00:00Z","key":"u1","sta` + "\n")
		_ = file.Close()
		if approvals, err := store.Approvals(run.ID); err != nil || len(approvals) != 3 {
			t.Fatalf("approvals with a torn line: %d err=%v", len(approvals), err)
		}
		if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionApprove, All: true, By: "owner"}); err != nil {
			t.Fatal(err)
		}
		if status, err := store.Status(run.ID); err != nil || !status.TornLog || status.Status != "done" || status.Counts.Done != 1 || status.Counts.Weak != 2 {
			t.Fatalf("status %#v err=%v", status, err)
		}
	})
}

func TestStatusCountsParkedUnits(t *testing.T) {
	store, run, _ := parkedRun(t)
	if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionRevise, Units: []string{"u3"}, Text: "x", By: "owner"}); err != nil {
		t.Fatal(err)
	}
	status, err := store.Status(run.ID)
	if err != nil || status.Counts.AwaitingApproval != 2 || status.Counts.Pending != 1 || status.Counts.Done != 0 {
		t.Fatalf("counts %#v err=%v", status.Counts, err)
	}
	for _, agent := range status.Agents {
		if agent.Elapsed != 0 {
			t.Fatalf("a parked unit or decision counted as agent activity: %#v", agent)
		}
	}
	if work, err := store.ResumeWork(run.ID, false); err != nil || work != (RunWork{Parked: 2, Revisions: 1}) {
		t.Fatalf("resume work %#v err=%v", work, err)
	}
	if count, err := store.AwaitingApproval(run.Workflow); err != nil || count != 2 {
		t.Fatalf("awaiting by workflow name: %d err=%v", count, err)
	}
}

// A rejected unit is failed, so resume --retry-failed sends it again. That
// works while the run is not done, that is while other units still wait.
func TestRetryFailedRetriesRejectedUnits(t *testing.T) {
	store, run, driver, clock := approvalRun(t, approveYAML("failed", 1, 1, ""), []string{"u1", "u2"}, "agent-a")
	engine := supervisedEngine(store, driver, clock, nil)
	engine.Validator = alwaysCode(2)
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionReject, Units: []string{"u1"}, By: "owner"}); err != nil {
		t.Fatal(err)
	}
	if work, err := store.ResumeWork(run.ID, true); err != nil || work != (RunWork{Pending: 1, Parked: 1}) {
		t.Fatalf("resume work %#v err=%v", work, err)
	}
	if _, err := store.Resume(run.ID, ResumeOptions{RetryFailed: true}); err != nil {
		t.Fatal(err)
	}
	engine.Validator = alwaysCode(0)
	if err := engine.Run(context.Background(), run.ID, true); err != nil {
		t.Fatal(err)
	}
	sends := driver.sendsFor("agent-a")
	rows := timesByUnit(t, store, run.ID)
	if len(sends) != 3 || promptKey(sends[2]) != "u1" || rows["u1"].Result != "done" || runStatus(t, store, run.ID) != StatusAwaitingApproval {
		t.Fatalf("a rejected unit was not retried: sends=%q rows=%#v", sends, rows)
	}
	if latest := latestState(t, store, run.ID); latest["u2"].State != StateAwaitingApproval {
		t.Fatalf("u2 is no longer parked: %#v", latest["u2"])
	}
}

func TestRevisionTokensExcludeParkedTime(t *testing.T) {
	store, run, driver, clock := approvalRun(t, approveYAML("weak", 1, 1, "budget:\n  run_tokens: 100000000\n"), []string{"u1", "u2"}, "agent-a")
	driver.metered, driver.perSend = true, TokenUse{Input: 1000}
	engine := supervisedEngine(store, driver, clock, nil)
	engine.Validator = keyValidator(driver, "agent-a", func(key string, call int) int {
		if key == "u1" && call == 1 {
			return 3
		}
		return 0
	})
	driver.beforeSend = func(_, key string) {
		sends := driver.sendsFor("agent-a")
		if key == "u2" && len(sends) == 1 {
			if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionRevise, Units: []string{"u1"}, Text: "more", By: "owner"}); err != nil {
				t.Error(err)
			}
		}
	}
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	rows := timesByUnit(t, store, run.ID)
	// u1: first round (1000) + revision round (1000); u2's send between them
	// is not u1's.
	if tokens := rows["u1"].Tokens; tokens == nil || tokens.Input != 2000 || tokens.Prior == nil || tokens.Prior.Input != 1000 || rows["u2"].Tokens.Input != 1000 {
		t.Fatalf("u1 tokens %#v u2 %#v", rows["u1"].Tokens, rows["u2"].Tokens)
	}
	if spent, _, err := store.RunSpend(run.ID); err != nil || spent != 3000 {
		t.Fatalf("run spend %d err=%v", spent, err)
	}
	// Settling a row with a prior keeps the prior and re-measures the rest.
	settled, unsettled := SettleTokens([]TimeRow{{Agent: "agent-a", FinishedAt: rows["u1"].FinishedAt, Tokens: &UnitTokens{Input: 1500, Weighted: 1500, Since: rows["u1"].Tokens.Since, AsOf: rows["u1"].Tokens.Since, Prior: &TokenCounts{Input: 1000, Weighted: 1000}}}}, 10,
		func(string, time.Time, time.Time) (TokenUse, bool) {
			return TokenUse{Input: 1000, AsOf: rows["u1"].FinishedAt}, true
		})
	if unsettled != 0 || settled[0].Tokens.Input != 2000 || settled[0].Tokens.Weighted != 2000 || settled[0].Tokens.Prior.Input != 1000 {
		t.Fatalf("settle with prior: %#v unsettled=%d", settled[0].Tokens, unsettled)
	}
}

func TestApproveDefinitionValidation(t *testing.T) {
	base, err := parseDefinitionYAML(baseYAML(""))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		on      []string
		wantErr string
	}{
		{on: []string{"done", "weak", "failed"}},
		{on: []string{"weak"}},
		{on: nil, wantErr: "approve.on must list at least one"},
		{on: []string{"ok"}, wantErr: `unknown result "ok"`},
		{on: []string{"weak", "weak"}, wantErr: `duplicate result "weak"`},
	} {
		d := base
		d.Approve = &ApproveSpec{On: test.on}
		err := ValidateDefinition(d)
		if test.wantErr == "" && err != nil || test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
			t.Fatalf("on=%v: err=%v want %q", test.on, err, test.wantErr)
		}
	}
	if _, err := parseDefinitionYAML(baseYAML("approve:\n  on: [weak]\n  when: always\n")); err == nil {
		t.Fatal("unknown key under approve was accepted")
	}
	parsed, err := parseDefinitionYAML(baseYAML("approve:\n  on: [weak, failed]\n"))
	if err != nil || !parsed.Approves("weak") || !parsed.Approves("failed") || parsed.Approves("done") {
		t.Fatalf("parsed approve %#v err=%v", parsed.Approve, err)
	}
	// Without the key nothing changes: no approve in either encoding.
	if base.Approve != nil || base.Approves("done") {
		t.Fatalf("definition without approve: %#v", base.Approve)
	}
	encoded, err := yaml.Marshal(base)
	if err != nil || strings.Contains(string(encoded), "approve") {
		t.Fatalf("yaml round trip: %s err=%v", encoded, err)
	}
	jsonEncoded, err := json.Marshal(base)
	if err != nil || strings.Contains(string(jsonEncoded), "approve") {
		t.Fatalf("json round trip: %s err=%v", jsonEncoded, err)
	}
	var again Definition
	if err := yaml.Unmarshal(encoded, &again); err != nil || ValidateDefinition(again) != nil || again.Approve != nil {
		t.Fatalf("re-decoded definition %#v err=%v", again, err)
	}
}

// parseDefinitionYAML reads a workflow.yaml through ReadDefinition with its
// prompt file in place.
func parseDefinitionYAML(text string) (Definition, error) {
	dir, err := os.MkdirTemp("", "workflow-def-")
	if err != nil {
		return Definition{}, err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "workflow.yaml"), []byte(text), 0600); err != nil {
		return Definition{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("{{.Key}}"), 0600); err != nil {
		return Definition{}, err
	}
	return ReadDefinition(dir)
}

// weakParkedRun runs keys on agent-a; every unit proposes weak and parks.
func weakParkedRun(t *testing.T, keys ...string) (*Store, Run, *scriptedDriver, *fakeClock, *Engine) {
	t.Helper()
	store, run, driver, clock := approvalRun(t, approveYAML("weak", 1, 1, ""), keys, "agent-a")
	engine := supervisedEngine(store, driver, clock, nil)
	engine.Validator = alwaysCode(3)
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	if status := runStatus(t, store, run.ID); status != StatusAwaitingApproval {
		t.Fatalf("status %s", status)
	}
	return store, run, driver, clock, engine
}

func TestDecisionCancelsPendingRevision(t *testing.T) {
	for _, test := range []struct {
		action, result, reason, decision string
	}{
		{action: DecisionApprove, result: "weak", reason: "still wrong", decision: "approved"},
		{action: DecisionReject, result: "failed", reason: "rejected: not worth it", decision: "rejected"},
	} {
		t.Run(test.action, func(t *testing.T) {
			store, run, driver, clock, _ := weakParkedRun(t, "u1")
			clock.Advance(2 * time.Minute)
			if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionRevise, Units: []string{"u1"}, Text: "redo", By: "owner"}); err != nil {
				t.Fatal(err)
			}
			clock.Advance(3 * time.Minute)
			text := ""
			if test.action == DecisionReject {
				text = "not worth it"
			}
			report, err := store.Decide(run.ID, DecideOptions{Action: test.action, Units: []string{"u1"}, Text: text, By: "boss"})
			if err != nil || len(report.Outcomes) != 1 || !report.Outcomes[0].CancelledRevision || report.Outcomes[0].Result != test.result || report.Outcomes[0].Wait < 300 || report.Outcomes[0].Wait >= 301 {
				t.Fatalf("cancel by %s: %#v err=%v", test.action, report, err)
			}
			// The run's last open item was that revision.
			if !report.Finished || runStatus(t, store, run.ID) != "done" {
				t.Fatalf("the run did not finish: %#v status=%s", report, runStatus(t, store, run.ID))
			}
			rows, _, _ := store.Times(run.ID)
			if len(rows) != 1 {
				t.Fatalf("times rows %#v", rows)
			}
			row := rows[0]
			if row.Result != test.result || row.Reason != test.reason || row.Approval == nil || row.Approval.Decision != test.decision || row.Approval.By != "boss" || row.Approval.Revisions != 1 || row.Approval.WaitSeconds < 300 || row.Approval.WaitSeconds >= 301 || row.Rounds != 1 {
				t.Fatalf("row %#v approval=%#v", row, row.Approval)
			}
			if event := latestState(t, store, run.ID)["u1"]; event.State != test.result || event.Decision == nil || !event.Decision.CancelledRevision {
				t.Fatalf("terminal event %#v", event)
			}
			if approvals, err := store.Approvals(run.ID); err != nil || len(approvals) != 0 {
				t.Fatalf("approvals still list the unit: %#v err=%v", approvals, err)
			}
			if _, ok, err := store.claimRevision(run.ID, "u1", "agent-a"); err != nil || ok {
				t.Fatalf("a cancelled revision was claimable: ok=%t err=%v", ok, err)
			}
			if len(driver.sendsFor("agent-a")) != 1 {
				t.Fatalf("sends %q", driver.sendsFor("agent-a"))
			}
		})
	}
}

func TestCancelledRevisionIsNotSentByRestartedWorker(t *testing.T) {
	store, run, driver, _, engine := weakParkedRun(t, "u1", "u2")
	if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionRevise, Units: []string{"u1"}, Text: "redo", By: "owner"}); err != nil {
		t.Fatal(err)
	}
	report, err := store.Decide(run.ID, DecideOptions{Action: DecisionApprove, Units: []string{"u1"}, By: "owner"})
	if err != nil || report.Finished || report.Left != (RunWork{Parked: 1}) {
		t.Fatalf("cancel: %#v err=%v", report, err)
	}
	if _, err := store.Resume(run.ID, ResumeOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	if sends := driver.sendsFor("agent-a"); len(sends) != 2 || runStatus(t, store, run.ID) != StatusAwaitingApproval {
		t.Fatalf("a restarted worker sent a cancelled revision: %q status=%s", sends, runStatus(t, store, run.ID))
	}
	if rows := timesByUnit(t, store, run.ID); len(rows) != 1 || rows["u1"].Result != "weak" {
		t.Fatalf("rows %#v", rows)
	}
}

func TestCancelledRevisionIsNotSentByLiveWorker(t *testing.T) {
	store, run, driver, clock := approvalRun(t, approveYAML("weak", 1, 1, ""), []string{"u1", "u2"}, "agent-a")
	driver.beforeSend = func(_, key string) {
		if key != "u2" {
			return
		}
		if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionRevise, Units: []string{"u1"}, Text: "redo", By: "owner"}); err != nil {
			t.Error(err)
		}
		if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionReject, Units: []string{"u1"}, By: "owner"}); err != nil {
			t.Error(err)
		}
	}
	engine := supervisedEngine(store, driver, clock, nil)
	engine.Validator = keyValidator(driver, "agent-a", func(key string, _ int) int {
		if key == "u1" {
			return 3
		}
		return 0
	})
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	rows := timesByUnit(t, store, run.ID)
	if sends := driver.sendsFor("agent-a"); len(sends) != 2 || runStatus(t, store, run.ID) != "done" || rows["u1"].Result != "failed" || rows["u2"].Result != "done" || len(rows) != 2 {
		t.Fatalf("live worker after a cancelled revision: sends=%q status=%s rows=%#v", sends, runStatus(t, store, run.ID), rows)
	}
}

func TestRevisingUnitCannotBeDecided(t *testing.T) {
	store, run, _, _, _ := weakParkedRun(t, "u1")
	if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionRevise, Units: []string{"u1"}, Text: "redo", By: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.claimRevision(run.ID, "u1", "agent-a"); err != nil || !ok {
		t.Fatalf("claim ok=%t err=%v", ok, err)
	}
	for _, action := range []string{DecisionApprove, DecisionReject} {
		if _, err := store.Decide(run.ID, DecideOptions{Action: action, Units: []string{"u1"}, By: "owner"}); err == nil || !strings.Contains(err.Error(), "its revision round is running on agent-a") {
			t.Fatalf("%s of a revising unit: %v", action, err)
		}
	}
	if approvals, _ := store.Approvals(run.ID); len(approvals) != 1 || approvals[0].State != StateRevising {
		t.Fatalf("approvals %#v", approvals)
	}
}

// TestClaimAndCancelRace races a worker's claim against a human's cancel
// through two Store instances (two processes): exactly one wins.
func TestClaimAndCancelRace(t *testing.T) {
	claims, cancels := 0, 0
	for attempt := 0; attempt < 6; attempt++ {
		store, run, _, _, _ := weakParkedRun(t, "u1")
		if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionRevise, Units: []string{"u1"}, Text: "redo", By: "owner"}); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var claimed bool
		var claimErr, decideErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, claimed, claimErr = NewStore(store.StateDir).claimRevision(run.ID, "u1", "agent-a")
		}()
		go func() {
			defer wg.Done()
			<-start
			_, decideErr = NewStore(store.StateDir).Decide(run.ID, DecideOptions{Action: DecisionApprove, Units: []string{"u1"}, By: "owner"})
		}()
		close(start)
		wg.Wait()
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		if claimed == (decideErr == nil) {
			t.Fatalf("attempt %d: claimed=%t decideErr=%v; exactly one must win", attempt, claimed, decideErr)
		}
		latest := latestState(t, store, run.ID)["u1"]
		rows, _, _ := store.Times(run.ID)
		if claimed {
			claims++
			if !strings.Contains(decideErr.Error(), "running on agent-a") || latest.State != StateRevising || len(rows) != 0 {
				t.Fatalf("claim won: err=%v latest=%#v rows=%d", decideErr, latest, len(rows))
			}
		} else {
			cancels++
			if latest.State != "weak" || len(rows) != 1 {
				t.Fatalf("cancel won: latest=%#v rows=%d", latest, len(rows))
			}
		}
	}
	t.Logf("claims won %d, cancels won %d", claims, cancels)
}
