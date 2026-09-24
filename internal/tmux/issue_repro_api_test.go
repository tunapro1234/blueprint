package tmux

// Reflection keeps the probe buildable against ead3de0, before Progress existed.

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestIssue22_OpenReportsProgressBeforeWaitingForReadiness(t *testing.T) {
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
	opts := OpenOptions{Codex: true, NoPrompt: true}
	progressField := reflect.ValueOf(&opts).Elem().FieldByName("Progress")
	if progressField.IsValid() && progressField.CanSet() && progressField.Type() == reflect.TypeOf((func(string))(nil)) {
		progressField.Set(reflect.ValueOf(func(line string) { progress = append(progress, line) }))
	}
	err := client.Open(context.Background(), "issue22", "/tmp/issue-22-project", opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(progress) < 2 || !strings.HasPrefix(progress[0], "harness started") || progress[len(progress)-1] != "harness ready" {
		t.Fatalf("open emitted no step-by-step progress before readiness: %v", progress)
	}
}
