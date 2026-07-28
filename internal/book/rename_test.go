package book

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeBookFile(t *testing.T, path string, file map[string]any) {
	t.Helper()
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readBookFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

// A rename has to reach three different places, and they are not always in the
// same file: the agent's own entry, the parent field of its children, and role
// prose that names it.
func TestRenameSpansBooks(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main.json")
	probot := filepath.Join(dir, "probot.json")

	writeBookFile(t, main, map[string]any{
		"orchestrator": "server-main",
		"agents": []any{
			map[string]any{"name": "server-main", "role": "fleet lead"},
			map[string]any{"name": "probot-business-outreach", "folder": "/srv/probot/outreach", "parent": "probot-main"},
		},
	})
	// The child lives in the other book, which is the case that made the manual
	// rename miss a spot.
	writeBookFile(t, probot, map[string]any{
		"agents": []any{
			map[string]any{"name": "probot-outreach-gpt", "parent": "probot-business-outreach"},
			map[string]any{"name": "probot-main", "role": "coordinates probot-business-outreach and the shop"},
		},
	})

	changes, err := Rename([]string{main, probot}, "probot-business-outreach", "probot-outreach")
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 3 {
		t.Fatalf("expected 3 changes (name, child parent, role), got %d: %v", len(changes), changes)
	}

	agents := readBookFile(t, main)["agents"].([]any)
	if got := agents[1].(map[string]any)["name"]; got != "probot-outreach" {
		t.Fatalf("entry name = %v, want probot-outreach", got)
	}
	other := readBookFile(t, probot)["agents"].([]any)
	if got := other[0].(map[string]any)["parent"]; got != "probot-outreach" {
		t.Fatalf("child parent = %v, want probot-outreach", got)
	}
	if got := other[1].(map[string]any)["role"]; got != "coordinates probot-outreach and the shop" {
		t.Fatalf("role = %v, want the mention rewritten", got)
	}
}

// Renaming "probot-outreach" must not corrupt "probot-outreach-gpt", which
// merely starts with it. A plain substring replace would have.
func TestRenameLeavesLongerNamesAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "book.json")
	writeBookFile(t, path, map[string]any{
		"agents": []any{
			map[string]any{"name": "probot-outreach"},
			map[string]any{"name": "probot-outreach-gpt", "parent": "probot-outreach", "role": "helper for probot-outreach-gpt duties under probot-outreach"},
		},
	})

	if _, err := Rename([]string{path}, "probot-outreach", "probot-reach"); err != nil {
		t.Fatal(err)
	}
	agents := readBookFile(t, path)["agents"].([]any)
	sibling := agents[1].(map[string]any)
	if got := sibling["name"]; got != "probot-outreach-gpt" {
		t.Fatalf("sibling name = %v, want it untouched", got)
	}
	if got := sibling["parent"]; got != "probot-reach" {
		t.Fatalf("sibling parent = %v, want probot-reach", got)
	}
	want := "helper for probot-outreach-gpt duties under probot-reach"
	if got := sibling["role"]; got != want {
		t.Fatalf("role = %v, want %q", got, want)
	}
}

// A book that mentions the agent nowhere must not be rewritten at all, so an
// unrelated hand edit cannot be lost to a no-op write.
func TestRenameLeavesUnrelatedBooksUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "book.json")
	writeBookFile(t, path, map[string]any{
		"agents": []any{map[string]any{"name": "kavram-main"}},
	})
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	changes, err := Rename([]string{path}, "probot-outreach", "probot-reach")
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected no changes, got %v", changes)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("book was rewritten despite having nothing to rename")
	}
}

// A missing book is normal (not every machine has both), so it must not fail
// the whole rename.
func TestRenameSkipsMissingBooks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "book.json")
	writeBookFile(t, path, map[string]any{
		"agents": []any{map[string]any{"name": "a"}},
	})
	changes, err := Rename([]string{filepath.Join(dir, "absent.json"), path}, "a", "b")
	if err != nil {
		t.Fatalf("missing book should be skipped, got %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected the present book to still be renamed, got %v", changes)
	}
}
