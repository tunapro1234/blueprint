package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"blueprint/internal/book"
	bpconfig "blueprint/internal/config"
	bptmux "blueprint/internal/tmux"
)

func TestRunAdoptsClosedNativeThreadRecord(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	path := filepath.Join(t.TempDir(), "book.json")
	if err := os.WriteFile(path, []byte(`{"agents":[{"name":"named-agent","folder":"/repo","status":"opening","role":"writer","parent":"lead","color":"blue","nativeTitle":{"threadId":"thread-123"}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &app{ctx: context.Background(), config: bpconfig.Config{Agentbooks: []string{path}}}
	name, attach, err := a.adoptNativeThread("thread-123", "")
	if err != nil || name != "named-agent" || attach {
		t.Fatalf("adoption=(%q,%v,%v), want named closed record", name, attach, err)
	}
	fleet, err := book.LoadFleet([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	// Empty placement means "keep what is on record" to book.SetStatus.
	if parent, role := localPlacement(fleet, name, "", ""); parent != "" || role != "" {
		t.Fatalf("placement=%q/%q, want recorded lead/writer kept", parent, role)
	}
	if fleet.Agents[name].Color != "blue" {
		t.Fatal("adoption fixture lost stored color")
	}
}

func TestRunRefusesASecondLiveOpenOwnerOfNativeThread(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	path := filepath.Join(t.TempDir(), "book.json")
	if err := os.WriteFile(path, []byte(`{"agents":[{"name":"live-agent","status":"open","localRuntime":{"pid":`+strconv.Itoa(os.Getpid())+`},"nativeTitle":{"threadId":"thread-live"}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &app{ctx: context.Background(), config: bpconfig.Config{Agentbooks: []string{path}}}
	if _, _, err := a.adoptNativeThread("thread-live", ""); err == nil || !strings.Contains(err.Error(), "refusing to create a second session") {
		t.Fatalf("error=%v, want live-owner refusal", err)
	}
}

func TestNativeFallbackPreservesClaudeContinueCommand(t *testing.T) {
	var gotProgram string
	var gotArgs []string
	a := &app{err: testOutput(t), execNative: func(program string, argv, _ []string) error {
		gotProgram, gotArgs = program, append([]string(nil), argv...)
		return nil
	}}
	if err := a.nativeFallback("/usr/bin/claude", "claude", []string{"-c"}, "tmux name is occupied"); err != nil {
		t.Fatal(err)
	}
	if gotProgram != "/usr/bin/claude" || strings.Join(gotArgs, " ") != "/usr/bin/claude -c" {
		t.Fatalf("native exec=%s %v", gotProgram, gotArgs)
	}
	if got := readTestOutput(t, a.err); !strings.Contains(got, "Escape hatch: command claude -c") {
		t.Fatalf("fallback output=%q, want explicit escape hatch", got)
	}
}

func TestLocalRunFallsThroughToNativeWhenManagedLaunchIsBlocked(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTBOOK", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	binDir := t.TempDir()
	claude := filepath.Join(binDir, "claude")
	if err := os.WriteFile(claude, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	bookPath := filepath.Join(t.TempDir(), "agentbook.json")
	if err := os.WriteFile(bookPath, []byte(`{"agents":[{"name":"retired","status":"closed","archivedAt":"2026-09-01"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tmuxBin := filepath.Join(t.TempDir(), "tmux")
	if err := os.WriteFile(tmuxBin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var argv []string
	a := &app{
		ctx: context.Background(), config: bpconfig.Config{Agentbooks: []string{bookPath}, StateDir: t.TempDir()},
		tmux: &bptmux.Client{Bin: tmuxBin}, out: testOutput(t), err: testOutput(t), interactive: func() bool { return true },
		execNative: func(_ string, args, _ []string) error { argv = append([]string(nil), args...); return nil },
	}
	if err := a.localRun([]string{"--name", "retired", "claude", "-c"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(argv, " ") != claude+" -c" {
		t.Fatalf("native argv=%v, want original continue command", argv)
	}
	if output := readTestOutput(t, a.err); !strings.Contains(output, "Escape hatch: command claude -c") {
		t.Fatalf("fallback output=%q", output)
	}
}
