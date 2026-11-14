package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blueprint/internal/workflow"
)

// Synthetic Claude Code transcript records. Only the fields the reader uses
// are present, plus a few it must ignore.
var activityBase = time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)

func stamp(second int) string {
	return activityBase.Add(time.Duration(second) * time.Second).Format(time.RFC3339Nano)
}

func toolUseLine(second int, id, tool string, input any, sidechain bool) string {
	record := map[string]any{"type": "assistant", "isSidechain": sidechain, "timestamp": stamp(second), "sessionId": "s",
		"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": tool, "input": input, "caller": map[string]any{"type": "direct"}}}}}
	data, _ := json.Marshal(record)
	return string(data) + "\n"
}

func toolResultLine(second int, sidechain bool, results map[string]bool) string {
	content := []any{}
	for id, failed := range results {
		content = append(content, map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": failed, "content": "synthetic output"})
	}
	record := map[string]any{"type": "user", "isSidechain": sidechain, "timestamp": stamp(second), "message": map[string]any{"role": "user", "content": content}, "toolUseResult": map[string]any{}}
	data, _ := json.Marshal(record)
	return string(data) + "\n"
}

func appendFile(t *testing.T, path, text string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(text); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func readActivity(t *testing.T, reader *activityReader, path string, since time.Time) workflow.Activity {
	t.Helper()
	activity, err := reader.read("worker", path, since)
	if err != nil {
		t.Fatal(err)
	}
	return activity
}

func outcomes(activity workflow.Activity) string {
	parts := []string{}
	for _, event := range activity.Events {
		outcome := "pending"
		switch event.Outcome {
		case workflow.ToolSucceeded:
			outcome = "ok"
		case workflow.ToolFailed:
			outcome = "error"
		}
		parts = append(parts, event.ID+":"+event.Tool+":"+outcome)
	}
	return strings.Join(parts, " ")
}

func TestActivityReaderIncrementalAndPartialLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	appendFile(t, path, `{"type":"system","subtype":"init","timestamp":"`+stamp(0)+`"}`+"\n"+
		toolUseLine(1, "c1", "Bash", map[string]any{"command": "x"}, false)+
		toolResultLine(2, false, map[string]bool{"c1": true})+
		toolUseLine(3, "c2", "Read", map[string]any{"file_path": "y"}, false))
	reader := newActivityReader(activityKeep)
	activity := readActivity(t, reader, path, activityBase)
	if got := outcomes(activity); got != "c1:Bash:error c2:Read:pending" {
		t.Fatalf("first read %q", got)
	}
	cursor := reader.cursors["worker"]
	info, _ := os.Stat(path)
	if cursor.offset != info.Size() {
		t.Fatalf("offset %d of %d", cursor.offset, info.Size())
	}
	// A record still being written is left for the next read.
	result := toolResultLine(4, false, map[string]bool{"c2": false})
	appendFile(t, path, result[:len(result)/2])
	activity = readActivity(t, reader, path, activityBase)
	if got := outcomes(activity); got != "c1:Bash:error c2:Read:pending" || cursor.offset != info.Size() {
		t.Fatalf("partial line consumed: %q offset %d", got, cursor.offset)
	}
	appendFile(t, path, result[len(result)/2:])
	activity = readActivity(t, reader, path, activityBase)
	if got := outcomes(activity); got != "c1:Bash:error c2:Read:ok" {
		t.Fatalf("completed line %q", got)
	}
	// Returned activity is a copy: later reads do not change it.
	held := activity.Events[1].Outcome
	appendFile(t, path, toolUseLine(5, "c3", "Bash", map[string]any{"command": "x"}, false))
	activity = readActivity(t, reader, path, activityBase)
	if len(activity.Events) != 3 || held != workflow.ToolSucceeded {
		t.Fatalf("third call %q", outcomes(activity))
	}
}

func TestActivityReaderSkipsSidechainCopiesAndOldCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	blob := map[string]any{"type": "api-request-blob", "hash": "h", "sessionId": "s", "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": "copy", "name": "Bash", "input": map[string]any{}}}}}
	blobLine, _ := json.Marshal(blob)
	appendFile(t, path, toolUseLine(-5, "old", "Bash", map[string]any{}, false)+
		toolResultLine(-4, false, map[string]bool{"old": true})+
		toolUseLine(1, "side", "Bash", map[string]any{}, true)+
		toolResultLine(2, true, map[string]bool{"side": true})+
		string(blobLine)+"\n"+
		`{"type":"user","timestamp":"`+stamp(3)+`","message":{"role":"user","content":"a prompt that mentions \"tool_use\" in plain text"}}`+"\n"+
		`{"type":"assistant","timestamp":"`+stamp(4)+`","message":{"content":[{"type":"text","text":"no tools"}]}}`+"\n"+
		`not json "tool_use"`+"\n"+
		toolUseLine(5, "main", "Edit", map[string]any{"a": 1}, false)+
		toolResultLine(6, false, map[string]bool{"main": false, "old": false, "unknown": true}))
	activity := readActivity(t, newActivityReader(activityKeep), path, activityBase)
	if got := outcomes(activity); got != "main:Edit:ok" {
		t.Fatalf("activity %q", got)
	}
}

