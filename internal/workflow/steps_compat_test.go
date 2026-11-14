package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// jsonShape lists the key paths of one JSON line, sorted, so a record's
// field set can be compared without its values.
func jsonShape(t *testing.T, line string) string {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(line), &value); err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	var walk func(prefix string, value any)
	walk = func(prefix string, value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				path := key
				if prefix != "" {
					path = prefix + "." + key
				}
				paths[path] = true
				walk(path, child)
			}
		case []any:
			for _, child := range typed {
				walk(prefix+"[]", child)
			}
		}
	}
	walk("", value)
	keys := make([]string, 0, len(paths))
	for key := range paths {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// recordShapes renders the state sequence and the field sets of a run's
// units.jsonl and times.jsonl, independent of times and values.
func recordShapes(t *testing.T, store *Store, runID string) string {
	t.Helper()
	var out strings.Builder
	for _, name := range []string{"units.jsonl", "times.jsonl"} {
		data, err := os.ReadFile(filepath.Join(store.RunPath(runID), name))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line == "" {
				continue
			}
			var head struct {
				State string `json:"state"`
			}
			_ = json.Unmarshal([]byte(line), &head)
			out.WriteString(name + " " + head.State + " {" + jsonShape(t, line) + "}\n")
		}
	}
	data, err := os.ReadFile(filepath.Join(store.RunPath(runID), "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	out.WriteString("run.json {" + jsonShape(t, string(data)) + "}\n")
	return out.String()
}

// TestSingleStepRecordsUnchanged pins the field sets and state sequences a
// workflow without steps writes to units.jsonl, times.jsonl and run.json.
// The golden file was captured before steps existed; steps must not add a
// field or an event to such a run.
func TestSingleStepRecordsUnchanged(t *testing.T) {
	var got strings.Builder
	// A: happy path.
	clock := newFakeClock()
	store, run := makeRun(t, clock, baseYAML(""), `[{"id":"one"}]`, "score {{.Key}}", "", "agent-a")
	runEngine(t, store, run, newFakeDriver(clock), clock)
	got.WriteString(recordShapes(t, store, run.ID))
	// B: follow-up round with tokens.
	store, run, driver, clock := meteredRun(t, budgetYAML("budget:\n  unit_tokens: 100000000\n", 2, 2), []string{"one"}, "agent-a")
	engine := supervisedEngine(store, driver, clock, nil)
	calls := 0
	engine.Validator = func(context.Context, string, []string, time.Duration) (int, string, string, error) {
		calls++
		if calls == 1 {
			return 1, "bad", "", nil
		}
		return 0, "", "", nil
	}
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	got.WriteString(recordShapes(t, store, run.ID))
	// C: park on weak, revise, park again, approve.
	store, run, sdriver, clock := approvalRun(t, approveYAML("weak", 1, 1, ""), []string{"one"}, "agent-a")
	engine = supervisedEngine(store, sdriver, clock, nil)
	engine.Validator = alwaysCode(1)
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionRevise, Units: []string{"one"}, Text: "again", By: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resume(run.ID, ResumeOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(run.ID, DecideOptions{Action: DecisionApprove, Units: []string{"one"}, By: "owner"}); err != nil {
		t.Fatal(err)
	}
	got.WriteString(recordShapes(t, store, run.ID))
	// D: guardrail halt at the boundary.
	store, run, tdriver, clock := guardedRun(t, budgetYAML(failuresGuard, 3, 4), []string{"one"}, func(at time.Time) []ToolEvent {
		return toolCalls(at, 12, "Bash", "x", ToolFailed)
	})
	engine = supervisedEngine(store, tdriver, clock, nil)
	engine.Validator = alwaysCode(1)
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		t.Fatal(err)
	}
	got.WriteString(recordShapes(t, store, run.ID))
	want, err := os.ReadFile(filepath.Join("testdata", "single_step_shapes.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != string(want) {
		t.Fatalf("single-step record shapes changed:\n got:\n%s\nwant:\n%s", got.String(), want)
	}
}
