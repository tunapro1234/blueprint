package api

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blueprint/internal/ledger"
)

func workRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestWorkCoreBoundaries(t *testing.T) {
	core := testCore(t)
	root := workRoot(t)
	lead := Caller{Name: "lead", Transport: "cli", Verified: true}
	if _, err := core.Work(lead, WorkRequest{Op: "project.create", Project: "p", Goal: "g", Roots: []string{root}}); err != nil {
		t.Fatal(err)
	}
	// Remote (gateway) callers never reach the ledger, not even reads.
	remote := Caller{Name: "chatgpt", Transport: "gateway", Remote: true}
	if _, err := core.Work(remote, WorkRequest{Op: "brief", Project: "p"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("remote read: %v", err)
	}
	// Writes need a caller name; reads do not.
	if _, err := core.Work(Caller{Transport: "http"}, WorkRequest{Op: "task.create", Project: "p", Title: "t"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("anonymous write: %v", err)
	}
	if _, err := core.Work(Caller{Transport: "http"}, WorkRequest{Op: "brief", Project: "p"}); err != nil {
		t.Fatalf("anonymous read: %v", err)
	}
	if _, err := core.Work(lead, WorkRequest{Op: "nope", Project: "p"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown op: %v", err)
	}

	// A self-declared HTTP label is a different identity from the proven
	// one, so it cannot act as the owner.
	if _, err := core.Work(lead, WorkRequest{Op: "task.create", Project: "p", Title: "t"}); err != nil {
		t.Fatal(err)
	}
	ada := Caller{Name: "ada", Transport: "cli", Verified: true}
	claimed, err := core.Work(ada, WorkRequest{Op: "task.claim", Project: "p", ID: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	fence := claimed.(ledger.Task).Fence
	fakeAda := Caller{Name: "ada", Transport: "http"}
	_, err = core.Work(fakeAda, WorkRequest{Op: "task.checkpoint", Project: "p", ID: "t1", Fence: fence, Note: "hi"})
	if !errors.Is(err, ErrConflict) || !errors.Is(err, ledger.ErrStaleOwner) || !strings.Contains(err.Error(), "http:ada") {
		t.Fatalf("label spoof: %v", err)
	}
	if text := errorText(err); strings.Contains(text, "read the key again") {
		t.Fatalf("ledger conflicts must not carry the board hint: %s", text)
	}

	// Self-reported validator records are marked.
	file := filepath.Join(root, "out.txt")
	os.WriteFile(file, []byte("x"), 0o600)
	if _, err := core.Work(ada, WorkRequest{Op: "task.submit", Project: "p", ID: "t1", Fence: fence, Artifacts: []ledger.ArtifactRef{{Path: file}}}); err != nil {
		t.Fatal(err)
	}
	bob := Caller{Name: "bob", Transport: "mcp", Verified: true}
	got, err := core.Work(bob, WorkRequest{Op: "task.verify", Project: "p", ID: "t1", Submission: 1, Result: "pass", Checks: "ran it",
		Validator: &ledger.ValidatorRun{Command: []string{"make", "test"}, Exit: 0}})
	if err != nil {
		t.Fatal(err)
	}
	if v := got.(ledger.Task).Verifications[0].Validator; v == nil || !v.SelfReported {
		t.Fatalf("validator record not marked self-reported: %+v", v)
	}
	// Writes are audited, refusals included.
	data, _ := os.ReadFile(filepath.Join(core.StateDir, "audit.jsonl"))
	if !strings.Contains(string(data), "api.work.task.verify") || !strings.Contains(string(data), "api.work.task.checkpoint.refused") {
		t.Fatalf("audit log missing work events:\n%s", data)
	}
}

func TestWorkMCPTools(t *testing.T) {
	core := testCore(t)
	root := workRoot(t)
	lead := NewMCPSession(core, Caller{Name: "lead", Transport: "mcp", Verified: true}, nil)
	worker := NewMCPSession(core, Caller{Name: "worker", Transport: "mcp", Verified: true}, nil)
	if r := call(t, lead, 1, "bp_work_project", map[string]any{"op": "project.create", "project": "p", "goal": "g", "roots": []string{root}}); r.Result.IsError {
		t.Fatalf("create: %s", r.Result.Content[0].Text)
	}
	call(t, lead, 2, "bp_work_task", map[string]any{"op": "task.create", "project": "p", "title": "do it"})
	r := call(t, worker, 3, "bp_work_task", map[string]any{"op": "task.claim", "project": "p", "id": "t1", "leaseSeconds": 60})
	if r.Result.IsError || r.Result.StructuredContent["fence"].(float64) != 1 {
		t.Fatalf("claim: %+v", r.Result)
	}
	// A tool refuses ops from another group.
	if r := call(t, worker, 4, "bp_work_task", map[string]any{"op": "finding.add", "project": "p"}); !r.Result.IsError {
		t.Fatal("bp_work_task accepted a finding op")
	}
	// A second claimant gets a conflict as a tool error.
	other := NewMCPSession(core, Caller{Name: "other", Transport: "mcp", Verified: true}, nil)
	if r := call(t, other, 5, "bp_work_task", map[string]any{"op": "task.claim", "project": "p", "id": "t1"}); !r.Result.IsError || !strings.HasPrefix(r.Result.Content[0].Text, "conflict: t1 is claimed by worker") {
		t.Fatalf("double claim: %+v", r.Result)
	}
	r = call(t, worker, 6, "bp_work_brief", map[string]any{"op": "brief", "project": "p"})
	if r.Result.IsError || r.Result.StructuredContent["cursor"] == "" {
		t.Fatalf("brief: %+v", r.Result)
	}
	feed := call(t, worker, 7, "bp_work_feed", map[string]any{"op": "feed", "project": "p", "filter": map[string]any{"kinds": []string{"task"}}})
	events := feed.Result.StructuredContent["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("feed events %d", len(events))
	}
	// An unnamed session can read but not write.
	anon := NewMCPSession(core, Caller{Transport: "mcp"}, nil)
	if r := call(t, anon, 8, "bp_work_brief", map[string]any{"op": "brief", "project": "p"}); r.Result.IsError {
		t.Fatalf("anonymous brief: %s", r.Result.Content[0].Text)
	}
	if r := call(t, anon, 9, "bp_work_task", map[string]any{"op": "task.create", "project": "p", "title": "x"}); !r.Result.IsError {
		t.Fatal("anonymous write accepted")
	}
	// A gateway (remote) session is refused.
	gw := NewMCPSession(core, Caller{Name: "web", Transport: "gateway", Remote: true}, &Policy{})
	if r := call(t, gw, 10, "bp_work_brief", map[string]any{"op": "brief", "project": "p"}); !r.Result.IsError || !strings.HasPrefix(r.Result.Content[0].Text, "refused:") {
		t.Fatalf("gateway brief: %+v", r.Result)
	}
}

func TestWorkREST(t *testing.T) {
	_, ts, token := testServer(t)
	root := workRoot(t)
	lead := map[string]string{"X-BP-Agent": "lead"}
	code, body := do(t, "POST", ts.URL+"/v1/work", token, lead, map[string]any{"op": "project.create", "project": "p", "goal": "g", "roots": []string{root}})
	if code != 200 || body["lead"] != "http:lead" {
		t.Fatalf("create %d %v", code, body)
	}
	do(t, "POST", ts.URL+"/v1/work", token, lead, map[string]any{"op": "task.create", "project": "p", "title": "x"})
	do(t, "POST", ts.URL+"/v1/work", token, lead, map[string]any{"op": "task.create", "project": "p", "title": "y", "deps": []string{"t1"}})
	code, body = do(t, "POST", ts.URL+"/v1/work", token, lead, map[string]any{"op": "task.dep", "project": "p", "id": "t1", "dep": "t2"})
	if code != 409 || !strings.Contains(body["error"].(map[string]any)["message"].(string), "cycle") {
		t.Fatalf("cycle %d %v", code, body)
	}
	if code, body := do(t, "GET", ts.URL+"/v1/work/p/brief?items=3", token, nil, nil); code != 200 || body["items"].(float64) != 3 {
		t.Fatalf("brief %d %v", code, body)
	}
	code, body = do(t, "GET", ts.URL+"/v1/work/p/feed?kinds=task&limit=1", token, nil, nil)
	if code != 200 || len(body["events"].([]any)) != 1 || body["more"] != true {
		t.Fatalf("feed %d %v", code, body)
	}
	next := body["next"].(string)
	code, body = do(t, "GET", ts.URL+"/v1/work/p/feed?kinds=task&after="+next, token, nil, nil)
	if code != 200 || len(body["events"].([]any)) != 1 {
		t.Fatalf("feed page 2 %d %v", code, body)
	}
	if code, _ := do(t, "GET", ts.URL+"/v1/work/p/feed?after=bogus", token, nil, nil); code != 400 {
		t.Fatalf("bad cursor %d", code)
	}
	if code, _ := do(t, "GET", ts.URL+"/v1/work/missing/brief", token, nil, nil); code != 404 {
		t.Fatalf("missing project %d", code)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), token) {
		t.Fatal("token leaked into a response")
	}
}
