package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blueprint/internal/tokens"
	"blueprint/internal/workflow"
)

const guardedWorkflowYAML = `name: guarded
version: 1
units:
  file: units.json
  key: id
prompt:
  template: prompt.md
supervise:
  breaker:
    failures: 2
    open_for: 240h
  meltdown:
    failures: 5
    window: 30m
budget:
  run_tokens: 1000
`

// guardedRun adds a supervised workflow next to the fixture's plain one and
// creates a run of it.
func guardedRun(t *testing.T) (*app, *workflow.Store, workflow.Run) {
	t.Helper()
	a, store, workdir := workflowCLIFixture(t)
	source := filepath.Join(t.TempDir(), "guarded")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"workflow.yaml": guardedWorkflowYAML, "prompt.md": "do {{.Key}}", "units.json": `[{"id":"one"},{"id":"two"}]`} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.Add(source, false); err != nil {
		t.Fatal(err)
	}
	definition, err := workflow.ReadDefinition(store.WorkflowPath("guarded"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(store.WorkflowPath("guarded"), definition, workdir, []string{"worker"}, "root")
	if err != nil {
		t.Fatal(err)
	}
	return a, store, run
}

func TestParseWorkflowResume(t *testing.T) {
	for _, test := range []struct {
		args    []string
		id      string
		retry   bool
		tokens  int64
		wantErr string
	}{
		{args: []string{"run-1"}, id: "run-1"},
		{args: []string{"run-1", "--retry-failed"}, id: "run-1", retry: true},
		{args: []string{"--run-tokens", "5000000", "run-1"}, id: "run-1", tokens: 5000000},
		{args: []string{"run-1", "--retry-failed", "--run-tokens", "7"}, id: "run-1", retry: true, tokens: 7},
		{args: []string{}, wantErr: "usage: bp workflow resume"},
		{args: []string{"run-1", "run-2"}, wantErr: "usage: bp workflow resume"},
		{args: []string{"run-1", "--bogus"}, wantErr: "usage: bp workflow resume"},
		{args: []string{"run-1", "--run-tokens"}, wantErr: "--run-tokens needs a value"},
		{args: []string{"run-1", "--run-tokens", "0"}, wantErr: "--run-tokens must be a positive integer"},
		{args: []string{"run-1", "--run-tokens", "-3"}, wantErr: "--run-tokens must be a positive integer"},
		{args: []string{"run-1", "--run-tokens", "lots"}, wantErr: "--run-tokens must be a positive integer"},
	} {
		options, err := parseWorkflowResume(test.args)
		if test.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("%v: want error %q, got %v", test.args, test.wantErr, err)
			}
			continue
		}
		if err != nil || options.id != test.id || options.RetryFailed != test.retry || options.RunTokens != test.tokens {
			t.Fatalf("%v: got %#v err=%v", test.args, options, err)
		}
	}
}

