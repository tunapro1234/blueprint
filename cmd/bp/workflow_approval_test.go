package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blueprint/internal/identity"
	"blueprint/internal/workflow"
)

const approvalWorkflowYAML = `name: gated
version: 1
units:
  file: units.json
  key: id
prompt:
  template: prompt.md
  followup: followup.md
approve:
  on: [weak, failed]
`

// approvalCLIRun creates a run of a gated workflow owned by "owner" with
// units one (proposed weak) and two (proposed failed) parked on worker.
func approvalCLIRun(t *testing.T) (*app, *workflow.Store, workflow.Run) {
	t.Helper()
	a, store, workdir := workflowCLIFixture(t)
	source := filepath.Join(t.TempDir(), "gated")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"workflow.yaml": approvalWorkflowYAML, "prompt.md": "do {{.Key}}", "followup.md": "fix {{.Problems}} {{.Key}}", "units.json": `[{"id":"one"},{"id":"two"}]`} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.Add(source, false); err != nil {
		t.Fatal(err)
	}
	definition, err := workflow.ReadDefinition(store.WorkflowPath("gated"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(store.WorkflowPath("gated"), definition, workdir, []string{"worker"}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	parkedAt := time.Now().UTC().Add(-90 * time.Second)
	for key, proposed := range map[string]string{"one": "weak", "two": "failed"} {
		started := parkedAt.Add(-time.Minute)
		row := workflow.TimeRow{Run: run.ID, Workflow: "gated", Unit: key, Agent: "worker", Result: proposed, Reason: "validator says " + proposed, Rounds: 1, StartedAt: &started, FinishedAt: parkedAt, WorkSeconds: 50, Approval: &workflow.RowApproval{Proposed: proposed}}
		event := workflow.Event{At: parkedAt, Unit: key, Key: key, Agent: "worker", State: workflow.StateAwaitingApproval, Round: 1, Reason: row.Reason, Park: &workflow.ParkRecord{Proposed: proposed, Started: started, Row: row}}
		if err := store.AppendEvent(run.ID, event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.UpdateRun(run.ID, func(current *workflow.Run) error {
		current.Status = workflow.StatusAwaitingApproval
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return a, store, run
}

func TestParseWorkflowDecide(t *testing.T) {
	for _, test := range []struct {
		command string
		args    []string
		want    workflowDecideOptions
		wantErr string
	}{
		{command: "approve", args: []string{"run-1", "one", "two"}, want: workflowDecideOptions{id: "run-1", DecideOptions: workflow.DecideOptions{Action: workflow.DecisionApprove, Units: []string{"one", "two"}}}},
		{command: "approve", args: []string{"run-1", "--all", "--note", "fine"}, want: workflowDecideOptions{id: "run-1", DecideOptions: workflow.DecideOptions{Action: workflow.DecisionApprove, All: true, Text: "fine"}}},
		{command: "approve", args: []string{"run-1", "one", "--edit", "use metres"}, want: workflowDecideOptions{id: "run-1", DecideOptions: workflow.DecideOptions{Action: workflow.DecisionRevise, Units: []string{"one"}, Text: "use metres"}}},
		{command: "reject", args: []string{"--reason", "wrong", "run-1", "--all"}, want: workflowDecideOptions{id: "run-1", DecideOptions: workflow.DecideOptions{Action: workflow.DecisionReject, All: true, Text: "wrong"}}},
		{command: "reject", args: []string{"run-1", "--", "-odd"}, want: workflowDecideOptions{id: "run-1", DecideOptions: workflow.DecideOptions{Action: workflow.DecisionReject, Units: []string{"-odd"}}}},
		{command: "approve", args: []string{}, wantErr: "usage: bp workflow approve"},
		{command: "approve", args: []string{"run-1"}, wantErr: "name the units to approve or use --all"},
		{command: "approve", args: []string{"run-1", "one", "--all"}, wantErr: "--all cannot be combined with unit names"},
		{command: "approve", args: []string{"run-1", "one", "two", "--edit", "x"}, wantErr: "--edit applies to exactly one unit"},
		{command: "approve", args: []string{"run-1", "--all", "--edit", "x"}, wantErr: "--edit applies to exactly one unit"},
		{command: "approve", args: []string{"run-1", "one", "--edit", " "}, wantErr: "--edit needs the revision text"},
		{command: "approve", args: []string{"run-1", "one", "--edit"}, wantErr: "--edit needs a value"},
		{command: "approve", args: []string{"run-1", "one", "--note", "a", "--edit", "b"}, wantErr: "only one text option"},
		{command: "approve", args: []string{"run-1", "one", "--reason", "x"}, wantErr: "unknown workflow approve option --reason"},
		{command: "reject", args: []string{"run-1", "one", "--note", "x"}, wantErr: "unknown workflow reject option --note"},
		{command: "reject", args: []string{"run-1", "one", "--edit", "x"}, wantErr: "unknown workflow reject option --edit"},
	} {
		got, err := parseWorkflowDecide(test.command, test.args)
		if test.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("%s %v: want error %q, got %v", test.command, test.args, test.wantErr, err)
			}
			continue
		}
		if err != nil || got.id != test.want.id || got.Action != test.want.Action || got.All != test.want.All || got.Text != test.want.Text || strings.Join(got.Units, ",") != strings.Join(test.want.Units, ",") {
			t.Fatalf("%s %v: got %#v err=%v", test.command, test.args, got, err)
		}
	}
}

func TestWorkflowApprovalsListing(t *testing.T) {
	a, store, run := approvalCLIRun(t)
	if err := a.workflow([]string{"approvals", run.ID}); err != nil {
		t.Fatal(err)
	}
	got := readTestOutput(t, a.out)
	for _, want := range []string{"one agent=worker proposed=weak round=1", "two agent=worker proposed=failed", "reason=validator says failed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("approvals output missing %q: %s", want, got)
		}
	}
	if _, err := store.Decide(run.ID, workflow.DecideOptions{Action: workflow.DecisionRevise, Units: []string{"one"}, Text: "again", By: "owner"}); err != nil {
		t.Fatal(err)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"approvals", run.ID, "--json"}); err != nil {
		t.Fatal(err)
	}
	var listed []workflow.Approval
	if err := json.Unmarshal([]byte(readTestOutput(t, a.out)), &listed); err != nil || len(listed) != 2 {
		t.Fatalf("approvals JSON %#v err=%v", listed, err)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"approvals", run.ID}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "revision pending on worker (requested by owner; approve or reject cancels it)") {
		t.Fatalf("pending revision not shown: %s", got)
	}
	// A worker claims it: the row says the round is running.
	events, _, _ := store.Events(run.ID)
	var revise workflow.Event
	for _, event := range events {
		if event.Key == "one" && event.State == workflow.StateRevise {
			revise = event
		}
	}
	revise.At, revise.State, revise.Reason = time.Now().UTC(), workflow.StateRevising, "revision claimed by the worker"
	if err := store.AppendEvent(run.ID, revise); err != nil {
		t.Fatal(err)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"approvals", run.ID}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "revision running on worker (requested by owner)") || strings.Contains(got, "cancels it") {
		t.Fatalf("running revision not shown: %s", got)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"approve", run.ID, "one"}); err == nil || !strings.Contains(err.Error(), "its revision round is running on worker") {
		t.Fatalf("approve of a running revision: %v", err)
	}
	for _, args := range [][]string{{"approvals"}, {"approvals", run.ID, "--bogus"}, {"approvals", run.ID, "extra"}} {
		if err := a.workflow(args); err == nil || !strings.Contains(err.Error(), "usage: bp workflow approvals") {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestWorkflowDecideAuthority(t *testing.T) {
	for _, test := range []struct {
		caller  identity.Identity
		wantErr string
	}{
		{caller: identity.Identity{Label: "owner", Certain: true, Source: "tmux"}},
		{caller: identity.Identity{Label: "root", Certain: true, Source: "tmux"}},
		{caller: identity.Identity{Label: "worker", Certain: true, Source: "tmux"}, wantErr: "only the run owner owner or an agent above it may decide; worker is not"},
		{caller: identity.Identity{Label: "side", Certain: true, Source: "tmux"}, wantErr: "side is not"},
		{caller: identity.Identity{Label: "owner?", Source: "codex-unverified"}, wantErr: "verified authority"},
	} {
		a, store, run := approvalCLIRun(t)
		a.resolveSender = func() identity.Identity { return test.caller }
		err := a.workflow([]string{"approve", run.ID, "one"})
		if test.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("%s: want %q, got %v", test.caller.Label, test.wantErr, err)
			}
			if approvals, _ := store.Approvals(run.ID); len(approvals) != 2 {
				t.Fatalf("%s: a refused decision changed the run", test.caller.Label)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", test.caller.Label, err)
		}
		row := timesRowFor(t, store, run.ID, "one")
		if row.Approval == nil || row.Approval.By != test.caller.Label {
			t.Fatalf("%s: decider not recorded: %#v", test.caller.Label, row.Approval)
		}
	}
}

func timesRowFor(t *testing.T, store *workflow.Store, runID, unit string) workflow.TimeRow {
	t.Helper()
	rows, _, err := store.Times(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Unit == unit {
			return row
		}
	}
	t.Fatalf("no times row for %s", unit)
	return workflow.TimeRow{}
}

func TestWorkflowApproveRejectOutput(t *testing.T) {
	a, store, run := approvalCLIRun(t)
	if err := a.workflow([]string{"approve", run.ID, "two", "--note", "good enough"}); err != nil {
		t.Fatal(err)
	}
	got := readTestOutput(t, a.out)
	if !strings.Contains(got, "approved two (worker): proposed failed, result weak (waited 1m30s)") || !strings.Contains(got, "1 units of run "+run.ID+" still await approval") {
		t.Fatalf("approve output: %s", got)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"approve", run.ID, "two"}); err == nil || !strings.Contains(err.Error(), `unit "two" is not awaiting approval (state weak)`) {
		t.Fatalf("second approve: %v", err)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"reject", run.ID, "--all", "--reason", "units are off"}); err != nil {
		t.Fatal(err)
	}
	got = readTestOutput(t, a.out)
	if !strings.Contains(got, "rejected one (worker): proposed weak, result failed") || !strings.Contains(got, "run "+run.ID+" is done") {
		t.Fatalf("reject output: %s", got)
	}
	if row := timesRowFor(t, store, run.ID, "one"); row.Reason != "rejected: units are off" || row.Approval.By != "root" {
		t.Fatalf("rejected row %#v", row)
	}
	if updated, _ := store.LoadRun(run.ID); updated.Status != "done" {
		t.Fatalf("status after the last decision: %s", updated.Status)
	}

	a.out = testOutput(t)
	if err := a.workflow([]string{"times", run.ID}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "approvals: approved=1 rejected=1 revised=0 median_decision_wait=") {
		t.Fatalf("times output: %s", got)
	}
}

