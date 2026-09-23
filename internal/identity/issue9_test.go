package identity

import (
	"context"
	"strings"
	"testing"
)

// Issue #9: a message sent from a Claude Code agent was labelled
// "zsh?:claude-e558-cwd". The detail came from the cwd-tracking temp file that
// Claude Code appends to every tool command it runs through `zsh -c`.
func TestIssue9ShellCommandStringIsNotTheSender(t *testing.T) {
	setEnv(t, map[string]string{"TMUX": "", "AGENT": "", "SUDO_USER": "", "USER": "", "LOGNAME": ""})
	chain := [][]string{
		{"/usr/bin/zsh", "-c", "source /root/.claude/shell-snapshots/snapshot-zsh-1.sh 2>/dev/null || true && eval 'bp msg writer-astra hello' < /dev/null && pwd -P >| /tmp/claude-e558-cwd"},
		{"claude", "--dangerously-skip-permissions"},
	}
	got := Resolve(context.Background(), &fakeSession{}, Options{Origin: noCodexOrigin, Infer: true, Ancestors: fixedAncestors(chain...)})
	if strings.Contains(got.Label, "claude-e558-cwd") || strings.Contains(got.Label, "/tmp") {
		t.Fatalf("label %q names a temp file from a shell command string, not the sender", got.Label)
	}
	if got.Certain {
		t.Fatalf("inferred identity is certain: %+v", got)
	}
}

// A shell running a script directly still names that script.
func TestIssue9ShellScriptPathStillNamed(t *testing.T) {
	setEnv(t, map[string]string{"TMUX": "", "AGENT": "", "SUDO_USER": "", "USER": "", "LOGNAME": ""})
	chain := [][]string{{"/bin/sh", "-c", "/srv/tools/inbox_watcher.py"}, {"/usr/sbin/cron", "-f"}}
	got := Resolve(context.Background(), &fakeSession{}, Options{Origin: noCodexOrigin, Infer: true, Ancestors: fixedAncestors(chain...)})
	if got.Label != "cron?:inbox_watcher.py" {
		t.Fatalf("label=%q, want cron?:inbox_watcher.py", got.Label)
	}
}
