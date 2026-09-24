package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blueprint/internal/book"
)

func writeCodexNoPromptResume(t *testing.T, folder, id string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "sessions", "2026", "09", "24", "rollout-"+id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	meta, err := json.Marshal(map[string]any{
		"type":    "session_meta",
		"payload": map[string]any{"id": id, "cwd": folder, "source": "cli"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(meta, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFreshCodexNoPromptIsRejectedBeforeAnyTmuxCall(t *testing.T) {
	dir := t.TempDir()
	bookPath := openTestBook(t, dir, "closed")
	tmux, calls, marker := issueTmux(t, "› Ask Codex to do anything\n", false)
	a := issueApp(t, bookPath, filepath.Join(t.TempDir(), "state"), tmux)

	err := a.open([]string{"ghost", dir, "--codex", "--no-prompt"})
	if err == nil || !strings.Contains(err.Error(), "--no-prompt cannot be used for a new Codex agent") || !strings.Contains(err.Error(), "omit --no-prompt") {
		t.Fatalf("open error=%v, want actionable fresh-Codex refusal", err)
	}
	if data, err := os.ReadFile(calls); err == nil && len(data) > 0 {
		t.Fatalf("rejected open called tmux: %s", data)
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read tmux call log: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("rejected open created a tmux session: %v", err)
	}
	if got := bookStatus(t, bookPath, "ghost"); got != "closed" {
		t.Fatalf("rejected open changed agentbook status to %q", got)
	}
}

func TestResumeCodexNoPromptIsAccepted(t *testing.T) {
	const id = "01a0715e-a5c0-7171-adf3-c95343ce6d5b"
	dir := t.TempDir()
	writeCodexNoPromptResume(t, dir, id)
	bookPath := openTestBook(t, dir, "closed")
	tmux, _, _ := openTestTmux(t, bookPath, "› Ask Codex to do anything", false)
	a := openTestApp(t, bookPath, tmux)

	if err := a.open([]string{"ghost", dir, "--codex", "--thread", id, "--no-prompt"}); err != nil {
		t.Fatalf("resume with --no-prompt failed: %v", err)
	}
	fleet, err := book.LoadFleet([]string{bookPath})
	if err != nil {
		t.Fatal(err)
	}
	launch := fleet.Agents["ghost"].Launch
	if launch == nil || !launch.Resume || launch.ResumeID != id {
		t.Fatalf("resume binding not recorded: %+v", launch)
	}
}

func TestClaudeNoPromptIsAccepted(t *testing.T) {
	dir := t.TempDir()
	bookPath := openTestBook(t, dir, "closed")
	tmux, _, _ := openTestTmux(t, bookPath, "bypass permissions", false)
	a := openTestApp(t, bookPath, tmux)

	if err := a.open([]string{"ghost", dir, "--claude", "--no-prompt"}); err != nil {
		t.Fatalf("Claude open with --no-prompt failed: %v", err)
	}
	fleet, err := book.LoadFleet([]string{bookPath})
	if err != nil {
		t.Fatal(err)
	}
	if launch := fleet.Agents["ghost"].Launch; launch == nil || launch.Codex {
		t.Fatalf("Claude launch was not recorded: %+v", launch)
	}
}