func TestWorkflowEditRevisionHints(t *testing.T) {
	a, store, run := approvalCLIRun(t)
	if err := a.workflow([]string{"approve", run.ID, "one", "--edit", "use metres"}); err != nil {
		t.Fatal(err)
	}
	got := readTestOutput(t, a.out)
	if !strings.Contains(got, "revision of one recorded for worker") || !strings.Contains(got, "deliver the revision with: bp workflow resume "+run.ID) {
		t.Fatalf("revision without a worker: %s", got)
	}
	if work, err := store.ResumeWork(run.ID, false); err != nil || work.Revisions != 1 || work.Parked != 1 {
		t.Fatalf("resume work %#v err=%v", work, err)
	}
	// A live worker picks it up by itself.
	a2, store2, run2 := approvalCLIRun(t)
	if _, err := store2.UpdateRun(run2.ID, func(current *workflow.Run) error { current.Status = "running"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := a2.workflow([]string{"approve", run2.ID, "two", "--edit", "retry the parse"}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a2.out); !strings.Contains(got, "the running worker sends it to worker at that agent's next unit boundary") {
		t.Fatalf("revision with a worker: %s", got)
	}
}

func TestWorkflowApproveCancelsPendingRevision(t *testing.T) {
	a, store, run := approvalCLIRun(t)
	if err := a.workflow([]string{"approve", run.ID, "one", "--edit", "use metres"}); err != nil {
		t.Fatal(err)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"reject", run.ID, "--all"}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "rejected two (worker)") || strings.Contains(got, "one") {
		t.Fatalf("--all touched the pending revision: %s", got)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"approve", run.ID, "one", "--note", "fine as it is"}); err != nil {
		t.Fatal(err)
	}
	got := readTestOutput(t, a.out)
	if !strings.Contains(got, "cancelled the pending revision of one on worker") || !strings.Contains(got, "approved one (worker): proposed weak, result weak") || !strings.Contains(got, "run "+run.ID+" is done") {
		t.Fatalf("cancel output: %s", got)
	}
	if row := timesRowFor(t, store, run.ID, "one"); row.Approval == nil || row.Approval.Revisions != 1 || row.Approval.Decision != "approved" || row.Approval.Note != "fine as it is" {
		t.Fatalf("row %#v", row.Approval)
	}
}

