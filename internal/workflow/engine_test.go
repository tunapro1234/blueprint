package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)} }
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}
func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.Advance(d)
	return nil
}

type fakeDriver struct {
	mu                 sync.Mutex
	clock              *fakeClock
	observations       map[string][]AgentState
	positions          map[string]int
	pendingWorking     map[string]bool
	active             map[string]bool
	idleStreak         map[string]int
	turnAt             map[string]time.Time
	ignoreWork         bool
	idleAfterSend      bool
	contextTokens      int
	contextKnown       bool
	compactReduction   int
	compactCount       int
	compactWhileActive bool
	sends              map[string][]string
	deliveryResults    []Delivery
	deliveryIndex      int
	onSend             func(string, string)
}

func newFakeDriver(clock *fakeClock) *fakeDriver {
	return &fakeDriver{clock: clock, observations: map[string][]AgentState{}, positions: map[string]int{}, pendingWorking: map[string]bool{}, active: map[string]bool{}, idleStreak: map[string]int{}, turnAt: map[string]time.Time{}, contextTokens: 100, contextKnown: true, sends: map[string][]string{}}
}

func (d *fakeDriver) Observe(_ context.Context, agent string) (Observation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	state := Idle
	if position := d.positions[agent]; position < len(d.observations[agent]) {
		state = d.observations[agent][position]
		d.positions[agent]++
	} else if d.pendingWorking[agent] && !d.ignoreWork {
		state = Working
		d.pendingWorking[agent] = false
	}
	if state == Working {
		d.active[agent], d.idleStreak[agent] = true, 0
	} else if state == Idle {
		d.idleStreak[agent]++
		if d.idleStreak[agent] >= 2 {
			d.active[agent] = false
		}
	} else {
		d.idleStreak[agent] = 0
	}
	d.clock.Advance(time.Millisecond)
	observation := Observation{State: state, ContextKnown: d.contextKnown, ContextTokens: d.contextTokens, Model: "gpt-6-luna", ObservedAt: d.clock.Now()}
	if state == Working {
		observation.TurnAt = observation.ObservedAt
	} else if turnAt := d.turnAt[agent]; !turnAt.IsZero() {
		observation.TurnAt = turnAt
	}
	return observation, nil
}

func (d *fakeDriver) Send(_ context.Context, agent, text string) (Delivery, error) {
	d.mu.Lock()
	d.sends[agent] = append(d.sends[agent], text)
	index := d.deliveryIndex
	d.deliveryIndex++
	var result Delivery
	if index < len(d.deliveryResults) {
		result = d.deliveryResults[index]
	} else {
		result.Status = DeliveryDelivered
	}
	if result.Status == DeliveryDelivered {
		d.pendingWorking[agent] = true
		d.active[agent] = true
	}
	d.clock.Advance(time.Millisecond)
	if result.QueuedAt.IsZero() {
		result.QueuedAt = d.clock.Now().Add(-time.Millisecond)
	}
	if result.Status == DeliveryDelivered && result.DeliveredAt.IsZero() {
		result.DeliveredAt = d.clock.Now()
	}
	if result.Status == DeliveryDelivered && d.idleAfterSend {
		d.pendingWorking[agent] = false
		d.turnAt[agent] = result.DeliveredAt.Add(time.Millisecond)
	}
	callback := d.onSend
	d.mu.Unlock()
	if callback != nil {
		callback(agent, text)
	}
	return result, nil
}

func (d *fakeDriver) Compact(_ context.Context, agent string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.active[agent] {
		d.compactWhileActive = true
		return ErrCompactBusy
	}
	d.compactCount++
	if d.compactReduction > 0 {
		d.contextTokens -= d.compactReduction
	}
	return nil
}

func (d *fakeDriver) sentCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	count := 0
	for _, sends := range d.sends {
		count += len(sends)
	}
	return count
}

