package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"blueprint/internal/tasks"
)

// taskRequest drives one request through a mux that has RegisterTasks mounted.
func taskRequest(t *testing.T, deps TasksDeps, method, path string, body any) (int, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	RegisterTasks(mux, deps)
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

// newTaskDeps builds deps over a temp store with a deterministic clock and id
// source, so tests need no tmux, BP_HOME or network.
func newTaskDeps(t *testing.T, observe func(context.Context, string) string) TasksDeps {
	t.Helper()
	n := 0
	fixed := time.Date(2026, 10, 11, 8, 0, 0, 0, time.UTC)
	return TasksDeps{
		Store:         tasks.NewStore(t.TempDir()),
		ObserveStatus: observe,
		Now:           func() time.Time { return fixed },
		NewID:         func() string { n++; return "t" + string(rune('0'+n)) },
	}
}

func TestTaskCreateAndGet(t *testing.T) {
	deps := newTaskDeps(t, func(_ context.Context, owner string) string {
		if owner == "worker" {
			return "working"
		}
		return ""
	})

	code, body := taskRequest(t, deps, "POST", "/v1/tasks", map[string]any{
		"title": "ship the console", "owner": "worker", "state": "in_progress",
	})
	if code != http.StatusOK {
		t.Fatalf("create status = %d, body %v", code, body)
	}
	created, _ := body["task"].(map[string]any)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("create returned no id: %v", body)
	}
	if created["state"] != "in_progress" {
		t.Fatalf("state = %v, want in_progress", created["state"])
	}
	// derivedStatus is the owner's live runtime, copied verbatim, beside the
	// authoritative state.
	if created["derivedStatus"] != "working" {
		t.Fatalf("derivedStatus = %v, want working", created["derivedStatus"])
	}

	code, body = taskRequest(t, deps, "GET", "/v1/tasks/"+id, nil)
	if code != http.StatusOK {
		t.Fatalf("get status = %d, body %v", code, body)
	}
	got, _ := body["task"].(map[string]any)
	if got["title"] != "ship the console" {
		t.Fatalf("get task = %v", got)
	}
	if _, ok := body["children"].([]any); !ok {
		t.Fatalf("get should always include a children array: %v", body)
	}
}

func TestTaskCreateValidationFails(t *testing.T) {
	deps := newTaskDeps(t, nil)

	// A missing title is refused.
	if code, _ := taskRequest(t, deps, "POST", "/v1/tasks", map[string]any{"owner": "worker"}); code != http.StatusBadRequest {
		t.Fatalf("empty-title status = %d, want 400", code)
	}
	// A bad state is refused (fail closed), not coerced.
	if code, _ := taskRequest(t, deps, "POST", "/v1/tasks", map[string]any{"title": "x", "state": "frozen"}); code != http.StatusBadRequest {
		t.Fatalf("bad-state status = %d, want 400", code)
	}
	// A malformed owner name is refused.
	if code, _ := taskRequest(t, deps, "POST", "/v1/tasks", map[string]any{"title": "x", "owner": "bad name!"}); code != http.StatusBadRequest {
		t.Fatalf("bad-owner status = %d, want 400", code)
	}
}

func TestTaskGetNotFound(t *testing.T) {
	deps := newTaskDeps(t, nil)
	if code, _ := taskRequest(t, deps, "GET", "/v1/tasks/missing", nil); code != http.StatusNotFound {
		t.Fatalf("missing task status = %d, want 404", code)
	}
}

func TestTaskListFilters(t *testing.T) {
	deps := newTaskDeps(t, nil)
	taskRequest(t, deps, "POST", "/v1/tasks", map[string]any{"title": "a", "owner": "alice", "state": "todo"})
	taskRequest(t, deps, "POST", "/v1/tasks", map[string]any{"title": "b", "owner": "bob", "state": "done"})

	code, body := taskRequest(t, deps, "GET", "/v1/tasks", nil)
	if code != http.StatusOK {
		t.Fatalf("list status = %d", code)
	}
	if list, _ := body["tasks"].([]any); len(list) != 2 {
		t.Fatalf("list = %v, want 2", body["tasks"])
	}

	code, body = taskRequest(t, deps, "GET", "/v1/tasks?owner=alice", nil)
	if code != http.StatusOK {
		t.Fatalf("filtered status = %d", code)
	}
	list, _ := body["tasks"].([]any)
	if len(list) != 1 {
		t.Fatalf("owner=alice = %v, want 1", body["tasks"])
	}
	only := list[0].(map[string]any)
	if only["owner"] != "alice" {
		t.Fatalf("filtered task = %v", only)
	}

	// An invalid state filter fails closed.
	if code, _ := taskRequest(t, deps, "GET", "/v1/tasks?state=frozen", nil); code != http.StatusBadRequest {
		t.Fatalf("bad state filter status = %d, want 400", code)
	}
}

