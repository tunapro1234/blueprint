package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"blueprint/internal/audit"
	"blueprint/internal/book"
	"blueprint/internal/cache"
)

// fleetRequest drives one request through a mux that has RegisterFleet mounted.
func fleetRequest(t *testing.T, deps FleetDeps, method, path string, body any) (int, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	RegisterFleet(mux, deps)
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

func TestFleetTreeEndpoint(t *testing.T) {
	turn := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	fleet := book.Fleet{
		Root: "root",
		Agents: map[string]book.Agent{
			"root":   {Name: "root", Role: "coordinator"},
			"worker": {Name: "worker", Role: "builder", Folder: "/srv/work"},
		},
		Parents: map[string]string{"root": "", "worker": "root"},
	}
	states := map[string]cache.State{
		"root":   {Runtime: "claude", Activity: &cache.Activity{State: "idle"}},
		"worker": {Runtime: "claude", TurnKnown: true, TurnAt: turn, Activity: &cache.Activity{State: "working"}},
		"ghost":  {}, // a live, unregistered session: unknown, never guessed idle.
	}
	deps := FleetDeps{
		Host:      "box",
		LoadFleet: func() (book.Fleet, error) { return fleet, nil },
		Sessions:  func(context.Context) ([]string, error) { return []string{"worker", "ghost"}, nil },
		Observe: func(_ context.Context, _ book.Fleet, name string, _ []string) cache.State {
			return states[name]
		},
	}

	code, body := fleetRequest(t, deps, "GET", "/v1/fleet/tree", nil)
	if code != http.StatusOK {
		t.Fatalf("tree status = %d, body %v", code, body)
	}
	agents, ok := body["agents"].([]any)
	if !ok || len(agents) != 3 {
		t.Fatalf("agents = %v (want 3: root, worker, ghost)", body["agents"])
	}
	found := map[string]map[string]any{}
	for _, a := range agents {
		m := a.(map[string]any)
		found[m["name"].(string)] = m
	}
	if w := found["worker"]; w["status"] != "working" || w["open"] != true || w["host"] != "box" {
		t.Fatalf("worker = %v", w)
	}
	if w := found["worker"]; w["lastTurnAt"] != turn.Format(time.RFC3339) {
		t.Fatalf("worker lastTurnAt = %v, want %s", w["lastTurnAt"], turn.Format(time.RFC3339))
	}
	if g := found["ghost"]; g["status"] != "unknown" || g["open"] != true {
		t.Fatalf("ghost (unregistered live) = %v, want unknown+open", g)
	}
	if r := found["root"]; r["parent"] != "" {
		t.Fatalf("root parent = %v, want empty", r["parent"])
	}
	// An idle agent must not report a lastTurnAt it does not know.
	if r := found["root"]; r["lastTurnAt"] != nil {
		t.Fatalf("root lastTurnAt = %v, want absent", r["lastTurnAt"])
	}
}

func TestFleetReparentEndpoint(t *testing.T) {
	var events []audit.Event
	var gotAgent, gotParent string
	deps := FleetDeps{
		Audit: func(ev audit.Event) { events = append(events, ev) },
		Reparent: func(name, parent string) (string, error) {
			gotAgent, gotParent = name, parent
			return "oldparent", nil
		},
	}
	code, body := fleetRequest(t, deps, "POST", "/v1/fleet/reparent", map[string]any{"agent": "leaf", "parent": "root"})
	if code != http.StatusOK {
		t.Fatalf("reparent status = %d, body %v", code, body)
	}
	if gotAgent != "leaf" || gotParent != "root" {
		t.Fatalf("Reparent called with %q,%q", gotAgent, gotParent)
	}
	if body["previous"] != "oldparent" {
		t.Fatalf("previous = %v, want oldparent", body["previous"])
	}
	if len(events) != 1 || events[0].Kind != "api.fleet.reparent" || events[0].Target != "leaf" {
		t.Fatalf("audit = %v, want one api.fleet.reparent on leaf", events)
	}
}

func TestFleetReparentErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
	}{
		{"unknown-agent", book.ErrUnknownAgent, http.StatusNotFound},
		{"unknown-parent", book.ErrUnknownParent, http.StatusNotFound},
		{"root", book.ErrReparentRoot, http.StatusBadRequest},
		{"cycle", book.ErrReparentCycle, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var events []audit.Event
			deps := FleetDeps{
				Audit:    func(ev audit.Event) { events = append(events, ev) },
				Reparent: func(string, string) (string, error) { return "", tc.err },
			}
			code, _ := fleetRequest(t, deps, "POST", "/v1/fleet/reparent", map[string]any{"agent": "leaf", "parent": "root"})
			if code != tc.code {
				t.Fatalf("status = %d, want %d", code, tc.code)
			}
			if len(events) != 1 || events[0].Kind != "api.fleet.reparent.rejected" || events[0].Severity != audit.Warn {
				t.Fatalf("audit = %v, want a rejected warn event", events)
			}
		})
	}
}

