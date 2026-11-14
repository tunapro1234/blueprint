package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blueprint/internal/workflow"
)

const stepsWorkflowYAML = `name: pipeline
version: 1
units:
  file: units.json
  key: id
steps:
  - name: code
    agent: coder
    prompt:
      template: code.md
      followup: code-fix.md
    output:
      path: "out/{{.Key}}.txt"
    validate:
      command: [check, "{{.Key}}"]
  - name: review
    agent: reviewer
    prompt:
      template: review.md
    validate:
      command: [check, "{{.Steps.code.Output}}"]
approve:
  on: [failed]
`

// stepsCLIFixture adds the pipeline workflow next to the sample one. Its
// steps run on roles coder and reviewer.
func stepsCLIFixture(t *testing.T) (*app, *workflow.Store, string) {
	t.Helper()
	a, store, workdir := workflowCLIFixture(t)
	source := filepath.Join(t.TempDir(), "pipeline")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"workflow.yaml": stepsWorkflowYAML, "code.md": "code {{.Key}}", "code-fix.md": "fix {{.Problems}} {{.Key}}", "review.md": "review {{.Steps.code.Output}} {{.Key}}", "units.json": `[{"id":"one"},{"id":"two"}]`} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.Add(source, false); err != nil {
		t.Fatal(err)
	}
	return a, store, workdir
}