func TestTaskGetIncludesChildren(t *testing.T) {
	deps := newTaskDeps(t, nil)
	_, body := taskRequest(t, deps, "POST", "/v1/tasks", map[string]any{"title": "epic"})
	parent := body["task"].(map[string]any)["id"].(string)
	taskRequest(t, deps, "POST", "/v1/tasks", map[string]any{"title": "child", "parent": parent})

	code, got := taskRequest(t, deps, "GET", "/v1/tasks/"+parent, nil)
	if code != http.StatusOK {
		t.Fatalf("get status = %d", code)
	}
	children, _ := got["children"].([]any)
	if len(children) != 1 {
		t.Fatalf("children = %v, want 1", got["children"])
	}
	if children[0].(map[string]any)["title"] != "child" {
		t.Fatalf("child = %v", children[0])
	}
}

func TestTaskPatch(t *testing.T) {
	deps := newTaskDeps(t, nil)
	_, body := taskRequest(t, deps, "POST", "/v1/tasks", map[string]any{"title": "orig", "owner": "worker", "state": "todo"})
	id := body["task"].(map[string]any)["id"].(string)

	code, patched := taskRequest(t, deps, "PATCH", "/v1/tasks/"+id, map[string]any{"state": "done"})
	if code != http.StatusOK {
		t.Fatalf("patch status = %d, body %v", code, patched)
	}
	task := patched["task"].(map[string]any)
	if task["state"] != "done" {
		t.Fatalf("state = %v, want done", task["state"])
	}
	// A field not named in the patch is preserved, not dropped.
	if task["owner"] != "worker" {
		t.Fatalf("patch dropped owner: %v", task)
	}
	if task["title"] != "orig" {
		t.Fatalf("patch dropped title: %v", task)
	}

	// An invalid state is refused (fail closed) and the stored state is unchanged.
	if code, _ := taskRequest(t, deps, "PATCH", "/v1/tasks/"+id, map[string]any{"state": "frozen"}); code != http.StatusBadRequest {
		t.Fatalf("bad-state patch status = %d, want 400", code)
	}
	_, after := taskRequest(t, deps, "GET", "/v1/tasks/"+id, nil)
	if after["task"].(map[string]any)["state"] != "done" {
		t.Fatalf("rejected patch changed state: %v", after["task"])
	}

	// Patching an unknown task is a 404.
	if code, _ := taskRequest(t, deps, "PATCH", "/v1/tasks/missing", map[string]any{"state": "done"}); code != http.StatusNotFound {
		t.Fatalf("patch missing status = %d, want 404", code)
	}
}

// TestTaskDerivedStatusNeverOverwritesState proves the stored state is
// authoritative: a derived "working" runtime does not change a task kept todo.
func TestTaskDerivedStatusNeverOverwritesState(t *testing.T) {
	deps := newTaskDeps(t, func(context.Context, string) string { return "working" })
	_, body := taskRequest(t, deps, "POST", "/v1/tasks", map[string]any{"title": "x", "owner": "worker", "state": "todo"})
	task := body["task"].(map[string]any)
	if task["state"] != "todo" {
		t.Fatalf("state = %v, want todo (authoritative)", task["state"])
	}
	if task["derivedStatus"] != "working" {
		t.Fatalf("derivedStatus = %v, want working (a hint beside state)", task["derivedStatus"])
	}
}

// TestTaskDerivedStatusUnknownWhenNoObserver proves an unknown runtime stays
// unknown (empty), never a guess, when no observer is wired.
func TestTaskDerivedStatusUnknownWhenNoObserver(t *testing.T) {
	deps := newTaskDeps(t, nil)
	_, body := taskRequest(t, deps, "POST", "/v1/tasks", map[string]any{"title": "x", "owner": "worker"})
	if body["task"].(map[string]any)["derivedStatus"] != "" {
		t.Fatalf("derivedStatus = %v, want empty", body["task"].(map[string]any)["derivedStatus"])
	}
}
