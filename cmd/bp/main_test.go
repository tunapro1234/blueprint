package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"blueprint/internal/book"
	bpconfig "blueprint/internal/config"
	"blueprint/internal/dashboard"
	bptmux "blueprint/internal/tmux"
	"blueprint/internal/worktree"
)

func TestFedCommandRejectsInvalidConfigFallback(t *testing.T) {
	a := &app{config: bpconfig.Config{InvalidConfig: "parse config.json: unexpected EOF"}}
	err := a.run([]string{"fed", "status"})
	if err == nil || !strings.Contains(err.Error(), "federation disabled because config.json is invalid") {
		t.Fatalf("error=%v", err)
	}
}

func TestDeliveryTallyClassifiesNonAgentAsSkip(t *testing.T) {
	var tally deliveryTally
	// A real send (no error, not queued) counts as delivered.
	if !tally.record("alpha", false, "", nil) {
		t.Fatal("nil-error send should count as a delivery")
	}
	// A queued send records its channel and counts as delivered.
	if !tally.record("beta", true, "q1", nil) {
		t.Fatal("queued send should count as a delivery")
	}
	// A non-agent target (even when wrapped) is a SKIP, not a delivery or error.
	if tally.record("gamma", false, "", fmt.Errorf("wrap: %w", bptmux.ErrNotAgent)) {
		t.Fatal("ErrNotAgent must not count as a delivery")
	}
	// A genuine failure is a hard error.
	if tally.record("delta", false, "", errors.New("boom")) {
		t.Fatal("a real error must not count as a delivery")
	}
	if tally.sent != 1 {
		t.Fatalf("sent=%d, want 1", tally.sent)
	}
	if len(tally.channels) != 1 || tally.channels[0] != "q1" {
		t.Fatalf("channels=%v, want [q1]", tally.channels)
	}
	if tally.skipped != 1 {
		t.Fatalf("skipped=%d, want 1", tally.skipped)
	}
	if len(tally.errs) != 1 || !strings.Contains(tally.errs[0].Error(), "delta") {
		t.Fatalf("errs=%v, want one error mentioning delta", tally.errs)
	}
}

func TestAnnouncementTargetsFollowHierarchy(t *testing.T) {
	fleet := book.Fleet{
		Root:  "server-main",
		Order: []string{"server-main", "alpha", "alpha-child", "alpha-grandchild", "beta", "orphan", "lab-scratch", "closed-agent"},
		Agents: map[string]book.Agent{
			"server-main":      {Name: "server-main"},
			"alpha":            {Name: "alpha"},
			"alpha-child":      {Name: "alpha-child"},
			"alpha-grandchild": {Name: "alpha-grandchild"},
			"beta":             {Name: "beta"},
			"orphan":           {Name: "orphan"},
			"lab-scratch":      {Name: "lab-scratch"},
			"closed-agent":     {Name: "closed-agent"},
		},
		Parents: map[string]string{
			"server-main":      "",
			"alpha":            "server-main",
			"alpha-child":      "alpha",
			"alpha-grandchild": "alpha-child",
			"beta":             "server-main",
			"orphan":           "",
			"lab-scratch":      "alpha",
			"closed-agent":     "alpha",
		},
	}
	states := map[string]book.State{}
	for _, name := range fleet.Order {
		states[name] = book.State{Alive: true}
	}
	delete(states, "closed-agent")

	if got, want := announcementTargets(fleet, states, "alpha"), []string{"alpha-child", "alpha-grandchild"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("alpha targets=%v, want %v", got, want)
	}
	if got, want := announcementTargets(fleet, states, "server-main"), []string{"alpha", "alpha-child", "alpha-grandchild", "beta", "orphan"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root targets=%v, want %v", got, want)
	}
}

func compactTestFleet() (book.Fleet, map[string]book.State) {
	fleet := book.Fleet{
		Root:  "server-main",
		Order: []string{"server-main", "alpha", "alpha-child", "alpha-grandchild", "beta", "orphan", "lab-scratch", "closed-agent"},
		Agents: map[string]book.Agent{
			"server-main":      {Name: "server-main"},
			"alpha":            {Name: "alpha"},
			"alpha-child":      {Name: "alpha-child"},
			"alpha-grandchild": {Name: "alpha-grandchild"},
			"beta":             {Name: "beta"},
			"orphan":           {Name: "orphan"},
			"lab-scratch":      {Name: "lab-scratch"},
			"closed-agent":     {Name: "closed-agent"},
		},
		Parents: map[string]string{
			"server-main":      "",
			"alpha":            "server-main",
			"alpha-child":      "alpha",
			"alpha-grandchild": "alpha-child",
			"beta":             "server-main",
			"orphan":           "",
			"lab-scratch":      "alpha",
			"closed-agent":     "alpha",
		},
	}
	states := map[string]book.State{}
	for _, name := range fleet.Order {
		states[name] = book.State{Alive: true}
	}
	delete(states, "closed-agent")
	return fleet, states
}