func TestActivityReaderSessionSwitchAndRescan(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first.jsonl"), filepath.Join(dir, "second.jsonl")
	appendFile(t, first, toolUseLine(1, "a1", "Bash", map[string]any{"n": 1}, false)+toolResultLine(2, false, map[string]bool{"a1": true}))
	reader := newActivityReader(activityKeep)
	readActivity(t, reader, first, activityBase)
	// The session moved to a new file that repeats a1 (same id) and goes on.
	appendFile(t, second, toolUseLine(1, "a1", "Bash", map[string]any{"n": 1}, false)+toolResultLine(2, false, map[string]bool{"a1": true})+
		toolUseLine(10, "b1", "Bash", map[string]any{"n": 2}, false)+toolResultLine(11, false, map[string]bool{"b1": true}))
	activity := readActivity(t, reader, second, activityBase)
	if got := outcomes(activity); got != "a1:Bash:error b1:Bash:error" {
		t.Fatalf("after the switch %q", got)
	}
	if tally := activity.Tally(); tally.LongestFailures("") != 2 {
		t.Fatalf("failure streak across files %d", tally.LongestFailures(""))
	}
	// The same file rewritten shorter is read again from the top.
	if err := os.WriteFile(second, []byte(toolUseLine(20, "c1", "Read", map[string]any{}, false)), 0600); err != nil {
		t.Fatal(err)
	}
	activity = readActivity(t, reader, second, activityBase)
	if got := outcomes(activity); got != "a1:Bash:error b1:Bash:error c1:Read:pending" {
		t.Fatalf("after a rewrite %q", got)
	}
	// A later window start (the next unit) keeps the offset and drops
	// earlier calls.
	offset := reader.cursors["worker"].offset
	activity = readActivity(t, reader, second, activityBase.Add(15*time.Second))
	if got := outcomes(activity); got != "c1:Read:pending" || reader.cursors["worker"].offset != offset {
		t.Fatalf("later window %q offset %d/%d", got, reader.cursors["worker"].offset, offset)
	}
	// An earlier window start (a restarted worker resuming an older unit)
	// re-scans the bound file by timestamp.
	activity = readActivity(t, reader, second, activityBase)
	if got := outcomes(activity); got != "c1:Read:pending" {
		t.Fatalf("re-scan %q", got)
	}
	appendFile(t, second, toolUseLine(30, "d1", "Read", map[string]any{}, false))
	if activity = readActivity(t, reader, second, activityBase); len(activity.Events) != 2 {
		t.Fatalf("after re-scan %q", outcomes(activity))
	}
}

