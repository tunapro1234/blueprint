package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptedDriver wraps fakeDriver with per-unit delivery failures, observation
// overrides and an optional token ledger. Prompts are "{{.Key}}" or
// "fix {{.Key}}", so the unit key is the prompt's last word.
type scriptedDriver struct {
	*fakeDriver
	mu         sync.Mutex
	failKeys   map[string]bool
	beforeSend func(agent, key string)
	observe    func(agent string) (AgentState, bool)
	sendTimes  map[string][]time.Time
	// Token ledger: every delivered send adds perSend to its agent.
	metered bool
	unknown map[string]bool
	perSend TokenUse
	ledger  []ledgerEntry
	// lag delays the meter's collection: usage newer than now-lag is not
	// visible yet and AsOf is now-lag. When lagCalls is positive, only that
	// many meter calls see the lag; the source has caught up afterwards.
	lag      time.Duration
	lagCalls int
	calls    int
}

type ledgerEntry struct {
	at    time.Time
	agent string
	use   TokenUse
}

type unmeteredDriver struct{ *scriptedDriver }

func newScriptedDriver(clock *fakeClock) *scriptedDriver {
	return &scriptedDriver{fakeDriver: newFakeDriver(clock), failKeys: map[string]bool{}, sendTimes: map[string][]time.Time{}, unknown: map[string]bool{}}
}

