package tmux

// Needs OpenOptions.Progress, introduced by the #22 fix.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestIssue22OpenReportsProgressBeforeWaitingForReadiness(t *testing.T) {
	var progress []string
	client := New()
	client.Sleep = func(time.Duration) {}
	client.exec = func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
		switch args[0] {
		case "has-session":
			return nil, fmt.Errorf("no session")
		case "new-session", "send-keys":
			return nil, nil
		case "capture-pane":
			return []byte("› Ask Codex to do anything\n"), nil
		default:
			return nil, nil
		}
	}
	err := client.Open(context.Background(), "issue22", "/tmp/issue-22-project", OpenOptions{Codex: true, NoPrompt: true, Progress: func(line string) {
		progress = append(progress, line)
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(progress) < 2 || !strings.HasPrefix(progress[0], "harness started") || progress[len(progress)-1] != "harness ready" {
		t.Fatalf("open emitted no step-by-step progress before readiness: %v", progress)
	}
}
