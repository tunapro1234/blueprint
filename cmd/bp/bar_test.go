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

// The bar used to name whatever the last reply ran. A /model stayed invisible
// until the next reply, a restarted agent kept its old model, and an agent that
// had not answered yet showed none.
func TestClaudeBarModelFollowsTheLiveSession(t *testing.T) {
	started := time.Date(2026, 11, 9, 20, 0, 0, 0, time.UTC)
	before, after := started.Add(-time.Hour), started.Add(time.Minute)
	ran := func(at time.Time) cache.State {
		return cache.State{Model: "claude-opus-5-5", Effort: "medium", ModelAt: at}
	}
	picked := func(s cache.State, model, effort string) cache.State {
		s.PickedModel, s.PickedEffort = model, effort
		return s
	}
	for _, tc := range []struct {
		name        string
		live        cache.State
		launch      claudeLaunch
		pin, global string
		want        string
	}{
		{"reply-from-this-process", ran(after), claudeLaunch{model: "claude-haiku-5-5", effort: "low", started: started},
			`{"model":"sonnet","effortLevel":"high"}`, `{"model":"fable","effortLevel":"high"}`, "opus med"},
		{"model-and-effort-picked", picked(ran(after), "Fable 5.2", "high"), claudeLaunch{started: started}, "", "", "fable high"},
		{"model-picked-keeps-effort", picked(ran(after), "Fable 5.2", ""), claudeLaunch{started: started}, "", "", "fable med"},
		{"effort-picked", picked(ran(after), "", "high"), claudeLaunch{started: started}, "", "", "opus high"},
		{"restarted-with-flags", ran(before), claudeLaunch{model: "claude-haiku-5-5", effort: "medium", started: started},
			`{"model":"sonnet","effortLevel":"high"}`, "", "haiku med"},
		{"restarted-onto-pin", ran(before), claudeLaunch{started: started},
			`{"model":"fable[1m]","effortLevel":"high"}`, `{"model":"sonnet","effortLevel":"low"}`, "fable high"},
		{"restarted-fills-each-field", ran(before), claudeLaunch{model: "claude-fable-5-2", started: started},
			`{"permissions":{}}`, `{"model":"sonnet","effortLevel":"low"}`, "fable low"},
		{"restarted-with-nothing-configured", ran(before), claudeLaunch{started: started}, "", "", "opus med"},
		{"pick-from-the-old-process", picked(ran(before), "Fable 5.2", "high"), claudeLaunch{model: "claude-opus-5-5", effort: "low", started: started},
			"", "", "opus low"},
		{"no-request-yet", cache.State{}, claudeLaunch{started: started}, "", `{"model":"fable","effortLevel":"high"}`, "fable high"},
		{"no-request-yet-effort-picked", cache.State{PickedEffort: "low", ModelAt: after}, claudeLaunch{started: started},
			"", `{"model":"fable","effortLevel":"high"}`, "fable low"},
		{"process-unknown-trusts-the-record", ran(before), claudeLaunch{}, `{"model":"sonnet","effortLevel":"high"}`, "", "opus med"},
		{"nothing-known", cache.State{}, claudeLaunch{}, "", "", ""},
	} {
		for _, cached := range []bool{false, true} {
			t.Run(tc.name, func(t *testing.T) {
				folder, home := t.TempDir(), t.TempDir()
				t.Setenv("HOME", home)
				if tc.pin != "" {
					writeBarTestFile(t, filepath.Join(folder, ".claude", "settings.local.json"), tc.pin)
				}
				if tc.global != "" {
					writeBarTestFile(t, filepath.Join(home, ".claude", "settings.json"), tc.global)
				}
				var models *barModelCache
				if cached {
					models = newBarModelCache()
				}
				if got := claudeBarModel(tc.live, tc.launch, folder, models); got != tc.want {
					t.Fatalf("cached=%v label=%q, want %q", cached, got, tc.want)
				}
			})
		}
	}
}

func TestClaudeArgsModel(t *testing.T) {
	for _, tc := range []struct {
		argv          []string
		model, effort string
	}{
		{[]string{"claude", "--model", "claude-haiku-5-5", "--effort", "medium", "--name", "a", "--resume", "id"}, "claude-haiku-5-5", "medium"},
		{[]string{"claude", "--effort=high", "--model=opus[1m]"}, "opus[1m]", "high"},
		{[]string{"claude", "--dangerously-skip-permissions", "--name", "server-main"}, "", ""},
		{[]string{"claude", "--model"}, "", ""},
	} {
		if model, effort := claudeArgsModel(tc.argv); model != tc.model || effort != tc.effort {
			t.Fatalf("%v: %q/%q", tc.argv, model, effort)
		}
	}
}

// A live pane: the label follows this very test process's launch line.
func TestClaudeLaunchOfReadsProc(t *testing.T) {
	if _, err := os.Stat("/proc/self/comm"); err != nil {
		t.Skip("no /proc")
	}
	if launch := claudeLaunchOf(os.Getpid()); !launch.started.IsZero() || launch.model != "" {
		t.Fatalf("a non-Claude process was taken for one: %+v", launch)
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
		{200000, true, "170k left"}, {0, true, "30k/?"}, {20000, true, "0 left"}, {0, false, "ctx —"},
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

// A Claude Team seat has no weekly window: its 5h figure becomes the headline,
// as for a Codex plan without a secondary window.
func TestBarQuotaShowsClaudeFiveHourWithoutWeeklyWindow(t *testing.T) {
	usage := filepath.Join(t.TempDir(), "usage.jsonl")
	if err := os.WriteFile(usage, []byte(`{"ts":"2026-10-07T10:59:51Z","claude_5h":2,"claude_7d":null,"codex_5h":21,"codex_7d":42}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a := &app{config: bpconfig.Config{UsageHistory: usage}}
	if got := a.barQuotaFor(false); !strings.Contains(got, "cc 2%") || strings.Contains(got, "/") {
		t.Fatalf("claude quota = %q, want cc 2%% alone", got)
	}
}
