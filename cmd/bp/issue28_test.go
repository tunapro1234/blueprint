package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blueprint/internal/book"
	bpconfig "blueprint/internal/config"
	bptmux "blueprint/internal/tmux"
)

func TestIssue28_UnboundClaudeSessionSkipsKnownOtherLaunch(t *testing.T) {
	const other = "33333333-3333-3333-3333-333333333333"
	guardWithUnboundClaude(t, unboundClaudeFixture{launchThread: other, statusAt: time.Now().Add(-10 * time.Second)}, "ordinary Claude startup", func(t *testing.T, a *app, cwd string) {
		guard, existing, err := a.guardClaudeResume("44444444-4444-4444-4444-444444444444", "requested", cwd)
		if err != nil || existing != "" || guard == nil || guard.name != "requested" {
			t.Fatalf("guard=%+v existing=%q err=%v; different explicit resume should not block", guard, existing, err)
		}
		guard.close()
	})
}

func TestIssue28_UnboundClaudeTrustPromptDoesNotBlockResume(t *testing.T) {
	guardWithUnboundClaude(t, unboundClaudeFixture{statusAt: time.Now().Add(-10 * time.Second)}, "Do you trust this folder? Press y to continue", func(t *testing.T, a *app, cwd string) {
		guard, existing, err := a.guardClaudeResume("55555555-5555-5555-5555-555555555555", "requested", cwd)
		if err != nil || existing != "" || guard == nil || guard.name != "requested" {
			t.Fatalf("guard=%+v existing=%q err=%v; trust prompt should not block", guard, existing, err)
		}
		guard.close()
	})
}

func TestIssue28_OldUnboundClaudeSessionExpiresAsBlocker(t *testing.T) {
	guardWithUnboundClaude(t, unboundClaudeFixture{statusAt: time.Now().Add(-claudeUnboundTimeout - time.Second)}, "Claude is starting", func(t *testing.T, a *app, cwd string) {
		guard, existing, err := a.guardClaudeResume("66666666-6666-6666-6666-666666666666", "requested", cwd)
		if err != nil || existing != "" || guard == nil || guard.name != "requested" {
			t.Fatalf("guard=%+v existing=%q err=%v; stale unbound session should expire", guard, existing, err)
		}
		guard.close()
	})
}

func TestIssue28_OldUnboundClaudeSessionWithoutStatusAtExpires(t *testing.T) {
	guardWithUnboundClaude(t, unboundClaudeFixture{unboundSince: time.Now().Add(-claudeUnboundTimeout - time.Second)}, "Claude is starting", func(t *testing.T, a *app, cwd string) {
		guard, existing, err := a.guardClaudeResume("99999999-9999-9999-9999-999999999999", "requested", cwd)
		if err != nil || existing != "" || guard == nil || guard.name != "requested" {
			t.Fatalf("guard=%+v existing=%q err=%v; persisted unbound age should expire", guard, existing, err)
		}
		guard.close()
	})
}

func TestIssue28_RecentUnboundClaudeErrorNamesReasonDurationAndActions(t *testing.T) {
	const thread = "77777777-7777-7777-7777-777777777777"
	guardWithUnboundClaude(t, unboundClaudeFixture{launchThread: thread, statusAt: time.Now().Add(-17 * time.Second)}, "Claude is starting", func(t *testing.T, a *app, cwd string) {
		_, _, err := a.guardClaudeResume(thread, "requested", cwd)
		if err == nil {
			t.Fatal("matching recent unbound launch was allowed")
		}
		for _, want := range []string{"unbound-agent", "explicitly names this thread", "unbound for ", "bp attach unbound-agent", "bp close unbound-agent"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not contain %q", err, want)
			}
		}
	})
}

type unboundClaudeFixture struct {
	launchThread string
	statusAt     time.Time
	unboundSince time.Time
}