func TestWorkflowStatusStopResumeTimesForAwaitingApproval(t *testing.T) {
	a, store, run := approvalCLIRun(t)
	if err := a.workflow([]string{"status", run.ID}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "status=awaiting-approval") || !strings.Contains(got, "awaiting_approval=2") {
		t.Fatalf("status output: %s", got)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"stop", run.ID}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "run "+run.ID+" is already waiting for decisions: 2 units await approval") {
		t.Fatalf("stop output: %s", got)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"resume", run.ID}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "nothing to run: run "+run.ID+" waits for decisions on 2 units") {
		t.Fatalf("resume output: %s", got)
	}
	if updated, _ := store.LoadRun(run.ID); updated.Status != workflow.StatusAwaitingApproval || updated.StopRequested {
		t.Fatalf("stop or resume changed the run: %#v", updated)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"times", "gated"}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "awaiting approval: 2 units (not in times.jsonl until decided)") {
		t.Fatalf("times output: %s", got)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"times", run.ID, "--json"}); err != nil {
		t.Fatal(err)
	}
	var stats workflow.TimeStats
	if err := json.Unmarshal([]byte(readTestOutput(t, a.out)), &stats); err != nil || stats.Approvals == nil || stats.Approvals.Waiting != 2 {
		t.Fatalf("times JSON %#v err=%v", stats.Approvals, err)
	}
	// A pending revision makes resume start a worker (which fails here: no
	// daemon runs in tests).
	if _, err := store.Decide(run.ID, workflow.DecideOptions{Action: workflow.DecisionRevise, Units: []string{"one"}, Text: "x", By: "owner"}); err != nil {
		t.Fatal(err)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"resume", run.ID}); err == nil || strings.Contains(readTestOutput(t, a.out), "nothing to run") {
		t.Fatalf("resume with a pending revision did not try to start a worker: %v", err)
	}
}

func TestWorkflowShowAndCheckApprove(t *testing.T) {
	a, _, _ := approvalCLIRun(t)
	if err := a.workflow([]string{"show", "gated"}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "approve: weak, failed wait for a human decision") {
		t.Fatalf("show output: %s", got)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"check", "gated"}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "OK: workflow gated") {
		t.Fatalf("check output: %s", got)
	}
	bad := filepath.Join(t.TempDir(), "bad")
	if err := os.MkdirAll(bad, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"workflow.yaml": strings.Replace(approvalWorkflowYAML, "[weak, failed]", "[weak, maybe]", 1), "prompt.md": "do {{.Key}}", "followup.md": "fix", "units.json": `[{"id":"one"}]`} {
		if err := os.WriteFile(filepath.Join(bad, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"check", bad}); err == nil || !strings.Contains(err.Error(), `approve.on: unknown result "maybe"`) {
		t.Fatalf("check of a bad approve.on: %v", err)
	}
}
