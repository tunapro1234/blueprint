package main

import (
	"bytes"
	"strings"
	"testing"

	"blueprint/internal/audit"
	bpconfig "blueprint/internal/config"
	"blueprint/internal/guard"
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
