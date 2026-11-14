package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// loopFiles are prompts that show the template data of send-backs and
// fallbacks: "<name> a<attempt> [<problems>] prev=<previous result> <key>"
// and "fix <name> a<attempt> [<problems>] <key>".
func loopFiles(names ...string) map[string]string {
	files := map[string]string{}
	for _, name := range names {
		files[name+".md"] = name + " a{{.Attempt}} [{{.Problems}}] prev={{.Previous.Result}} {{.Key}}"
		files[name+"-fix.md"] = "fix " + name + " a{{.Attempt}} [{{.Problems}}] {{.Key}}"
	}
	return files
}

func withSendBack(block, to string, max int) string {
	block += "    send_back:\n      to: " + to + "\n"
	if max > 0 {
		block += fmt.Sprintf("      max: %d\n", max)
	}
	return block
}

func withFallback(block string, roles ...string) string {
	return block + "    fallback: [" + strings.Join(roles, ", ") + "]\n"
}

// codeBlock and reviewBlock are the steps of the loop tests: code writes
// out/<key>.txt, review checks it.
func codeBlock(role string, rounds int) string {
	return stepBlock("code", role, "out/{{.Key}}.txt", "none", rounds, rounds+1)
}

func reviewBlock(role string, rounds int) string {
	return stepBlock("review", role, "", "none", rounds, rounds+1)
}

// stepDetails lists name/agent/result/attempt/role/attempt_reason per step.
func stepDetails(row TimeRow) []string {
	var details []string
	for _, step := range row.Steps {
		details = append(details, fmt.Sprintf("%s/%s/%s/%d/%s/%s", step.Name, step.Agent, step.Result, step.Attempt, step.Role, step.AttemptReason))
	}
	return details
}

func unitRows(t *testing.T, store *Store, runID, key string) []TimeRow {
	t.Helper()
	rows, _, err := store.Times(runID)
	if err != nil {
		t.Fatal(err)
	}
	var result []TimeRow
	for _, row := range rows {
		if row.Unit == key {
			result = append(result, row)
		}
	}
	return result
}

// assertStepAttemptsInOneRow checks that every (unit, step, attempt) is in
// exactly one row and that each row's totals are the sums of its steps.
func assertStepAttemptsInOneRow(t *testing.T, rows []TimeRow) map[string]int {
	t.Helper()
	seen := map[string]int{}
	for _, row := range rows {
		var work, validate, wait float64
		var rounds int
		for _, step := range row.Steps {
			seen[fmt.Sprintf("%s/%s/%d", row.Unit, step.Name, step.Attempt)]++
			work, validate, wait, rounds = work+step.WorkSeconds, validate+step.ValidateSeconds, wait+step.WaitSeconds, rounds+step.Rounds
		}
		near := func(a, b float64) bool { return a-b < 1e-6 && b-a < 1e-6 }
		if !near(row.WorkSeconds, work) || !near(row.ValidateSeconds, validate) || !near(row.WaitSeconds, wait) || row.Rounds != rounds || len(row.RoundSeconds) != rounds {
			t.Fatalf("%s row totals are not the sums of its steps: %#v", row.Unit, row)
		}
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("%s is in %d rows", id, count)
		}
	}
	return seen
}

func TestStepLoopDefinitionRules(t *testing.T) {
	code, review := codeBlock("coder", 2), reviewBlock("reviewer", 2)
	files := loopFiles("code", "review")
	noFollowup := loopFiles("code", "review")
	for _, test := range []struct {
		name  string
		yaml  string
		files map[string]string
		want  string
	}{
		{"send_back on the first step", stepsHeader + withSendBack(code, "review", 0) + review, files, "steps.code.send_back is not allowed on the first step"},
		{"send_back without to", stepsHeader + code + review + "    send_back:\n      max: 2\n", files, "steps.review.send_back.to is required"},
		{"send_back to an unknown step", stepsHeader + code + withSendBack(review, "coding", 0), files, `steps.review.send_back.to refers to unknown step "coding"`},
		{"send_back to itself", stepsHeader + code + withSendBack(review, "review", 0), files, `steps.review.send_back.to must name an earlier step; "review" does not run before "review"`},
		{"send_back to a later step", stepsHeader + withSendBack(code, "review", 0) + review + stepBlock("ship", "coder", "", "none", 1, 2), loopFiles("code", "review", "ship"), "steps.code.send_back is not allowed on the first step"},
		{"send_back forward from a middle step", stepsHeader + code + withSendBack(review, "ship", 0) + stepBlock("ship", "coder", "", "none", 1, 2), loopFiles("code", "review", "ship"), `steps.review.send_back.to must name an earlier step; "ship" does not run before "review"`},
		{"send_back to a step without follow-up", stepsHeader + strings.Replace(code, "      followup: code-fix.md\n", "", 1) + withSendBack(review, "code", 0), noFollowup, `steps.review.send_back.to names step "code", which has no prompt.followup`},
		{"send_back max zero", stepsHeader + code + withSendBack(review, "code", 0) + "      max: 0\n", files, "steps.review.send_back.max must be a positive integer, got 0"},
		{"send_back max negative", stepsHeader + code + withSendBack(review, "code", 0) + "      max: -1\n", files, "steps.review.send_back.max must be a positive integer, got -1"},
		{"send_back unknown key", stepsHeader + code + withSendBack(review, "code", 0) + "      tries: 2\n", files, "field tries not found"},
		{"fallback is the step's own role", stepsHeader + withFallback(code, "senior", "coder") + review, files, `steps.code.fallback lists "coder", the step's own role`},
		{"fallback default role", stepsHeader + withFallback(codeBlock("", 2), "default") + review, files, `steps.code.fallback lists "default", the step's own role`},
		{"fallback twice", stepsHeader + withFallback(code, "senior", "senior") + review, files, `steps.code.fallback lists role "senior" twice`},
		{"fallback invalid role", stepsHeader + withFallback(code, "Senior") + review, files, `steps.code.fallback lists "Senior", which is not a valid role`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := readPipeline(t, test.yaml, test.files); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want %q, got %v", test.want, err)
			}
		})
	}
	t.Run("valid loops", func(t *testing.T) {
		d, err := readPipeline(t, stepsHeader+withFallback(code, "senior", "lead")+withFallback(withSendBack(review, "code", 3), "senior"), files)
		if err != nil {
			t.Fatal(err)
		}
		if d.Steps[1].SendBack == nil || d.Steps[1].SendBack.To != "code" || d.Steps[1].SendBack.Limit() != 3 || !reflect.DeepEqual(d.Steps[0].Fallback, []string{"senior", "lead"}) {
			t.Fatalf("steps %#v", d.Steps)
		}
		if roles := d.Roles(); !reflect.DeepEqual(roles, []string{"coder", "reviewer", "senior", "lead"}) {
			t.Fatalf("roles %v", roles)
		}
		if err := ValidateRoles(d, []string{"a", "b", "s"}, map[string]string{"a": "coder", "b": "reviewer", "s": "senior"}); err == nil || !strings.Contains(err.Error(), `role "lead" has no agent; pass --agent lead=<name>`) {
			t.Fatalf("a fallback role without an agent: %v", err)
		}
		// An agent whose role is only a fallback role is valid.
		if err := ValidateRoles(d, []string{"a", "b", "s", "l"}, map[string]string{"a": "coder", "b": "reviewer", "s": "senior", "l": "lead"}); err != nil {
			t.Fatal(err)
		}
		def, err := readPipeline(t, stepsHeader+code+withSendBack(review, "code", 0), files)
		if err != nil || def.Steps[1].SendBack.Limit() != 1 || def.Steps[1].SendBack.Max != nil {
			t.Fatalf("default max: %#v %v", def.Steps[1].SendBack, err)
		}
	})
}

