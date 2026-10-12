package msgq

import (
	"context"
	"path/filepath"
	"testing"
)

// settleHanging writes a record into done as a hanging-composer paste: the state
// a message reaches when bp pasted it, could not verify it, and then could not
// finish it. This is what server-main's 2026-10-11 workflow completion notices
// looked like on lh-hack-climatechain / lh-hack-qloo.
func settleHanging(t *testing.T, q *Queue, to, from, text string) string {
	t.Helper()
	id, err := q.Enqueue(to, from, text)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(q.pending(), id+".json")
	m, err := read(path)
	if err != nil {
		t.Fatal(err)
	}
	m.NoRepaste = true
	m.Reason = hangingPasteReason + "; recovery blocked: runtime working: "
	if err := q.finish(path, m, StatusHangingComposer); err != nil {
		t.Fatal(err)
	}
	return id
}

// settleHangingChip writes a record whose own paste collapsed into an unreadable
// chip: NoRepaste with a recorded ChipSig label and the binding it was pasted
// under. This is the state behind server-main's probot-tedarik hang, where bp
// could see a "[Pasted text #N]" chip blocking the composer but could not read or
// match the text to clear it. The record stays in pending (still waiting for the
// witness), which is where the chip blocks the queue.
func settleHangingChip(t *testing.T, q *Queue, to, from, text, chipSig, binding string) string {
	t.Helper()
	id, err := q.Enqueue(to, from, text)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(q.pending(), id+".json")
	m, err := read(path)
	if err != nil {
		t.Fatal(err)
	}
	m.NoRepaste = true
	m.Reason = unverifiedReason
	m.ChipSig = chipSig
	m.AttemptBinding = binding
	if err := writePending(path, m); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestClearHangingComposerRetractsOwnText pins `bp qclear`: a hanging-composer
// record whose exact text still sits in an idle composer is cleared; everything
// else is left exactly as it is. Regression guard for server-main's report that
// bp had no way to retract its own unsent workflow notices, which then blocked
// every later message to those agents with "foreign text".
func TestClearHangingComposerRetractsOwnText(t *testing.T) {
	t.Run("clears bp's own hanging text", func(t *testing.T) {
		q := newBoundTestQueue(t.TempDir())
		id := settleHanging(t, q, "target", "workflow:run", "workflow hack-gorev run R done")
		target := &fakeTarget{alive: true, pane: composerPane("workflow hack-gorev run R done")}
		outcomes, err := q.ClearHangingComposer(context.Background(), target, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(outcomes) != 1 || !outcomes[0].Cleared || outcomes[0].To != "target" {
			t.Fatalf("expected a cleared outcome for target, got %#v", outcomes)
		}
		if len(target.cleared) != 1 || target.cleared[0][0] != "workflow hack-gorev run R done" {
			t.Fatalf("ClearDelivered was not called with the record's exact text: %#v", target.cleared)
		}
	})
	t.Run("leaves a composer holding other text untouched", func(t *testing.T) {
		q := newBoundTestQueue(t.TempDir())
		id := settleHanging(t, q, "target", "workflow:run", "workflow hack-gorev run R done")
		// The pane now holds someone else's text, not bp's hanging paste. The guarded
		// clear (fakeTarget mirrors the real StuckPaste check) must refuse it.
		target := &fakeTarget{alive: true, pane: composerPane("a human is typing here")}
		outcomes, err := q.ClearHangingComposer(context.Background(), target, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(outcomes) != 1 || outcomes[0].Cleared {
			t.Fatalf("foreign composer text must not be cleared: %#v", outcomes)
		}
	})
	t.Run("does not touch a record that is not a hanging paste", func(t *testing.T) {
		q := newBoundTestQueue(t.TempDir())
		id, err := q.Enqueue("target", "sender", "ordinary message")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(q.pending(), id+".json")
		m, err := read(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := q.finish(path, m, "delivered"); err != nil {
			t.Fatal(err)
		}
		target := &fakeTarget{alive: true, pane: composerPane("ordinary message")}
		outcomes, err := q.ClearHangingComposer(context.Background(), target, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(outcomes) != 1 || outcomes[0].Cleared {
			t.Fatalf("a non-hanging record must not be cleared: %#v", outcomes)
		}
		if len(target.cleared) != 0 {
			t.Fatalf("ClearDelivered must not be called for a non-hanging record: %#v", target.cleared)
		}
	})
	t.Run("clears bp's own chip by label when the binding still matches", func(t *testing.T) {
		q := newBoundTestQueue(t.TempDir())
		chip := "[Pasted text #36 +3 lines]"
		// Same binding the fixture queue hands out, so the target session is unchanged.
		id := settleHangingChip(t, q, "target", "workflow:run", "a long multi-line prompt", chip, "claude:fixture-thread:100")
		target := &fakeTarget{alive: true, pane: composerPane(chip)}
		outcomes, err := q.ClearHangingComposer(context.Background(), target, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(outcomes) != 1 || !outcomes[0].Cleared {
			t.Fatalf("expected the chip to be cleared: %#v", outcomes)
		}
		if len(target.chipCleared) != 1 || target.chipCleared[0] != chip {
			t.Fatalf("ClearChip was not called with the recorded label: %#v", target.chipCleared)
		}
		if len(target.cleared) != 0 {
			t.Fatalf("the exact-text clear must not run for a chip record: %#v", target.cleared)
		}
	})
	t.Run("refuses a chip when the target session changed", func(t *testing.T) {
		q := newBoundTestQueue(t.TempDir())
		chip := "[Pasted text #36 +3 lines]"
		// The record was bound to an OLD session; the fixture now binds a different
		// thread, so the chip counter the label carries is from a session that is gone.
		id := settleHangingChip(t, q, "target", "workflow:run", "a long multi-line prompt", chip, "claude:old-thread:1")
		target := &fakeTarget{alive: true, pane: composerPane(chip)}
		outcomes, err := q.ClearHangingComposer(context.Background(), target, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(outcomes) != 1 || outcomes[0].Cleared {
			t.Fatalf("a chip from a changed session must not be cleared: %#v", outcomes)
		}
		if len(target.chipCleared) != 0 {
			t.Fatalf("ClearChip must not be called when the binding changed: %#v", target.chipCleared)
		}
	})
	t.Run("leaves a different chip in the box untouched", func(t *testing.T) {
		q := newBoundTestQueue(t.TempDir())
		id := settleHangingChip(t, q, "target", "workflow:run", "a long multi-line prompt", "[Pasted text #36 +3 lines]", "claude:fixture-thread:100")
		// The composer now holds a DIFFERENT chip (a later paste), not the one we recorded.
		target := &fakeTarget{alive: true, pane: composerPane("[Pasted text #99 +1 lines]")}
		outcomes, err := q.ClearHangingComposer(context.Background(), target, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(outcomes) != 1 || outcomes[0].Cleared {
			t.Fatalf("a different chip must not be cleared: %#v", outcomes)
		}
	})
	t.Run("reports a missing channel without touching any pane", func(t *testing.T) {
		q := newBoundTestQueue(t.TempDir())
		target := &fakeTarget{alive: true, pane: composerPane("")}
		outcomes, err := q.ClearHangingComposer(context.Background(), target, "qDOESNOTEXIST")
		if err != nil {
			t.Fatal(err)
		}
		if len(outcomes) != 1 || outcomes[0].Err == nil || outcomes[0].Cleared {
			t.Fatalf("missing channel must report an error and clear nothing: %#v", outcomes)
		}
		if len(target.cleared) != 0 {
			t.Fatalf("no pane should be touched for a missing channel: %#v", target.cleared)
		}
	})
}
