package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"blueprint/internal/book"
	"blueprint/internal/dashboard"
	bptmux "blueprint/internal/tmux"
	"blueprint/internal/worktree"
)

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