func makeRun(t *testing.T, clock *fakeClock, yamlText, units, prompt, followup string, agents ...string) (*Store, Run) {
	t.Helper()
	root := t.TempDir()
	source, workdir := filepath.Join(root, "workflow-src"), filepath.Join(root, "workdir")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workdir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "workflow.yaml"), []byte(yamlText), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "prompt.md"), []byte(prompt), 0600); err != nil {
		t.Fatal(err)
	}
	if followup != "" {
		if err := os.WriteFile(filepath.Join(source, "followup.md"), []byte(followup), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workdir, "units.json"), []byte(units), 0600); err != nil {
		t.Fatal(err)
	}
	definition, err := ReadDefinition(source)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(root, "state"))
	store.Now = clock.Now
	run, err := store.CreateRun(source, definition, workdir, agents, "owner")
	if err != nil {
		t.Fatal(err)
	}
	return store, run
}

func baseYAML(extra string) string {
	return "name: sample\nversion: 1\nunits:\n  file: units.json\n  key: id\nprompt:\n  template: prompt.md\n" + extra
}

func runEngine(t *testing.T, store *Store, run Run, driver *fakeDriver, clock *fakeClock) *Engine {
	t.Helper()
	engine := NewEngine(store, driver)
	engine.Now, engine.Sleep, engine.Poll = clock.Now, clock.Sleep, time.Second
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	return engine
}

func TestEngineHappyPathAndTimes(t *testing.T) {
	clock := newFakeClock()
	store, run := makeRun(t, clock, baseYAML(""), `[{"id":"one","name":"First"}]`, "score {{.Key}}", "", "agent-a")
	driver := newFakeDriver(clock)
	runEngine(t, store, run, driver, clock)
	if driver.sentCount() != 1 {
		t.Fatalf("sent %d prompts, want one", driver.sentCount())
	}
	status, err := store.Status(run.ID)
	if err != nil || status.Status != "done" || status.Counts.Done != 1 || status.Counts.Pending != 0 {
		t.Fatalf("unexpected status %#v, %v", status, err)
	}
	rows, torn, err := store.Times(run.ID)
	if err != nil || torn || len(rows) != 1 || rows[0].Result != "done" || rows[0].Model != "gpt-6-luna" || rows[0].StartedAt == nil || rows[0].DeliveredAt == nil {
		t.Fatalf("unexpected timing row %#v torn=%t err=%v", rows, torn, err)
	}
}

func TestEngineValidatorRoundsAndWeakRules(t *testing.T) {
	for _, test := range []struct {
		name      string
		weakAfter int
		codes     []int
		want      string
	}{
		{name: "problems then ok", weakAfter: 2, codes: []int{1, 0}, want: "done"},
		{name: "rounds exhausted weak", weakAfter: 2, codes: []int{1, 1}, want: "weak"},
		{name: "weak-after not reached", weakAfter: 3, codes: []int{1, 1}, want: "failed"},
		{name: "exit three weak when allowed", weakAfter: 1, codes: []int{3}, want: "weak"},
		{name: "exit three retries before threshold", weakAfter: 2, codes: []int{3, 3}, want: "weak"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := newFakeClock()
			yamlText := baseYAML("prompt:\n  template: prompt.md\n  followup: followup.md\nvalidate:\n  rounds: 2\n  weak_after: " + fmt.Sprint(test.weakAfter) + "\n")
			// baseYAML already includes prompt.template; replace that block to avoid duplicate YAML keys.
			yamlText = strings.Replace(yamlText, "prompt:\n  template: prompt.md\n", "", 1)
			store, run := makeRun(t, clock, yamlText, `[{"id":"one"}]`, "initial {{.Key}}", "fix {{.Key}}: {{.Problems}}", "agent-a")
			driver := newFakeDriver(clock)
			engine := NewEngine(store, driver)
			engine.Now, engine.Sleep, engine.Poll = clock.Now, clock.Sleep, time.Second
			calls := 0
			engine.Validator = func(context.Context, string, []string, time.Duration) (int, string, string, error) {
				index := calls
				calls++
				if index < len(test.codes) {
					if test.codes[index] == 1 {
						return 1, "invalid score", "", nil
					}
					return test.codes[index], "unit exhausted", "", nil
				}
				return test.codes[len(test.codes)-1], "still invalid", "", nil
			}
			if err := engine.Run(context.Background(), run.ID, false); err != nil {
				t.Fatal(err)
			}
			rows, _, err := store.Times(run.ID)
			if err != nil || len(rows) != 1 || rows[0].Result != test.want {
				t.Fatalf("want %s, got %#v err=%v", test.want, rows, err)
			}
			if test.name == "problems then ok" && (driver.sentCount() != 2 || !strings.Contains(driver.sends["agent-a"][1], "invalid score")) {
				t.Fatalf("follow-up prompt did not include validator output: %#v", driver.sends)
			}
		})
	}
}

