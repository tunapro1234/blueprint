package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blueprint/internal/book"
)

// Issue #23: there was no way to fix the parent of an existing agent short of
// editing agentbook.json by hand under the lock.
func TestIssue23_ReparentMovesExistingAgent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENTBOOK", "")
	bookPath := filepath.Join(dir, "agentbook.json")
	content := `{"agents":[{"name":"main","folder":"/w"},` +
		`{"name":"lead","folder":"/w/lead","parent":"main"},` +
		`{"name":"worker","folder":"/w/a","parent":"lead"},` +
		`{"name":"worker-2","folder":"/w/b","parent":"main"}]}`
	if err := os.WriteFile(bookPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	a := openTestApp(t, bookPath, nil)
	if err := a.run([]string{"reparent", "worker-2", "lead"}); err != nil {
		t.Fatal(err)
	}
	fleet, err := book.LoadFleet([]string{bookPath})
	if err != nil {
		t.Fatal(err)
	}
	if got := fleet.Parents["worker-2"]; got != "lead" {
		t.Fatalf("worker-2 parent=%q want lead", got)
	}
	if err := a.run([]string{"reparent", "lead", "worker"}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle not refused: %v", err)
	}
	if err := a.run([]string{"reparent", "worker", "nobody"}); err == nil || !strings.Contains(err.Error(), "unknown parent") {
		t.Fatalf("unknown parent not refused: %v", err)
	}
}

// Issue #23: bp run had no --parent and reset every relaunch to the root.
func TestIssue23_RunPlacementKeepsRecordAndHonoursParent(t *testing.T) {
	fleet := book.Fleet{Root: "main", Agents: map[string]book.Agent{
		"main":   {Name: "main"},
		"lead":   {Name: "lead", Parent: "main"},
		"worker": {Name: "worker", Parent: "lead", Role: "tests"},
	}}
	if parent, role := localPlacement(fleet, "worker", "", ""); parent != "" || role != "" {
		t.Fatalf("relaunch overwrote placement: parent=%q role=%q", parent, role)
	}
	if parent, role := localPlacement(fleet, "worker-2", "lead", "fixes"); parent != "lead" || role != "fixes" {
		t.Fatalf("explicit placement ignored: %q %q", parent, role)
	}
	if parent, role := localPlacement(fleet, "fresh", "", ""); parent != "main" || role != "local CLI" {
		t.Fatalf("new record default: %q %q", parent, role)
	}
}
