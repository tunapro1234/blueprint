package main

// Extra issue coverage kept buildable against the ead3de0 API surface.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIssue05_ClaudeResumeReusesTheRecordWithTheMatchingThread(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	path := filepath.Join(dir, "agentbook.json")
	const thread = "11111111-1111-1111-1111-111111111111"
	issueWriteBook(t, path, []map[string]any{
		{"name": "server-main", "folder": dir, "status": "open"},
		{"name": "named-agent", "folder": dir, "status": "opening", "nativeTitle": map[string]any{"threadId": thread, "text": "named-agent"}},
	})
	tmux, _, _ := issueTmux(t, "", false)
	a := issueApp(t, path, filepath.Join(dir, "state"), tmux)
	// Use the new adoption seam when present. On the ead3de0 API surface, fall
	// back to the old resume guard so the test fails on the missing behavior,
	// rather than failing to compile because the helper did not exist yet.
	var existing string
	var attach bool
	var err error
	if adopter, ok := any(a).(interface {
		adoptNativeThread(string, string) (string, bool, error)
	}); ok {
		existing, attach, err = adopter.adoptNativeThread(thread, "")
	} else {
		guard, name, guardErr := a.guardClaudeResume(thread, "", dir)
		if guard != nil {
			defer guard.close()
		}
		existing, err = name, guardErr
	}
	if err != nil {
		t.Fatal(err)
	}
	if existing != "named-agent" || attach {
		t.Fatalf("matching thread was not bound to its existing record: got %q", existing)
	}
}

// Issue #17 comment: renaming an opencode agent (folder = the shell's cwd,
// often $HOME) ran its reference scan while holding launch.lock, so every
// later bp open/run queued behind it.
func TestIssue17_RenameReferenceScanDoesNotHoldTheLaunchLock(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	resume := filepath.Join(state, "local-resume")
	if err := os.MkdirAll(resume, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resume, "claim.json"), []byte(`{"Name":"someone-else"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bin, lockSeen := t.TempDir(), filepath.Join(dir, "lock-during-scan")
	grep := "#!/bin/sh\nif flock -n " + quoteShell(filepath.Join(resume, "launch.lock")) + " true; then echo free; else echo held; fi > " + quoteShell(lockSeen) + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "grep"), []byte(grep), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	path := filepath.Join(dir, "agentbook.json")
	issueWriteBook(t, path, []map[string]any{{"name": "opencode-agent", "folder": dir, "status": "closed"}})
	tmux, _, _ := issueTmux(t, "", false)
	a := issueApp(t, path, state, tmux)
	if err := a.rename([]string{"opencode-agent", "renamed-agent", "--no-retitle"}); err != nil {
		t.Fatal(err)
	}
	if seen, _ := os.ReadFile(lockSeen); strings.TrimSpace(string(seen)) != "free" {
		t.Fatalf("launch.lock during reference scan: %q, want free", seen)
	}
}