func TestActivityReaderMemoryCapKeepsCountsExact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	var text strings.Builder
	for index := 0; index < 30; index++ {
		id := fmt.Sprintf("c%02d", index)
		text.WriteString(toolUseLine(index*2, id, "Bash", map[string]any{"command": "same"}, false))
		text.WriteString(toolResultLine(index*2+1, false, map[string]bool{id: true}))
	}
	// One call whose result arrives only after it was folded stays pending.
	text.WriteString(toolUseLine(100, "late", "Read", map[string]any{}, false))
	appendFile(t, path, text.String())
	reader := newActivityReader(8)
	activity := readActivity(t, reader, path, activityBase)
	if len(activity.Events) > 8 {
		t.Fatalf("kept %d events with a cap of 8", len(activity.Events))
	}
	tally := activity.Tally()
	repeat, tool := tally.LongestRepeat("")
	if tally.Calls() != 31 || tally.LongestFailures("Bash") != 30 || repeat != 30 || tool != "Bash" {
		t.Fatalf("counts after folding: calls=%d failures=%d repeat=%d %s", tally.Calls(), tally.LongestFailures("Bash"), repeat, tool)
	}
	var more strings.Builder
	for index := 0; index < 10; index++ {
		more.WriteString(toolUseLine(200+index, fmt.Sprintf("r%d", index), "Grep", map[string]any{"i": index}, false))
	}
	more.WriteString(toolResultLine(220, false, map[string]bool{"late": false, "r9": true}))
	appendFile(t, path, more.String())
	activity = readActivity(t, reader, path, activityBase)
	if tally := activity.Tally(); tally.Calls() != 41 || len(activity.Events) > 8 || activity.Events[len(activity.Events)-1].Outcome != workflow.ToolFailed {
		t.Fatalf("after more calls: calls=%d kept=%d", tally.Calls(), len(activity.Events))
	}
}

func TestActivityReaderLongLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	big := strings.Repeat("x", 700*1024)
	appendFile(t, path, toolUseLine(1, "big", "Write", map[string]any{"content": big}, false)+toolResultLine(2, false, map[string]bool{"big": false}))
	activity := readActivity(t, newActivityReader(activityKeep), path, activityBase)
	if got := outcomes(activity); got != "big:Write:ok" {
		t.Fatalf("long line %q", got)
	}
}

func TestCanonicalInputHash(t *testing.T) {
	same := []string{`{"b":1,"a":"x"}`, `{ "a" : "x", "b" : 1 }`}
	if canonicalInputHash(json.RawMessage(same[0])) != canonicalInputHash(json.RawMessage(same[1])) {
		t.Fatal("key order or spacing changed the hash")
	}
	if canonicalInputHash(json.RawMessage(`{"a":"x","b":2}`)) == canonicalInputHash(json.RawMessage(same[0])) {
		t.Fatal("different inputs hashed equal")
	}
	if canonicalInputHash(json.RawMessage(`{"n":12345678901234567890}`)) == canonicalInputHash(json.RawMessage(`{"n":12345678901234567891}`)) {
		t.Fatal("large numbers lost precision")
	}
	if canonicalInputHash(nil) != canonicalInputHash(json.RawMessage("null")) {
		t.Fatal("a missing input must hash like null")
	}
}

