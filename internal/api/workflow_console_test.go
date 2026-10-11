package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"blueprint/internal/workflow"
)

// wfConsoleRequest drives one request through a mux with RegisterWorkflowConsole
// mounted, mirroring fleetRequest: stub deps, no real store, tmux or network.
func wfConsoleRequest(t *testing.T, deps WorkflowConsoleDeps, method, path string, body any) (int, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	RegisterWorkflowConsole(mux, deps)
	var reader *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	out := map[string]any{}
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestWorkflowConsoleList(t *testing.T) {
	deps := WorkflowConsoleDeps{
		List: func() ([]workflow.Definition, error) {
			return []workflow.Definition{
				{Name: "alpha", Version: 1, Description: "first"},
				{Name: "beta", Version: 1},
			}, nil
		},
	}
	code, body := wfConsoleRequest(t, deps, "GET", "/v1/workflows", nil)
	if code != http.StatusOK {
		t.Fatalf("list status = %d, body %v", code, body)
	}
	workflows, ok := body["workflows"].([]any)
	if !ok || len(workflows) != 2 {
		t.Fatalf("workflows = %v (want 2)", body["workflows"])
	}
	first := workflows[0].(map[string]any)
	if first["name"] != "alpha" || first["description"] != "first" {
		t.Fatalf("first workflow = %v", first)
	}
	// A missing description is omitted, not rendered as empty-but-present noise
	// that a client must special-case.
	second := workflows[1].(map[string]any)
	if _, present := second["description"]; present {
		t.Fatalf("beta description should be omitted, got %v", second)
	}
}

func TestWorkflowConsoleGet(t *testing.T) {
	created := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 10, 10, 9, 30, 0, 0, time.UTC)
	deps := WorkflowConsoleDeps{
		Get: func(name string) (workflow.Definition, []workflow.Unit, error) {
			if name != "alpha" {
				t.Fatalf("Get called with %q, want alpha", name)
			}
			return workflow.Definition{Name: "alpha", Version: 1, Description: "first"},
				[]workflow.Unit{{Key: "u1", Data: map[string]any{"key": "u1"}}}, nil
		},
		RecentRuns: func(name string, limit int) ([]workflow.Run, error) {
			return []workflow.Run{{ID: "run-1", Status: "running", CreatedAt: created, UpdatedAt: updated}}, nil
		},
	}
	code, body := wfConsoleRequest(t, deps, "GET", "/v1/workflows/alpha", nil)
	if code != http.StatusOK {
		t.Fatalf("get status = %d, body %v", code, body)
	}
	def, ok := body["definition"].(map[string]any)
	if !ok || def["name"] != "alpha" {
		t.Fatalf("definition = %v", body["definition"])
	}
	units, ok := body["units"].([]any)
	if !ok || len(units) != 1 {
		t.Fatalf("units = %v (want 1)", body["units"])
	}
	runs, ok := body["recentRuns"].([]any)
	if !ok || len(runs) != 1 {
		t.Fatalf("recentRuns = %v (want 1)", body["recentRuns"])
	}
	run := runs[0].(map[string]any)
	if run["id"] != "run-1" || run["status"] != "running" {
		t.Fatalf("run = %v", run)
	}
	if run["createdAt"] != created.Format(time.RFC3339) || run["updatedAt"] != updated.Format(time.RFC3339) {
		t.Fatalf("run timestamps = %v, want %s/%s", run, created.Format(time.RFC3339), updated.Format(time.RFC3339))
	}
}

func TestWorkflowConsoleGetOmitsUnknownRunTimestamp(t *testing.T) {
	deps := WorkflowConsoleDeps{
		Get: func(string) (workflow.Definition, []workflow.Unit, error) {
			return workflow.Definition{Name: "alpha", Version: 1}, nil, nil
		},
		// A run whose timestamps are unknown (zero) must not render an epoch.
		RecentRuns: func(string, int) ([]workflow.Run, error) {
			return []workflow.Run{{ID: "run-0", Status: "unknown"}}, nil
		},
	}
	code, body := wfConsoleRequest(t, deps, "GET", "/v1/workflows/alpha", nil)
	if code != http.StatusOK {
		t.Fatalf("get status = %d, body %v", code, body)
	}
	// Units with no bundled sample come back as an empty list, never null.
	if units, ok := body["units"].([]any); !ok || len(units) != 0 {
		t.Fatalf("units = %v, want empty list", body["units"])
	}
	run := body["recentRuns"].([]any)[0].(map[string]any)
	if _, present := run["createdAt"]; present {
		t.Fatalf("unknown createdAt should be omitted, got %v", run)
	}
}

func TestWorkflowConsoleGetNotFound(t *testing.T) {
	deps := WorkflowConsoleDeps{
		Get: func(string) (workflow.Definition, []workflow.Unit, error) {
			return workflow.Definition{}, nil, os.ErrNotExist
		},
	}
	code, _ := wfConsoleRequest(t, deps, "GET", "/v1/workflows/ghost", nil)
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
}

