package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blueprint/internal/book"
)

func TestIssue27_AdoptMovesClosedAndArchivedThreadClaims(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agentbook.json")
	const thread = "11111111-1111-1111-1111-111111111111"
	issueWriteBook(t, path, []map[string]any{
		{"name": "airpods", "folder": dir, "status": "closed"},
		{"name": "claude-tuna-8da88b", "folder": dir, "status": "closed", "identityThreadId": thread},
		{"name": "claude-tuna-a360d0", "folder": dir, "status": "closed", "archivedAt": "2026-01-01", "launch": map[string]any{"resumeId": thread}},
	})
	tmux, _, _ := issueTmux(t, "", false)
	a := issueApp(t, path, filepath.Join(dir, "state"), tmux)
	if err := a.checkThreadBinding("airpods", thread, false); err == nil {
		t.Fatal("closed holder did not block without --adopt")
	} else {
		for _, want := range []string{"claude-tuna-8da88b", "claude-tuna-a360d0", "closed", "archived", path, "--adopt"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("conflict %q does not include %q", err, want)
			}
		}
	}
	if err := a.checkThreadBinding("airpods", thread, true); err != nil {
		t.Fatalf("--adopt check rejected closed holders: %v", err)
	}
	if err := a.adoptThreadBinding("airpods", thread); err != nil {
		t.Fatal(err)
	}
	bindings, err := book.ThreadBindings([]string{path}, thread)
	if err != nil || len(bindings) != 0 {
		t.Fatalf("old claims remain: %+v, %v", bindings, err)
	}
	file, err := book.Load(path)
	if err != nil || len(file.Agents) != 3 || file.Agents[2].ArchivedAt == "" {
		t.Fatalf("adoption deleted/changed rows: %+v, %v", file.Agents, err)
	}
	if output := readTestOutput(t, a.out); !strings.Contains(output, "adopted thread "+thread+" from claude-tuna-8da88b") || !strings.Contains(output, "from claude-tuna-a360d0 (archived)") {
		t.Fatalf("adoption did not print moved claims: %q", output)
	}
}

func TestIssue27_LiveTmuxHolderBlocksAdoption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agentbook.json")
	const thread = "22222222-2222-2222-2222-222222222222"
	issueWriteBook(t, path, []map[string]any{
		{"name": "airpods", "folder": dir, "status": "closed"},
		{"name": "live-holder", "folder": dir, "status": "open", "identityThreadId": thread},
	})
	tmux, _, _ := issueTmux(t, "", true)
	a := issueApp(t, path, filepath.Join(dir, "state"), tmux)
	err := a.checkThreadBinding("airpods", thread, true)
	if err == nil || !strings.Contains(err.Error(), "live tmux session live-holder") || !strings.Contains(err.Error(), "bp attach live-holder") {
		t.Fatalf("live holder error=%v, want attach guidance", err)
	}
}

func TestIssue27_NamedRunAdoptsClosedThread(t *testing.T) {
	root, state, home := t.TempDir(), t.TempDir(), t.TempDir()
	path := filepath.Join(root, "agentbook.json")
	const thread = "88888888-8888-8888-8888-888888888888"
	issueWriteBook(t, path, []map[string]any{
		{"name": "airpods", "folder": root, "status": "closed"},
		{"name": "old-name", "folder": root, "status": "closed", "identityThreadId": thread},
	})
	cliDir := t.TempDir()
	claude := filepath.Join(cliDir, "claude")
	if err := os.WriteFile(claude, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", cliDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	tmux, _, _ := issueTmux(t, "", false)
	a := issueApp(t, path, state, tmux)
	a.interactive = func() bool { return true }
	args := []string{"--name", "airpods", "claude", "--resume", thread}
	if err := a.localRun(args); err == nil || !strings.Contains(err.Error(), "--adopt") {
		t.Fatalf("named resume without --adopt error=%v", err)
	}
	args = []string{"--name", "airpods", "--adopt", "claude", "--resume", thread}
	if err := a.localRun(args); err != nil {
		t.Fatal(err)
	}
	bindings, err := book.ThreadBindings([]string{path}, thread)
	if err != nil || len(bindings) != 0 {
		t.Fatalf("named bp run left old thread claim: %+v, %v", bindings, err)
	}
	if output := readTestOutput(t, a.out); !strings.Contains(output, "adopted thread "+thread+" from old-name") {
		t.Fatalf("named bp run did not report the adoption: %q", output)
	}
}
