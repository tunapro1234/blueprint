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

// TestCompactDeliveryOutcome pins the reinterpretation of a /compact delivery. A
// /compact confirms through neither the screen nor the transcript witness, so the
// queue can only settle a compaction that ran as "not delivered (hanging in
// composer)" or "unconfirmed". deliver() returns those as a hard error; here they
// must become ErrUnverified so the engine defers to the context-drop observation
// instead of failing the run. Regression guard for the probot-egitim 2026-10-11
// runs that errored "not delivered (hanging in composer)" after the transcript
// showed "Compacted". A refusal with no channel keeps its own error.
func TestCompactDeliveryOutcome(t *testing.T) {
	hangErr := fmt.Errorf("message was not delivered: %s (channel q357141626)", "not delivered (hanging in composer)")
	noSession := errors.New("no open session named probot-egitim-hat3")
	cases := []struct {
		name      string
		queued    bool
		channel   string
		err       error
		wantErr   error // sentinel the result wraps, or the exact err, or nil
		exactErr  bool  // compare with == instead of errors.Is
		wantQueue bool
	}{
		{"hanging-becomes-unverified", false, "q357141626", hangErr, bptmux.ErrUnverified, false, false},
		{"unverified-stays-unverified", false, "q9", bptmux.ErrUnverified, bptmux.ErrUnverified, false, false},
		{"queued-not-ready-becomes-unverified", true, "q5", bptmux.ErrNotReady, bptmux.ErrUnverified, false, true},
		{"no-channel-keeps-error", false, "", noSession, noSession, true, false},
		{"busy-with-channel-kept", false, "q7", bptmux.ErrBusy, bptmux.ErrBusy, false, false},
		{"clean-success", false, "q1", nil, nil, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotQueued, gotChannel, gotErr := compactDeliveryOutcome(tc.queued, tc.channel, tc.err)
			if gotQueued != tc.wantQueue {
				t.Fatalf("queued: want %v, got %v", tc.wantQueue, gotQueued)
			}
			if gotChannel != tc.channel {
				t.Fatalf("channel must pass through: want %q, got %q", tc.channel, gotChannel)
			}
			switch {
			case tc.wantErr == nil:
				if gotErr != nil {
					t.Fatalf("expected nil error, got %v", gotErr)
				}
			case tc.exactErr:
				if gotErr != tc.wantErr {
					t.Fatalf("expected exact error %v, got %v", tc.wantErr, gotErr)
				}
			default:
				if !errors.Is(gotErr, tc.wantErr) {
					t.Fatalf("expected error wrapping %v, got %v", tc.wantErr, gotErr)
				}
			}
		})
	}
}

// TestCompactDeliveryEndToEnd composes the two steps the driver actually runs: a
// hanging /compact outcome must travel compactDeliveryOutcome -> mapCompactResult
// and arrive as ErrCompactQueued, so compactIfNeeded records it pending and the run
// continues rather than failing. This is the whole bug, start to finish.
func TestCompactDeliveryEndToEnd(t *testing.T) {
	hangErr := fmt.Errorf("message was not delivered: %s (channel q357141626)", "not delivered (hanging in composer)")
	got := mapCompactResult(compactDeliveryOutcome(false, "q357141626", hangErr))
	if !errors.Is(got, workflow.ErrCompactQueued) {
		t.Fatalf("a hanging /compact must end as ErrCompactQueued, got %v", got)
	}
	// A genuine no-session refusal must still fail the run.
	noSession := errors.New("no open session named probot-egitim-hat3")
	got = mapCompactResult(compactDeliveryOutcome(false, "", noSession))
	if errors.Is(got, workflow.ErrCompactQueued) || errors.Is(got, workflow.ErrCompactBusy) {
		t.Fatalf("a no-session refusal must stay a hard error, got %v", got)
	}
}
