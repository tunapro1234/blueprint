package main

import (
	"reflect"
	"testing"
)

func TestNativeLaunchRecordsModeNotConversationOrPrompt(t *testing.T) {
	id := "019a0d02-a847-76d1-ba01-8b67fbe755c1"
	codex := nativeLaunch("codex", []string{"resume", id, "--search", "--yolo", "-m", "gpt-x", "-c", "model_reasoning_effort=high"})
	if codex.ResumeID != id || !codex.Resume || !codex.Codex || !codex.NoSandbox ||
		!reflect.DeepEqual(codex.Args, []string{"--search", "--yolo", "-m", "gpt-x", "-c", "model_reasoning_effort=high"}) {
		t.Fatalf("codex launch: %+v", codex)
	}
	claude := nativeLaunch("claude", []string{"--settings", "/state/run-1/settings.json", "--resume", id, "--model", "opus", "--effort=high", "fix the bug"})
	if claude.ResumeID != id || claude.Codex || claude.NoSandbox ||
		!reflect.DeepEqual(claude.Args, []string{"--model", "opus", "--effort=high"}) {
		t.Fatalf("claude launch: %+v", claude)
	}
	if fresh := nativeLaunch("codex", []string{"-m", "gpt-x", "write tests"}); fresh.Resume || !reflect.DeepEqual(fresh.Args, []string{"-m", "gpt-x"}) {
		t.Fatalf("fresh codex launch: %+v", fresh)
	}
	if nativeLaunch("opencode", nil) != nil {
		t.Fatal("OpenOptions cannot express opencode")
	}
}