func promptKey(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

func (d *scriptedDriver) Observe(ctx context.Context, agent string) (Observation, error) {
	if d.observe != nil {
		if state, ok := d.observe(agent); ok {
			d.clock.Advance(time.Millisecond)
			return Observation{State: state, ContextKnown: true, ContextTokens: 100, Model: "gpt-6-luna", ObservedAt: d.clock.Now(), TurnAt: d.clock.Now()}, nil
		}
	}
	return d.fakeDriver.Observe(ctx, agent)
}

func (d *scriptedDriver) Send(ctx context.Context, agent, text string) (Delivery, error) {
	key := promptKey(text)
	if d.beforeSend != nil {
		d.beforeSend(agent, key)
	}
	d.mu.Lock()
	d.sendTimes[agent] = append(d.sendTimes[agent], d.clock.Now())
	fail := d.failKeys[key]
	d.mu.Unlock()
	if fail {
		d.fakeDriver.mu.Lock()
		d.fakeDriver.sends[agent] = append(d.fakeDriver.sends[agent], text)
		d.fakeDriver.mu.Unlock()
		d.clock.Advance(time.Millisecond)
		return Delivery{Status: DeliveryFailed, Reason: "not delivered: broken pane", QueuedAt: d.clock.Now()}, nil
	}
	delivery, err := d.fakeDriver.Send(ctx, agent, text)
	if err == nil && delivery.Status == DeliveryDelivered {
		d.mu.Lock()
		d.ledger = append(d.ledger, ledgerEntry{at: d.clock.Now(), agent: agent, use: d.perSend})
		d.mu.Unlock()
	}
	return delivery, err
}

func (d *scriptedDriver) Tokens(_ context.Context, agent string, since, until time.Time) (TokenUse, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.metered || d.unknown[agent] {
		return TokenUse{}, false, nil
	}
	now := d.clock.Now()
	if until.IsZero() || until.After(now) {
		until = now
	}
	// A lagging source has collected only up to now-lag.
	d.calls++
	lag := d.lag
	if d.lagCalls > 0 && d.calls > d.lagCalls {
		lag = 0
	}
	asOf := now.Add(-lag)
	total := TokenUse{AsOf: asOf}
	for _, entry := range d.ledger {
		if entry.agent == agent && !entry.at.Before(since) && entry.at.Before(until) && !entry.at.After(asOf) {
			total.Input += entry.use.Input
			total.Output += entry.use.Output
			total.CacheWrite += entry.use.CacheWrite
			total.CacheRead += entry.use.CacheRead
		}
	}
	return total, true, nil
}

func (d *scriptedDriver) sendsFor(agent string) []string {
	d.fakeDriver.mu.Lock()
	defer d.fakeDriver.mu.Unlock()
	return append([]string(nil), d.fakeDriver.sends[agent]...)
}

type notifications struct {
	mu       sync.Mutex
	messages []string
}

func (n *notifications) notify(_ context.Context, _ string, message string) error {
	n.mu.Lock()
	n.messages = append(n.messages, message)
	n.mu.Unlock()
	return nil
}

func (n *notifications) count(substring string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	count := 0
	for _, message := range n.messages {
		if strings.Contains(message, substring) {
			count++
		}
	}
	return count
}

func supervisedEngine(store *Store, driver Driver, clock *fakeClock, notes *notifications) *Engine {
	engine := NewEngine(store, driver)
	engine.Now, engine.Sleep, engine.Poll = clock.Now, clock.Sleep, time.Second
	if notes != nil {
		engine.Notify = notes.notify
	}
	return engine
}

func unitsJSON(keys ...string) string {
	parts := make([]string, len(keys))
	for i, key := range keys {
		parts[i] = `{"id":"` + key + `"}`
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func eventsWithState(t *testing.T, store *Store, runID, state string) []Event {
	t.Helper()
	events, _, err := store.Events(runID)
	if err != nil {
		t.Fatal(err)
	}
	var result []Event
	for _, event := range events {
		if event.State == state {
			result = append(result, event)
		}
	}
	return result
}

func timesByUnit(t *testing.T, store *Store, runID string) map[string]TimeRow {
	t.Helper()
	rows, _, err := store.Times(runID)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]TimeRow{}
	for _, row := range rows {
		result[row.Unit] = row
	}
	return result
}

const breakerYAML = "supervise:\n  breaker:\n    failures: 2\n    open_for: %s\n"

func withBreaker(openFor string) string {
	return baseYAML(strings.Replace(breakerYAML, "%s", openFor, 1))
}

func TestBreakerOpensForOneAgentWhileAnotherKeepsWorking(t *testing.T) {
	clock := newFakeClock()
	keys := []string{"u1", "u2", "u3", "u4", "u5", "u6", "u7", "u8"}
	store, run := makeRun(t, clock, withBreaker("240h"), unitsJSON(keys...), "{{.Key}}", "", "agent-a", "agent-b")
	driver := newScriptedDriver(clock)
	// agent-a is broken: every prompt it gets fails delivery.
	brokenSends := 0
	var mu sync.Mutex
	driver.beforeSend = func(agent, key string) {
		if agent == "agent-a" {
			driver.mu.Lock()
			driver.failKeys[key] = true
			driver.mu.Unlock()
			mu.Lock()
			brokenSends++
			mu.Unlock()
		}
	}
	notes := &notifications{}
	engine := supervisedEngine(store, driver, clock, notes)
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	rows := timesByUnit(t, store, run.ID)
	counts := map[string]map[string]int{}
	for _, row := range rows {
		if counts[row.Agent] == nil {
			counts[row.Agent] = map[string]int{}
		}
		counts[row.Agent][row.Result]++
	}
	if counts["agent-a"]["failed"] != 2 || len(rows) != len(keys) || counts["agent-b"]["done"] != len(keys)-2 {
		t.Fatalf("breaker did not stop agent-a after two failures while agent-b drained the queue: %v", counts)
	}
	opened := eventsWithState(t, store, run.ID, eventBreakerOpen)
	if len(opened) != 1 || opened[0].Agent != "agent-a" || opened[0].Until == nil || !strings.Contains(opened[0].Reason, "2 consecutive failed units") {
		t.Fatalf("expected one breaker_open event for agent-a, got %#v", opened)
	}
	if notes.count("breaker open for agent-a") != 1 {
		t.Fatalf("expected one opening notification, got %#v", notes.messages)
	}
	status, err := store.Status(run.ID)
	if err != nil || status.Status != "done" || len(status.Breakers) != 1 || status.Breakers[0].Agent != "agent-a" || status.Breakers[0].State != "open" || !status.Breakers[0].ReopensAt.Equal(*opened[0].Until) {
		t.Fatalf("status does not show the open breaker: %#v err=%v", status, err)
	}
}

func TestBreakerSuccessResetsCount(t *testing.T) {
	clock := newFakeClock()
	store, run := makeRun(t, clock, withBreaker("1h"), unitsJSON("u1", "u2", "u3", "u4", "u5"), "{{.Key}}", "", "agent-a")
	driver := newScriptedDriver(clock)
	driver.failKeys = map[string]bool{"u1": true, "u3": true, "u5": true}
	notes := &notifications{}
	engine := supervisedEngine(store, driver, clock, notes)
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	if len(driver.sendsFor("agent-a")) != 5 || len(eventsWithState(t, store, run.ID, eventBreakerOpen)) != 0 || notes.count("breaker") != 0 {
		t.Fatalf("alternating results must not open the breaker: sends=%d events=%#v", len(driver.sendsFor("agent-a")), eventsWithState(t, store, run.ID, eventBreakerOpen))
	}
}

func TestBreakerHalfOpenTrial(t *testing.T) {
	for _, test := range []struct {
		name     string
		fail     []string
		openings int
		closed   int
	}{
		{name: "trial success closes", fail: []string{"u1", "u2"}, openings: 1, closed: 1},
		{name: "trial failure reopens", fail: []string{"u1", "u2", "u3"}, openings: 2, closed: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := newFakeClock()
			store, run := makeRun(t, clock, withBreaker("30s"), unitsJSON("u1", "u2", "u3", "u4", "u5"), "{{.Key}}", "", "agent-a")
			driver := newScriptedDriver(clock)
			for _, key := range test.fail {
				driver.failKeys[key] = true
			}
			notes := &notifications{}
			engine := supervisedEngine(store, driver, clock, notes)
			waiting := false
			engine.Sleep = func(ctx context.Context, delay time.Duration) error {
				if current, err := store.LoadRun(run.ID); err == nil && strings.HasPrefix(current.Status, statusBreakersWaiting) {
					waiting = true
				}
				return clock.Sleep(ctx, delay)
			}
			if err := engine.Run(context.Background(), run.ID, false); err != nil {
				t.Fatal(err)
			}
			opened := eventsWithState(t, store, run.ID, eventBreakerOpen)
			half := eventsWithState(t, store, run.ID, eventBreakerHalfOpen)
			closed := eventsWithState(t, store, run.ID, eventBreakerClosed)
			if len(opened) != test.openings || len(half) != test.openings || len(closed) != test.closed || notes.count("breaker open") != test.openings {
				t.Fatalf("opened=%d half=%d closed=%d notes=%v", len(opened), len(half), len(closed), notes.messages)
			}
			if !waiting {
				t.Fatal("run status never showed that every agent breaker was open")
			}
			// Each trial starts only after open_for has passed since the
			// opening failure.
			sendTimes := driver.sendTimes["agent-a"]
			for i, event := range opened {
				trial := sendTimes[2+i]
				if trial.Before(*event.Until) {
					t.Fatalf("trial %d sent at %s before the breaker reopened at %s", i, trial, event.Until)
				}
			}
			status, err := store.Status(run.ID)
			if err != nil || status.Status != "done" || len(status.Breakers) != 0 || status.Counts.Done+status.Counts.Failed != 5 {
				t.Fatalf("unexpected final status %#v err=%v", status, err)
			}
		})
	}
}

func appendFailures(t *testing.T, store *Store, runID, agent string, at time.Time, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if err := store.AppendEvent(runID, Event{At: at, Unit: key, Key: key, Agent: agent, State: "failed", Reason: "validator exited with code 2"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBreakerStateSurvivesWorkerRestartAndResetsOnResume(t *testing.T) {
	t.Run("restart keeps the breaker open without a second notification", func(t *testing.T) {
		clock := newFakeClock()
		store, run := makeRun(t, clock, withBreaker("10m"), unitsJSON("u1", "u2", "u3"), "{{.Key}}", "", "agent-a")
		failedAt := clock.Now()
		appendFailures(t, store, run.ID, "agent-a", failedAt, "u1", "u2")
		until := failedAt.Add(10 * time.Minute)
		if err := store.AppendEvent(run.ID, Event{At: failedAt, Agent: "agent-a", State: eventBreakerOpen, Until: &until}); err != nil {
			t.Fatal(err)
		}
		driver := newScriptedDriver(clock)
		notes := &notifications{}
		if err := supervisedEngine(store, driver, clock, notes).Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		if sends := driver.sendTimes["agent-a"]; len(sends) != 1 || sends[0].Before(until) {
			t.Fatalf("restarted worker did not honor the open breaker: sends=%v until=%s", sends, until)
		}
		if notes.count("breaker open") != 0 || len(eventsWithState(t, store, run.ID, eventBreakerOpen)) != 1 {
			t.Fatalf("restart re-announced an opening: %v", notes.messages)
		}
	})
	t.Run("restart announces an opening the crashed worker never recorded", func(t *testing.T) {
		clock := newFakeClock()
		store, run := makeRun(t, clock, withBreaker("10m"), unitsJSON("u1", "u2", "u3"), "{{.Key}}", "", "agent-a")
		appendFailures(t, store, run.ID, "agent-a", clock.Now(), "u1", "u2")
		driver := newScriptedDriver(clock)
		notes := &notifications{}
		if err := supervisedEngine(store, driver, clock, notes).Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		if notes.count("breaker open") != 1 || len(eventsWithState(t, store, run.ID, eventBreakerOpen)) != 1 {
			t.Fatalf("missing opening was not announced exactly once: %v", notes.messages)
		}
	})
	t.Run("resume after stop starts closed", func(t *testing.T) {
		clock := newFakeClock()
		store, run := makeRun(t, clock, withBreaker("10m"), unitsJSON("u1", "u2", "u3"), "{{.Key}}", "", "agent-a")
		failedAt := clock.Now()
		appendFailures(t, store, run.ID, "agent-a", failedAt, "u1", "u2")
		if _, err := store.RequestStop(run.ID); err != nil {
			t.Fatal(err)
		}
		clock.Advance(time.Second)
		if _, err := store.Resume(run.ID, ResumeOptions{}); err != nil {
			t.Fatal(err)
		}
		if status, err := store.Status(run.ID); err != nil || len(status.Breakers) != 0 {
			t.Fatalf("resume did not reset the breaker view: %#v err=%v", status, err)
		}
		driver := newScriptedDriver(clock)
		notes := &notifications{}
		if err := supervisedEngine(store, driver, clock, notes).Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		if sends := driver.sendTimes["agent-a"]; len(sends) != 1 || !sends[0].Before(failedAt.Add(10*time.Minute)) || notes.count("breaker") != 0 {
			t.Fatalf("resumed run still treated the breaker as open: sends=%v notes=%v", sends, notes.messages)
		}
	})
}

func TestBreakerToleratesTornLogLine(t *testing.T) {
	clock := newFakeClock()
	store, run := makeRun(t, clock, withBreaker("10m"), unitsJSON("u1", "u2", "u3"), "{{.Key}}", "", "agent-a")
	appendFailures(t, store, run.ID, "agent-a", clock.Now(), "u1")
	path := filepath.Join(store.RunPath(run.ID), "units.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"at":"2026-09-25T10:00:00Z","key":"u3","state":"do` + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	appendFailures(t, store, run.ID, "agent-a", clock.Now(), "u2")
	events, torn, err := store.Events(run.ID)
	if err != nil || !torn {
		t.Fatalf("expected a torn log, torn=%t err=%v", torn, err)
	}
	breakers := deriveBreakers(events, run.SupervisionStart())
	if b := breakers["agent-a"]; b == nil || b.consecutive != 2 || b.lastUnit != "u2" {
		t.Fatalf("torn line corrupted the breaker state: %#v", b)
	}
	status, err := store.Status(run.ID)
	if err != nil || !status.TornLog || len(status.Breakers) != 1 {
		t.Fatalf("status with a torn line: %#v err=%v", status, err)
	}
}

const meltdownYAML = "supervise:\n  meltdown:\n    failures: 3\n    window: 30m\n"

func TestMeltdownPausesAtThreshold(t *testing.T) {
	clock := newFakeClock()
	store, run := makeRun(t, clock, baseYAML(meltdownYAML), unitsJSON("u1", "u2", "u3", "u4", "u5", "u6"), "{{.Key}}", "", "agent-a")
	driver := newScriptedDriver(clock)
	for _, key := range []string{"u1", "u2", "u3", "u4", "u5", "u6"} {
		driver.failKeys[key] = true
	}
	notes := &notifications{}
	if err := supervisedEngine(store, driver, clock, notes).Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	updated, err := store.LoadRun(run.ID)
	if err != nil || updated.Status != "paused" || !strings.HasPrefix(updated.PauseReason, "meltdown: 3 units failed within 30m; last: u3 on agent-a: not delivered") {
		t.Fatalf("run did not pause on meltdown: %#v err=%v", updated, err)
	}
	if len(driver.sendsFor("agent-a")) != 3 || notes.count("paused: meltdown") != 1 || len(eventsWithState(t, store, run.ID, eventRunPaused)) != 1 {
		t.Fatalf("sends=%d notes=%v", len(driver.sendsFor("agent-a")), notes.messages)
	}
	if notes.count("run "+run.ID+" stopped") != 0 || notes.count("run "+run.ID+" done") != 0 {
		t.Fatalf("pause sent an extra end notification: %v", notes.messages)
	}
	status, err := store.Status(run.ID)
	if err != nil || status.PauseReason != updated.PauseReason || status.Counts.Failed != 3 || status.Counts.Pending != 3 {
		t.Fatalf("paused status %#v err=%v", status, err)
	}
	if stopped, err := store.RequestStop(run.ID); err != nil || stopped.Status != "paused" || stopped.StopRequested {
		t.Fatalf("stop changed a paused run: %#v err=%v", stopped, err)
	}

	// Resume restarts the window: the three earlier failures no longer count.
	clock.Advance(time.Second)
	if _, err := store.Resume(run.ID, ResumeOptions{}); err != nil {
		t.Fatal(err)
	}
	driver.failKeys = map[string]bool{"u4": true}
	if err := supervisedEngine(store, driver, clock, notes).Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	if final, err := store.LoadRun(run.ID); err != nil || final.Status != "done" || final.PauseRequested || final.PauseReason != "" {
		t.Fatalf("resumed run did not continue to done: %#v err=%v", final, err)
	}
}

func TestMeltdownIgnoresFailuresOutsideWindow(t *testing.T) {
	clock := newFakeClock()
	store, run := makeRun(t, clock, baseYAML(meltdownYAML), unitsJSON("u1", "u2", "u3", "u4", "u5"), "{{.Key}}", "", "agent-a")
	appendFailures(t, store, run.ID, "agent-a", clock.Now(), "u1", "u2")
	clock.Advance(2 * time.Hour)
	driver := newScriptedDriver(clock)
	driver.failKeys = map[string]bool{"u3": true}
	notes := &notifications{}
	if err := supervisedEngine(store, driver, clock, notes).Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	if updated, _ := store.LoadRun(run.ID); updated.Status != "done" || notes.count("paused") != 0 {
		t.Fatalf("old failures counted toward meltdown: %#v notes=%v", updated, notes.messages)
	}
}

func TestMeltdownLetsInFlightUnitFinish(t *testing.T) {
	clock := newFakeClock()
	yamlText := baseYAML("supervise:\n  meltdown:\n    failures: 2\n    window: 30m\n")
	store, run := makeRun(t, clock, yamlText, unitsJSON("u1", "u2", "u3", "u4", "u5", "u6"), "{{.Key}}", "", "agent-a", "agent-b")
	driver := newScriptedDriver(clock)
	bSent := make(chan struct{})
	var once sync.Once
	driver.beforeSend = func(agent, key string) {
		if agent == "agent-b" {
			once.Do(func() { close(bSent) })
			return
		}
		select {
		case <-bSent:
		case <-time.After(5 * time.Second):
		}
		driver.mu.Lock()
		driver.failKeys[key] = true
		driver.mu.Unlock()
	}
	// agent-b reports Working once, then holds its next observation in real
	// time until the pause is requested and goes idle. Holding in real time
	// keeps agent-b's poll loop from racing the shared fake clock ahead.
	bWorking := false
	driver.observe = func(agent string) (AgentState, bool) {
		if agent != "agent-b" {
			return "", false
		}
		driver.mu.Lock()
		sent := len(driver.sendTimes["agent-b"]) > 0
		driver.mu.Unlock()
		if !sent {
			return Idle, true
		}
		if !bWorking {
			bWorking = true
			return Working, true
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if current, err := store.LoadRun(run.ID); err == nil && current.PauseRequested {
				break
			}
			time.Sleep(time.Millisecond)
		}
		return Idle, true
	}
	notes := &notifications{}
	if err := supervisedEngine(store, driver, clock, notes).Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	rows, _, _ := store.Times(run.ID)
	var bRows []TimeRow
	for _, row := range rows {
		if row.Agent == "agent-b" {
			bRows = append(bRows, row)
		}
	}
	updated, _ := store.LoadRun(run.ID)
	if !bWorking || len(bRows) != 1 || bRows[0].Result != "done" || len(driver.sendsFor("agent-b")) != 1 || updated.Status != "paused" {
		t.Fatalf("in-flight unit did not finish before the pause: working=%t rows=%#v status=%s", bWorking, bRows, updated.Status)
	}
}

func TestSupervisorDoesNotRestartPausedRun(t *testing.T) {
	clock := newFakeClock()
	store, run := makeRun(t, clock, baseYAML(""), unitsJSON("u1"), "{{.Key}}", "", "agent-a")
	attempts := 0
	launch := func(_ context.Context, id string) (ChildWait, error) {
		attempts++
		return func() error {
			_, err := store.UpdateRun(id, func(current *Run) error {
				current.Status, current.PauseReason = "paused", "meltdown: test"
				return nil
			})
			if err != nil {
				return err
			}
			return os.ErrClosed // an exit that would otherwise be restarted
		}, nil
	}
	supervisor := NewSupervisor(store, store.StateDir, launch, nil)
	supervisor.Delay = func(context.Context, time.Duration) error { return nil }
	if err := supervisor.Start(run.ID); err != nil {
		t.Fatal(err)
	}
	waitSupervisor(t, supervisor, run.ID)
	if attempts != 1 {
		t.Fatalf("supervisor restarted a paused run %d times", attempts-1)
	}
	if err := supervisor.Start(run.ID); err == nil || !strings.Contains(err.Error(), "not resumable") {
		t.Fatalf("paused run must not start without resume, got %v", err)
	}
	restore := NewSupervisor(store, store.StateDir, launch, nil)
	if err := restore.restore(); err != nil || attempts != 1 {
		t.Fatalf("daemon startup restored a paused run: attempts=%d err=%v", attempts, err)
	}
	if !resumableStatus("pausing") || resumableStatus("paused") {
		t.Fatal("pausing must be resumable by the supervisor and paused must not")
	}
}

func budgetYAML(budget string, rounds, weakAfter int) string {
	return "name: sample\nversion: 1\nunits:\n  file: units.json\n  key: id\nprompt:\n  template: prompt.md\n  followup: followup.md\nvalidate:\n  rounds: " + fmt.Sprint(rounds) + "\n  weak_after: " + fmt.Sprint(weakAfter) + "\n" + budget
}

func meteredRun(t *testing.T, yamlText string, keys []string, agents ...string) (*Store, Run, *scriptedDriver, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	store, run := makeRun(t, clock, yamlText, unitsJSON(keys...), "{{.Key}}", "fix {{.Key}}", agents...)
	driver := newScriptedDriver(clock)
	driver.metered = true
	driver.perSend = TokenUse{Input: 200000, Output: 50000}
	return store, run, driver, clock
}

func alwaysCode(code int) func(context.Context, string, []string, time.Duration) (int, string, string, error) {
	return func(context.Context, string, []string, time.Duration) (int, string, string, error) {
		return code, "still wrong", "", nil
	}
}

func TestUnitBudgetStopsFollowups(t *testing.T) {
	for _, test := range []struct {
		name      string
		weakAfter int
		want      string
	}{
		{name: "weak allowed", weakAfter: 2, want: "weak"},
		{name: "weak not reached", weakAfter: 4, want: "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, run, driver, clock := meteredRun(t, budgetYAML("budget:\n  unit_tokens: 400000\n", 5, test.weakAfter), []string{"u1"}, "agent-a")
			engine := supervisedEngine(store, driver, clock, nil)
			engine.Validator = alwaysCode(1)
			if err := engine.Run(context.Background(), run.ID, false); err != nil {
				t.Fatal(err)
			}
			row := timesByUnit(t, store, run.ID)["u1"]
			if len(driver.sendsFor("agent-a")) != 2 || row.Result != test.want || row.Reason != "unit token budget exhausted (spent 500000 of 400000)" || row.Rounds != 2 {
				t.Fatalf("unit budget did not end the unit: sends=%d row=%#v", len(driver.sendsFor("agent-a")), row)
			}
			if row.Tokens == nil || row.Tokens.Weighted != 500000 || row.Tokens.Input != 400000 {
				t.Fatalf("unit tokens not recorded: %#v", row.Tokens)
			}
		})
	}
}

func TestUnitBudgetFailureCountsForBreaker(t *testing.T) {
	yamlText := budgetYAML("budget:\n  unit_tokens: 300000\nsupervise:\n  breaker:\n    failures: 1\n    open_for: 240h\n", 5, 9)
	store, run, driver, clock := meteredRun(t, yamlText, []string{"u1", "u2"}, "agent-a", "agent-b")
	driver.unknown["agent-b"] = true // agent-b runs unenforced and succeeds after one follow-up
	engine := supervisedEngine(store, driver, clock, nil)
	calls := map[string]int{}
	var mu sync.Mutex
	engine.Validator = func(context.Context, string, []string, time.Duration) (int, string, string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls["all"]++
		return 1, "still wrong", "", nil
	}
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	for _, event := range eventsWithState(t, store, run.ID, eventBreakerOpen) {
		if event.Agent == "agent-a" {
			return
		}
	}
	t.Fatalf("budget-failed unit did not open agent-a's breaker: %#v", timesByUnit(t, store, run.ID))
}

func TestRunBudgetPausesBeforeNewDispatch(t *testing.T) {
	store, run, driver, clock := meteredRun(t, budgetYAML("budget:\n  run_tokens: 300000\n", 3, 3), []string{"u1", "u2", "u3"}, "agent-a")
	engine := supervisedEngine(store, driver, clock, &notifications{})
	notes := &notifications{}
	engine.Notify = notes.notify
	// Round one fails validation, round two passes: the follow-up crosses the
	// run ceiling and must still be sent.
	calls := 0
	engine.Validator = func(context.Context, string, []string, time.Duration) (int, string, string, error) {
		calls++
		if calls%2 == 1 {
			return 1, "fix it", "", nil
		}
		return 0, "", "", nil
	}
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	updated, _ := store.LoadRun(run.ID)
	reason := "run token budget exhausted (spent 500000 of 300000)"
	if len(driver.sendsFor("agent-a")) != 2 || updated.Status != "paused" || updated.PauseReason != reason || notes.count(reason) != 1 {
		t.Fatalf("run budget did not pause before u2: sends=%v run=%#v notes=%v", driver.sendsFor("agent-a"), updated, notes.messages)
	}
	if row := timesByUnit(t, store, run.ID)["u1"]; row.Result != "done" || row.Rounds != 2 {
		t.Fatalf("in-flight unit's follow-up was blocked: %#v", row)
	}
	if spent, ceiling, err := store.RunSpend(run.ID); err != nil || spent != 500000 || ceiling != 300000 {
		t.Fatalf("RunSpend=%d/%d err=%v", spent, ceiling, err)
	}

	// Resuming without raising the ceiling pauses again before any send.
	if _, err := store.Resume(run.ID, ResumeOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := supervisedEngine(store, driver, clock, notes).Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	again, _ := store.LoadRun(run.ID)
	if again.Status != "paused" || again.PauseReason != reason || len(driver.sendsFor("agent-a")) != 2 {
		t.Fatalf("resume without a raise did not pause immediately: %#v sends=%d", again, len(driver.sendsFor("agent-a")))
	}

	// --run-tokens raises the ceiling in run.json and the run continues. This
	// engine has no validator override, so u2 and u3 take one send each.
	if _, err := store.Resume(run.ID, ResumeOptions{RunTokens: 5000000}); err != nil {
		t.Fatal(err)
	}
	if err := supervisedEngine(store, driver, clock, notes).Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	final, _ := store.LoadRun(run.ID)
	if final.Status != "done" || final.RunTokens != 5000000 {
		t.Fatalf("override did not let the run finish: %#v", final)
	}
	status, err := store.Status(run.ID)
	if err != nil || status.RunTokens != 5000000 || status.RunTokensSpent != 1000000 {
		t.Fatalf("status budget view: %#v err=%v", status, err)
	}
}

func TestRunBudgetCountsInFlightUnits(t *testing.T) {
	store, run, driver, clock := meteredRun(t, budgetYAML("budget:\n  run_tokens: 400000\n", 1, 2), []string{"u1", "u2", "u3"}, "agent-a", "agent-b")
	engine := supervisedEngine(store, driver, clock, nil)
	// agent-a holds its unit in flight until agent-b has asked for admission
	// of a second unit; agent-b's own finished unit alone is under the
	// ceiling, so only the in-flight spend can trip it.
	release := make(chan struct{})
	var once sync.Once
	driver.observe = func(agent string) (AgentState, bool) {
		if agent != "agent-a" {
			return "", false
		}
		driver.mu.Lock()
		sent := len(driver.sendTimes["agent-a"]) > 0
		driver.mu.Unlock()
		if !sent {
			return Idle, true
		}
		select {
		case <-release:
			return Idle, true
		default:
			return Working, true
		}
	}
	engine.Validator = func(context.Context, string, []string, time.Duration) (int, string, string, error) {
		once.Do(func() {
			go func() {
				// Give agent-b's worker time to reach its admission check.
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					if current, err := store.LoadRun(run.ID); err == nil && current.PauseRequested {
						break
					}
					time.Sleep(time.Millisecond)
				}
				close(release)
			}()
		})
		return 0, "", "", nil
	}
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	updated, _ := store.LoadRun(run.ID)
	if updated.Status != "paused" || !strings.Contains(updated.PauseReason, "spent 500000 of 400000") {
		t.Fatalf("in-flight spend was not counted at admission: %#v", updated)
	}
}

func TestUnknownTokensNotEnforcedAndRecordedOnce(t *testing.T) {
	store, run, driver, clock := meteredRun(t, budgetYAML("budget:\n  unit_tokens: 1\n  run_tokens: 1\n", 2, 3), []string{"u1", "u2"}, "agent-a")
	driver.unknown["agent-a"] = true
	engine := supervisedEngine(store, driver, clock, nil)
	calls := 0
	engine.Validator = func(context.Context, string, []string, time.Duration) (int, string, string, error) {
		calls++
		if calls%2 == 1 {
			return 1, "fix", "", nil
		}
		return 0, "", "", nil
	}
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	rows := timesByUnit(t, store, run.ID)
	unknown := eventsWithState(t, store, run.ID, eventTokensUnknown)
	if len(driver.sendsFor("agent-a")) != 4 || rows["u1"].Result != "done" || rows["u2"].Result != "done" || rows["u1"].Tokens != nil {
		t.Fatalf("unknown tokens were enforced: sends=%d rows=%#v", len(driver.sendsFor("agent-a")), rows)
	}
	if len(unknown) != 1 || unknown[0].Agent != "agent-a" || !strings.Contains(unknown[0].Reason, "tokens unknown") {
		t.Fatalf("expected one tokens_unknown event, got %#v", unknown)
	}
	if updated, _ := store.LoadRun(run.ID); updated.Status != "done" {
		t.Fatalf("unknown tokens paused the run: %#v", updated)
	}
}

func TestTokensRecordedWithoutBudget(t *testing.T) {
	store, run, driver, clock := meteredRun(t, baseYAML(""), []string{"u1"}, "agent-a")
	driver.perSend = TokenUse{Input: 1000, Output: 200, CacheWrite: 300, CacheRead: 5000}
	if err := supervisedEngine(store, driver, clock, nil).Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	row := timesByUnit(t, store, run.ID)["u1"]
	if row.Tokens == nil || row.Tokens.Input != 1000 || row.Tokens.Output != 200 || row.Tokens.CacheWrite != 300 || row.Tokens.CacheRead != 5000 || row.Tokens.Weighted != 1000+200+300+500 {
		t.Fatalf("tokens not recorded without a budget: %#v", row.Tokens)
	}
	if len(eventsWithState(t, store, run.ID, eventTokensUnknown)) != 0 {
		t.Fatal("tokens_unknown recorded without a budget")
	}
	// A driver without a meter records no tokens and no event.
	store2, run2, driver2, clock2 := meteredRun(t, baseYAML(""), []string{"u1"}, "agent-a")
	if err := supervisedEngine(store2, unmeteredDriver{}.with(driver2), clock2, nil).Run(context.Background(), run2.ID, false); err != nil {
		t.Fatal(err)
	}
	if row := timesByUnit(t, store2, run2.ID)["u1"]; row.Tokens != nil {
		t.Fatalf("unmetered driver recorded tokens: %#v", row.Tokens)
	}
}

// with returns a Driver that hides the TokenMeter capability.
func (unmeteredDriver) with(driver *scriptedDriver) Driver {
	return struct {
		Driver
	}{driver}
}

func TestOldTimeRowsLoadAndTokenStats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "times.jsonl")
	old := `{"run":"r","workflow":"w","unit":"a","agent":"x","model":"m","result":"done","rounds":1,"finished_at":"2026-09-25T10:00:00Z","work_s":5}` + "\n"
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	rows, torn, err := readJSONLines[TimeRow](path)
	if err != nil || torn || len(rows) != 1 || rows[0].Tokens != nil || rows[0].WorkSeconds != 5 {
		t.Fatalf("old row did not load: %#v torn=%t err=%v", rows, torn, err)
	}
	if stats := SummarizeTimes(rows); stats.Tokens != nil {
		t.Fatalf("rows without tokens produced token stats: %#v", stats.Tokens)
	}
	rows = append(rows,
		TimeRow{Unit: "b", Agent: "x", Model: "m", Result: "done", Tokens: &UnitTokens{Input: 100, Output: 10, CacheRead: 1000, Weighted: 210}},
		TimeRow{Unit: "c", Agent: "y", Model: "n", Result: "failed", Tokens: &UnitTokens{Input: 400, Weighted: 400}},
		TimeRow{Unit: "d", Agent: "y", Model: "n", Result: "weak", Tokens: &UnitTokens{Input: 50, Weighted: 50}},
	)
	stats := SummarizeTimes(rows).Tokens
	if stats == nil || stats.Units != 3 || stats.Total != 1560 || stats.Weighted != 660 || stats.MedianWeighted != 210 || stats.P90Weighted != 400 || stats.ByAgent["y"].Weighted != 450 || stats.ByModel["m"].Total != 1110 || len(stats.Costliest) != 3 || stats.Costliest[0].Unit != "c" {
		t.Fatalf("unexpected token stats: %#v", stats)
	}
}

func TestDefinitionWithoutSupervisionKeepsBehavior(t *testing.T) {
	clock := newFakeClock()
	store, run := makeRun(t, clock, baseYAML(""), unitsJSON("u1", "u2", "u3", "u4"), "{{.Key}}", "", "agent-a")
	driver := newScriptedDriver(clock)
	driver.failKeys = map[string]bool{"u1": true, "u2": true, "u3": true, "u4": true}
	notes := &notifications{}
	if err := supervisedEngine(store, driver, clock, notes).Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	events, _, _ := store.Events(run.ID)
	for _, event := range events {
		if event.Key == "" && !strings.HasPrefix(event.State, "compact_") {
			t.Fatalf("supervision event without supervision configured: %#v", event)
		}
	}
	if len(driver.sendsFor("agent-a")) != 4 || notes.count("paused") != 0 || notes.count("breaker") != 0 {
		t.Fatalf("unsupervised run changed behavior: sends=%d notes=%v", len(driver.sendsFor("agent-a")), notes.messages)
	}
	if updated, _ := store.LoadRun(run.ID); updated.Status != "done" {
		t.Fatalf("status %s", updated.Status)
	}
}

func TestPauseRequestedButAllFinishedEndsDoneWithoutReason(t *testing.T) {
	clock := newFakeClock()
	store, run := makeRun(t, clock, baseYAML("supervise:\n  meltdown:\n    failures: 1\n    window: 30m\n"), unitsJSON("u1"), "{{.Key}}", "", "agent-a")
	driver := newScriptedDriver(clock)
	driver.failKeys["u1"] = true
	notes := &notifications{}
	if err := supervisedEngine(store, driver, clock, notes).Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	updated, err := store.LoadRun(run.ID)
	if err != nil || updated.Status != "done" || updated.PauseRequested || updated.PauseReason != "" {
		t.Fatalf("a run whose units all finished must end done without a pause reason: %#v err=%v", updated, err)
	}
	if notes.count("paused: meltdown") != 1 {
		t.Fatalf("the pause request itself is still announced: %v", notes.messages)
	}
}

func TestRunBudgetSettlesLaggingUnits(t *testing.T) {
	store, run, driver, clock := meteredRun(t, budgetYAML("budget:\n  run_tokens: 200000\n", 1, 2), []string{"u1", "u2"}, "agent-a")
	// The source lags an hour behind when u1 finishes, then catches up.
	driver.lag, driver.lagCalls = time.Hour, 1
	notes := &notifications{}
	if err := supervisedEngine(store, driver, clock, notes).Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	row := timesByUnit(t, store, run.ID)["u1"]
	if row.Tokens == nil || row.Tokens.Weighted != 0 || row.Tokens.Since == nil || row.Tokens.Settled(row.FinishedAt) {
		t.Fatalf("u1's row should keep the short, unsettled figure seen at finish: %#v", row.Tokens)
	}
	updated, _ := store.LoadRun(run.ID)
	reason := "run token budget exhausted (spent 250000 of 200000)"
	if updated.Status != "paused" || updated.PauseReason != reason || len(driver.sendsFor("agent-a")) != 1 {
		t.Fatalf("admission did not settle u1 and pause before u2: %#v sends=%d", updated, len(driver.sendsFor("agent-a")))
	}
	// Readers settle the row the same way without rewriting times.jsonl.
	if spent, _, err := store.RunSpend(run.ID); err != nil || spent != 0 {
		t.Fatalf("without a measure RunSpend reports the recorded figure: %d err=%v", spent, err)
	}
	store.Measure = func(agent string, since, until time.Time) (TokenUse, bool) {
		use, known, _ := driver.Tokens(context.Background(), agent, since, until)
		return use, known
	}
	if spent, _, err := store.RunSpend(run.ID); err != nil || spent != 250000 {
		t.Fatalf("settled RunSpend=%d err=%v", spent, err)
	}
	if status, err := store.Status(run.ID); err != nil || status.RunTokensSpent != 250000 {
		t.Fatalf("settled status spend: %#v err=%v", status, err)
	}
	if row := timesByUnit(t, store, run.ID)["u1"]; row.Tokens.Weighted != 0 {
		t.Fatalf("times.jsonl was rewritten: %#v", row.Tokens)
	}
}

func TestRunBudgetRestartRebuildsUnsettledUnits(t *testing.T) {
	for _, test := range []struct {
		name    string
		tokens  func(start, finish time.Time) *UnitTokens
		paused  bool
		u2Sends int
	}{
		{name: "unsettled row is re-measured", tokens: func(start, finish time.Time) *UnitTokens {
			asOf := start
			return &UnitTokens{Since: &start, AsOf: &asOf}
		}, paused: true},
		{name: "settled row is trusted", tokens: func(start, finish time.Time) *UnitTokens {
			asOf := finish
			return &UnitTokens{Since: &start, AsOf: &asOf}
		}, u2Sends: 1},
		{name: "row without a start counts as settled", tokens: func(start, finish time.Time) *UnitTokens {
			return &UnitTokens{}
		}, u2Sends: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, run, driver, clock := meteredRun(t, budgetYAML("budget:\n  run_tokens: 400000\n", 1, 2), []string{"u1", "u2"}, "agent-a")
			start := clock.Now()
			driver.ledger = append(driver.ledger, ledgerEntry{at: start.Add(30 * time.Second), agent: "agent-a", use: TokenUse{Input: 500000}})
			finish := start.Add(time.Minute)
			if err := store.AppendEvent(run.ID, Event{At: finish, Unit: "u1", Key: "u1", Agent: "agent-a", State: "done"}); err != nil {
				t.Fatal(err)
			}
			if err := store.AppendTime(run.ID, TimeRow{Run: run.ID, Unit: "u1", Agent: "agent-a", Result: "done", Rounds: 1, FinishedAt: finish, Tokens: test.tokens(start, finish)}); err != nil {
				t.Fatal(err)
			}
			clock.Advance(2 * time.Minute)
			if err := supervisedEngine(store, driver, clock, nil).Run(context.Background(), run.ID, false); err != nil {
				t.Fatal(err)
			}
			updated, _ := store.LoadRun(run.ID)
			if (updated.Status == "paused") != test.paused || len(driver.sendsFor("agent-a")) != test.u2Sends {
				t.Fatalf("status=%s reason=%q sends=%d", updated.Status, updated.PauseReason, len(driver.sendsFor("agent-a")))
			}
			if test.paused && updated.PauseReason != "run token budget exhausted (spent 500000 of 400000)" {
				t.Fatalf("pause reason %q", updated.PauseReason)
			}
		})
	}
}

func TestSettleTokens(t *testing.T) {
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	finish := base.Add(10 * time.Minute)
	at := func(offset time.Duration) *time.Time { value := base.Add(offset); return &value }
	rows := []TimeRow{
		{Unit: "settled", Agent: "a", FinishedAt: finish, Tokens: &UnitTokens{Input: 5, Weighted: 5, Since: at(0), AsOf: at(11 * time.Minute)}},
		{Unit: "expired", Agent: "gone", FinishedAt: finish, Tokens: &UnitTokens{Input: 7, Weighted: 7, Since: at(0), AsOf: at(5 * time.Minute)}},
		{Unit: "caught-up", Agent: "a", FinishedAt: finish, Tokens: &UnitTokens{Input: 7, Weighted: 7, Since: at(0), AsOf: at(5 * time.Minute)}},
		{Unit: "still-lagging", Agent: "lag", FinishedAt: finish, Tokens: &UnitTokens{Input: 7, Weighted: 7, Since: at(0), AsOf: at(5 * time.Minute)}},
		{Unit: "shrunk", Agent: "shrink", FinishedAt: finish, Tokens: &UnitTokens{Input: 7, Weighted: 7, Since: at(0), AsOf: at(5 * time.Minute)}},
		{Unit: "unknown-at-finish", Agent: "a", FinishedAt: finish},
		{Unit: "no-start", Agent: "a", FinishedAt: finish, Tokens: &UnitTokens{Input: 3, Weighted: 3, AsOf: at(time.Minute)}},
	}
	var measured []string
	measure := func(agent string, since, until time.Time) (TokenUse, bool) {
		measured = append(measured, agent)
		if !since.Equal(base) || !until.Equal(finish) {
			t.Fatalf("measured the wrong interval [%s, %s)", since, until)
		}
		switch agent {
		case "a":
			return TokenUse{Input: 100, CacheRead: 1000, AsOf: finish.Add(time.Minute)}, true
		case "lag":
			return TokenUse{Input: 50, AsOf: finish.Add(-time.Minute)}, true
		case "shrink":
			return TokenUse{Input: 1, AsOf: finish.Add(time.Minute)}, true
		}
		return TokenUse{}, false
	}
	settled, unsettled := SettleTokens(rows, 10, measure)
	byUnit := map[string]TimeRow{}
	for _, row := range settled {
		byUnit[row.Unit] = row
	}
	if unsettled != 3 || strings.Join(measured, ",") != "gone,a,lag,shrink" {
		t.Fatalf("unsettled=%d measured=%v", unsettled, measured)
	}
	if byUnit["settled"].Tokens.Weighted != 5 || byUnit["expired"].Tokens.Weighted != 7 || byUnit["shrunk"].Tokens.Weighted != 7 || byUnit["no-start"].Tokens.Weighted != 3 {
		t.Fatalf("recorded numbers not kept: %#v", byUnit)
	}
	if got := byUnit["caught-up"].Tokens; got.Input != 100 || got.CacheRead != 1000 || got.Weighted != 200 || !got.Settled(finish) {
		t.Fatalf("caught-up row: %#v", got)
	}
	if got := byUnit["still-lagging"].Tokens; got.Weighted != 50 || got.Settled(finish) {
		t.Fatalf("still-lagging row: %#v", got)
	}
	if byUnit["unknown-at-finish"].Tokens != nil {
		t.Fatal("a unit unknown at finish was back-filled")
	}
	if rows[2].Tokens.Weighted != 7 {
		t.Fatal("SettleTokens modified its input rows")
	}
	if _, count := SettleTokens(rows, 10, nil); count != 4 {
		t.Fatalf("without a measure every unsettled row stays unsettled, got %d", count)
	}
}
