package tmux

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The screen from the issue: the cursor sits on "No, exit". bp must not answer
// it, and --no-prompt must fail with an actionable reason and remove the pane it
// created instead of leaving an orphan holding the prompt (and the resume handle).
// Auto-accepting the exact-directory "❯ 1. Yes" modal bp itself opened is an
// existing, separate behaviour and is not what this reproduces.
func TestIssue06OpenDoesNotAcceptAClaudeTrustPromptForTheUser(t *testing.T) {
	dir := "/tmp/issue-06-project"
	prompt := "Accessing workspace:\n" + dir + "\n" +
		"Quick safety check: Is this a project you created or one you trust?\n" +
		"❯ No, exit\n  Yes, I trust this folder\n" +
		"Enter to confirm · Esc to cancel\n"
	h := &openHarness{paneCommand: "zsh", capture: prompt}
	err := openClient(h).Open(context.Background(), "issue06", dir, OpenOptions{NoPrompt: true}, nil)
	for _, mutation := range h.mutations {
		if mutation == "send-keys -t =issue06: Enter" {
			t.Fatalf("bp answered a security prompt on the user's behalf: %v", h.mutations)
		}
	}
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "trust") || !strings.Contains(err.Error(), "bp ") {
		t.Fatalf("trust dialog should produce an actionable trust-prompt error, got %v", err)
	}
}

func TestIssue16CodexFollowUpCannotAppendBehindAnUnsubmittedMessage(t *testing.T) {
	first := "[bp] start the migration"
	h := &codexTerminal{draft: first}
	err := h.client().Send(context.Background(), "agent", "[bp] cancel the migration")
	if !errors.Is(err, ErrTyping) {
		t.Fatalf("follow-up error=%v, want the held composer to block delivery", err)
	}
	if h.pasted != 0 || len(h.submitted) != 0 || h.draft != first {
		t.Fatalf("follow-up changed or submitted the first draft: %+v", h)
	}
}

// #20 (dead pane re-entry) is reproduced by TestIssue20OpenRespawnsDeadPaneInPlace,
// which drives the authoritative #{pane_dead} flag rather than the pane text.