func TestWorkflowConsoleGetRejectsBadName(t *testing.T) {
	called := false
	deps := WorkflowConsoleDeps{
		Get: func(string) (workflow.Definition, []workflow.Unit, error) {
			called = true
			return workflow.Definition{}, nil, nil
		},
	}
	code, _ := wfConsoleRequest(t, deps, "GET", "/v1/workflows/Bad_Name", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	if called {
		t.Fatal("Get must not run for an invalid workflow name")
	}
}

func TestWorkflowConsoleClone(t *testing.T) {
	var gotFrom, gotTo string
	deps := WorkflowConsoleDeps{
		Clone: func(from, to string) error {
			gotFrom, gotTo = from, to
			return nil
		},
	}
	code, body := wfConsoleRequest(t, deps, "POST", "/v1/workflows/alpha/clone", map[string]any{"to": "alpha-copy"})
	if code != http.StatusOK {
		t.Fatalf("clone status = %d, body %v", code, body)
	}
	if gotFrom != "alpha" || gotTo != "alpha-copy" {
		t.Fatalf("Clone called with %q,%q", gotFrom, gotTo)
	}
	if body["from"] != "alpha" || body["to"] != "alpha-copy" {
		t.Fatalf("clone body = %v", body)
	}
}

func TestWorkflowConsoleCloneCollision(t *testing.T) {
	deps := WorkflowConsoleDeps{
		Clone: func(from, to string) error { return errWfConsoleExists },
	}
	code, _ := wfConsoleRequest(t, deps, "POST", "/v1/workflows/alpha/clone", map[string]any{"to": "beta"})
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", code)
	}
}

func TestWorkflowConsoleCloneSourceMissing(t *testing.T) {
	deps := WorkflowConsoleDeps{
		Clone: func(from, to string) error { return os.ErrNotExist },
	}
	code, _ := wfConsoleRequest(t, deps, "POST", "/v1/workflows/ghost/clone", map[string]any{"to": "copy"})
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
}

func TestWorkflowConsoleCloneRejectsBadTarget(t *testing.T) {
	called := false
	deps := WorkflowConsoleDeps{
		Clone: func(from, to string) error { called = true; return nil },
	}
	// An invalid target name, and a target equal to the source, both fail
	// closed before Clone runs.
	for _, to := range []string{"../escape", "alpha"} {
		code, _ := wfConsoleRequest(t, deps, "POST", "/v1/workflows/alpha/clone", map[string]any{"to": to})
		if code != http.StatusBadRequest {
			t.Fatalf("to=%q status = %d, want 400", to, code)
		}
	}
	if called {
		t.Fatal("Clone must not run for an invalid target")
	}
}

// TestWorkflowConsoleCloneProduction exercises the real store-backed deps end to
// end against a temp state dir: add a workflow, clone it under a new name, and
// confirm the copy is listed, readable and refuses to clobber on a second clone.
func TestWorkflowConsoleCloneProduction(t *testing.T) {
	srcDir := t.TempDir()
	writeFile(t, filepath.Join(srcDir, "workflow.yaml"),
		"name: src-wf\nversion: 1\nunits:\n  file: units.txt\nprompt:\n  template: prompt.tmpl\n")
	writeFile(t, filepath.Join(srcDir, "units.txt"), "alpha\nbeta\n")
	writeFile(t, filepath.Join(srcDir, "prompt.tmpl"), "Do {{.Key}}\n")

	stateDir := t.TempDir()
	store := workflow.NewStore(stateDir)
	if _, _, err := store.Add(srcDir, false); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
	deps := NewWorkflowConsoleDeps(stateDir)

	code, body := wfConsoleRequest(t, deps, "POST", "/v1/workflows/src-wf/clone", map[string]any{"to": "dst-wf"})
	if code != http.StatusOK {
		t.Fatalf("clone status = %d, body %v", code, body)
	}

	code, body = wfConsoleRequest(t, deps, "GET", "/v1/workflows", nil)
	if code != http.StatusOK {
		t.Fatalf("list status = %d", code)
	}
	names := map[string]bool{}
	for _, w := range body["workflows"].([]any) {
		names[w.(map[string]any)["name"].(string)] = true
	}
	if !names["src-wf"] || !names["dst-wf"] {
		t.Fatalf("workflows = %v, want both src-wf and dst-wf", names)
	}

	// The clone is a real, readable workflow renamed to its new name, with its
	// units carried over.
	code, body = wfConsoleRequest(t, deps, "GET", "/v1/workflows/dst-wf", nil)
	if code != http.StatusOK {
		t.Fatalf("detail status = %d, body %v", code, body)
	}
	if def := body["definition"].(map[string]any); def["name"] != "dst-wf" {
		t.Fatalf("cloned definition = %v, want name dst-wf", def)
	}
	if units := body["units"].([]any); len(units) != 2 {
		t.Fatalf("cloned units = %v, want 2", body["units"])
	}

	// A second clone to the same target must not clobber the first (409).
	code, _ = wfConsoleRequest(t, deps, "POST", "/v1/workflows/src-wf/clone", map[string]any{"to": "dst-wf"})
	if code != http.StatusConflict {
		t.Fatalf("second clone status = %d, want 409", code)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
