package main

// Repro tests that need an API introduced by the fix itself; against ead3de0
// they fail to compile rather than on behaviour. See issue_repro_test.go.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIssue05ClaudeResumeReusesTheRecordWithTheMatchingThread(t *testing.T) {
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
	// bp run resolves the thread's owner before taking the launch guard.
	existing, attach, err := a.adoptNativeThread(thread, "")
	if err != nil {
		t.Fatal(err)
	}
	if existing != "named-agent" || attach {
		t.Fatalf("matching thread was not bound to its existing record: got %q", existing)
	}
}

// Issue #17 comment: renaming an opencode agent (folder = the shell's cwd,
// often $HOME) hung in the reference scan while holding launch.lock, so every
// later bp open/run queued behind it.
func TestIssue17RenameReferenceScanIsBoundedAndDoesNotHoldTheLaunchLock(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	resume := filepath.Join(state, "local-resume")
	if err := os.MkdirAll(resume, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resume, "claim.json"), []byte(`{"Name":"someone-else"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bin, lockSeen := t.TempDir(), filepath.Join(dir, "lock-during-scan")
	grep := "#!/bin/sh\nif flock -n " + quoteShell(filepath.Join(resume, "launch.lock")) + " true; then echo free; else echo held; fi > " + quoteShell(lockSeen) + "\nexec sleep 30\n"
	if err := os.WriteFile(filepath.Join(bin, "grep"), []byte(grep), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	previous := referenceScanTimeout
	referenceScanTimeout = 300 * time.Millisecond
	t.Cleanup(func() { referenceScanTimeout = previous })
	path := filepath.Join(dir, "agentbook.json")
	issueWriteBook(t, path, []map[string]any{{"name": "opencode-agent", "folder": dir, "status": "closed"}})
	tmux, _, _ := issueTmux(t, "", false)
	a := issueApp(t, path, state, tmux)
	start := time.Now()
	if err := a.rename([]string{"opencode-agent", "renamed-agent", "--no-retitle"}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("rename blocked %s in the reference scan", elapsed)
	}
	if seen, _ := os.ReadFile(lockSeen); strings.TrimSpace(string(seen)) != "free" {
		t.Fatalf("launch.lock during reference scan: %q, want free", seen)
	}
}
