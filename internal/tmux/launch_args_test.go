package tmux

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenPassesRecordedArgsAfterBinaryWithoutDuplicates(t *testing.T) {
	id := "019a0d02-a847-76d1-ba01-8b67fbe755c1"
	h := &launchHarness{capture: "› Ask Codex to do anything\n"}
	opts := OpenOptions{Codex: true, NoSandbox: true, Resume: true, ResumeID: id, NoPrompt: true, Args: []string{"--yolo", "-m", "gpt-x"}}
	if err := h.client().Open(context.Background(), "claude-w", "/work", opts, nil); err != nil {
		t.Fatal(err)
	}
	var launch string
	for _, call := range h.calls {
		if strings.HasPrefix(call, "send-keys") && strings.Contains(call, "codex") {
			launch = call
		}
	}
	want := "CODEX_BWRAPPED=1 codex '-m' 'gpt-x' --dangerously-bypass-approvals-and-sandbox resume '" + id + "'"
	if !strings.Contains(launch, want) {
		t.Fatalf("launch=%q want %q", launch, want)
	}
	h = &launchHarness{capture: "bypass permissions\n"}
	if err := h.client().Open(context.Background(), "claude-main", "/work", OpenOptions{NoPrompt: true, Args: []string{"--dangerously-skip-permissions", "--model", "opus"}}, nil); err != nil {
		t.Fatal(err)
	}
	for _, call := range h.calls {
		if strings.HasPrefix(call, "send-keys") && strings.Contains(call, " claude ") {
			launch = call
		}
	}
	if strings.Count(launch, "--dangerously-skip-permissions") != 1 || !strings.Contains(launch, "PREFIX=claude-main claude '--model' 'opus' --dangerously-skip-permissions") {
		t.Fatalf("claude launch=%q", launch)
	}
}

func TestClaudeResumeDoesNotRepeatRecordedPermissionFlag(t *testing.T) {
	id := "019a0d02-a847-76d1-ba01-8b67fbe755c1"
	claudeHome := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
	transcriptDir := filepath.Join(claudeHome, "projects", mungeProjectPath("/work"))
	if err := os.MkdirAll(transcriptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transcriptDir, id+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &launchHarness{capture: "bypass permissions\n"}
	opts := OpenOptions{
		Resume: true, ResumeID: id, NoPrompt: true,
		Args: []string{"--dangerously-skip-permissions", "--model", "opus"},
	}
	if err := h.client().Open(context.Background(), "claude-worker", "/work", opts, nil); err != nil {
		t.Fatal(err)
	}
	var launch string
	for _, call := range h.calls {
		if strings.HasPrefix(call, "send-keys") && strings.Contains(call, "--resume") {
			launch = call
		}
	}
	if strings.Count(launch, "--dangerously-skip-permissions") != 1 || !strings.Contains(launch, "--resume '"+id+"'") || !strings.Contains(launch, "'--model' 'opus'") {
		t.Fatalf("resumed Claude launch=%q", launch)
	}
}