func TestSelectCompactTargetsHardExclusions(t *testing.T) {
	fleet, states := compactTestFleet()
	now := time.Now()

	// server-main sees the whole fleet but never itself.
	plan := selectCompactTargets(fleet, states, "server-main", nil, nil, 30*time.Minute, now)
	if want := []string{"alpha", "alpha-child", "alpha-grandchild", "beta", "orphan"}; !reflect.DeepEqual(plan.Send, want) {
		t.Fatalf("root send=%v, want %v", plan.Send, want)
	}
	if len(plan.SkippedRecent) != 0 || len(plan.Excluded) != 0 {
		t.Fatalf("expected no skips for root, got %+v", plan)
	}

	// A non-root sender only sees strict descendants (server-main is never a target here anyway).
	plan = selectCompactTargets(fleet, states, "alpha", nil, nil, 30*time.Minute, now)
	if want := []string{"alpha-child", "alpha-grandchild"}; !reflect.DeepEqual(plan.Send, want) {
		t.Fatalf("alpha send=%v, want %v", plan.Send, want)
	}
}

func TestSelectCompactTargetsUserExclude(t *testing.T) {
	fleet, states := compactTestFleet()
	now := time.Now()

	plan := selectCompactTargets(fleet, states, "server-main", []string{"beta", "does-not-exist"}, nil, 30*time.Minute, now)
	if want := []string{"alpha", "alpha-child", "alpha-grandchild", "orphan"}; !reflect.DeepEqual(plan.Send, want) {
		t.Fatalf("send=%v, want %v", plan.Send, want)
	}
	if want := []string{"beta"}; !reflect.DeepEqual(plan.Excluded, want) {
		t.Fatalf("excluded=%v, want %v", plan.Excluded, want)
	}
	if want := []string{"does-not-exist"}; !reflect.DeepEqual(plan.UnknownExcludes, want) {
		t.Fatalf("unknown=%v, want %v", plan.UnknownExcludes, want)
	}
}

func TestSelectCompactTargetsMinAge(t *testing.T) {
	fleet, states := compactTestFleet()
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	lastCompact := map[string]time.Time{
		"alpha":       now.Add(-5 * time.Minute),  // recent -> skipped
		"alpha-child": now.Add(-40 * time.Minute), // old enough -> sent
	}

	plan := selectCompactTargets(fleet, states, "server-main", nil, lastCompact, 30*time.Minute, now)
	if want := []string{"alpha-child", "alpha-grandchild", "beta", "orphan"}; !reflect.DeepEqual(plan.Send, want) {
		t.Fatalf("send=%v, want %v", plan.Send, want)
	}
	if len(plan.SkippedRecent) != 1 || plan.SkippedRecent[0].Name != "alpha" {
		t.Fatalf("skippedRecent=%+v, want only alpha", plan.SkippedRecent)
	}
	if got := formatAge(plan.SkippedRecent[0].Age); got != "5m" {
		t.Fatalf("age=%q, want 5m", got)
	}
}

