package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blueprint/internal/audit"
	bpconfig "blueprint/internal/config"
	"blueprint/internal/guard"
	"blueprint/internal/modules"
)

func TestGuardHookArgs(t *testing.T) {
	cfg, err := parseGuardHookArgs([]string{"hook", "claude", "--ask", "--canary", "/tmp/c1", "--canary", "~/c2"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != guard.HookAsk || len(cfg.Canaries) != 2 || cfg.Window != guard.DefaultTaintWindow {
		t.Fatalf("cfg = %+v", cfg)
	}
	for _, bad := range [][]string{nil, {"hook"}, {"hook", "codex"}, {"hook", "claude", "--canary"}, {"hook", "claude", "--deny"}} {
		if _, err := parseGuardHookArgs(bad); err == nil {
			t.Errorf("%v: want usage error", bad)
		}
	}
}

// The hook never fails the tool call: bad input or arguments print to
// stderr only, and a miss prints nothing at all.
func TestGuardHookFailsQuietly(t *testing.T) {
	for _, c := range []struct {
		args  []string
		stdin string
	}{
		{[]string{"hook", "claude"}, "not json"},
		{[]string{"hook", "nope"}, `{}`},
		{[]string{"hook", "claude"}, `{"tool_name":"Read","tool_input":{"file_path":"/src/main.go"}}`},
	} {
		var out, errOut bytes.Buffer
		guardHookMain(c.args, strings.NewReader(c.stdin), &out, &errOut)
		if out.Len() != 0 {
			t.Errorf("%v %q: stdout %q, want empty", c.args, c.stdin, out.String())
		}
	}
}

// A canary hit writes one guard.reach.canary alert to the audit log of the
// installation in BP_HOME, and prints nothing in observe mode.
func TestGuardHookCanaryWritesAlert(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BP_HOME", home)
	config, err := bpconfig.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(config.StateDir, home) {
		t.Fatalf("state dir %q is outside the test home %q; refusing to touch it", config.StateDir, home)
	}
	var out, errOut bytes.Buffer
	stdin := `{"tool_name":"Read","tool_input":{"file_path":"/tmp/bp-canary-test/token.txt"}}`
	guardHookMain([]string{"hook", "claude", "--canary", "/tmp/bp-canary-test/token.txt"}, strings.NewReader(stdin), &out, &errOut)
	if out.Len() != 0 {
		t.Fatalf("observe mode printed %q", out.String())
	}
	events, err := audit.Read(config.StateDir, audit.Filter{Kind: "guard.reach.canary"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Severity != audit.Alert || !strings.Contains(events[0].Reason, "token.txt") {
		t.Fatalf("events = %+v (stderr %q)", events, errOut.String())
	}
}

// The guard-hooks module adds exactly one PreToolUse group to bp's per-agent
// settings layer, carrying mode and canaries on the command line; with the
// module off the layer has no PreToolUse group at all.
func TestLocalObservationGuardHookFollowsModule(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	layer := func(cfg bpconfig.Config) map[string][]struct {
		Matcher string
		Hooks   []struct{ Command string }
	} {
		t.Helper()
		a := &app{config: cfg}
		args, _, err := a.prepareLocalObservation("claude", nil)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(args[1])
		if err != nil {
			t.Fatal(err)
		}
		var settings struct {
			Hooks map[string][]struct {
				Matcher string
				Hooks   []struct{ Command string }
			}
		}
		if err := json.Unmarshal(data, &settings); err != nil {
			t.Fatal(err)
		}
		return settings.Hooks
	}
	base := bpconfig.Config{LocalObservation: true, StateDir: filepath.Join(home, "state"), ModulesSet: true, Modules: map[string]bool{}}
	if hooks := layer(base); len(hooks["PreToolUse"]) != 0 {
		t.Fatalf("module off but PreToolUse = %+v", hooks["PreToolUse"])
	}
	on := base
	on.Modules = map[string]bool{modules.GuardHooks: true}
	on.GuardHooks = &bpconfig.GuardHooksConfig{Mode: "ask", Canaries: []string{"~/canary/it's.txt"}}
	groups := layer(on)["PreToolUse"]
	if len(groups) != 1 || groups[0].Matcher != guardHookMatcher || len(groups[0].Hooks) != 1 {
		t.Fatalf("PreToolUse = %+v", groups)
	}
	command := groups[0].Hooks[0].Command
	if !strings.Contains(command, " guard hook claude --ask --canary '~/canary/it'\\''s.txt'") {
		t.Fatalf("command = %q", command)
	}
	// The command line parses back to the same configuration.
	words := strings.Fields(strings.SplitN(command, " guard ", 2)[1])
	cfg, err := parseGuardHookArgs(append(words[:3], "--canary", "~/canary/it's.txt"))
	if err != nil || cfg.Mode != guard.HookAsk || len(cfg.Canaries) != 1 || cfg.Canaries[0] != "~/canary/it's.txt" {
		t.Fatalf("parsed %+v, %v", cfg, err)
	}
	on.GuardHooks = nil
	if command := layer(on)["PreToolUse"][0].Hooks[0].Command; !strings.HasSuffix(command, " guard hook claude") {
		t.Fatalf("default command = %q", command)
	}
}

func TestGuardStatusStatesCoverageCaveat(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, "state")
	now := time.Now()
	var out bytes.Buffer
	off := bpconfig.Config{StateDir: state, ModulesSet: true, Modules: map[string]bool{}}
	guardStatus(off, home, now, &out)
	if text := out.String(); !strings.HasPrefix(text, "guard-hooks: off") || !strings.Contains(text, "mode: observe") || !strings.Contains(text, "canaries: none") {
		t.Fatalf("off status:\n%s", text)
	}
	if err := audit.Append(state, audit.Event{Kind: "guard.reach.canary", Severity: audit.Alert}); err != nil {
		t.Fatal(err)
	}
	on := off
	on.Modules = map[string]bool{modules.GuardHooks: true}
	on.GuardHooks = &bpconfig.GuardHooksConfig{Mode: "ask", Canaries: []string{"~/canary.txt"}}
	out.Reset()
	guardStatus(on, home, now.Add(time.Second), &out)
	text := out.String()
	for _, want := range []string{"guard-hooks: on", "mode: ask", "canaries: ~/canary.txt",
		"only Claude Code agents opened or restarted", "already running keep their old settings", "Codex, Hermes or OpenCode agents are not watched",
		"not working: sessions is off", "not working: localObservation is off", "alerts in the last 24h: 1"} {
		if !strings.Contains(text, want) {
			t.Errorf("status lacks %q:\n%s", want, text)
		}
	}
}