func TestWorkflowDriverActivity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	driver := &workflowDriver{app: &app{}}
	ctx := context.Background()
	if _, known, err := driver.Activity(ctx, "worker", activityBase); known || err != nil {
		t.Fatalf("an agent never observed must be unknown: known=%t err=%v", known, err)
	}
	// A bound session whose file Claude Code has not written yet made no
	// tool calls.
	driver.noteTranscript("worker", "claude", path, "session", 0)
	if activity, known, err := driver.Activity(ctx, "worker", activityBase); !known || err != nil || len(activity.Events) != 0 {
		t.Fatalf("missing bound file: known=%t err=%v %#v", known, err, activity)
	}
	appendFile(t, path, toolUseLine(1, "c1", "Bash", map[string]any{}, false))
	if activity, known, err := driver.Activity(ctx, "worker", activityBase); !known || err != nil || len(activity.Events) != 1 {
		t.Fatalf("bound file: known=%t err=%v %#v", known, err, activity)
	}
	// An observation that could not bind keeps the last binding.
	driver.noteTranscript("worker", "", "", "", 0)
	driver.noteTranscript("worker", "claude", "", "", 0)
	if _, known, _ := driver.Activity(ctx, "worker", activityBase); !known {
		t.Fatal("a transient unbound observation dropped the transcript")
	}
	// Conflicting bindings and other harnesses are unknown.
	driver.noteTranscript("worker", "claude", path, "session", 2)
	if _, known, _ := driver.Activity(ctx, "worker", activityBase); known {
		t.Fatal("a binding conflict must make activity unknown")
	}
	driver.noteTranscript("worker", "claude", path, "session", 0)
	driver.noteTranscript("worker", "codex", filepath.Join(t.TempDir(), "rollout.jsonl"), "thread", 0)
	if _, known, _ := driver.Activity(ctx, "worker", activityBase); known {
		t.Fatal("a codex agent must be unknown")
	}
	// An unreadable bound path is an error, which the engine records as
	// unknown activity.
	driver.noteTranscript("worker", "claude", t.TempDir(), "session", 0)
	if _, known, err := driver.Activity(ctx, "worker", activityBase); known || err == nil {
		t.Fatalf("a directory must not read as a transcript: known=%t err=%v", known, err)
	}
	var _ workflow.ActivityMeter = driver
}

const railedWorkflowYAML = `name: railed
version: 1
units:
  file: units.json
  key: id
prompt:
  template: prompt.md
guardrails:
  tool_calls:
    warn: 150
  repeated_tool_call:
    halt: 10
  tool_failures:
    warn: 5
    halt: 12
    tool: Bash
`

func railedRun(t *testing.T) (*app, *workflow.Store, workflow.Run) {
	t.Helper()
	a, store, workdir := workflowCLIFixture(t)
	source := filepath.Join(t.TempDir(), "railed")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"workflow.yaml": railedWorkflowYAML, "prompt.md": "do {{.Key}}", "units.json": `[{"id":"one"},{"id":"two"}]`} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.Add(source, false); err != nil {
		t.Fatal(err)
	}
	definition, err := workflow.ReadDefinition(store.WorkflowPath("railed"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(store.WorkflowPath("railed"), definition, workdir, []string{"worker"}, "root")
	if err != nil {
		t.Fatal(err)
	}
	return a, store, run
}

func TestWorkflowShowAndCheckGuardrails(t *testing.T) {
	a, _, _ := railedRun(t)
	if err := a.workflow([]string{"show", "railed"}); err != nil {
		t.Fatal(err)
	}
	got := readTestOutput(t, a.out)
	for _, want := range []string{
		"guardrail tool_calls: warn 150 tool calls per unit",
		"guardrail repeated_tool_call: halt 10 identical calls in a row (any tool)",
		"guardrail tool_failures: warn 5, halt 12 failed results in a row (tool Bash)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("show output missing %q: %s", want, got)
		}
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"check", "railed"}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "warning: guardrails read tool activity from Claude Code transcripts only") || !strings.Contains(got, "OK: workflow railed") {
		t.Fatalf("check did not warn about guardrails: %s", got)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"show", "sample"}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); strings.Contains(got, "guardrail") {
		t.Fatalf("plain workflow printed guardrails: %s", got)
	}
}

