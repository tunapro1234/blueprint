package tmux

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestScrubClaudeSessionEnvKeepsConfiguration(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"CLAUDE_CODE_CHILD_SESSION=1",
		"CLAUDE_CONFIG_DIR=/profiles/a",
		"CLAUDE_CODE_MESSAGING_TOKEN=secret",
		"CLAUDE_CODE_MAX_WEB_SEARCHES_PER_SESSION=5",
		"CLAUDECODE=1",
		"CLAUDE_CODE_SESSION_ID_EXTRA=x",
	}
	kept, removed := ScrubClaudeSessionEnv(env)
	if want := []string{"PATH=/usr/bin", "CLAUDE_CONFIG_DIR=/profiles/a", "CLAUDE_CODE_MAX_WEB_SEARCHES_PER_SESSION=5", "CLAUDE_CODE_SESSION_ID_EXTRA=x"}; !reflect.DeepEqual(kept, want) {
		t.Fatalf("kept = %q, want %q", kept, want)
	}
	if want := []string{"CLAUDECODE", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_MESSAGING_TOKEN"}; !reflect.DeepEqual(removed, want) {
		t.Fatalf("removed = %q, want %q", removed, want)
	}
}

func TestGlobalClaudeSessionEnvReportsNamesOnly(t *testing.T) {
	client := &Client{exec: func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
		if strings.Join(args, " ") != "show-environment -g" {
			t.Fatalf("unexpected tmux call %q", args)
		}
		return []byte("HOME=/root\n-CLAUDE_PID\nCLAUDE_CODE_SESSION_ID=abc\nCLAUDE_CODE_MESSAGING_TOKEN=secret\n"), nil
	}}
	found, err := client.GlobalClaudeSessionEnv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"CLAUDE_CODE_MESSAGING_TOKEN", "CLAUDE_CODE_SESSION_ID"}; !reflect.DeepEqual(found, want) {
		t.Fatalf("found = %q, want %q", found, want)
	}
}