func TestFleetReparentRejectsBadName(t *testing.T) {
	called := false
	deps := FleetDeps{Reparent: func(string, string) (string, error) { called = true; return "", nil }}
	code, _ := fleetRequest(t, deps, "POST", "/v1/fleet/reparent", map[string]any{"agent": "bad name!", "parent": "root"})
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	if called {
		t.Fatal("Reparent must not run for an invalid agent name")
	}
}

func TestFleetLayoutRoundTrip(t *testing.T) {
	dir := t.TempDir()
	deps := FleetDeps{StateDir: dir}

	// An empty store reads as an empty position map, never an error.
	code, body := fleetRequest(t, deps, "GET", "/v1/fleet/layout", nil)
	if code != http.StatusOK {
		t.Fatalf("empty layout status = %d", code)
	}
	if pos, _ := body["positions"].(map[string]any); len(pos) != 0 {
		t.Fatalf("empty layout = %v, want no positions", body["positions"])
	}

	post := map[string]any{"positions": map[string]any{
		"root":   map[string]any{"x": 1.5, "y": -2},
		"worker": map[string]any{"x": 10, "y": 20},
	}}
	if code, _ := fleetRequest(t, deps, "POST", "/v1/fleet/layout", post); code != http.StatusOK {
		t.Fatalf("post layout status = %d", code)
	}
	// It persists to its own file under the state dir, atomically (no temp left).
	path := filepath.Join(dir, "console", "fleet-layout.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("layout file not written: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "console"))
	for _, e := range entries {
		if e.Name() != "fleet-layout.json" && e.Name() != "fleet-layout.json.lock" {
			t.Fatalf("stray file left in console dir: %s", e.Name())
		}
	}

	code, body = fleetRequest(t, deps, "GET", "/v1/fleet/layout", nil)
	if code != http.StatusOK {
		t.Fatalf("get layout status = %d", code)
	}
	pos := body["positions"].(map[string]any)
	root := pos["root"].(map[string]any)
	if root["x"].(float64) != 1.5 || root["y"].(float64) != -2 {
		t.Fatalf("root position = %v, want {1.5,-2}", root)
	}
}

func TestFleetLayoutValidation(t *testing.T) {
	dir := t.TempDir()
	deps := FleetDeps{StateDir: dir}

	bad := []map[string]any{
		{"positions": map[string]any{"ok": map[string]any{"x": "notnum", "y": 0}}},
		{"positions": map[string]any{"bad name!": map[string]any{"x": 0, "y": 0}}},
		{"positions": map[string]any{"ok": map[string]any{"x": 1e20, "y": 0}}},
	}
	for i, b := range bad {
		if code, _ := fleetRequest(t, deps, "POST", "/v1/fleet/layout", b); code != http.StatusBadRequest {
			t.Fatalf("case %d status = %d, want 400", i, code)
		}
	}
	// A rejected POST must not create the layout file.
	if _, err := os.Stat(filepath.Join(dir, "console", "fleet-layout.json")); !os.IsNotExist(err) {
		t.Fatalf("rejected layout still wrote a file: %v", err)
	}
}
