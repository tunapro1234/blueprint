package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blueprint/internal/cache"
	bpconfig "blueprint/internal/config"
	"blueprint/internal/pending"
	bptmux "blueprint/internal/tmux"
)

func TestBarWidgetOrderFollowsConfig(t *testing.T) {
	stateDir := t.TempDir()
	if err := pending.Append(stateDir, "agent", pending.Entry{
		TS: time.Now().Unix(), From: "test-sender", Text: "waiting",
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

func TestBarCacheTemperatureRequiresRecordedTTL(t *testing.T) {
	bookPath := filepath.Join(t.TempDir(), "agentbook.json")
	writeBarTestFile(t, bookPath, `{"agents":[{"name":"agent","folder":"/test"}]}`)
	for _, tc := range []struct {
		ttl  time.Duration
		want string
	}{{time.Hour, "warm~ 36m"}, {5 * time.Minute, "cold~ 36m"}, {0, "age 36m"}} {
		a := &app{config: bpconfig.Config{Agentbooks: []string{bookPath}, Bar: bpconfig.BarConfig{Widgets: []string{"temp"}}},
			loadCache: func(map[string]string) map[string]cache.State {
				return map[string]cache.State{"agent": {Known: true, Age: 36 * time.Minute, CacheAge: 36 * time.Minute, CacheTTL: tc.ttl}}
			}}
		if line := a.barLine("agent"); !strings.Contains(line, tc.want) {
			t.Fatalf("TTL %v: %s", tc.ttl, line)
		}
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

func TestBarClaudeEffortUsesTranscriptWithActivity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeBarTestFile(t, filepath.Join(home, ".claude", "settings.json"),
		`{"model":"claude-opus-5","effortLevel":"high"}`)
	path := filepath.Join(home, "session.jsonl")
	writeBarTestFile(t, path, `{"type":"assistant","effort":"medium","message":{"model":"claude-fable-5-1"}}`+"\n")
	s := cache.ReadClaudePath(path)
	s.Runtime, s.Activity = "claude", &cache.Activity{State: "idle"}
	a := &app{}
	if got := a.barModel("agent", bptmux.PaneProcess{Command: "claude"}, home, func() cache.State { return s }); got != "fable med" {
		t.Fatalf("bar=%q", got)
	}
	// An old transcript without effort must not borrow the global high default.
	s.Effort = ""
	if got := a.barModel("agent", bptmux.PaneProcess{Command: "claude"}, home, func() cache.State { return s }); got != "fable" {
		t.Fatalf("unknown effort fabricated: %q", got)
	}
}

func TestBarRemainingContextDoesNotInventAWindow(t *testing.T) {
	bookPath := filepath.Join(t.TempDir(), "agentbook.json")
	writeBarTestFile(t, bookPath, `{"agents":[{"name":"agent","folder":"/test"}]}`)
	for _, tc := range []struct {
		window int
		known  bool
		want   string
	}{
		{200000, true, "170k boş"}, {0, true, "30k/?"}, {20000, true, "0 boş"}, {0, false, "ctx —"},
	} {
		a := &app{config: bpconfig.Config{Agentbooks: []string{bookPath}, Bar: bpconfig.BarConfig{Context: "remaining", Widgets: []string{"ctx"}}},
			loadCache: func(map[string]string) map[string]cache.State {
				return map[string]cache.State{"agent": {Runtime: "claude", Known: tc.known, CtxTokens: 30000, Window: tc.window}}
			}}
		if got := a.barLine("agent"); !strings.Contains(got, tc.want) {
			t.Fatalf("window=%d: %s", tc.window, got)
		}
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

// codexPane decides which reader the bar uses for an agent's context and model.
// Since the sandbox came off (2026-08-25) a Codex pane reports "node", which is
// also every build watcher on this machine, so the command may only nominate:
// the Codex screen has to confirm before the bar reads codex rollouts, and a
// node pane that is NOT Codex must fall through to the Claude reader rather
// than be labelled a codex agent.
func TestCodexPaneRequiresScreenForNode(t *testing.T) {
	codexScreen := "› Ask Codex to do anything\n  gpt-5.6-sol medium fast · /srv/probot/out-codex\n"
	otherScreen := "> vite v5 building for production...\n"
	for _, tc := range []struct {
		name    string
		command string
		screen  string
		want    bool
	}{
		{"sandboxed codex answers on its own name", "bwrap", otherScreen, true},
		{"plain codex answers on its own name", "codex", otherScreen, true},
		{"node with codex on screen", "node", codexScreen, true},
		{"node running something else", "node", otherScreen, false},
		{"claude is never codex", "claude", codexScreen, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "tmux")
			script := "#!/bin/sh\ncat <<'SCREEN'\n" + tc.screen + "SCREEN\n"
			if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			a := &app{ctx: context.Background(), tmux: &bptmux.Client{Bin: path, Sleep: func(time.Duration) {}, Now: time.Now}}
			if got := a.codexPane("agent", bptmux.PaneProcess{Command: tc.command, PID: 1}); got != tc.want {
				t.Fatalf("codexPane(%q)=%v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

// The launch line is the live truth for a codex pane's model and effort:
// `-c model_reasoning_effort=medium` changes what the pane runs and writes
// nothing to config.toml. Measured 2026-08-25 on both codex agents, whose
// config.toml said xhigh — an effort the fleet has banned — while their panes
// ran medium.
func TestCodexArgsModelReadsLaunchOverrides(t *testing.T) {
	model, effort := codexArgsModel([]string{
		"codex", "--search", "-c", "model_reasoning_effort=medium", "-c", "service_tier=fast", "-c", "sandbox_mode=danger-full-access",
	})
	if model != "" || effort != "medium" {
		t.Fatalf("model=%q effort=%q, want empty model and medium effort", model, effort)
	}
	if model, effort := codexArgsModel([]string{"codex", "-m", "gpt-5.6-luna"}); model != "gpt-5.6-luna" || effort != "" {
		t.Fatalf("model=%q effort=%q, want the -m model", model, effort)
	}
	if model, effort := codexArgsModel([]string{"codex", "-c", `model="gpt-5.6-terra"`, "-c", "broken"}); model != "gpt-5.6-terra" || effort != "" {
		t.Fatalf("model=%q effort=%q, want the quoted -c model", model, effort)
	}
	if model, effort := codexArgsModel([]string{"codex", "--search"}); model != "" || effort != "" {
		t.Fatalf("model=%q effort=%q, want nothing when the launch line says nothing", model, effort)
	}
}

func TestBarQuotaUsesObservedHarnessAndNeverDefaultsToClaude(t *testing.T) {
	dir := t.TempDir()
	bookPath := filepath.Join(dir, "agentbook.json")
	if err := os.WriteFile(bookPath, []byte(`{"agents":[{"name":"probot-studio-astra","folder":"/work"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	usage := filepath.Join(dir, "usage.jsonl")
	if err := os.WriteFile(usage, []byte(`{"ts":"2026-09-05T12:00:00Z","claude_5h":17,"claude_7d":83,"codex_5h":21,"codex_7d":42}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, harness := range []string{"codex", "codex-remote", "claude", ""} {
		a := &app{config: bpconfig.Config{Agentbooks: []string{bookPath}, UsageHistory: usage}, loadCache: func(map[string]string) map[string]cache.State {
			return map[string]cache.State{"probot-studio-astra": {Runtime: harness, Activity: &cache.Activity{State: "unknown"}}}
		}}
		a.config.Bar.Widgets = []string{"quota"}
		line := a.barLine("probot-studio-astra")
		if strings.HasPrefix(harness, "codex") && (!strings.Contains(line, "gpt 42%/21%") || strings.Contains(line, "cc ")) {
			t.Fatalf("wrong provider: %s", line)
		}
		if harness == "claude" && !strings.Contains(line, "cc 17%/83%") {
			t.Fatal(line)
		}
		if harness == "" && (strings.Contains(line, "cc ") || strings.Contains(line, "gpt ") || strings.Contains(line, "%")) {
			t.Fatalf("invented provider: %s", line)
		}
	}
}