func TestWorkflowResumeRunTokensOverride(t *testing.T) {
	a, store, run := guardedRun(t)
	if err := store.AppendTime(run.ID, workflow.TimeRow{Run: run.ID, Workflow: "guarded", Unit: "one", Agent: "worker", Result: "done", Rounds: 1, FinishedAt: time.Now().UTC(), Tokens: &workflow.UnitTokens{Input: 1500, Weighted: 1500}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateRun(run.ID, func(current *workflow.Run) error {
		current.Status, current.PauseReason = "paused", workflow.RunBudgetReason(1500, 1000)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// No daemon runs in the test, so Start fails after the resume is stored.
	err := a.workflow([]string{"resume", run.ID})
	if err == nil {
		t.Fatal("resume without a daemon should fail to start the worker")
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "note: run token budget exhausted (spent 1500 of 1000); the run will pause again before its next unit. Raise the ceiling with --run-tokens N.") {
		t.Fatalf("resume without a raise did not warn: %q", got)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"resume", run.ID, "--run-tokens", "5000"}); err == nil {
		t.Fatal("resume without a daemon should fail to start the worker")
	}
	if got := readTestOutput(t, a.out); strings.Contains(got, "note:") {
		t.Fatalf("raised ceiling still warned: %q", got)
	}
	updated, err := store.LoadRun(run.ID)
	if err != nil || updated.RunTokens != 5000 || updated.PauseReason != "" || updated.SupervisedSince.IsZero() {
		t.Fatalf("resume override not stored: %#v err=%v", updated, err)
	}
	if spent, ceiling, err := store.RunSpend(run.ID); err != nil || spent != 1500 || ceiling != 5000 {
		t.Fatalf("RunSpend=%d/%d err=%v", spent, ceiling, err)
	}
}

func TestWorkflowStatusShowsPauseBreakersAndBudget(t *testing.T) {
	a, store, run := guardedRun(t)
	now := time.Now().UTC()
	for _, key := range []string{"one", "two"} {
		if err := store.AppendEvent(run.ID, workflow.Event{At: now, Key: key, Unit: key, Agent: "worker", State: "failed", Reason: "validator exited with code 2"}); err != nil {
			t.Fatal(err)
		}
		if err := store.AppendTime(run.ID, workflow.TimeRow{Run: run.ID, Workflow: "guarded", Unit: key, Agent: "worker", Result: "failed", Rounds: 1, FinishedAt: now, Tokens: &workflow.UnitTokens{Input: 300, CacheRead: 1000, Weighted: 400}}); err != nil {
			t.Fatal(err)
		}
	}
	reason := "meltdown: 5 units failed within 30m; last: two on worker: validator exited with code 2"
	if _, err := store.UpdateRun(run.ID, func(current *workflow.Run) error {
		current.Status, current.PauseReason = "paused", reason
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.workflow([]string{"status", run.ID}); err != nil {
		t.Fatal(err)
	}
	got := readTestOutput(t, a.out)
	until := now.Add(240 * time.Hour).Format(time.RFC3339)
	for _, want := range []string{"status=paused", "pause_reason=" + reason, "breakers=[worker open until " + until + " (2 consecutive failures)]", "run_tokens=800/1000"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status text missing %q: %s", want, got)
		}
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"status", run.ID, "--json"}); err != nil {
		t.Fatal(err)
	}
	var status workflow.RunStatus
	if err := json.Unmarshal([]byte(readTestOutput(t, a.out)), &status); err != nil || status.PauseReason != reason || len(status.Breakers) != 1 || status.RunTokensSpent != 800 {
		t.Fatalf("status JSON: %#v err=%v", status, err)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"stop", run.ID}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "already paused") {
		t.Fatalf("stop of a paused run: %q", got)
	}
	if updated, _ := store.LoadRun(run.ID); updated.Status != "paused" || updated.StopRequested {
		t.Fatalf("stop changed a paused run: %#v", updated)
	}
}

func TestWorkflowShowAndCheckSupervision(t *testing.T) {
	a, _, _ := guardedRun(t)
	if err := a.workflow([]string{"show", "guarded"}); err != nil {
		t.Fatal(err)
	}
	got := readTestOutput(t, a.out)
	for _, want := range []string{"breaker: 2 consecutive failed units per agent, open for 240h", "meltdown: pause after 5 failed units within 30m", "budget: run=1000 cache_read=10%"} {
		if !strings.Contains(got, want) {
			t.Fatalf("show output missing %q: %s", want, got)
		}
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"check", "guarded"}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "warning: budget needs token usage data") || !strings.Contains(got, "OK: workflow guarded") {
		t.Fatalf("check did not warn about budget token data: %s", got)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"check", "sample"}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); strings.Contains(got, "warning") || strings.Contains(got, "breaker") {
		t.Fatalf("plain workflow printed supervision output: %s", got)
	}
}

