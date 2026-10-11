package main

import (
	"errors"
	"fmt"
	"testing"

	bptmux "blueprint/internal/tmux"
	"blueprint/internal/workflow"
)

// TestMapCompactResult pins how a compact delivery outcome maps to the sentinel
// the workflow engine acts on. The key case is ErrUnverified: a /compact that the
// screen submit check could not witness must NOT fail the run — it is reported as
// pending/queued so the engine confirms the real outcome by observing the context
// drop (see compactIfNeeded). Regression guard for the hack-gorev run that errored
// with "compact was not verified" after compaction had actually started.
func TestMapCompactResult(t *testing.T) {
	otherErr := errors.New("boom")
	cases := []struct {
		name    string
		queued  bool
		channel string
		err     error
		want    error // sentinel the result must wrap (nil means no error)
	}{
		{"busy", false, "", bptmux.ErrBusy, workflow.ErrCompactBusy},
		{"typing", false, "", bptmux.ErrTyping, workflow.ErrCompactBusy},
		{"pane-locked", false, "", bptmux.ErrPaneLocked, workflow.ErrCompactBusy},
		{"unverified-is-pending", false, "q123", bptmux.ErrUnverified, workflow.ErrCompactQueued},
		{"unverified-wrapped", false, "q9", fmt.Errorf("x: %w", bptmux.ErrUnverified), workflow.ErrCompactQueued},
		{"explicitly-queued", true, "q456", nil, workflow.ErrCompactQueued},
		{"other-error-fails", false, "", otherErr, otherErr},
		{"clean-success", false, "q789", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapCompactResult(tc.queued, tc.channel, tc.err)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("expected nil error, got %v", got)
				}
				return
			}
			if !errors.Is(got, tc.want) {
				t.Fatalf("expected error wrapping %v, got %v", tc.want, got)
			}
		})
	}
}

// TestMapCompactResultBusyBeatsQueued documents precedence: a busy/typing error
// is transient and must win over the queued flag so the engine waits idle and
// retries rather than recording a bogus pending compaction.
func TestMapCompactResultBusyBeatsQueued(t *testing.T) {
	got := mapCompactResult(true, "q1", bptmux.ErrBusy)
	if !errors.Is(got, workflow.ErrCompactBusy) {
		t.Fatalf("busy must take precedence over queued, got %v", got)
	}
}
