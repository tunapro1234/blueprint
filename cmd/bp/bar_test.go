package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bpconfig "blueprint/internal/config"
	"blueprint/internal/pending"
)

func TestBarWidgetOrderFollowsConfig(t *testing.T) {
	stateDir := t.TempDir()
	if err := pending.Append(stateDir, "agent", pending.Entry{
		TS: time.Now().Unix(), Text: "waiting",
	}); err != nil {
		t.Fatal(err)
	}
	a := &app{
		ctx: context.Background(),
		config: bpconfig.Config{
			StateDir: stateDir,
			Bar:      bpconfig.BarConfig{Widgets: []string{"clock", "queue"}},
		},
	}
	line := a.barLine("agent")
	clock := strings.Index(line, ":")
	queue := strings.Index(line, "queue 1")
	if clock < 0 || queue < 0 || clock > queue {
		t.Fatalf("widgets did not render in configured order: %q", line)
	}
}

// The queue widget must read the real spool layout, state/pending/<agent>.jsonl.
// It used to be handed StateDir+"/pending", which pending then extended again,
// so the bar looked in state/pending/pending/ and the widget never appeared
// however many messages were waiting. Writing the file at the true path here
// makes the widget vanish again if that doubling comes back. Reading it must
// also leave the spool alone: a status bar has nowhere to report a drop count.
func TestBarQueueWidgetReadsSpoolLayoutWithoutPruning(t *testing.T) {
	stateDir := t.TempDir()
	spool := filepath.Join(stateDir, "pending", "agent.jsonl")
	now := time.Now().Unix()
	writeBarTestFile(t, spool, fmt.Sprintf(
		"{\"ts\":%d,\"from\":\"ada\",\"kind\":\"msg\",\"text\":\"one\"}\n"+
			"{\"ts\":%d,\"from\":\"ada\",\"kind\":\"msg\",\"text\":\"two\"}\n", now-60, now))
	before, err := os.Stat(spool)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{
		ctx: context.Background(),
		config: bpconfig.Config{
			StateDir: stateDir,
			Bar:      bpconfig.BarConfig{Widgets: []string{"queue"}},
		},
	}
	if line := a.barLine("agent"); !strings.Contains(line, "queue 2") {
		t.Fatalf("queue widget missing for a real spool: %q", line)
	}
	after, err := os.Stat(spool)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("bar rewrote the spool: %d/%v -> %d/%v",
			before.Size(), before.ModTime(), after.Size(), after.ModTime())
	}
}

func TestBarUnknownWidgetIsSkipped(t *testing.T) {
	a := &app{config: bpconfig.Config{
		StateDir: t.TempDir(),
		Bar:      bpconfig.BarConfig{Widgets: []string{"not-a-widget"}},
	}}
	gap := "#[bg=" + barGap + "] "
	if got, want := a.barLine("agent"), gap+gap+"#[default]"; got != want {
		t.Fatalf("unknown widget output=%q, want %q", got, want)
	}
}

func TestReadClaudeModelPrefersLocalSettings(t *testing.T) {
	folder := t.TempDir()
	home := t.TempDir()
	writeBarTestFile(t, filepath.Join(folder, ".claude", "settings.local.json"),
		`{"model":"claude-opus-4-1","effortLevel":"medium"}`)
	writeBarTestFile(t, filepath.Join(home, ".claude", "settings.json"),
		`{"model":"claude-sonnet-4","effortLevel":"low"}`)

	model, effort := readClaudeModel(folder, home)
	if model != "claude-opus-4-1" || effort != "medium" {
		t.Fatalf("model/effort=%q/%q", model, effort)
	}
	if got := modelLabel(model, effort); got != "opus med" {
		t.Fatalf("model label=%q, want %q", got, "opus med")
	}
}

func TestReadCodexModelTopLevelLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeBarTestFile(t, path, `# active model
model = "gpt-5.6-sol"
model_reasoning_effort = "high"

[profiles.other]
model = "gpt-5.6-terra"
model_reasoning_effort = "low"
`)
	model, effort := readCodexModel(path)
	if model != "gpt-5.6-sol" || effort != "high" {
		t.Fatalf("model/effort=%q/%q", model, effort)
	}
	if got := modelLabel(model, effort); got != "sol high" {
		t.Fatalf("model label=%q, want %q", got, "sol high")
	}
}

func writeBarTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
