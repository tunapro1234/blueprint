package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blueprint/internal/book"
	bpconfig "blueprint/internal/config"
	"blueprint/internal/identity"
	"blueprint/internal/workflow"
)

func workflowCLIFixture(t *testing.T) (*app, *workflow.Store, string) {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "state")
	source := filepath.Join(root, "source")
	workdir := filepath.Join(root, "project")
	for _, path := range []string{source, workdir} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	definition := `name: sample
version: 1
units:
  file: units.json
  key: id
prompt:
  template: prompt.md
`
	if err := os.WriteFile(filepath.Join(source, "workflow.yaml"), []byte(definition), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "prompt.md"), []byte("do {{.Key}}"), 0600); err != nil {
		t.Fatal(err)
	}
	units := `[{"id":"one"},{"id":"two"}]`
	if err := os.WriteFile(filepath.Join(source, "units.json"), []byte(units), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "units.json"), []byte(units), 0600); err != nil {
		t.Fatal(err)
	}
	store := workflow.NewStore(state)
	if _, _, err := store.Add(source, false); err != nil {
		t.Fatal(err)
	}
	fleet := book.Fleet{
		Root: "root",
		Agents: map[string]book.Agent{
			"root": {Name: "root"}, "owner": {Name: "owner", Parent: "root"},
			"worker": {Name: "worker", Parent: "owner"}, "side": {Name: "side", Parent: "root"},
		},
		Parents: map[string]string{"owner": "root", "worker": "owner", "side": "root"},
	}
	a := &app{ctx: context.Background(), config: bpconfig.Config{StateDir: state, MsgqRoot: filepath.Join(state, "msgq")}, out: testOutput(t), err: testOutput(t)}
	a.loadFleet = func() (book.Fleet, map[string]book.State, error) { return fleet, map[string]book.State{}, nil }
	a.resolveSender = func() identity.Identity { return identity.Identity{Label: "root", Certain: true, Source: "tmux"} }
	return a, store, workdir
}

func TestWorkflowStartAuthorityAndDaemonChecks(t *testing.T) {
	t.Run("unverified caller refused", func(t *testing.T) {
		a, _, workdir := workflowCLIFixture(t)
		a.resolveSender = func() identity.Identity { return identity.Identity{Label: "root?", Source: "codex-unverified"} }
		err := a.workflow([]string{"start", "sample", "--workdir", workdir, "--agent", "worker", "--dry-run"})
		if err == nil || !strings.Contains(err.Error(), "verified authority") {
			t.Fatalf("expected unverified authority refusal, got %v", err)
		}
	})
	t.Run("sideways target refused", func(t *testing.T) {
		a, _, workdir := workflowCLIFixture(t)
		a.resolveSender = func() identity.Identity { return identity.Identity{Label: "owner", Certain: true, Source: "tmux"} }
		err := a.workflow([]string{"start", "sample", "--workdir", workdir, "--agent", "side", "--dry-run"})
		if err == nil || !strings.Contains(err.Error(), "outside owner") {
			t.Fatalf("expected hierarchy refusal, got %v", err)
		}
	})
	t.Run("daemon missing refuses before creating run", func(t *testing.T) {
		a, store, workdir := workflowCLIFixture(t)
		err := a.workflow([]string{"start", "sample", "--workdir", workdir, "--agent", "worker"})
		if err == nil || !strings.Contains(err.Error(), "requires the bp daemon") {
			t.Fatalf("expected daemon refusal, got %v", err)
		}
		runs, err := store.Runs()
		if err != nil || len(runs) != 0 {
			t.Fatalf("daemon refusal should not create a run: %#v err=%v", runs, err)
		}
	})
}

func TestWorkflowStatusAndTimesTextAndJSON(t *testing.T) {
	a, store, workdir := workflowCLIFixture(t)
	definition, err := workflow.ReadDefinition(store.WorkflowPath("sample"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(store.WorkflowPath("sample"), definition, workdir, []string{"worker"}, "root")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvent(run.ID, workflow.Event{Key: "one", Unit: "one", Agent: "worker", State: "done"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendTime(run.ID, workflow.TimeRow{Run: run.ID, Workflow: "sample", Unit: "one", Agent: "worker", Model: "gpt-6-luna", Result: "done", Rounds: 1, WorkSeconds: 12, FinishedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	run.Status = "done"
	if err := store.SaveRun(run); err != nil {
		t.Fatal(err)
	}
	if err := a.workflow([]string{"status", run.ID}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "done=1") || !strings.Contains(got, "pending=1") {
		t.Fatalf("unexpected status text: %s", got)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"status", run.ID, "--json"}); err != nil {
		t.Fatal(err)
	}
	var status workflow.RunStatus
	if err := json.Unmarshal([]byte(readTestOutput(t, a.out)), &status); err != nil || status.Counts.Done != 1 || status.Counts.Pending != 1 {
		t.Fatalf("unexpected status JSON: %#v err=%v", status, err)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"times", run.ID}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "count=1") || !strings.Contains(got, "median_work=12.0s") {
		t.Fatalf("unexpected times text: %s", got)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"times", run.ID, "--json"}); err != nil {
		t.Fatal(err)
	}
	var stats workflow.TimeStats
	if err := json.Unmarshal([]byte(readTestOutput(t, a.out)), &stats); err != nil || stats.Count != 1 || stats.Done != 1 {
		t.Fatalf("unexpected times JSON: %#v err=%v", stats, err)
	}
}