func TestWorkflowTimesTokenLines(t *testing.T) {
	a, store, run := guardedRun(t)
	rows := []workflow.TimeRow{
		{Unit: "one", Agent: "worker", Model: "opus", Result: "done", Tokens: &workflow.UnitTokens{Input: 100, Output: 50, CacheRead: 1000, Weighted: 250}},
		{Unit: "two", Agent: "helper", Model: "haiku", Result: "failed", Tokens: &workflow.UnitTokens{Input: 900, Weighted: 900}},
		{Unit: "three", Agent: "worker", Model: "opus", Result: "done"},
	}
	for _, row := range rows {
		row.Run, row.Workflow, row.Rounds, row.FinishedAt = run.ID, "guarded", 1, time.Now().UTC()
		if err := store.AppendTime(run.ID, row); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.workflow([]string{"times", run.ID}); err != nil {
		t.Fatal(err)
	}
	got := readTestOutput(t, a.out)
	ordered := []string{
		"tokens: units=2/3 total=2050 weighted=1150 in=1000 out=50 cache_write=0 cache_read=1000",
		"tokens per unit: median=",
		"tokens agent helper: units=1 total=900 weighted=900",
		"tokens agent worker: units=1 total=1150 weighted=250",
		"tokens model haiku: units=1",
		"tokens model opus: units=1",
		"costliest: two agent=helper model=haiku result=failed total=900 weighted=900",
		"costliest: one agent=worker model=opus result=done total=1150 weighted=250",
	}
	last := -1
	for _, want := range ordered {
		index := strings.Index(got, want)
		if index <= last {
			t.Fatalf("times output missing or out of order at %q: %s", want, got)
		}
		last = index
	}
	// A run without token data prints no token lines.
	plain, plainStore, workdir := workflowCLIFixture(t)
	definition, err := workflow.ReadDefinition(plainStore.WorkflowPath("sample"))
	if err != nil {
		t.Fatal(err)
	}
	plainRun, err := plainStore.CreateRun(plainStore.WorkflowPath("sample"), definition, workdir, []string{"worker"}, "root")
	if err != nil {
		t.Fatal(err)
	}
	if err := plainStore.AppendTime(plainRun.ID, workflow.TimeRow{Run: plainRun.ID, Unit: "one", Agent: "worker", Result: "done", Rounds: 1, FinishedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := plain.workflow([]string{"times", plainRun.ID}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, plain.out); strings.Contains(got, "tokens") || strings.Contains(got, "costliest") {
		t.Fatalf("times without token data printed token lines: %s", got)
	}
}

func TestWorkflowDriverTokens(t *testing.T) {
	storeDir := t.TempDir()
	now := time.Now()
	since := now.Add(-time.Minute)
	day := now.In(time.FixedZone("Europe/Istanbul", 3*60*60)).Format("2006-01-02")
	if err := os.MkdirAll(filepath.Join(storeDir, "raw"), 0700); err != nil {
		t.Fatal(err)
	}
	record := tokens.RawRecord{TS: now.Add(-30 * time.Second).Format(time.RFC3339Nano), Agent: "worker", Requests: 1, In: 10, Out: 20, CacheWrite: 30, CacheRead: 40}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, "raw", day+".jsonl"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	checkpoints := filepath.Join(storeDir, "checkpoints.json")
	if err := os.WriteFile(checkpoints, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	driver := &workflowDriver{app: &app{}, runtimes: map[string]string{"worker": "claude", "script": "python", "ghost": "claude", "remote": "codex-remote"}, tokenStore: storeDir, now: func() time.Time { return now }}
	use, known, err := driver.Tokens(context.Background(), "worker", since, time.Time{})
	if err != nil || !known || use.Input != 10 || use.Output != 20 || use.CacheWrite != 30 || use.CacheRead != 40 || use.AsOf.IsZero() {
		t.Fatalf("claude agent tokens: %#v known=%t err=%v", use, known, err)
	}
	if _, known, err := driver.Tokens(context.Background(), "script", since, time.Time{}); err != nil || known {
		t.Fatalf("a harness the collector does not read must be unknown: known=%t err=%v", known, err)
	}
	if _, known, err := driver.Tokens(context.Background(), "unseen", since, time.Time{}); err != nil || known {
		t.Fatalf("an agent never observed must be unknown: known=%t err=%v", known, err)
	}
	// A metered agent the store has no record of at all is an attribution
	// miss, not zero usage.
	if use, known, err := driver.Tokens(context.Background(), "ghost", since, time.Time{}); err != nil || known {
		t.Fatalf("an agent with no records must be unknown, not zero: %#v known=%t err=%v", use, known, err)
	}
	// codex-remote is not proven to be collected under the agent's name.
	if _, known, err := driver.Tokens(context.Background(), "remote", since, time.Time{}); err != nil || known {
		t.Fatalf("codex-remote must be unknown: known=%t err=%v", known, err)
	}
	// A bounded interval that ends before the record counts nothing, but the
	// agent is still known (it has records nearby).
	if use, known, err := driver.Tokens(context.Background(), "worker", since, now.Add(-45*time.Second)); err != nil || !known || use.Input != 0 {
		t.Fatalf("bounded interval: %#v known=%t err=%v", use, known, err)
	}
	stale := now.Add(-tokenStoreStale - time.Minute)
	if err := os.Chtimes(checkpoints, stale, stale); err != nil {
		t.Fatal(err)
	}
	if _, known, err := driver.Tokens(context.Background(), "worker", since, time.Time{}); err != nil || known {
		t.Fatalf("a stale collection must be unknown: known=%t err=%v", known, err)
	}
	driver.tokenStore = filepath.Join(t.TempDir(), "missing")
	if _, known, err := driver.Tokens(context.Background(), "worker", since, time.Time{}); err != nil || known {
		t.Fatalf("a store without a collection must be unknown: known=%t err=%v", known, err)
	}
}

func TestWorkflowTimesSettlesTokensFromStore(t *testing.T) {
	a, store, run := guardedRun(t)
	finish := time.Now().UTC().Add(-time.Minute)
	start := finish.Add(-10 * time.Minute)
	early := start.Add(time.Minute)
	for _, row := range []workflow.TimeRow{
		// Recorded while the collector lagged: as_of before finished_at.
		{Unit: "one", Agent: "worker", Model: "opus", Result: "done", FinishedAt: finish, Tokens: &workflow.UnitTokens{Input: 10, Weighted: 10, Since: &start, AsOf: &early}},
		{Unit: "two", Agent: "helper", Model: "opus", Result: "done", FinishedAt: finish, Tokens: &workflow.UnitTokens{Input: 20, Weighted: 20, Since: &start, AsOf: &early}},
	} {
		row.Run, row.Workflow, row.Rounds = run.ID, "guarded", 1
		if err := store.AppendTime(run.ID, row); err != nil {
			t.Fatal(err)
		}
	}
	// No token store yet: both rows keep their recorded figures and are
	// reported as not final.
	if err := a.workflow([]string{"times", run.ID}); err != nil {
		t.Fatal(err)
	}
	got := readTestOutput(t, a.out)
	if !strings.Contains(got, "tokens: units=2/2 total=30 weighted=30") || !strings.Contains(got, "tokens not final yet: 2 units") {
		t.Fatalf("times without a token store: %s", got)
	}
	// The collector has caught up for worker; helper has no record at all.
	storeDir := a.tokenStoreDir()
	if err := os.MkdirAll(filepath.Join(storeDir, "raw"), 0700); err != nil {
		t.Fatal(err)
	}
	istanbul := time.FixedZone("Europe/Istanbul", 3*60*60)
	day := filepath.Join(storeDir, "raw", start.Add(5*time.Minute).In(istanbul).Format("2006-01-02")+".jsonl")
	record := tokens.RawRecord{TS: start.Add(5 * time.Minute).In(istanbul).Format(time.RFC3339Nano), Agent: "worker", Requests: 1, In: 1000, Out: 100}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(day, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, "checkpoints.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"times", run.ID}); err != nil {
		t.Fatal(err)
	}
	got = readTestOutput(t, a.out)
	if !strings.Contains(got, "tokens: units=2/2 total=1120 weighted=1120") || !strings.Contains(got, "tokens not final yet: 1 units") || !strings.Contains(got, "tokens agent worker: units=1 total=1100") {
		t.Fatalf("times did not settle worker's row from the store: %s", got)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"times", run.ID, "--json"}); err != nil {
		t.Fatal(err)
	}
	var stats workflow.TimeStats
	if err := json.Unmarshal([]byte(readTestOutput(t, a.out)), &stats); err != nil || stats.Tokens == nil || stats.Tokens.Unsettled != 1 || stats.Tokens.Weighted != 1120 {
		t.Fatalf("times JSON: %#v err=%v", stats.Tokens, err)
	}
	// Status run spend goes through the same settlement.
	a.out = testOutput(t)
	if err := a.workflow([]string{"status", run.ID}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "run_tokens=1120/1000") {
		t.Fatalf("status spend not settled: %s", got)
	}
}
