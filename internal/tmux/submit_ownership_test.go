package tmux

import (
	"context"
	"testing"
)

func TestSubmitRetriesExactWrappedClaudeComposer(t *testing.T) {
	pane := claudePane("❯ [bp] first part", "  second part")
	h := &sendHarness{captures: []string{pane, claudePane("❯ ")}, activities: []string{"target\t900\n", "target\t900\n"}}
	got := testClient(h).submit(context.Background(), "=target:", "target", "[bp] first part second part", &pane)
	if got != sendVerified || countEnter(h.mutations) != 2 {
		t.Fatalf("verdict=%v keys=%v", got, h.mutations)
	}
}

func TestSubmitNeverQueuesForeignDraftWithBusyHint(t *testing.T) {
	pane := modernCodexPane("user's unrelated draft") + "\n  \x1b[2mtab to queue message\x1b[0m\n"
	h := &sendHarness{captures: []string{pane, pane, pane, pane, pane, pane}, activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"}}
	testClient(h).submit(context.Background(), "=target:", "target", "[bp] intended message", &pane)
	if len(h.mutations) != 0 {
		t.Fatalf("foreign draft received keys: %v", h.mutations)
	}
}

func TestSubmitDoesNotSendUserSuffixOnFirstAttempt(t *testing.T) {
	pane := claudePane("❯ [bp] intended message USER DRAFT")
	h := &sendHarness{captures: []string{pane, pane, pane, pane, pane, pane}, activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"}}
	testClient(h).submit(context.Background(), "=target:", "target", "[bp] intended message", &pane)
	if len(h.mutations) != 0 {
		t.Fatalf("user suffix was submitted: %v", h.mutations)
	}
}
