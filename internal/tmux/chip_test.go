package tmux

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const chipMessage = "[lead] first line of a pasted note\nsecond line\nthird line"

func TestClaudeChipForMatchesOnlyThisTextsLineCount(t *testing.T) {
	single := strings.Repeat("one long single-line message ", 40)
	for _, tc := range []struct {
		name string
		pane string
		text string
		want bool
	}{
		{"three lines, +2", claudePane("❯ [Pasted text #21 +2 lines]"), chipMessage, true},
		{"carriage returns count as breaks", claudePane("❯ [Pasted text #3 +2 lines]"), "a\r\nb\rc", true},
		{"three lines, +3", claudePane("❯ [Pasted text #21 +3 lines]"), chipMessage, false},
		{"single line has no count", claudePane("❯ [Pasted text #7]"), single, true},
		{"single line with a count", claudePane("❯ [Pasted text #7 +1 lines]"), single, false},
		{"three lines without a count", claudePane("❯ [Pasted text #7]"), chipMessage, false},
		{"text after the chip", claudePane("❯ [Pasted text #21 +2 lines] more"), chipMessage, false},
		{"text before the chip", claudePane("❯ note [Pasted text #21 +2 lines]"), chipMessage, false},
		{"two chips", claudePane("❯ [Pasted text #21 +2 lines][Pasted text #22 +2 lines]"), chipMessage, false},
		{"chip on a second row", claudePane("❯ typed", "  [Pasted text #21 +2 lines]"), chipMessage, false},
		{"image chip", claudePane("❯ [Image #2]"), chipMessage, false},
		{"literal text", claudePane("❯ " + chipMessage), chipMessage, false},
	} {
		if got := ClaudeChipFor(tc.pane, tc.text); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSubmitStuckChipPressesOneEnterOnAMatchingChip(t *testing.T) {
	chip := claudePane("❯ [Pasted text #21 +2 lines]")
	h := &sendHarness{captures: []string{chip, claudePane("❯ ")}}
	if submitted, err := testClient(h).SubmitStuckChip(context.Background(), "target", chipMessage); err != nil || !submitted {
		t.Fatalf("submitted=%v err=%v", submitted, err)
	}
	if got := countEnter(h.mutations); got != 1 {
		t.Fatalf("Enter presses=%d: %v", got, h.mutations)
	}
	h = &sendHarness{captures: []string{chip}}
	if submitted, err := testClient(h).SubmitStuckChip(context.Background(), "target", chipMessage); submitted || !errors.Is(err, ErrSubmitUnconfirmed) {
		t.Fatalf("chip still on screen: submitted=%v err=%v", submitted, err)
	}
	if got := countEnter(h.mutations); got != 1 {
		t.Fatalf("Enter presses=%d: %v", got, h.mutations)
	}
}

func TestSubmitStuckChipRefusesWithoutAKey(t *testing.T) {
	busy := spinnerPane("  agent: output", "✻ Working… (23s · esc to interrupt)")
	if !Busy(busy) {
		t.Fatal("fixture is not busy")
	}
	for _, tc := range []struct {
		name string
		pane string
		err  error
	}{
		{"count differs", claudePane("❯ [Pasted text #21 +5 lines]"), nil},
		{"extra text", claudePane("❯ [Pasted text #21 +2 lines] mine"), nil},
		{"busy pane", busy, ErrBusy},
	} {
		h := &sendHarness{captures: []string{tc.pane}}
		submitted, err := testClient(h).SubmitStuckChip(context.Background(), "target", chipMessage)
		if submitted || !errors.Is(err, tc.err) || (tc.err == nil && err != nil) {
			t.Errorf("%s: submitted=%v err=%v", tc.name, submitted, err)
		}
		if got := countEnter(h.mutations); got != 0 {
			t.Errorf("%s: Enter pressed: %v", tc.name, h.mutations)
		}
	}
}