func TestEngineDeliveryFailureNeverStartedAndBlocked(t *testing.T) {
	t.Run("delivery failure continues", func(t *testing.T) {
		clock := newFakeClock()
		store, run := makeRun(t, clock, baseYAML(""), `[{"id":"one"},{"id":"two"}]`, "{{.Key}}", "", "agent-a")
		driver := newFakeDriver(clock)
		driver.deliveryResults = []Delivery{{Status: DeliveryFailed, Reason: "not delivered: queue failure"}, {Status: DeliveryDelivered}}
		runEngine(t, store, run, driver, clock)
		rows, _, err := store.Times(run.ID)
		if err != nil || len(rows) != 2 || rows[0].Result != "failed" || !strings.Contains(rows[0].Reason, "not delivered") || rows[1].Result != "done" {
			t.Fatalf("unit failure did not continue: %#v err=%v", rows, err)
		}
	})
	t.Run("delivered but never starts", func(t *testing.T) {
		clock := newFakeClock()
		yamlText := baseYAML("timeouts:\n  start: 2s\n")
		store, run := makeRun(t, clock, yamlText, `[{"id":"one"}]`, "{{.Key}}", "", "agent-a")
		driver := newFakeDriver(clock)
		driver.ignoreWork = true
		engine := NewEngine(store, driver)
		engine.Now, engine.Sleep, engine.Poll = clock.Now, clock.Sleep, time.Second
		if err := engine.Run(context.Background(), run.ID, false); err != nil {
			t.Fatal(err)
		}
		rows, _, err := store.Times(run.ID)
		if err != nil || len(rows) != 1 || rows[0].Result != "failed" || !strings.Contains(rows[0].Reason, "never started") || !strings.Contains(rows[0].Reason, "delivery record") {
			t.Fatalf("missing never-started evidence: %#v err=%v", rows, err)
		}
	})
	t.Run("completed turn is newer than delivery even when working was missed", func(t *testing.T) {
		clock := newFakeClock()
		store, run := makeRun(t, clock, baseYAML(""), `[{"id":"one"}]`, "{{.Key}}", "", "agent-a")
		driver := newFakeDriver(clock)
		driver.idleAfterSend = true
		runEngine(t, store, run, driver, clock)
		rows, _, err := store.Times(run.ID)
		if err != nil || len(rows) != 1 || rows[0].Result != "done" || rows[0].StartedAt.IsZero() {
			t.Fatalf("newer completed turn was mistaken for never-started: %#v err=%v", rows, err)
		}
	})
	t.Run("blocked waits before send", func(t *testing.T) {
		clock := newFakeClock()
		store, run := makeRun(t, clock, baseYAML(""), `[{"id":"one"}]`, "{{.Key}}", "", "agent-a")
		driver := newFakeDriver(clock)
		driver.observations["agent-a"] = []AgentState{Blocked, Blocked, Blocked, Idle, Idle}
		driver.onSend = func(agent, _ string) {
			if driver.idleStreak[agent] < 2 {
				t.Error("prompt sent before two idle observations")
			}
		}
		runEngine(t, store, run, driver, clock)
		if driver.sentCount() != 1 {
			t.Fatalf("expected one send after blocked state cleared, got %d", driver.sentCount())
		}
	})
}