func TestWorkflowStatusShowsGuardrailMarks(t *testing.T) {
	a, store, run := railedRun(t)
	now := time.Now().UTC()
	for _, event := range []workflow.Event{
		{At: now, Key: "one", Unit: "one", Agent: "worker", State: "working", Round: 1},
		{At: now, Agent: "worker", State: "guardrail_warn", Reason: "tool Bash failed 5 times in a row", Guardrail: &workflow.GuardrailRecord{Unit: "one", Rule: workflow.RuleToolFailures, Level: "warn", Value: 5, Limit: 5}},
		{At: now, Agent: "worker", State: "guardrail_warn", Reason: "the unit made 150 tool calls", Guardrail: &workflow.GuardrailRecord{Unit: "one", Rule: workflow.RuleToolCalls, Level: "warn", Value: 150, Limit: 150}},
		{At: now, Agent: "worker", State: "guardrail_halt", Reason: "tool Bash failed 12 times in a row", Guardrail: &workflow.GuardrailRecord{Unit: "one", Rule: workflow.RuleToolFailures, Level: "halt", Value: 12, Limit: 12}},
	} {
		if err := store.AppendEvent(run.ID, event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.UpdateRun(run.ID, func(current *workflow.Run) error {
		current.Status = "running"
		current.Current = map[string]string{"worker": "one"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.workflow([]string{"status", run.ID}); err != nil {
		t.Fatal(err)
	}
	got := readTestOutput(t, a.out)
	if !strings.Contains(got, "agents=[worker:one (") || !strings.Contains(got, ") [guardrail tool_failures halt, tool_calls warn]]") {
		t.Fatalf("status without guardrail marks: %s", got)
	}
	if err := store.AppendEvent(run.ID, workflow.Event{At: now, Key: "one", Unit: "one", Agent: "worker", State: "failed", Reason: "guardrail: tool Bash failed 12 times in a row"}); err != nil {
		t.Fatal(err)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"status", run.ID}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); strings.Contains(got, "[guardrail") {
		t.Fatalf("a finished unit kept its mark: %s", got)
	}
}

func TestWorkflowTimesActivityLines(t *testing.T) {
	a, store, run := railedRun(t)
	rows := []workflow.TimeRow{
		{Unit: "one", Agent: "worker", Result: "done", Activity: &workflow.RowActivity{ToolCalls: 10, LongestRepeat: 2}},
		{Unit: "two", Agent: "worker", Result: "failed", Reason: "guardrail: tool Bash failed 12 times in a row", Activity: &workflow.RowActivity{ToolCalls: 30, LongestFailures: 12, Verdict: "halt", Rule: workflow.RuleToolFailures}},
		{Unit: "three", Agent: "worker", Result: "done", Activity: &workflow.RowActivity{ToolCalls: 151, Verdict: "warn", Rule: workflow.RuleToolCalls}},
		{Unit: "four", Agent: "worker", Result: "done"},
	}
	for _, row := range rows {
		row.Run, row.Workflow, row.Rounds, row.FinishedAt = run.ID, "railed", 1, time.Now().UTC()
		if err := store.AppendTime(run.ID, row); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.workflow([]string{"times", run.ID}); err != nil {
		t.Fatal(err)
	}
	got := readTestOutput(t, a.out)
	first := strings.Index(got, "activity: units=3/4 median_tool_calls=30 p90_tool_calls=")
	second := strings.Index(got, "guardrail halts: tool_failures=1\n")
	if first < 0 || second < first || !strings.Contains(got, "warned=1 halted=1\n") || !strings.Contains(got, "slowest:") {
		t.Fatalf("times activity lines: %s", got)
	}
	a.out = testOutput(t)
	if err := a.workflow([]string{"times", run.ID, "--json"}); err != nil {
		t.Fatal(err)
	}
	var stats workflow.TimeStats
	if err := json.Unmarshal([]byte(readTestOutput(t, a.out)), &stats); err != nil || stats.Activity == nil || stats.Activity.Halted != 1 || stats.Activity.HaltsByRule[workflow.RuleToolFailures] != 1 {
		t.Fatalf("times JSON activity %#v err=%v", stats.Activity, err)
	}
}
