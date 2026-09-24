package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bptmux "blueprint/internal/tmux"
)

// launchLogTmux records every tmux invocation's full argv (one per line).
func launchLogTmux(t *testing.T, exists bool, pane string) (*bptmux.Client, func() string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tmux")
	log := filepath.Join(dir, "calls")
	session := filepath.Join(dir, "session")
	if exists {
		if err := os.WriteFile(session, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\ncase \"$1\" in\n" +
		"has-session) if [ -f " + session + " ]; then exit 0; fi; exit 1 ;;\n" +
		"new-session) touch " + session + " ;;\n" +
		"capture-pane) printf '" + pane + "\\n' ;;\n" +
		"list-panes) printf '1\\tclaude\\t4242\\n' ;;\n" +
		"display-message) echo 0 ;;\n" +
		"*) : ;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &bptmux.Client{Bin: path, Sleep: func(time.Duration) {}, Now: time.Now}, func() string {
		data, _ := os.ReadFile(log)
		return string(data)
	}
}

// Issue #10: bp open typed a bare `claude --dangerously-skip-permissions` into
// the user's interactive shell — alias flags doubled up and no run settings or
// _local-worker observed the pane, so the record stayed "opening".
func TestIssue10_ClaudeOpenUsesManagedLauncher(t *testing.T) {
	dir := t.TempDir()
	bookPath := openTestBook(t, dir, "closed")
	client, calls := launchLogTmux(t, false, "bypass permissions")
	a := openTestApp(t, bookPath, client)
	if err := a.open([]string{"ghost", dir, "--claude", "--no-prompt"}); err != nil {
		t.Fatal(err)
	}
	var launch string
	for _, line := range strings.Split(calls(), "\n") {
		if strings.HasPrefix(line, "send-keys") && strings.Contains(line, "claude") {
			launch = line
		}
	}
	if !strings.Contains(launch, "exec env ") || !strings.Contains(launch, "_open-session 'ghost' claude") {
		t.Fatalf("Claude launch is not managed (alias-expanded, unobserved): %q", launch)
	}
	if !strings.Contains(launch, "'CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=ghost claude --dangerously-skip-permissions'") || strings.Contains(launch, "'CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=ghost command claude") {
		t.Fatalf("managed launcher command was changed into an env argument named command: %q", launch)
	}
}

// Issue #13: bp open left the tmux global default bar; the bp bar (bp name /
// bp bar) was applied only by bp run. Reusing an existing agent session must
// apply it too.
func TestIssue13_OpenAppliesBarToExistingSession(t *testing.T) {
	dir := t.TempDir()
	bookPath := openTestBook(t, dir, "closed")
	client, calls := launchLogTmux(t, true, "bypass permissions")
	a := openTestApp(t, bookPath, client)
	if err := a.open([]string{"ghost", dir, "--claude", "--no-prompt"}); err != nil {
		t.Fatal(err)
	}
	log := calls()
	for _, option := range []string{"status-left", "status-right", "status-style", "status-interval"} {
		if !strings.Contains(log, "set-option -t =ghost: "+option) {
			t.Fatalf("bp open did not set %s on the session:\n%s", option, log)
		}
	}
	if !strings.Contains(log, " name 'ghost')") || !strings.Contains(log, " bar 'ghost')") {
		t.Fatalf("status bar does not run bp name/bp bar:\n%s", log)
	}
}