func TestParseWorkflowStartRoles(t *testing.T) {
	base := []string{"pipeline", "--workdir", "/tmp/w"}
	for _, test := range []struct {
		name   string
		args   []string
		agents []string
		roles  map[string]string
		want   string
	}{
		{name: "plain names", args: []string{"--agent", "a", "--agent", "b"}, agents: []string{"a", "b"}},
		{name: "roles", args: []string{"--agent", "coder=a", "--agent", "b", "--agent", "reviewer=c"}, agents: []string{"a", "b", "c"}, roles: map[string]string{"a": "coder", "c": "reviewer"}},
		{name: "empty role", args: []string{"--agent", "=a"}, want: `--agent "=a" needs both a role and an agent name`},
		{name: "empty name", args: []string{"--agent", "coder="}, want: `--agent "coder=" needs both a role and an agent name`},
		{name: "invalid role", args: []string{"--agent", "Coder=a"}, want: `--agent "Coder=a": invalid role "Coder"`},
		{name: "agent twice with two roles", args: []string{"--agent", "coder=a", "--agent", "reviewer=a"}, want: `invalid or duplicate agent "a"`},
		{name: "agent twice plain and with role", args: []string{"--agent", "a", "--agent", "coder=a"}, want: `invalid or duplicate agent "a"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			options, err := parseWorkflowStart(append(append([]string(nil), base...), test.args...))
			if test.want != "" {
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("want %q, got %v", test.want, err)
				}
				return
			}
			if err != nil || strings.Join(options.agents, ",") != strings.Join(test.agents, ",") || len(options.roles) != len(test.roles) {
				t.Fatalf("options %#v err=%v", options, err)
			}
			for agent, role := range test.roles {
				if options.roles[agent] != role {
					t.Fatalf("roles %v", options.roles)
				}
			}
		})
	}
	if _, err := parseWorkflowStart(base); err == nil || err.Error() != workflowStartUsage || !strings.Contains(err.Error(), "--agent [<role>=]<name>") {
		t.Fatalf("usage %v", err)
	}
}

func TestWorkflowStepsShowCheckAndDryRun(t *testing.T) {
	a, _, workdir := stepsCLIFixture(t)
	if err := a.workflow([]string{"show", "pipeline"}); err != nil {
		t.Fatal(err)
	}
	got := readTestOutput(t, a.out)
	for _, want := range []string{"steps: 2 (roles: coder, reviewer)\n", "step 1 code: role=coder prompt=code.md followup=code-fix.md\n", "  output: out/{{.Key}}.txt (any, fresh=false)\n", "  validator: check {{.Key}}\n", "step 2 review: role=reviewer prompt=review.md\n", "approve: failed wait"} {
		if !strings.Contains(got, want) {
			t.Fatalf("show lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\nprompt:") {
		t.Fatalf("show printed a top-level prompt:\n%s", got)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"check", "pipeline", "--workdir", workdir}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "steps: code (role coder) -> review (role reviewer)\nOK: workflow pipeline\n") {
		t.Fatalf("check output:\n%s", got)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"start", "pipeline", "--workdir", workdir, "--agent", "coder=worker", "--agent", "reviewer=side", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); got != "would run pipeline: units=2 agents=worker,side roles=coder=worker reviewer=side\n" {
		t.Fatalf("dry run %q", got)
	}
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"--agent", "coder=worker"}, `role "reviewer" has no agent; pass --agent reviewer=<name>`},
		{[]string{"--agent", "worker", "--agent", "reviewer=side"}, "agent worker has no role, but no step uses the default role"},
		{[]string{"--agent", "coder=worker", "--agent", "reviewer=side", "--agent", "tester=owner"}, `agent owner has role "tester", which no step uses`},
	} {
		err := a.workflow(append([]string{"start", "pipeline", "--workdir", workdir, "--dry-run"}, test.args...))
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%v: want %q, got %v", test.args, test.want, err)
		}
	}
	// A workflow without steps takes plain agents as before; a role there
	// is an error.
	a.out = testOutput(t)
	if err := a.workflow([]string{"start", "sample", "--workdir", workdir, "--agent", "worker", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); got != "would run sample: units=2 agents=worker\n" {
		t.Fatalf("dry run without steps %q", got)
	}
	if err := a.workflow([]string{"start", "sample", "--workdir", workdir, "--agent", "coder=worker", "--dry-run"}); err == nil || !strings.Contains(err.Error(), `agent worker has role "coder", which no step uses (roles: default)`) {
		t.Fatalf("role on a workflow without steps: %v", err)
	}
}

func TestWorkflowStepsStatusTimesAndApprovals(t *testing.T) {
	a, store, workdir := stepsCLIFixture(t)
	definition, err := workflow.ReadDefinition(store.WorkflowPath("pipeline"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRunWithRoles(store.WorkflowPath("pipeline"), definition, workdir, []string{"worker", "side"}, map[string]string{"worker": "coder", "side": "reviewer"}, "root")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	started := now.Add(-time.Minute)
	codeRow := workflow.TimeRow{Run: run.ID, Workflow: "pipeline", Unit: "one", Agent: "worker", Result: "done", Rounds: 1, StartedAt: &started, FinishedAt: now, WorkSeconds: 40}
	for _, event := range []workflow.Event{
		{Unit: "one", Key: "one", Agent: "worker", State: workflow.StateStepDone, Step: "code", Attempt: 1, StepRecord: &workflow.StepRecord{Name: "code", Attempt: 1, Output: "out/one.txt", Row: codeRow}},
		{Unit: "two", Key: "two", Agent: "side", State: "working", Step: "review", Attempt: 1},
	} {
		if err := store.AppendEvent(run.ID, event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.UpdateRun(run.ID, func(current *workflow.Run) error {
		current.Current = map[string]string{"side": "two"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.workflow([]string{"status", run.ID}); err != nil {
		t.Fatal(err)
	}
	got := readTestOutput(t, a.out)
	for _, want := range []string{"pending=2 ready=[review=1] agents=[", "worker[coder]:-", "side[reviewer]:two@review"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status lacks %q: %s", want, got)
		}
	}

	row := workflow.TimeRow{Run: run.ID, Workflow: "pipeline", Unit: "two", Agent: "side", Result: "done", Rounds: 2, FinishedAt: now, WorkSeconds: 50, Steps: []workflow.StepRow{
		{Name: "code", Attempt: 1, Agent: "worker", Result: "done", Rounds: 1, WorkSeconds: 30},
		{Name: "review", Attempt: 1, Agent: "side", Result: "done", Rounds: 1, WorkSeconds: 20, QueuedSeconds: 4},
	}}
	if err := store.AppendTime(run.ID, row); err != nil {
		t.Fatal(err)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"times", run.ID}); err != nil {
		t.Fatal(err)
	}
	got = readTestOutput(t, a.out)
	for _, want := range []string{"step code: units=1 failed=0 median_work=30.0s p90_work=30.0s median_queue=0.0s\n", "step review: units=1 failed=0 median_work=20.0s p90_work=20.0s median_queue=4.0s\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("times lacks %q:\n%s", want, got)
		}
	}

	// Unit one fails its code step and parks; approving it continues the
	// unit with the review step.
	failedRow := codeRow
	failedRow.Result, failedRow.Reason = "failed", "validator exited with code 2"
	park := &workflow.ParkRecord{Proposed: "failed", Started: started, Row: failedRow, StepRecord: &workflow.StepRecord{Name: "code", Attempt: 2, Output: "out/one.txt", Row: failedRow}}
	if err := store.AppendEvent(run.ID, workflow.Event{At: now, Unit: "one", Key: "one", Agent: "worker", State: workflow.StateAwaitingApproval, Round: 1, Reason: failedRow.Reason, Step: "code", Attempt: 2, Park: park}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateRun(run.ID, func(current *workflow.Run) error {
		current.Status, current.Current = workflow.StatusAwaitingApproval, nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"approvals", run.ID}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "one agent=worker step=code proposed=failed") {
		t.Fatalf("approvals:\n%s", got)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"approve", run.ID, "one"}); err != nil {
		t.Fatal(err)
	}
	got = readTestOutput(t, a.out)
	if !strings.Contains(got, "approved one (worker) at step code as weak; continues with step review") || !strings.Contains(got, "run "+run.ID+" has no worker (status awaiting-approval); continue it with: bp workflow resume "+run.ID) {
		t.Fatalf("approve output:\n%s", got)
	}
}

const loopsWorkflowYAML = `name: loops
version: 1
units:
  file: units.json
  key: id
steps:
  - name: code
    agent: coder
    fallback: [senior]
    prompt:
      template: code.md
      followup: code-fix.md
    output:
      path: "out/{{.Key}}.txt"
    validate:
      command: [check, "{{.Key}}"]
  - name: review
    agent: reviewer
    send_back:
      to: code
      max: 2
    prompt:
      template: review.md
    validate:
      command: [check, "{{.Steps.code.Output}}"]
`

func TestWorkflowStepsLoopsShowCheckStatusAndTimes(t *testing.T) {
	a, store, workdir := workflowCLIFixture(t)
	source := filepath.Join(t.TempDir(), "loops")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"workflow.yaml": loopsWorkflowYAML, "code.md": "code a{{.Attempt}} {{.Previous.Result}} {{.Key}}", "code-fix.md": "fix {{.Problems}} {{.Key}}", "review.md": "review {{.Steps.code.Output}} {{.Key}}"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.Add(source, false); err != nil {
		t.Fatal(err)
	}
	if err := a.workflow([]string{"show", "loops"}); err != nil {
		t.Fatal(err)
	}
	got := readTestOutput(t, a.out)
	for _, want := range []string{"steps: 2 (roles: coder, reviewer, senior)\n", "  fallback: senior\n", "  send_back: to=code max=2 (validator exit 4)\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("show lacks %q:\n%s", want, got)
		}
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"check", "loops", "--workdir", workdir}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "steps: code (role coder, fallback senior) -> review (role reviewer, send_back to code max 2)\n") {
		t.Fatalf("check output:\n%s", got)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"start", "loops", "--workdir", workdir, "--agent", "coder=worker", "--agent", "reviewer=side", "--agent", "senior=owner", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); got != "would run loops: units=2 agents=worker,side,owner roles=coder=worker reviewer=side senior=owner\n" {
		t.Fatalf("dry run %q", got)
	}
	if err := a.workflow([]string{"start", "loops", "--workdir", workdir, "--agent", "coder=worker", "--agent", "reviewer=side", "--dry-run"}); err == nil || !strings.Contains(err.Error(), `role "senior" has no agent; pass --agent senior=<name>`) {
		t.Fatalf("a fallback role without an agent: %v", err)
	}

	definition, err := workflow.ReadDefinition(store.WorkflowPath("loops"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRunWithRoles(store.WorkflowPath("loops"), definition, workdir, []string{"worker", "side", "owner"}, map[string]string{"worker": "coder", "side": "reviewer", "owner": "senior"}, "root")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	codeRow := workflow.TimeRow{Run: run.ID, Workflow: "loops", Unit: "one", Agent: "worker", Result: "done", Rounds: 1, FinishedAt: now, WorkSeconds: 40}
	reviewRow := workflow.TimeRow{Run: run.ID, Workflow: "loops", Unit: "one", Agent: "side", Result: workflow.StateSentBack, Reason: "missing tests", Rounds: 1, FinishedAt: now, WorkSeconds: 10}
	for _, event := range []workflow.Event{
		{Unit: "one", Key: "one", Agent: "worker", State: workflow.StateStepDone, Step: "code", Attempt: 1, StepRecord: &workflow.StepRecord{Name: "code", Attempt: 1, Role: "coder", AttemptReason: workflow.AttemptFirst, Output: "out/one.txt", Row: codeRow}},
		{Unit: "one", Key: "one", Agent: "side", State: workflow.StateSentBack, Step: "review", Attempt: 1, Reason: "sent back to step code: missing tests", SendBack: &workflow.SendBackRecord{To: "code", Count: 1, Max: 2, Problems: "missing tests"}, StepRecord: &workflow.StepRecord{Name: "review", Attempt: 1, Role: "reviewer", AttemptReason: workflow.AttemptFirst, SentBackTo: "code", Row: reviewRow}},
		{Unit: "two", Key: "two", Agent: "owner", State: "working", Step: "code", Attempt: 2, AttemptReason: workflow.AttemptFallback},
	} {
		if err := store.AppendEvent(run.ID, event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.UpdateRun(run.ID, func(current *workflow.Run) error {
		current.Current = map[string]string{"owner": "two"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"status", run.ID}); err != nil {
		t.Fatal(err)
	}
	got = readTestOutput(t, a.out)
	for _, want := range []string{"ready=[code=1 (1 for worker)]", "owner[senior]:two@code#2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status lacks %q: %s", want, got)
		}
	}

	row := workflow.TimeRow{Run: run.ID, Workflow: "loops", Unit: "one", Agent: "side", Result: "done", Rounds: 5, FinishedAt: now, WorkSeconds: 100, Steps: []workflow.StepRow{
		{Name: "code", Attempt: 1, Agent: "worker", Role: "coder", AttemptReason: workflow.AttemptFirst, Result: "failed", Rounds: 1, WorkSeconds: 30},
		{Name: "code", Attempt: 2, Agent: "owner", Role: "senior", AttemptReason: workflow.AttemptFallback, Result: "done", Rounds: 1, WorkSeconds: 30},
		{Name: "review", Attempt: 1, Agent: "side", Role: "reviewer", AttemptReason: workflow.AttemptFirst, Result: workflow.StateSentBack, SentBackTo: "code", Rounds: 1, WorkSeconds: 10},
		{Name: "code", Attempt: 3, Agent: "owner", Role: "senior", AttemptReason: workflow.AttemptSendBack, Result: "done", Rounds: 1, WorkSeconds: 20},
		{Name: "review", Attempt: 2, Agent: "side", Role: "reviewer", AttemptReason: workflow.AttemptRerun, Result: "done", Rounds: 1, WorkSeconds: 10},
	}}
	if err := store.AppendTime(run.ID, row); err != nil {
		t.Fatal(err)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"times", run.ID}); err != nil {
		t.Fatal(err)
	}
	got = readTestOutput(t, a.out)
	for _, want := range []string{
		"step code: units=3 failed=1 ",
		"step code loops: sent_back=0 redo=1 rerun=0 fallback=1 rescued=1\n",
		"step review loops: sent_back=1 redo=0 rerun=1 fallback=0 rescued=0\n",
		"send-backs: units=1/1 median_per_unit=1.0\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("times lacks %q:\n%s", want, got)
		}
	}
}
