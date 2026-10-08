package main

import (
	"reflect"
	"strings"
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
	opencode := nativeLaunch("opencode", []string{"--model", "example/model", "run", "fix the bug"})
	if opencode == nil || !opencode.OpenCode || opencode.Codex || opencode.Resume ||
		!reflect.DeepEqual(opencode.Args, []string{"--model", "example/model"}) {
		t.Fatalf("opencode launch: %+v", opencode)
	}
}

// bp owns the Claude session title: a recorded --name (with its value) would
// replay a stale title, or the value would turn into a bare argument.
func TestNativeLaunchDropsClaudeNameWithItsValue(t *testing.T) {
	for _, args := range [][]string{
		{"--name", "worker-a", "--model", "opus"},
		{"-n", "worker-a", "--model", "opus"},
		{"--name=worker-a", "--model", "opus"},
	} {
		if got := nativeLaunch("claude", args); !reflect.DeepEqual(got.Args, []string{"--model", "opus"}) {
			t.Fatalf("%q recorded %q", args, got.Args)
		}
	}
}

func TestClaudeKeepsTitle(t *testing.T) {
	for args, want := range map[string]bool{
		"--model opus":       false,
		"--name x":           true,
		"-n x":               true,
		"--name=x":           true,
		"--resume":           true,
		"-r id":              true,
		"-c":                 true,
		"--continue":         true,
		"--fork-session":     true,
		"--model opus -- -n": false,
	} {
		if got := claudeKeepsTitle(strings.Fields(args)); got != want {
			t.Fatalf("claudeKeepsTitle(%q) = %v", args, got)
		}
	}
}