func TestParseCompactArgs(t *testing.T) {
	opts, err := parseCompactArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.minAge != 30*time.Minute || opts.dryRun || len(opts.exclude) != 0 {
		t.Fatalf("defaults=%+v", opts)
	}

	opts, err = parseCompactArgs([]string{"--min-age=15", "--exclude", "a, b ,c", "--exclude=d", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.minAge != 15*time.Minute {
		t.Fatalf("minAge=%v, want 15m", opts.minAge)
	}
	if !opts.dryRun {
		t.Fatalf("expected dryRun")
	}
	if want := []string{"a", "b", "c", "d"}; !reflect.DeepEqual(opts.exclude, want) {
		t.Fatalf("exclude=%v, want %v", opts.exclude, want)
	}

	for _, args := range [][]string{{"--min-age"}, {"--min-age", "-1"}, {"--min-age", "x"}, {"--min-age", "1", "--min-age", "2"}, {"--exclude"}, {"--bogus"}} {
		if _, err := parseCompactArgs(args); err == nil {
			t.Errorf("parseCompactArgs(%v) succeeded, want error", args)
		}
	}
}

func TestLoadConnectConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("# laptop target\nREMOTE = ops@example.com\nREMOTE_METHOD=SSH\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadConnectConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := connectConfig{Remote: "ops@example.com", Method: "ssh"}
	if got != want {
		t.Fatalf("config=%+v, want %+v", got, want)
	}
}

func TestLoadConnectConfigDefaultsToMosh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("REMOTE=server.example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadConnectConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "mosh" {
		t.Fatalf("method=%q, want mosh", got.Method)
	}
}

func TestLoadConnectConfigRejectsInvalidMethod(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("REMOTE=server\nREMOTE_METHOD=telnet\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadConnectConfig(path)
	if err == nil || !strings.Contains(err.Error(), "mosh or ssh") {
		t.Fatalf("error=%v, want invalid method error", err)
	}
}

func TestLoadDashboardURL(t *testing.T) {
	t.Run("default when missing", func(t *testing.T) {
		got, err := loadDashboardURL(filepath.Join(t.TempDir(), "missing"))
		if err != nil {
			t.Fatal(err)
		}
		if got != dashboard.DefaultURL {
			t.Fatalf("URL=%q, want %q", got, dashboard.DefaultURL)
		}
	})

	t.Run("configured", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config")
		contents := "REMOTE=ops@example.com\nDASH_URL = 'https://dash.example.com/monitor'\n"
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := loadDashboardURL(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != "https://dash.example.com/monitor" {
			t.Fatalf("URL=%q, want configured URL", got)
		}
	})
}

func TestParseDashboardPort(t *testing.T) {
	for _, test := range []struct {
		args []string
		want int
	}{
		{want: dashboard.DefaultPort},
		{args: []string{"--port", "9000"}, want: 9000},
		{args: []string{"--port=4321"}, want: 4321},
	} {
		got, err := parseDashboardPort(test.args)
		if err != nil {
			t.Fatalf("parseDashboardPort(%v): %v", test.args, err)
		}
		if got != test.want {
			t.Fatalf("parseDashboardPort(%v)=%d, want %d", test.args, got, test.want)
		}
	}

	for _, args := range [][]string{{"--port"}, {"--port", "0"}, {"--port", "70000"}, {"--listen", "9000"}, {"--port", "1", "--port", "2"}} {
		if _, err := parseDashboardPort(args); err == nil {
			t.Errorf("parseDashboardPort(%v) succeeded, want error", args)
		}
	}
}

func TestSafeSessionName(t *testing.T) {
	for _, name := range []string{"server-main", "agent_2", "build.v3"} {
		if !safeSessionName.MatchString(name) {
			t.Errorf("expected %q to be safe", name)
		}
	}
	for _, name := range []string{"", "-server", "agent name", "agent;whoami"} {
		if safeSessionName.MatchString(name) {
			t.Errorf("expected %q to be rejected", name)
		}
	}
}

func TestRemoteAttachCommandUsesMoshByDefault(t *testing.T) {
	binDir := t.TempDir()
	writeTestExecutable(t, binDir, "mosh")
	writeTestExecutable(t, binDir, "ssh")
	t.Setenv("PATH", binDir)

	got, err := remoteAttachCommand(connectConfig{Remote: "ops@example.com", Method: "mosh"}, "server-main")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mosh", "ops@example.com", "--", "tmux", "attach", "-t", "server-main"}
	if !reflect.DeepEqual(got.Args, want) {
		t.Fatalf("args=%v, want %v", got.Args, want)
	}
}

func TestRemoteAttachCommandFallsBackToSSH(t *testing.T) {
	binDir := t.TempDir()
	writeTestExecutable(t, binDir, "ssh")
	t.Setenv("PATH", binDir)

	got, err := remoteAttachCommand(connectConfig{Remote: "ops@example.com", Method: "mosh"}, "server-main")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ssh", "-t", "ops@example.com", "tmux", "attach", "-t", "server-main"}
	if !reflect.DeepEqual(got.Args, want) {
		t.Fatalf("args=%v, want %v", got.Args, want)
	}
}

func writeTestExecutable(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestTranslatePolicyOutput(t *testing.T) {
	input := "usage: 7g %42, fable %10, 5s %8, reset 12.5s, E %25, fresh (1s)\noverride kaldirildi\n"
	want := "usage: 7d %42, fable %10, 5h %8, reset 12.5h, E %25, fresh (1s)\noverride cleared\n"
	if got := translatePolicyOutput(input); got != want {
		t.Fatalf("translated output=%q, want %q", got, want)
	}
}

func TestAgentsByWorktreeMatchesPaneAndSessionDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo", ".worktrees")
	entries := []worktree.Info{
		{Path: filepath.Join(root, "shop"), Branch: "shop/dev"},
		{Path: filepath.Join(root, "builder"), Branch: "builder/dev"},
	}
	locations := []bptmux.Location{
		{Session: "shop-agent", CurrentDir: filepath.Join(root, "shop", "cmd")},
		{Session: "builder-agent", CurrentDir: "/tmp", StartDir: filepath.Join(root, "builder")},
		{Session: "unrelated-agent", CurrentDir: filepath.Join(root, "shop-old")},
		{Session: "shop-agent", StartDir: filepath.Join(root, "shop")},
	}

	got := agentsByWorktree(entries, locations)
	if want := []string{"shop-agent"}; !reflect.DeepEqual(got[entries[0].Path], want) {
		t.Fatalf("shop agents=%v, want %v", got[entries[0].Path], want)
	}
	if want := []string{"builder-agent"}; !reflect.DeepEqual(got[entries[1].Path], want) {
		t.Fatalf("builder agents=%v, want %v", got[entries[1].Path], want)
	}
}
