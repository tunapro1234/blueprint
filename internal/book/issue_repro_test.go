package book

import (
	"path/filepath"
	"testing"
)

func TestIssue11RenameRefusesAnArchivedDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentbook.json")
	writeBookFile(t, path, map[string]any{"agents": []any{
		map[string]any{"name": "worker", "status": "open"},
		map[string]any{"name": "target", "status": "closed", "archivedAt": "2026-09-01T00:00:00Z"},
	}})

	if _, err := Rename([]string{path}, "worker", "target"); err == nil {
		t.Fatal("rename succeeded and created a duplicate registration for an archived name")
	}
	file, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, agent := range file.Agents {
		if agent.Name == "target" {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("book has %d registrations named target after refusal, want the archived entry only", seen)
	}
}