func TestEngineCompactionBeforeUnitsAndTwoAgentQueue(t *testing.T) {
	t.Run("context threshold", func(t *testing.T) {
		clock := newFakeClock()
		store, run := makeRun(t, clock, baseYAML("compact:\n  ctx_above: 150\n"), `[{"id":"one"}]`, "{{.Key}}", "", "agent-a")
		driver := newFakeDriver(clock)
		driver.contextTokens, driver.compactReduction = 200, 100
		runEngine(t, store, run, driver, clock)
		rows, _, _ := store.Times(run.ID)
		if driver.compactCount != 1 || driver.compactWhileActive || len(rows) != 1 || rows[0].CompactResult != "effective" || rows[0].CompactCtxBefore == nil || rows[0].CompactCtxAfter == nil {
			t.Fatalf("unexpected context compact: count=%d active=%t rows=%#v", driver.compactCount, driver.compactWhileActive, rows)
		}
	})
	t.Run("every unit threshold", func(t *testing.T) {
		clock := newFakeClock()
		store, run := makeRun(t, clock, baseYAML("compact:\n  every_units: 1\n"), `[{"id":"one"},{"id":"two"}]`, "{{.Key}}", "", "agent-a")
		driver := newFakeDriver(clock)
		runEngine(t, store, run, driver, clock)
		if driver.compactCount != 1 || driver.compactWhileActive {
			t.Fatalf("expected one between-unit compact, got count=%d active=%t", driver.compactCount, driver.compactWhileActive)
		}
	})
	t.Run("two agents share queue without duplicates", func(t *testing.T) {
		clock := newFakeClock()
		store, run := makeRun(t, clock, baseYAML(""), `[{"id":"one"},{"id":"two"},{"id":"three"},{"id":"four"}]`, "{{.Key}}", "", "agent-a", "agent-b")
		driver := newFakeDriver(clock)
		runEngine(t, store, run, driver, clock)
		rows, _, err := store.Times(run.ID)
		if err != nil || len(rows) != 4 {
			t.Fatalf("expected four results, got %#v err=%v", rows, err)
		}
		seen := map[string]bool{}
		for _, row := range rows {
			if seen[row.Unit] {
				t.Fatalf("unit %s assigned twice", row.Unit)
			}
			seen[row.Unit] = true
		}
	})
}

func TestEngineResumeDoesNotResendAndAuthorityStops(t *testing.T) {
	for _, state := range []string{"sent", "working", "validating"} {
		t.Run("resume-"+state, func(t *testing.T) {
			clock := newFakeClock()
			store, run := makeRun(t, clock, baseYAML(""), `[{"id":"one"}]`, "{{.Key}}", "", "agent-a")
			start := clock.Now()
			_ = store.AppendEvent(run.ID, Event{Key: "one", Unit: "one", Agent: "agent-a", State: "sending", Round: 1})
			_ = store.AppendEvent(run.ID, Event{Key: "one", Unit: "one", Agent: "agent-a", State: "sent", Round: 1, DeliveryID: "queue-1", DeliveredAt: timePointer(start)})
			if state == "working" || state == "validating" {
				_ = store.AppendEvent(run.ID, Event{Key: "one", Unit: "one", Agent: "agent-a", State: "working", Round: 1, DeliveryID: "queue-1"})
			}
			if state == "validating" {
				_ = store.AppendEvent(run.ID, Event{Key: "one", Unit: "one", Agent: "agent-a", State: "validating", Round: 1, DeliveryID: "queue-1"})
			}
			driver := newFakeDriver(clock)
			runEngine(t, store, run, driver, clock)
			if driver.sentCount() != 0 {
				t.Fatalf("resume from %s resent the unit", state)
			}
			status, err := store.Status(run.ID)
			if err != nil || status.Counts.Done != 1 {
				t.Fatalf("resume from %s did not finish: %#v err=%v", state, status, err)
			}
		})
	}
	t.Run("authority lost mid-run", func(t *testing.T) {
		clock := newFakeClock()
		store, run := makeRun(t, clock, baseYAML(""), `[{"id":"one"},{"id":"two"}]`, "{{.Key}}", "", "agent-a")
		driver := newFakeDriver(clock)
		engine := NewEngine(store, driver)
		engine.Now, engine.Sleep, engine.Poll = clock.Now, clock.Sleep, time.Second
		checks := 0
		engine.Authority = func(string, string) error {
			checks++
			if checks == 2 {
				return errors.New("agent was reparented")
			}
			return nil
		}
		if err := engine.Run(context.Background(), run.ID, false); err == nil || !strings.Contains(err.Error(), "authority lost") {
			t.Fatalf("expected authority failure, got %v", err)
		}
		updated, _ := store.LoadRun(run.ID)
		if updated.Status != "error" || driver.sentCount() != 1 {
			t.Fatalf("authority loss did not stop run: status=%s sends=%d", updated.Status, driver.sentCount())
		}
	})
}

