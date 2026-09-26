package tmux

import (
	"context"
	"strings"
	"testing"
	"time"
)

type launchHarness struct {
	exists  bool
	dead    bool
	capture string
	calls   []string
}

func (h *launchHarness) client() *Client {
	client := New()
	client.Sleep = func(time.Duration) {}
	client.exec = func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		h.calls = append(h.calls, joined)
		switch {
		case strings.HasPrefix(joined, "has-session"):
			if !h.exists {
				return nil, errNoSession
			}
			return nil, nil
		case strings.HasPrefix(joined, "new-session"):
			h.exists = true
		case strings.HasPrefix(joined, "kill-session"):
			h.exists = false
		case strings.HasPrefix(joined, "respawn-pane"):
			h.dead = false
		case strings.HasPrefix(joined, "display-message") && strings.Contains(joined, "#{pane_dead}"):
			if h.dead {
				return []byte("1\n"), nil
			}
			return []byte("0\n"), nil
		case strings.HasPrefix(joined, "list-panes -t"):
			if h.dead {
				return []byte("1\tbp\t100\n"), nil
			}
			return []byte("1\tzsh\t100\n"), nil
		case strings.HasPrefix(joined, "capture-pane"):
			return []byte(h.capture), nil
		}
		return nil, nil
	}
	return client
}

func (h *launchHarness) called(prefix string) bool {
	for _, call := range h.calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}

var errNoSession = &noSessionError{}

type noSessionError struct{}

func (*noSessionError) Error() string { return "can't find session" }

// Issue #6: a first-run trust prompt made open time out with the bare
// "agent did not become ready" and kept the blocked harness alive forever.
func TestIssue06_TrustPromptTimeoutNamesCauseAndRemovesOrphan(t *testing.T) {
	h := &launchHarness{capture: "Accessing workspace:\n/work/elsewhere\nQuick safety check: Is this a project you created or one you trust?\n❯ No, exit\n  Yes, I trust this folder\nEnter to confirm · Esc to cancel\n"}
	err := h.client().Open(context.Background(), "agent", "/work/new", OpenOptions{NoPrompt: true}, nil)
	if err == nil {
		t.Fatal("open unexpectedly succeeded")
	}
	msg := err.Error()
	if !strings.Contains(msg, "trust prompt") || !strings.Contains(msg, "Quick safety check") {
		t.Fatalf("error does not name the prompt or show the pane: %v", msg)
	}
	if !h.called("kill-session") || h.exists {
		t.Fatalf("session created by this open was left blocked on the prompt: %v", h.calls)
	}
	for _, call := range h.calls {
		if strings.HasPrefix(call, "send-keys") && (strings.HasSuffix(call, " Enter") || strings.HasSuffix(call, " Down")) && !strings.Contains(call, "claude") {
			t.Fatalf("bp answered the user's trust prompt: %q", call)
		}
	}
}

// A session that existed before the open is not killed on timeout.
func TestIssue06_TimeoutKeepsPreexistingSession(t *testing.T) {
	h := &launchHarness{exists: true, capture: "$ \n"}
	err := h.client().Open(context.Background(), "agent", "/work", OpenOptions{NoPrompt: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "retained") {
		t.Fatalf("err=%v", err)
	}
	if h.called("kill-session") {
		t.Fatal("killed a session this open did not create")
	}
}

// Issue #20: a managed pane left as "Pane is dead" (remain-on-exit) after the
// harness exits could not be re-entered: open saw command "bp" and refused.
func TestIssue20_OpenRespawnsDeadPaneInPlace(t *testing.T) {
	h := &launchHarness{exists: true, dead: true, capture: "bypass permissions\n"}
	var warnings []string
	err := h.client().Open(context.Background(), "main", "/work", OpenOptions{NoPrompt: true}, func(s string) { warnings = append(warnings, s) })
	if err != nil {
		t.Fatalf("dead pane re-entry failed: %v", err)
	}
	if !h.called("respawn-pane -k -t =main: -c /work") || h.called("new-session") {
		t.Fatalf("dead pane not respawned in place: %v", h.calls)
	}
	launched := false
	for _, call := range h.calls {
		if strings.HasPrefix(call, "send-keys") && strings.Contains(call, "claude --dangerously-skip-permissions") {
			launched = true
		}
	}
	if !launched {
		t.Fatalf("harness not launched into respawned pane: %v", h.calls)
	}
}