func TestStepTemplateAttemptAndPrevious(t *testing.T) {
	data := templateData(Unit{Key: "u1"}, "/w", "run", 1, "", "", "", stepData{})
	if data.Attempt != 1 || data.Previous != (StepAttempt{}) {
		t.Fatalf("data without steps: attempt %d previous %#v", data.Attempt, data.Previous)
	}
	files := loopFiles("code", "review")
	root := t.TempDir()
	writeDefinition(t, root, stepsHeader+codeBlock("coder", 2)+withSendBack(reviewBlock("reviewer", 2), "code", 0), "", `[{"id":"one"}]`)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	d, err := ReadDefinition(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckSampleTemplates(root, root, d); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "review.md"), []byte("review {{.Previous.Score}} {{.Key}}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckSampleTemplates(root, root, d); err == nil || !strings.Contains(err.Error(), "Score") {
		t.Fatalf("an unknown .Previous field passed the check: %v", err)
	}
	// A branch taken only on a later attempt is checked as well.
	if err := os.WriteFile(filepath.Join(root, "review.md"), []byte("review {{.Key}}{{if gt .Attempt 1}} {{.Previous.Nope}}{{end}}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckSampleTemplates(root, root, d); err == nil || !strings.Contains(err.Error(), "as attempt 2") || !strings.Contains(err.Error(), "Nope") {
		t.Fatalf("an unknown field behind .Attempt passed the check: %v", err)
	}
}

func TestStepsSendBackRedoesOnTheSameAgent(t *testing.T) {
	agents, roles := coderReviewer()
	p := newPipeline(t, stepsHeader+codeBlock("coder", 2)+withSendBack(reviewBlock("reviewer", 2), "code", 0)+"compact:\n  every_units: 1\nsupervise:\n  breaker:\n    failures: 1\n    open_for: 30m\n", loopFiles("code", "review"), []string{"u1"}, agents, roles)
	p.checks.code = func(step, _ string, call int) int {
		if step == "review" && call == 1 {
			return 4
		}
		return 0
	}
	p.runEngine(t, false)
	want := []string{
		"agent-a|code a1 [] prev= u1",
		"agent-b|review a1 [] prev= u1",
		"agent-a|fix code a2 [problems in review u1] u1",
		"agent-b|review a2 [] prev=sent_back u1",
	}
	if got := p.sends(); !reflect.DeepEqual(got, want) {
		t.Fatalf("sends %q", got)
	}
	if p.compactionsAt(0) != p.compactionsAt(2) {
		t.Fatalf("compacted before the send-back redo: %q", p.log)
	}
	rows := unitRows(t, p.store, p.run.ID, "u1")
	wantSteps := []string{"code/agent-a/done/1/coder/first", "review/agent-b/sent_back/1/reviewer/first", "code/agent-a/done/2/coder/send_back", "review/agent-b/done/2/reviewer/rerun"}
	if len(rows) != 1 || rows[0].Result != "done" || !reflect.DeepEqual(stepDetails(rows[0]), wantSteps) || rows[0].Steps[1].SentBackTo != "code" || rows[0].Steps[1].Reason != "problems in review u1" || rows[0].Rounds != 4 {
		t.Fatalf("rows %#v steps %v", rows, stepDetails(rows[0]))
	}
	sent := eventsWithState(t, p.store, p.run.ID, StateSentBack)
	if len(sent) != 1 || sent[0].SendBack == nil || *sent[0].SendBack != (SendBackRecord{To: "code", Count: 1, Max: 1, Problems: "problems in review u1"}) || sent[0].Reason != "sent back to step code: problems in review u1" || sent[0].AttemptReason != AttemptFirst {
		t.Fatalf("sent_back events %#v", sent)
	}
	// A send-back is a success for the reviewer's breaker.
	if opened := eventsWithState(t, p.store, p.run.ID, eventBreakerOpen); len(opened) != 0 {
		t.Fatalf("breaker opened: %#v", opened)
	}
	assertStepAttemptsInOneRow(t, rows)
	stats := SummarizeTimes(rows)
	if code, review := stats.Steps[0], stats.Steps[1]; code.Units != 2 || code.Redos != 1 || code.SentBack != 0 || review.SentBack != 1 || review.Reruns != 1 || review.Redos != 0 {
		t.Fatalf("step stats %#v", stats.Steps)
	}
	if stats.SendBacks == nil || stats.SendBacks.Units != 1 || stats.SendBacks.Median != 1 {
		t.Fatalf("send-back stats %#v", stats.SendBacks)
	}
	if status, err := p.store.Status(p.run.ID); err != nil || status.Status != "done" || status.Counts.Done != 1 || status.Counts.Ready != nil {
		t.Fatalf("status %#v %v", status, err)
	}
}

func TestStepsSendBackExhausted(t *testing.T) {
	t.Run("fails the unit for meltdown, not the breaker, without fallback", func(t *testing.T) {
		agents := []string{"agent-a", "agent-b", "agent-s"}
		roles := map[string]string{"agent-a": "coder", "agent-b": "reviewer", "agent-s": "senior"}
		yaml := stepsHeader + codeBlock("coder", 2) + withFallback(withSendBack(reviewBlock("reviewer", 2), "code", 0), "senior") +
			"supervise:\n  breaker:\n    failures: 1\n    open_for: 30m\n  meltdown:\n    failures: 1\n    window: 30m\n"
		p := newPipeline(t, yaml, loopFiles("code", "review"), []string{"u1"}, agents, roles)
		p.checks.code = func(step, _ string, _ int) int {
			if step == "review" {
				return 4
			}
			return 0
		}
		p.runEngine(t, false)
		if got := p.sends(); len(got) != 4 || len(p.driver.sendsFor("agent-s")) != 0 {
			t.Fatalf("sends %q", got)
		}
		failed := latestState(t, p.store, p.run.ID)["u1"]
		if failed.State != "failed" || failed.Reason != "sent back 1 times; last: problems in review u1" || failed.SendBack == nil || !failed.SendBack.Exhausted || failed.Step != "review" || failed.Attempt != 2 {
			t.Fatalf("failed event %#v", failed)
		}
		row := unitRows(t, p.store, p.run.ID, "u1")[0]
		if row.Result != "failed" || row.Agent != "agent-b" || !reflect.DeepEqual(stepNamesOf(row), []string{"code/agent-a/done/1", "review/agent-b/sent_back/1", "code/agent-a/done/2", "review/agent-b/failed/2"}) {
			t.Fatalf("row %#v %v", row, stepNamesOf(row))
		}
		if opened := eventsWithState(t, p.store, p.run.ID, eventBreakerOpen); len(opened) != 0 {
			t.Fatalf("an exhausted send-back opened a breaker: %#v", opened)
		}
		events, _, _ := p.store.Events(p.run.ID)
		if b := deriveBreakers(events, time.Time{})["agent-b"]; b != nil && b.consecutive != 0 {
			t.Fatalf("replayed breaker %#v", b)
		}
		if paused := eventsWithState(t, p.store, p.run.ID, eventRunPaused); len(paused) != 1 || !strings.HasPrefix(paused[0].Reason, "meltdown: 1 units failed within 30m; last: u1 on agent-b: sent back 1 times") {
			t.Fatalf("meltdown %#v", paused)
		}
	})
	t.Run("parks; approval accepts the step as weak and continues", func(t *testing.T) {
		agents, roles := coderReviewer()
		yaml := stepsHeader + codeBlock("coder", 2) + withSendBack(reviewBlock("reviewer", 2), "code", 2) + stepBlock("ship", "coder", "", "none", 1, 2) + "approve:\n  on: [failed]\n"
		p := newPipeline(t, yaml, loopFiles("code", "review", "ship"), []string{"u1"}, agents, roles)
		p.checks.code = func(step, _ string, _ int) int {
			if step == "review" {
				return 4
			}
			return 0
		}
		p.runEngine(t, false)
		parked := latestState(t, p.store, p.run.ID)["u1"]
		if parked.State != StateAwaitingApproval || parked.Step != "review" || parked.Attempt != 3 || parked.Park.Proposed != "failed" || parked.Reason != "sent back 2 times; last: problems in review u1" {
			t.Fatalf("parked %#v", parked)
		}
		outcome, err := p.store.Decide(p.run.ID, DecideOptions{Action: DecisionApprove, Units: []string{"u1"}, By: "boss"})
		if err != nil || outcome.Outcomes[0].NextStep != "ship" {
			t.Fatalf("approve %#v %v", outcome, err)
		}
		if _, err := p.store.Resume(p.run.ID, ResumeOptions{}); err != nil {
			t.Fatal(err)
		}
		p.runEngine(t, false)
		row := unitRows(t, p.store, p.run.ID, "u1")[0]
		want := []string{"code/agent-a/done/1", "review/agent-b/sent_back/1", "code/agent-a/done/2", "review/agent-b/sent_back/2", "code/agent-a/done/3", "review/agent-b/weak/3", "ship/agent-a/done/1"}
		if row.Result != "weak" || !reflect.DeepEqual(stepNamesOf(row), want) || runStatus(t, p.store, p.run.ID) != "done" {
			t.Fatalf("row %s %v status %s", row.Result, stepNamesOf(row), runStatus(t, p.store, p.run.ID))
		}
		assertStepAttemptsInOneRow(t, []TimeRow{row})
	})
}

// ghostFailure appends a failed unit outside the run for agent: its breaker
// (failures: 1) opens now.
func ghostFailure(t *testing.T, p *pipeline, agent string) {
	t.Helper()
	if err := p.store.AppendEvent(p.run.ID, Event{At: p.clock.Now(), Unit: "ghost", Key: "ghost", Agent: agent, State: "failed", Reason: "ghost failure"}); err != nil {
		t.Fatal(err)
	}
}

func TestStepsSendBackRedoWaitsForThePinnedAgentsBreaker(t *testing.T) {
	agents := []string{"agent-a1", "agent-a2", "agent-b"}
	roles := map[string]string{"agent-a1": "coder", "agent-a2": "coder", "agent-b": "reviewer"}
	p := newPipeline(t, stepsHeader+codeBlock("coder", 2)+withSendBack(reviewBlock("reviewer", 2), "code", 0)+"supervise:\n  breaker:\n    failures: 1\n    open_for: 30m\n", loopFiles("code", "review"), []string{"u1"}, agents, roles)
	p.checks.code = func(step, _ string, call int) int {
		if step == "review" && call == 1 {
			if _, err := p.store.RequestStop(p.run.ID); err != nil {
				t.Error(err)
			}
			return 4
		}
		return 0
	}
	p.runEngine(t, false)
	pinned := latestState(t, p.store, p.run.ID)["u1"]
	if pinned.State != StateSentBack {
		t.Fatalf("u1 %#v", pinned)
	}
	coder := unitCoder(t, p)
	other := "agent-a1"
	if coder == other {
		other = "agent-a2"
	}
	if status, err := p.store.Status(p.run.ID); err != nil || status.Counts.Ready["code"] != 1 || status.Counts.ReadyPinned["code"][coder] != 1 {
		t.Fatalf("status counts %#v %v", status.Counts, err)
	}
	if _, err := p.store.Resume(p.run.ID, ResumeOptions{}); err != nil {
		t.Fatal(err)
	}
	ghostFailure(t, p, coder)
	opened := p.clock.Now()
	p.runEngine(t, false)
	if sends := p.driver.sendsFor(other); len(sends) != 0 {
		t.Fatalf("the redo went to %s: %q", other, sends)
	}
	sends := p.driver.sendsFor(coder)
	if len(sends) != 2 || sends[1] != "fix code a2 [problems in review u1] u1" {
		t.Fatalf("%s sends %q", coder, sends)
	}
	half := eventsWithState(t, p.store, p.run.ID, eventBreakerHalfOpen)
	if len(half) != 1 || half[0].Agent != coder || half[0].At.Before(opened.Add(30*time.Minute)) {
		t.Fatalf("half-open %#v", half)
	}
	if closed := eventsWithState(t, p.store, p.run.ID, eventBreakerClosed); len(closed) != 1 || closed[0].Agent != coder {
		t.Fatalf("the redo was not the trial: %#v", closed)
	}
	row := unitRows(t, p.store, p.run.ID, "u1")[0]
	if row.Result != "done" || row.Steps[2].Agent != coder || row.Steps[2].AttemptReason != AttemptSendBack {
		t.Fatalf("row %v", stepDetails(row))
	}
}

// unitCoder is the agent that ran u1's first code attempt.
func unitCoder(t *testing.T, p *pipeline) string {
	t.Helper()
	events, _, err := p.store.Events(p.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Key == "u1" && event.State == StateStepDone && event.Step == "code" {
			return event.Agent
		}
	}
	t.Fatal("u1 has no finished code step")
	return ""
}

func TestStepsSendBackPinnedAgentLeftTheRun(t *testing.T) {
	agents, roles := coderReviewer()
	p := newPipeline(t, stepsHeader+codeBlock("coder", 2)+withSendBack(reviewBlock("reviewer", 2), "code", 0), loopFiles("code", "review"), []string{"u1"}, agents, roles)
	p.checks.code = func(step, _ string, call int) int {
		if step == "review" && call == 1 {
			if _, err := p.store.RequestStop(p.run.ID); err != nil {
				t.Error(err)
			}
			return 4
		}
		return 0
	}
	p.runEngine(t, false)
	if status, err := p.store.Status(p.run.ID); err != nil || status.Counts.ReadyPinned["code"]["agent-a"] != 1 {
		t.Fatalf("status %#v %v", status.Counts, err)
	}
	// The run's agents are edited by hand: agent-a is replaced by agent-c.
	if _, err := p.store.UpdateRun(p.run.ID, func(current *Run) error {
		current.Agents = []string{"agent-c", "agent-b"}
		current.Roles = map[string]string{"agent-c": "coder", "agent-b": "reviewer"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if status, err := p.store.Status(p.run.ID); err != nil || status.Counts.Ready["code"] != 1 || status.Counts.ReadyPinned != nil {
		t.Fatalf("status %#v %v", status.Counts, err)
	}
	if _, err := p.store.Resume(p.run.ID, ResumeOptions{}); err != nil {
		t.Fatal(err)
	}
	p.runEngine(t, false)
	if sends := p.driver.sendsFor("agent-c"); len(sends) != 1 || sends[0] != "code a2 [problems in review u1] prev=done u1" {
		t.Fatalf("the redo did not go to another coder through the main template: %q", sends)
	}
	if row := unitRows(t, p.store, p.run.ID, "u1")[0]; row.Result != "done" || stepDetails(row)[2] != "code/agent-c/done/2/coder/send_back" {
		t.Fatalf("row %v", stepDetails(row))
	}
}

func TestStepsRerunPrefersTheAgentThatRanTheStep(t *testing.T) {
	agents := []string{"agent-a", "agent-t1", "agent-t2", "agent-b"}
	roles := map[string]string{"agent-a": "coder", "agent-t1": "tester", "agent-t2": "tester", "agent-b": "reviewer"}
	yaml := stepsHeader + codeBlock("coder", 2) + stepBlock("test", "tester", "", "none", 1, 2) + withSendBack(reviewBlock("reviewer", 2), "code", 0)
	p := newPipeline(t, yaml, loopFiles("code", "test", "review"), []string{"u1"}, agents, roles)
	p.checks.code = func(step, _ string, call int) int {
		if step == "review" && call == 1 {
			return 4
		}
		return 0
	}
	p.runEngine(t, false)
	row := unitRows(t, p.store, p.run.ID, "u1")[0]
	if row.Result != "done" || len(row.Steps) != 6 {
		t.Fatalf("row %v", stepDetails(row))
	}
	first, rerun := row.Steps[1], row.Steps[4]
	if first.Name != "test" || rerun.Name != "test" || rerun.Agent != first.Agent || rerun.AttemptReason != AttemptRerun || rerun.Attempt != 2 {
		t.Fatalf("the rerun of test did not go to %s: %v", first.Agent, stepDetails(row))
	}
}

// agentFailDriver fails every send to one agent.
type agentFailDriver struct {
	*scriptedDriver
	agent string
}

func (d agentFailDriver) Send(ctx context.Context, agent, text string) (Delivery, error) {
	if agent != d.agent {
		return d.scriptedDriver.Send(ctx, agent, text)
	}
	d.fakeDriver.mu.Lock()
	d.fakeDriver.sends[agent] = append(d.fakeDriver.sends[agent], text)
	d.fakeDriver.mu.Unlock()
	d.clock.Advance(time.Millisecond)
	return Delivery{Status: DeliveryFailed, Reason: "not delivered: broken pane", QueuedAt: d.clock.Now()}, nil
}

func fallbackAgents() ([]string, map[string]string) {
	return []string{"agent-a", "agent-s", "agent-b"}, map[string]string{"agent-a": "coder", "agent-s": "senior", "agent-b": "reviewer"}
}

func TestStepsFallbackRetriesTheStepOnTheNextRole(t *testing.T) {
	t.Run("validator failure", func(t *testing.T) {
		agents, roles := fallbackAgents()
		yaml := stepsHeader + withFallback(codeBlock("coder", 1), "senior") + reviewBlock("reviewer", 1) +
			"supervise:\n  breaker:\n    failures: 1\n    open_for: 30m\n  meltdown:\n    failures: 1\n    window: 30m\n"
		p := newPipeline(t, yaml, loopFiles("code", "review"), []string{"u1"}, agents, roles)
		p.checks.code = func(step, _ string, call int) int {
			if step == "code" && call == 1 {
				return 2
			}
			return 0
		}
		p.runEngine(t, false)
		want := []string{"agent-a|code a1 [] prev= u1", "agent-s|code a2 [] prev=failed u1", "agent-b|review a1 [] prev= u1"}
		if got := p.sends(); !reflect.DeepEqual(got, want) {
			t.Fatalf("sends %q", got)
		}
		row := unitRows(t, p.store, p.run.ID, "u1")[0]
		wantSteps := []string{"code/agent-a/failed/1/coder/first", "code/agent-s/done/2/senior/fallback", "review/agent-b/done/1/reviewer/first"}
		if row.Result != "done" || !reflect.DeepEqual(stepDetails(row), wantSteps) || row.Steps[0].Reason != "validator exited with code 2" {
			t.Fatalf("row %#v %v", row, stepDetails(row))
		}
		failed := eventsWithState(t, p.store, p.run.ID, StateStepFailed)
		if len(failed) != 1 || failed[0].Fallback != "senior" || failed[0].Agent != "agent-a" || failed[0].Reason != "validator exited with code 2" {
			t.Fatalf("step_failed events %#v", failed)
		}
		// The failed attempt counts for its agent's breaker; meltdown only
		// counts units that end failed.
		if opened := eventsWithState(t, p.store, p.run.ID, eventBreakerOpen); len(opened) != 1 || opened[0].Agent != "agent-a" {
			t.Fatalf("breaker %#v", opened)
		}
		if paused := eventsWithState(t, p.store, p.run.ID, eventRunPaused); len(paused) != 0 {
			t.Fatalf("meltdown counted a rescued unit: %#v", paused)
		}
		stats := SummarizeTimes([]TimeRow{row})
		if code := stats.Steps[0]; code.Units != 2 || code.Failed != 1 || code.Fallbacks != 1 || code.Rescued != 1 {
			t.Fatalf("step stats %#v", stats.Steps)
		}
	})
	t.Run("not delivered", func(t *testing.T) {
		agents, roles := fallbackAgents()
		p := newPipeline(t, stepsHeader+withFallback(codeBlock("coder", 1), "senior")+reviewBlock("reviewer", 1), loopFiles("code", "review"), []string{"u1"}, agents, roles)
		if err := p.engine(agentFailDriver{scriptedDriver: p.driver, agent: "agent-a"}, nil).Run(context.Background(), p.run.ID, false); err != nil {
			t.Fatal(err)
		}
		row := unitRows(t, p.store, p.run.ID, "u1")[0]
		if row.Result != "done" || row.Steps[0].Result != "failed" || row.Steps[0].Reason != "not delivered: broken pane" || row.Steps[1].Agent != "agent-s" {
			t.Fatalf("row %#v %v", row, stepDetails(row))
		}
	})
}

func TestStepsFallbackLadderExhaustedParks(t *testing.T) {
	agents := []string{"agent-a", "agent-s", "agent-l", "agent-b"}
	roles := map[string]string{"agent-a": "coder", "agent-s": "senior", "agent-l": "lead", "agent-b": "reviewer"}
	yaml := stepsHeader + withFallback(codeBlock("coder", 1), "senior", "lead") + reviewBlock("reviewer", 1) + "approve:\n  on: [failed]\nsupervise:\n  meltdown:\n    failures: 1\n    window: 30m\n"
	p := newPipeline(t, yaml, loopFiles("code", "review"), []string{"u1"}, agents, roles)
	p.checks.code = func(step, _ string, _ int) int {
		if step == "code" {
			return 2
		}
		return 0
	}
	p.runEngine(t, false)
	want := []string{"agent-a|code a1 [] prev= u1", "agent-s|code a2 [] prev=failed u1", "agent-l|code a3 [] prev=failed u1"}
	if got := p.sends(); !reflect.DeepEqual(got, want) {
		t.Fatalf("sends %q", got)
	}
	failed := eventsWithState(t, p.store, p.run.ID, StateStepFailed)
	if len(failed) != 2 || failed[0].Fallback != "senior" || failed[1].Fallback != "lead" {
		t.Fatalf("step_failed %#v", failed)
	}
	parked := latestState(t, p.store, p.run.ID)["u1"]
	if parked.State != StateAwaitingApproval || parked.Agent != "agent-l" || parked.Attempt != 3 || runStatus(t, p.store, p.run.ID) != StatusAwaitingApproval {
		t.Fatalf("parked %#v status %s", parked, runStatus(t, p.store, p.run.ID))
	}
	if paused := eventsWithState(t, p.store, p.run.ID, eventRunPaused); len(paused) != 0 {
		t.Fatalf("meltdown counted fallbacks: %#v", paused)
	}
	// The revision round goes to the parked step's agent; its failure parks
	// the unit again and never falls back.
	if _, err := p.store.Decide(p.run.ID, DecideOptions{Action: DecisionRevise, Units: []string{"u1"}, Text: "try harder", By: "boss"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.store.Resume(p.run.ID, ResumeOptions{}); err != nil {
		t.Fatal(err)
	}
	p.runEngine(t, false)
	if sends := p.driver.sendsFor("agent-l"); len(sends) != 2 || sends[1] != "fix code a3 [try harder] u1" {
		t.Fatalf("revision %q", sends)
	}
	if failed := eventsWithState(t, p.store, p.run.ID, StateStepFailed); len(failed) != 2 || len(p.driver.sendsFor("agent-a")) != 1 || len(p.driver.sendsFor("agent-s")) != 1 {
		t.Fatalf("a revision round fell back: %#v", failed)
	}
	if parked := latestState(t, p.store, p.run.ID)["u1"]; parked.State != StateAwaitingApproval || parked.Agent != "agent-l" {
		t.Fatalf("after the revision %#v", parked)
	}
	if _, err := p.store.Decide(p.run.ID, DecideOptions{Action: DecisionReject, Units: []string{"u1"}, By: "boss"}); err != nil {
		t.Fatal(err)
	}
	row := unitRows(t, p.store, p.run.ID, "u1")[0]
	if row.Result != "failed" || !reflect.DeepEqual(stepNamesOf(row), []string{"code/agent-a/failed/1", "code/agent-s/failed/2", "code/agent-l/failed/3"}) {
		t.Fatalf("row %s %v", row.Result, stepNamesOf(row))
	}
}

func TestStepsFallbackSkipsBudgetFailures(t *testing.T) {
	agents, roles := fallbackAgents()
	yaml := stepsHeader + codeBlock("coder", 3) + withFallback(reviewBlock("reviewer", 3), "senior") + "budget:\n  unit_tokens: 400000\n"
	p := newPipeline(t, yaml, loopFiles("code", "review"), []string{"u1"}, agents, roles)
	p.driver.metered = true
	p.driver.perSend = TokenUse{Input: 200000, Output: 50000}
	p.checks.code = func(step, _ string, _ int) int {
		if step == "review" {
			return 1
		}
		return 0
	}
	p.runEngine(t, false)
	row := unitRows(t, p.store, p.run.ID, "u1")[0]
	if row.Result != "failed" || row.Reason != UnitBudgetReason(500000, 400000) || len(p.driver.sendsFor("agent-s")) != 0 {
		t.Fatalf("row %#v senior sends %q", row, p.driver.sendsFor("agent-s"))
	}
}

func TestStepsFallbackWaitsForTheFallbackRolesBreaker(t *testing.T) {
	agents, roles := fallbackAgents()
	yaml := stepsHeader + withFallback(codeBlock("coder", 1), "senior") + reviewBlock("reviewer", 1) + "supervise:\n  breaker:\n    failures: 1\n    open_for: 10h\n"
	p := newPipeline(t, yaml, loopFiles("code", "review"), []string{"u1"}, agents, roles)
	ghostFailure(t, p, "agent-s")
	p.checks.code = func(step, _ string, call int) int {
		if step == "code" && call == 1 {
			return 2
		}
		return 0
	}
	engine := p.engine(nil, nil)
	var mu sync.Mutex
	var statuses []string
	sleep := engine.Sleep
	engine.Sleep = func(ctx context.Context, d time.Duration) error {
		// Idle workers poll and move the fake clock one second per poll;
		// open_for is long enough that they cannot reach it before the
		// fallback is ready. The wait itself jumps to the reopen time.
		if run, err := p.store.LoadRun(p.run.ID); err == nil && strings.HasPrefix(run.Status, statusBreakersWaiting) {
			mu.Lock()
			statuses = append(statuses, run.Status)
			mu.Unlock()
			p.clock.Advance(10 * time.Hour)
		}
		return sleep(ctx, d)
	}
	if err := engine.Run(context.Background(), p.run.ID, false); err != nil {
		t.Fatal(err)
	}
	if len(statuses) == 0 || !strings.HasPrefix(statuses[0], statusBreakersWaiting+" for role senior; next trial agent-s at ") {
		t.Fatalf("waiting status does not name the fallback role: %q", statuses)
	}
	if row := unitRows(t, p.store, p.run.ID, "u1")[0]; row.Result != "done" || row.Steps[1].Agent != "agent-s" || row.Steps[1].AttemptReason != AttemptFallback {
		t.Fatalf("row %v", stepDetails(row))
	}
	if half := eventsWithState(t, p.store, p.run.ID, eventBreakerHalfOpen); len(half) != 1 || half[0].Agent != "agent-s" {
		t.Fatalf("half-open %#v", half)
	}
}

func TestStepsFallbackContinuesStickyAndPinsSendBacks(t *testing.T) {
	agents := []string{"agent-a", "agent-s1", "agent-s2", "agent-b"}
	roles := map[string]string{"agent-a": "coder", "agent-s1": "senior", "agent-s2": "senior", "agent-b": "reviewer"}
	yaml := stepsHeader + withFallback(codeBlock("coder", 1), "senior") + stepBlock("polish", "senior", "", "none", 1, 2) + withSendBack(reviewBlock("reviewer", 1), "code", 0)
	p := newPipeline(t, yaml, loopFiles("code", "polish", "review"), []string{"u1"}, agents, roles)
	p.checks.code = func(step, _ string, call int) int {
		switch {
		case step == "code" && call == 1:
			return 2
		case step == "review" && call == 1:
			return 4
		}
		return 0
	}
	p.runEngine(t, false)
	row := unitRows(t, p.store, p.run.ID, "u1")[0]
	if row.Result != "done" || len(row.Steps) != 7 {
		t.Fatalf("row %v", stepDetails(row))
	}
	senior := row.Steps[1].Agent
	if row.Steps[1].AttemptReason != AttemptFallback || row.Steps[2].Name != "polish" || row.Steps[2].Agent != senior {
		t.Fatalf("polish did not continue on the fallback agent %s: %v", senior, stepDetails(row))
	}
	if redo := row.Steps[4]; redo.Name != "code" || redo.Agent != senior || redo.AttemptReason != AttemptSendBack || redo.Role != "senior" {
		t.Fatalf("the send-back did not pin to the fallback agent %s: %v", senior, stepDetails(row))
	}
	if sends := p.driver.sendsFor(senior); len(sends) < 3 || sends[2] != "fix code a3 [problems in review u1] u1" {
		t.Fatalf("%s sends %q", senior, sends)
	}
	if len(p.driver.sendsFor("agent-a")) != 1 {
		t.Fatalf("coder sends %q", p.driver.sendsFor("agent-a"))
	}
}

func TestStepsSendBackSurvivesRestarts(t *testing.T) {
	agents, roles := coderReviewer()
	p := newPipeline(t, stepsHeader+codeBlock("coder", 2)+withSendBack(reviewBlock("reviewer", 2), "code", 0), loopFiles("code", "review"), []string{"u1"}, agents, roles)
	stop := func() {
		if _, err := p.store.RequestStop(p.run.ID); err != nil {
			t.Error(err)
		}
	}
	p.checks.code = func(step, _ string, call int) int {
		switch {
		case step == "review" && call == 1:
			stop() // stopped after sent_back, before the redo is claimed
			return 4
		case step == "code" && call == 2:
			stop() // stopped at the step boundary after the redo
		}
		return 0
	}
	p.runEngine(t, false)
	if latest := latestState(t, p.store, p.run.ID)["u1"]; latest.State != StateSentBack || runStatus(t, p.store, p.run.ID) != "stopped" || len(p.sends()) != 2 {
		t.Fatalf("after the first stop %#v %s %q", latest, runStatus(t, p.store, p.run.ID), p.sends())
	}
	// A torn last line (a crash mid-append) must not lose the send-back.
	logPath := filepath.Join(p.store.RunPath(p.run.ID), "units.jsonl")
	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"unit":"u1","state":"sen`); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := p.store.Resume(p.run.ID, ResumeOptions{}); err != nil {
		t.Fatal(err)
	}
	p.runEngine(t, false)
	if latest := latestState(t, p.store, p.run.ID)["u1"]; latest.State != StateStepDone || latest.Step != "code" || latest.Attempt != 2 || runStatus(t, p.store, p.run.ID) != "stopped" {
		t.Fatalf("after the second stop %#v", latest)
	}
	status, err := p.store.Status(p.run.ID)
	if err != nil || status.Counts.Ready["review"] != 1 {
		t.Fatalf("status %#v %v", status.Counts, err)
	}
	if _, err := p.store.Resume(p.run.ID, ResumeOptions{}); err != nil {
		t.Fatal(err)
	}
	p.runEngine(t, false)
	want := []string{"agent-a|code a1 [] prev= u1", "agent-b|review a1 [] prev= u1", "agent-a|fix code a2 [problems in review u1] u1", "agent-b|review a2 [] prev=sent_back u1"}
	if got := p.sends(); !reflect.DeepEqual(got, want) || runStatus(t, p.store, p.run.ID) != "done" {
		t.Fatalf("sends %q status %s", got, runStatus(t, p.store, p.run.ID))
	}
	rows := unitRows(t, p.store, p.run.ID, "u1")
	if len(rows) != 1 || !reflect.DeepEqual(stepNamesOf(rows[0]), []string{"code/agent-a/done/1", "review/agent-b/sent_back/1", "code/agent-a/done/2", "review/agent-b/done/2"}) {
		t.Fatalf("rows %v", rows)
	}
}

func TestStepsSendBackRedoRecoversInFlight(t *testing.T) {
	agents, roles := coderReviewer()
	p := newPipeline(t, stepsHeader+codeBlock("coder", 2)+withSendBack(reviewBlock("reviewer", 2), "code", 0), loopFiles("code", "review"), []string{"u1"}, agents, roles)
	p.checks.code = func(step, _ string, call int) int {
		if step == "review" && call == 1 {
			return 4
		}
		return 0
	}
	ctx, cancel := context.WithCancel(context.Background())
	var during RunStatus
	p.driver.beforeSend = func(agent, _ string) {
		if agent == "agent-a" && len(p.driver.sendsFor("agent-a")) == 1 {
			during, _ = p.store.Status(p.run.ID)
			cancel()
		}
	}
	if err := p.engine(nil, nil).Run(ctx, p.run.ID, false); err == nil {
		t.Fatal("a cancelled run returned no error")
	}
	var marker AgentStatus
	for _, agent := range during.Agents {
		if agent.Agent == "agent-a" {
			marker = agent
		}
	}
	if marker.CurrentUnit != "u1" || marker.CurrentStep != "code" || marker.CurrentAttempt != 2 {
		t.Fatalf("in-flight marker %#v", marker)
	}
	if latest := latestState(t, p.store, p.run.ID)["u1"]; latest.Step != "code" || latest.Attempt != 2 || latest.AttemptReason != AttemptSendBack || terminalState(latest.State) {
		t.Fatalf("u1 is not in flight in its redo: %#v", latest)
	}
	p.driver.beforeSend = nil
	p.runEngine(t, false)
	if sends := p.driver.sendsFor("agent-a"); len(sends) != 2 {
		t.Fatalf("recovery resent the redo: %q", sends)
	}
	row := unitRows(t, p.store, p.run.ID, "u1")[0]
	if row.Result != "done" || stepDetails(row)[2] != "code/agent-a/done/2/coder/send_back" || len(row.Steps) != 4 {
		t.Fatalf("row %v", stepDetails(row))
	}
}

func TestStepsFallbackSurvivesARestartBeforeItStarts(t *testing.T) {
	agents, roles := fallbackAgents()
	p := newPipeline(t, stepsHeader+withFallback(codeBlock("coder", 1), "senior")+reviewBlock("reviewer", 1), loopFiles("code", "review"), []string{"u1"}, agents, roles)
	p.checks.code = func(step, _ string, call int) int {
		if step == "code" && call == 1 {
			if _, err := p.store.RequestStop(p.run.ID); err != nil {
				t.Error(err)
			}
			return 2
		}
		return 0
	}
	p.runEngine(t, false)
	if latest := latestState(t, p.store, p.run.ID)["u1"]; latest.State != StateStepFailed || latest.Fallback != "senior" || len(p.driver.sendsFor("agent-s")) != 0 {
		t.Fatalf("u1 %#v", latest)
	}
	if status, err := p.store.Status(p.run.ID); err != nil || status.Counts.Ready["code"] != 1 || status.Counts.Pending != 1 {
		t.Fatalf("status %#v %v", status.Counts, err)
	}
	if _, err := p.store.Resume(p.run.ID, ResumeOptions{}); err != nil {
		t.Fatal(err)
	}
	p.runEngine(t, false)
	if len(p.driver.sendsFor("agent-a")) != 1 || len(p.driver.sendsFor("agent-s")) != 1 {
		t.Fatalf("a=%q s=%q", p.driver.sendsFor("agent-a"), p.driver.sendsFor("agent-s"))
	}
	if row := unitRows(t, p.store, p.run.ID, "u1")[0]; row.Result != "done" || stepDetails(row)[1] != "code/agent-s/done/2/senior/fallback" {
		t.Fatalf("row %v", stepDetails(row))
	}
}

func TestStepsRetryFailedAfterSendBacksResetsTheCount(t *testing.T) {
	agents, roles := coderReviewer()
	p := newPipeline(t, stepsHeader+codeBlock("coder", 2)+withSendBack(reviewBlock("reviewer", 2), "code", 0), loopFiles("code", "review"), []string{"u1", "u2"}, agents, roles)
	p.checks.code = func(step, key string, call int) int {
		if step != "review" || key != "u1" {
			return 0
		}
		switch call {
		case 2:
			// Exhausted; stop while u2 still waits so the run can resume.
			if _, err := p.store.RequestStop(p.run.ID); err != nil {
				t.Error(err)
			}
			return 4
		case 1, 3:
			return 4
		}
		return 0
	}
	p.runEngine(t, false)
	if failed := latestState(t, p.store, p.run.ID)["u1"]; failed.State != "failed" || failed.Step != "review" || failed.SendBack == nil || !failed.SendBack.Exhausted {
		t.Fatalf("u1 %#v", failed)
	}
	if _, err := p.store.Resume(p.run.ID, ResumeOptions{RetryFailed: true}); err != nil {
		t.Fatal(err)
	}
	if err := p.engine(nil, nil).Run(context.Background(), p.run.ID, true); err != nil {
		t.Fatal(err)
	}
	rows := unitRows(t, p.store, p.run.ID, "u1")
	if len(rows) != 2 || rows[1].Result != "done" || runStatus(t, p.store, p.run.ID) != "done" {
		t.Fatalf("rows %#v status %s", rows, runStatus(t, p.store, p.run.ID))
	}
	first := []string{"code/agent-a/done/1/coder/first", "review/agent-b/sent_back/1/reviewer/first", "code/agent-a/done/2/coder/send_back", "review/agent-b/failed/2/reviewer/rerun"}
	second := []string{"review/agent-b/sent_back/3/reviewer/retry", "code/agent-a/done/3/coder/send_back", "review/agent-b/done/4/reviewer/rerun"}
	if !reflect.DeepEqual(stepDetails(rows[0]), first) || !reflect.DeepEqual(stepDetails(rows[1]), second) {
		t.Fatalf("rows\n%v\n%v", stepDetails(rows[0]), stepDetails(rows[1]))
	}
	all, _, _ := p.store.Times(p.run.ID)
	assertStepAttemptsInOneRow(t, all)
	// The retried review saw its failed attempt as .Previous.
	if sends := p.driver.sendsFor("agent-b"); !contains(sends, "review a3 [] prev=failed u1") {
		t.Fatalf("reviews %q", sends)
	}
}

func TestStepLoopRecordsLoadOldLogs(t *testing.T) {
	var event Event
	old := `{"at":"2026-11-01T10:00:00Z","unit":"u1","key":"u1","agent":"a","state":"step_done","step":"code","attempt":1,"step_record":{"name":"code","attempt":1,"output":"out/u1.txt","row":{"run":"r","workflow":"w","unit":"u1","agent":"a","result":"done","rounds":1,"finished_at":"2026-11-01T10:00:00Z"}}}`
	if err := json.Unmarshal([]byte(old), &event); err != nil || event.AttemptReason != "" || event.SendBack != nil || event.Fallback != "" || event.StepRecord.Role != "" {
		t.Fatalf("old event %#v %v", event, err)
	}
	d, err := readPipeline(t, stepsHeader+codeBlock("coder", 1)+reviewBlock("reviewer", 1), loopFiles("code", "review"))
	if err != nil {
		t.Fatal(err)
	}
	progress := progressOf(d, []Event{event}, "u1")
	if len(progress.pass) != 1 || len(progress.history) != 1 || progress.attempts["code"] != 1 || progress.previousAttempt("code", 2) != (StepAttempt{Agent: "a", Result: "done", Output: "out/u1.txt"}) {
		t.Fatalf("progress %#v", progress)
	}
	var row StepRow
	if err := json.Unmarshal([]byte(`{"name":"code","attempt":1,"agent":"a","result":"done","rounds":1,"work_s":1,"validate_s":0,"wait_s":0,"queued_s":0,"finished_at":"2026-11-01T10:00:00Z"}`), &row); err != nil || row.Role != "" || row.AttemptReason != "" {
		t.Fatalf("old step row %#v %v", row, err)
	}
	encoded, _ := json.Marshal(row)
	for _, key := range []string{"role", "attempt_reason", "sent_back_to"} {
		if strings.Contains(string(encoded), key) {
			t.Fatalf("an old step row grew %s: %s", key, encoded)
		}
	}
}

// eachStepAttemptWithLoops extends TestStepsEachStepAttemptInOneRow: with a
// fallback, send-backs (one exhausted, parked and rejected) and a
// --retry-failed restart, every (unit, step, attempt) is still in one row.
func eachStepAttemptWithLoops(t *testing.T) {
	agents, roles := fallbackAgents()
	yaml := stepsHeader + withFallback(codeBlock("coder", 1), "senior") + withSendBack(reviewBlock("reviewer", 1), "code", 0) + "approve:\n  on: [failed]\n"
	p := newPipeline(t, yaml, loopFiles("code", "review"), []string{"u1", "u2", "u3"}, agents, roles)
	p.driver.metered = true
	p.driver.perSend = TokenUse{Input: 1000, Output: 100}
	p.checks.code = func(step, key string, call int) int {
		switch {
		case step == "code" && key == "u1" && call == 1:
			return 2
		case step == "review" && key == "u1" && call == 2:
			// Exhausted: the unit parks; the stop keeps the run resumable.
			if _, err := p.store.RequestStop(p.run.ID); err != nil {
				t.Error(err)
			}
			return 4
		case step == "review" && key != "u3" && call == 1:
			return 4
		}
		return 0
	}
	p.runEngine(t, false)
	if parked := latestState(t, p.store, p.run.ID)["u1"]; parked.State != StateAwaitingApproval || parked.Step != "review" || parked.SendBack == nil || !parked.SendBack.Exhausted {
		t.Fatalf("u1 %#v", parked)
	}
	if _, err := p.store.Decide(p.run.ID, DecideOptions{Action: DecisionReject, Units: []string{"u1"}, By: "boss"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.store.Resume(p.run.ID, ResumeOptions{RetryFailed: true}); err != nil {
		t.Fatal(err)
	}
	if err := p.engine(nil, nil).Run(context.Background(), p.run.ID, true); err != nil {
		t.Fatal(err)
	}
	if status := runStatus(t, p.store, p.run.ID); status != "done" {
		t.Fatalf("status %s", status)
	}
	rows, _, err := p.store.Times(p.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := assertStepAttemptsInOneRow(t, rows)
	var keys []string
	for id := range seen {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	want := []string{"u1/code/1", "u1/code/2", "u1/code/3", "u1/review/1", "u1/review/2", "u1/review/3", "u2/code/1", "u2/code/2", "u2/review/1", "u2/review/2", "u3/code/1", "u3/review/1"}
	if !reflect.DeepEqual(keys, want) || len(rows) != 4 {
		t.Fatalf("step attempts %v (rows %d)", keys, len(rows))
	}
	for _, row := range rows {
		var input int64
		for _, step := range row.Steps {
			if step.Tokens == nil {
				t.Fatalf("%s/%s/%d has no tokens", row.Unit, step.Name, step.Attempt)
			}
			input += step.Tokens.Input
		}
		if row.Tokens == nil || row.Tokens.Input != input {
			t.Fatalf("%s row tokens %#v, steps %d", row.Unit, row.Tokens, input)
		}
	}
	u1 := unitRows(t, p.store, p.run.ID, "u1")
	wantFirst := []string{"code/agent-a/failed/1/coder/first", "code/agent-s/done/2/senior/fallback", "review/agent-b/sent_back/1/reviewer/first", "code/agent-s/done/3/senior/send_back", "review/agent-b/failed/2/reviewer/rerun"}
	wantRetry := []string{"review/agent-b/done/3/reviewer/retry"}
	if len(u1) != 2 || !reflect.DeepEqual(stepDetails(u1[0]), wantFirst) || u1[0].Approval == nil || !reflect.DeepEqual(stepDetails(u1[1]), wantRetry) || u1[1].Result != "done" {
		t.Fatalf("u1 rows %v", u1)
	}
}

func TestStepLoopExampleHasSendBack(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "examples", "workflow-steps")
	definition, err := ReadDefinition(dir)
	if err != nil {
		t.Fatal(err)
	}
	review := definition.Steps[1]
	if review.SendBack == nil || review.SendBack.To != "code" || review.SendBack.Limit() != 2 || len(definition.Steps[0].Fallback) != 0 {
		t.Fatalf("example steps %#v", definition.Steps)
	}
	if err := CheckSampleTemplates(dir, dir, definition); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(filepath.Join(dir, "workflow.yaml"))
	if err != nil || !strings.Contains(string(text), "# fallback: [senior]") {
		t.Fatalf("the example lacks the commented fallback: %v", err)
	}
	checker, err := os.ReadFile(filepath.Join(dir, "check_review.py"))
	if err != nil || !strings.Contains(string(checker), "return 4") {
		t.Fatalf("check_review.py does not exit 4: %v", err)
	}
}

func TestStepsRunWithoutAgentForARoleFails(t *testing.T) {
	agents, roles := fallbackAgents()
	p := newPipeline(t, stepsHeader+withFallback(codeBlock("coder", 1), "senior")+reviewBlock("reviewer", 1), loopFiles("code", "review"), []string{"u1"}, agents, roles)
	// The run's agents are edited by hand (or the run predates fallback
	// roles): nobody has the senior role any more.
	if _, err := p.store.UpdateRun(p.run.ID, func(current *Run) error {
		current.Agents = []string{"agent-a", "agent-b"}
		current.Roles = map[string]string{"agent-a": "coder", "agent-b": "reviewer"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err := p.engine(nil, nil).Run(context.Background(), p.run.ID, false)
	want := `run ` + p.run.ID + ` has no agent for role "senior", which step code (fallback) uses`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("want %q, got %v", want, err)
	}
	run, loadErr := p.store.LoadRun(p.run.ID)
	if loadErr != nil || run.Status != "error" || !strings.Contains(run.LastError, want) {
		t.Fatalf("run %s %q %v", run.Status, run.LastError, loadErr)
	}
	if sends := p.sends(); len(sends) != 0 {
		t.Fatalf("a worker started: %q", sends)
	}
	if events, _, _ := p.store.Events(p.run.ID); len(events) != 0 {
		t.Fatalf("events %#v", events)
	}
}

func TestRunAgainWithoutProgressWaitsAPollInterval(t *testing.T) {
	agents, roles := coderReviewer()
	p := newPipeline(t, stepsHeader+codeBlock("coder", 1)+reviewBlock("reviewer", 1), loopFiles("code", "review"), []string{"u1"}, agents, roles)
	// A step_done record for the last step (a hand-edited or foreign log):
	// finalize sees a unit between steps that no worker can take, so every
	// pass asks for another one without appending anything.
	if err := p.store.AppendEvent(p.run.ID, Event{At: p.clock.Now(), Unit: "u1", Key: "u1", Agent: "agent-b", State: StateStepDone, Step: "review", Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	engine := p.engine(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var delays []time.Duration
	start := p.clock.Now()
	engine.Sleep = func(ctx context.Context, d time.Duration) error {
		delays = append(delays, d)
		p.clock.Advance(d)
		if len(delays) == 3 {
			cancel()
		}
		return ctx.Err()
	}
	if err := engine.Run(ctx, p.run.ID, false); err == nil {
		t.Fatal("a cancelled run returned no error")
	}
	if len(delays) != 3 || p.clock.Now().Sub(start) != 3*engine.interval() {
		t.Fatalf("passes slept %v, clock advanced %s", delays, p.clock.Now().Sub(start))
	}
	for _, d := range delays {
		if d != engine.interval() {
			t.Fatalf("slept %s, want the poll interval %s", d, engine.interval())
		}
	}
	if sends := p.sends(); len(sends) != 0 {
		t.Fatalf("sends %q", sends)
	}
}