func guardWithUnboundClaude(t *testing.T, fixture unboundClaudeFixture, pane string, check func(*testing.T, *app, string)) {
	t.Helper()
	client, _ := privateResumeTmux(t, pane)
	dir, home, state := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	path := filepath.Join(dir, "agentbook.json")
	obsPath := filepath.Join(state, "local", "unbound", "observation.json")
	local := map[string]any{"path": obsPath, "pid": os.Getpid(), "harness": "claude", "cwd": dir}
	row := map[string]any{"name": "unbound-agent", "folder": dir, "status": "open", "localRuntime": local}
	if !fixture.statusAt.IsZero() {
		row["statusAt"] = fixture.statusAt.UTC().Format(time.RFC3339Nano)
	}
	if fixture.launchThread != "" {
		row["launch"] = map[string]any{"resumeId": fixture.launchThread}
	}
	issueWriteBook(t, path, []map[string]any{row})
	if !fixture.unboundSince.IsZero() {
		root := filepath.Join(state, "local-resume")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		marker, err := json.Marshal(struct {
			Since time.Time `json:"since"`
		}{fixture.unboundSince.UTC()})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(unboundMarker(root, "unbound-agent"), marker, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a := issueApp(t, path, state, client)
	check(t, a, dir)
}

func privateResumeTmux(t *testing.T, pane string) (*bptmux.Client, string) {
	t.Helper()
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	dir := t.TempDir()
	socket := filepath.Join(dir, "socket")
	privateDir := filepath.Join(dir, "tmux-tmp")
	if err := os.MkdirAll(privateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_TMPDIR", privateDir)
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	base := func(args ...string) *exec.Cmd {
		cmd := exec.Command(tmuxPath, append([]string{"-f", "/dev/null", "-S", socket}, args...)...)
		cmd.Env = withoutIssueTmux(os.Environ())
		cmd.Env = append(cmd.Env, "TMUX_TMPDIR="+privateDir)
		return cmd
	}
	command := "printf '%s\\n' " + quoteShell(pane) + "; exec sleep 60"
	if output, err := base("new-session", "-d", "-s", "unbound-agent", command).CombinedOutput(); err != nil {
		t.Fatalf("start private tmux: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = base("kill-server").Run() })
	wrapper := filepath.Join(dir, "tmux-isolated")
	script := "#!/bin/sh\nexec " + quoteShell(tmuxPath) + " -f /dev/null -S " + quoteShell(socket) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	client := &bptmux.Client{Bin: wrapper}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		text, err := client.Capture(context.Background(), "unbound-agent")
		if err == nil && strings.Contains(text, pane) {
			return client, socket
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("private pane never displayed %q", pane)
	return nil, socket
}

func withoutIssueTmux(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, value := range environment {
		if strings.HasPrefix(value, "TMUX=") || strings.HasPrefix(value, "TMUX_PANE=") || strings.HasPrefix(value, "TMUX_TMPDIR=") {
			continue
		}
		result = append(result, value)
	}
	return result
}

func TestIssue28_FailedLauncherErrorIsRetainedForDoctor(t *testing.T) {
	state := t.TempDir()
	exitDir := filepath.Join(state, "local", "run-test")
	if err := os.MkdirAll(exitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(exitDir, "exit.json")
	t.Setenv("BP_EXIT_REPORT", reportPath)
	err := context.DeadlineExceeded
	retainSessionLaunchFailure([]string{"_session", "failed-agent", "claude"}, err)
	data, readErr := os.ReadFile(reportPath)
	var report localExitReport
	if readErr != nil || json.Unmarshal(data, &report) != nil || report.Agent != "failed-agent" || report.Status != "1" || !strings.Contains(report.Screen, err.Error()) {
		t.Fatalf("retained report=%+v raw=%s readErr=%v", report, data, readErr)
	}
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	tmuxDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmuxDir, "tmux"), []byte("#!/bin/sh\ncase \"$1\" in list-sessions) exit 0 ;; has-session) exit 1 ;; esac\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmuxDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	fleet := book.Fleet{Agents: map[string]book.Agent{"failed-agent": {Name: "failed-agent"}}}
	checks := doctorRuntimeChecks(bpconfig.Config{StateDir: state}, fleet, "failed-agent")
	found := false
	for _, check := range checks {
		found = found || check.Name == "native_exit/failed-agent" && strings.Contains(check.Next, reportPath)
	}
	if !found {
		t.Fatalf("doctor did not surface detached launcher failure: %+v", checks)
	}
}