func TestValidateOutputAndTimeSummary(t *testing.T) {
	workdir := t.TempDir()
	definition := Definition{Output: OutputSpec{Path: "result.json", Format: "json", RequireFresh: true}}
	path := filepath.Join(workdir, "result.json")
	if err := os.WriteFile(path, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := validateOutput(definition, workdir, "result.json", time.Now().UTC().Format(time.RFC3339Nano)); err == nil || !strings.Contains(err.Error(), "not fresh") {
		t.Fatalf("expected freshness failure, got %v", err)
	}
	definition.Output.RequireFresh = false
	if err := validateOutput(definition, workdir, "result.json", time.Now().UTC().Format(time.RFC3339Nano)); err == nil || !strings.Contains(err.Error(), "valid JSON") {
		t.Fatalf("expected JSON parse failure, got %v", err)
	}
	stats := SummarizeTimes([]TimeRow{{Unit: "a", Agent: "one", Model: "m", Result: "done", Rounds: 1, WorkSeconds: 10}, {Unit: "b", Agent: "one", Model: "m", Result: "weak", Rounds: 2, WorkSeconds: 20, CompactSeconds: 3}, {Unit: "c", Agent: "two", Model: "n", Result: "failed", Rounds: 2, WorkSeconds: 30}})
	if stats.Count != 3 || stats.Done != 1 || stats.Weak != 1 || stats.Failed != 1 || stats.MedianWork != 20 || stats.P90Work != 30 || stats.ByModel["m"].MedianWork != 15 || stats.Rounds[2] != 2 || stats.CompactTotal != 3 {
		t.Fatalf("unexpected time summary: %#v", stats)
	}
}

func TestWaitReadOnlySemantics(t *testing.T) {
	clock := newFakeClock()
	driver := newFakeDriver(clock)
	driver.observations["agent-a"] = []AgentState{Working, Idle, Idle}
	result, err := Wait(context.Background(), driver, "agent-a", Idle, 2, 10*time.Second, time.Second, clock.Now, clock.Sleep)
	if err != nil || result.Confirmed != 2 || driver.sentCount() != 0 {
		t.Fatalf("unexpected wait result %#v err=%v sends=%d", result, err, driver.sentCount())
	}
	clock = newFakeClock()
	driver = newFakeDriver(clock)
	_, err = Wait(context.Background(), driver, "agent-a", Working, 1, 2*time.Second, time.Second, clock.Now, clock.Sleep)
	if !errors.Is(err, ErrWaitTimeout) || driver.sentCount() != 0 {
		t.Fatalf("want read-only timeout, got %v", err)
	}
	clock = newFakeClock()
	driver = newFakeDriver(clock)
	driver.observations["agent-a"] = []AgentState{Unknown}
	_, err = Wait(context.Background(), driver, "agent-a", Idle, 2, time.Second, time.Second, clock.Now, clock.Sleep)
	if !errors.Is(err, ErrAgentUnavailable) {
		t.Fatalf("want unavailable agent error, got %v", err)
	}
}

func TestTornFinalLineIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte("{\"ok\":true}\n{\"broken\":"), 0600); err != nil {
		t.Fatal(err)
	}
	rows, torn, err := readJSONLines[map[string]any](path)
	if err != nil || !torn || len(rows) != 1 {
		t.Fatalf("expected one record and torn flag, got %#v %t %v", rows, torn, err)
	}
}

func TestNoFailedOutputPathRequirement(t *testing.T) {
	if err := validateOutput(Definition{}, t.TempDir(), "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := RenderText("test", "{{.Missing}}", TemplateData{}); err == nil {
		t.Fatal("missing template fields must fail")
	}
	if got := truncateUTF8("abc", 2); got != "ab" {
		t.Fatalf("truncate mismatch: %s", got)
	}
}
