package book

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"blueprint/internal/cache"
	bptmux "blueprint/internal/tmux"
)

func TestConsoleTreeCopiesStatusVerbatim(t *testing.T) {
	turn := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	fleet := Fleet{
		Root: "root",
		Agents: map[string]Agent{
			"root":   {Name: "root", Role: "coordinator"},
			"worker": {Name: "worker", Role: "builder", Folder: "/srv/work (home: /srv/work)"},
			"remote": {Name: "remote", Launch: &bptmux.OpenOptions{Remote: "peer-box", Codex: true}},
			"closed": {Name: "closed", Launch: &bptmux.OpenOptions{}},
		},
		Parents: map[string]string{"root": "", "worker": "root", "remote": "root", "closed": "root"},
	}
	open := map[string]bool{"worker": true, "remote": true}
	states := map[string]cache.State{
		"root":   {Runtime: "claude", Activity: &cache.Activity{State: "idle"}},
		"worker": {Runtime: "claude", TurnKnown: true, TurnAt: turn, Activity: &cache.Activity{State: "working", Reason: "pane"}},
		"remote": {Runtime: "codex-remote", Activity: &cache.Activity{State: "unknown", Reason: "disconnected"}},
		// closed: no observation at all -> must stay unknown, not guessed idle.
		"closed": {},
	}
	observe := func(name string) cache.State { return states[name] }

	tree := ConsoleTree(fleet, open, "local-box", observe)

	byName := map[string]ConsoleAgent{}
	for _, a := range tree {
		byName[a.Name] = a
	}
	if len(tree) != 4 {
		t.Fatalf("got %d agents, want 4", len(tree))
	}
	// Sorted order: closed, remote, root, worker.
	if tree[0].Name != "closed" || tree[3].Name != "worker" {
		t.Fatalf("not name-sorted: %s..%s", tree[0].Name, tree[3].Name)
	}

	if a := byName["closed"]; a.Status != "unknown" || a.StatusReason != "" {
		t.Fatalf("closed agent status = %q/%q, want unknown with no reason", a.Status, a.StatusReason)
	}
	if a := byName["closed"]; a.Open || a.Runtime != "claude" {
		// runtime falls back to the configured harness for a closed agent.
		t.Fatalf("closed agent open=%v runtime=%q, want open=false runtime=claude", a.Open, a.Runtime)
	}
	if a := byName["worker"]; a.Status != "working" || a.StatusReason != "pane" {
		t.Fatalf("worker status = %q/%q", a.Status, a.StatusReason)
	}
	if a := byName["worker"]; !a.HasTurn || !a.LastTurnAt.Equal(turn) {
		t.Fatalf("worker lastTurn = %v (known %v), want %v", a.LastTurnAt, a.HasTurn, turn)
	}
	if a := byName["worker"]; a.Folder != "/srv/work" {
		t.Fatalf("worker folder = %q, want the annotation stripped", a.Folder)
	}
	if a := byName["worker"]; a.Host != "local-box" || !a.Open {
		t.Fatalf("worker host=%q open=%v, want local-box/true", a.Host, a.Open)
	}
	if a := byName["remote"]; a.Host != "peer-box" {
		t.Fatalf("remote host = %q, want the recorded remote peer-box", a.Host)
	}
	if a := byName["root"]; a.Parent != "" || a.Status != "idle" {
		t.Fatalf("root parent=%q status=%q, want empty parent / idle", a.Parent, a.Status)
	}
	if a := byName["worker"]; a.HasTurn && a.LastTurnAt.IsZero() {
		t.Fatal("HasTurn must not be set for a zero turn time")
	}
	// An agent with no turn record reports none rather than a zero timestamp.
	if a := byName["root"]; a.HasTurn {
		t.Fatalf("root should have no known turn, got %v", a.LastTurnAt)
	}
}

func writeReparentBook(t *testing.T, path string) {
	t.Helper()
	writeBookFile(t, path, map[string]any{
		"orchestrator": "root",
		"agents": []any{
			map[string]any{"name": "root", "role": "lead"},
			map[string]any{"name": "mid", "parent": "root"},
			map[string]any{"name": "leaf", "parent": "mid"},
		},
	})
}

func TestReparentMovesAgent(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	path := filepath.Join(t.TempDir(), "agentbook.json")
	writeReparentBook(t, path)

	previous, err := Reparent([]string{path}, "leaf", "root")
	if err != nil {
		t.Fatal(err)
	}
	if previous != "mid" {
		t.Fatalf("previous = %q, want mid", previous)
	}
	fleet, err := LoadFleet([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if fleet.Parents["leaf"] != "root" {
		t.Fatalf("leaf parent = %q after reparent, want root", fleet.Parents["leaf"])
	}
}

func TestReparentDetachToRoot(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	path := filepath.Join(t.TempDir(), "agentbook.json")
	writeReparentBook(t, path)

	previous, err := Reparent([]string{path}, "leaf", "")
	if err != nil {
		t.Fatal(err)
	}
	if previous != "mid" {
		t.Fatalf("previous = %q, want mid", previous)
	}
	fleet, err := LoadFleet([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if fleet.Parents["leaf"] != "root" {
		t.Fatalf("leaf parent = %q after detach, want root", fleet.Parents["leaf"])
	}
	// The stored row carries an empty parent a previous release still reads.
	raw := readBookFile(t, path)["agents"].([]any)
	for _, value := range raw {
		agent := value.(map[string]any)
		if agent["name"] == "leaf" {
			if p, ok := agent["parent"].(string); !ok || p != "" {
				t.Fatalf("stored leaf parent = %v, want empty string", agent["parent"])
			}
		}
	}
}

func TestReparentRejectsCycle(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	path := filepath.Join(t.TempDir(), "agentbook.json")
	writeReparentBook(t, path)

	if _, err := Reparent([]string{path}, "mid", "leaf"); !errors.Is(err, ErrReparentCycle) {
		t.Fatalf("mid under leaf err = %v, want ErrReparentCycle", err)
	}
	// Reparenting an agent under itself is also a cycle.
	if _, err := Reparent([]string{path}, "mid", "mid"); !errors.Is(err, ErrReparentCycle) {
		t.Fatalf("mid under mid err = %v, want ErrReparentCycle", err)
	}
	// The book must be unchanged after a rejected move.
	fleet, _ := LoadFleet([]string{path})
	if fleet.Parents["mid"] != "root" {
		t.Fatalf("mid parent changed to %q despite rejection", fleet.Parents["mid"])
	}
}

func TestReparentValidatesExistence(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	path := filepath.Join(t.TempDir(), "agentbook.json")
	writeReparentBook(t, path)

	if _, err := Reparent([]string{path}, "ghost", "root"); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("unknown agent err = %v, want ErrUnknownAgent", err)
	}
	if _, err := Reparent([]string{path}, "leaf", "ghost"); !errors.Is(err, ErrUnknownParent) {
		t.Fatalf("unknown parent err = %v, want ErrUnknownParent", err)
	}
	if _, err := Reparent([]string{path}, "root", "mid"); !errors.Is(err, ErrReparentRoot) {
		t.Fatalf("reparent root err = %v, want ErrReparentRoot", err)
	}
}
