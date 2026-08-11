package msgq

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bptmux "blueprint/internal/tmux"
)

type fakeTarget struct {
	alive   bool
	pane    string
	sent    []string
	sendErr error // when set, Send returns it instead of recording the delivery
	// cleared records every ClearDelivered call and, when the pane provably holds
	// one of the texts, empties it — the way the real client's C-u loop does.
	cleared  [][]string
	clearErr error
}

func (f *fakeTarget) HasSession(context.Context, string) bool         { return f.alive }
func (f *fakeTarget) Capture(context.Context, string) (string, error) { return f.pane, nil }
func (f *fakeTarget) Send(_ context.Context, to, text string) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, to+":"+text)
	return nil
}

func (f *fakeTarget) CaptureAnsi(ctx context.Context, session string) (string, error) {
	return f.Capture(ctx, session)
}

func (f *fakeTarget) ClearDelivered(_ context.Context, _ string, texts []string) (bool, error) {
	f.cleared = append(f.cleared, texts)
	if f.clearErr != nil {
		return false, f.clearErr
	}
	if _, ours := bptmux.StuckPaste(f.pane, texts); !ours {
		return false, nil
	}
	f.pane = composerPane("")
	return true, nil
}

func TestEnqueueListAndStatus(t *testing.T) {
	q := New(t.TempDir())
	now := time.Date(2026, 7, 10, 10, 0, 0, 123456789, time.Local)
	q.Now = func() time.Time { return now }
	id, err := q.Enqueue("target", "sender", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "q") {
		t.Fatalf("bad id: %s", id)
	}
	rows, err := q.List()
	if err != nil || len(rows) != 1 || rows[0].Msg != "hello" {
		t.Fatalf("list=%v err=%v", rows, err)
	}
	q.Now = func() time.Time { return now.Add(12 * time.Second) }
	status, _ := q.Status(id)
	if status != "PENDING: target is still busy (12 seconds queued)" {
		t.Fatalf("status=%q", status)
	}
}

func TestEnqueueCollisionKeepsFileAndPayloadIDsEqual(t *testing.T) {
	q := New(t.TempDir())
	now := time.Date(2026, 7, 10, 10, 0, 0, 123456789, time.Local)
	q.Now = func() time.Time { return now }
	first, err := q.Enqueue("a", "sender", "one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := q.Enqueue("b", "sender", "two")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || second != first+"-1" {
		t.Fatalf("unexpected ids: %q %q", first, second)
	}
	rows, err := q.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if _, err := os.Stat(filepath.Join(q.pending(), row.ID+".json")); err != nil {
			t.Fatalf("payload id has no matching file: %s: %v", row.ID, err)
		}
	}
}

func TestDispatchWaitsForTypingThenDelivers(t *testing.T) {
	q := New(t.TempDir())
	q.Now = time.Now
	id, err := q.Enqueue("target", "sender", "line1\nline2")
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: "❯ user is typing"}
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 0 {
		t.Fatal("message sent into a non-empty composer")
	}
	target.pane = "❯ \u00a0"
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 1 {
		t.Fatalf("sent=%v", target.sent)
	}
	if _, err = os.Stat(filepath.Join(q.done(), id+".json")); err != nil {
		t.Fatal(err)
	}
	status, _ := q.Status(id)
	if !strings.HasPrefix(status, "DELIVERED: target") {
		t.Fatalf("status=%q", status)
	}
}

func TestDispatchLeavesNonAgentTargetPending(t *testing.T) {
	// The target session exists and its composer is empty, but it dropped to a
	// shell: Send returns ErrNotAgent. The message must stay PENDING (never lost,
	// never typed into the shell) and a distinct skip line must be reported.
	q := New(t.TempDir())
	q.Now = time.Now
	id, err := q.Enqueue("target", "sender", "hello")
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: "❯  ", sendErr: bptmux.ErrNotAgent}
	var reports []string
	if err = q.Dispatch(context.Background(), target, func(m string) { reports = append(reports, m) }); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 0 {
		t.Fatalf("message was sent to a non-agent target: %v", target.sent)
	}
	if _, err = os.Stat(filepath.Join(q.pending(), id+".json")); err != nil {
		t.Fatalf("message did not stay pending: %v", err)
	}
	if _, err = os.Stat(filepath.Join(q.done(), id+".json")); !os.IsNotExist(err) {
		t.Fatalf("message must not be finished, stat err=%v", err)
	}
	found := false
	for _, r := range reports {
		if strings.Contains(r, "skipped: target not an agent") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a distinct non-agent skip report, got: %v", reports)
	}
}

func TestDispatchCancelsClosedTarget(t *testing.T) {
	q := New(t.TempDir())
	id, err := q.Enqueue("closed", "sender", "message")
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Dispatch(context.Background(), &fakeTarget{}, nil); err != nil {
		t.Fatal(err)
	}
	status, _ := q.Status(id)
	if !strings.HasPrefix(status, "CANCELED (TARGET CLOSED): closed") {
		t.Fatalf("status=%q", status)
	}
}

func TestDispatchContinuesAfterFinishError(t *testing.T) {
	q := New(t.TempDir())
	now := time.Date(2026, 7, 10, 10, 0, 0, 123456789, time.Local)
	current := now
	q.Now = func() time.Time { return current }
	first, err := q.Enqueue("one", "sender", "first")
	if err != nil {
		t.Fatal(err)
	}
	current = current.Add(time.Nanosecond)
	second, err := q.Enqueue("two", "sender", "second")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(q.done(), first+".json"), 0755); err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: "❯ "}
	var reports []string
	if err = q.Dispatch(context.Background(), target, func(message string) {
		reports = append(reports, message)
	}); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 2 {
		t.Fatalf("dispatcher stopped early: sent=%v", target.sent)
	}
	if _, err = os.Stat(filepath.Join(q.done(), second+".json")); err != nil {
		t.Fatalf("second message was not finished: %v", err)
	}
	if len(reports) == 0 || !strings.Contains(reports[0], "could not finish "+first) {
		t.Fatalf("finish error was not reported: %v", reports)
	}
}

func TestStatusTranslatesLegacyQueueRecord(t *testing.T) {
	q := New(t.TempDir())
	if err := os.MkdirAll(q.done(), 0755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"id":"qlegacy","to":"target","durum":"iletildi","bitis":1783677600}`
	if err := os.WriteFile(filepath.Join(q.done(), "qlegacy.json"), []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}
	status, err := q.Status("qlegacy")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(status, "DELIVERED: target") {
		t.Fatalf("legacy status=%q", status)
	}
}

// composerPane wraps text in the structure of a live Claude Code composer box
// (two borders, the prompt marker on the first row, the "-- INSERT --" status
// footer below). Synthetic text only.
func composerPane(text string) string {
	// Live structure: the TOP border carries the agent name, the bottom is pure.
	top := "──────────────────────── target ──"
	border := "──────────────────────────────────────"
	row := "❯   "
	if text != "" {
		row = "❯ " + text
	}
	return strings.Join([]string{
		"  agent: onceki turdan kalan cikti",
		top,
		row,
		border,
		"  -- INSERT -- ⏵⏵ bypass permissions on (shift+tab to cycle)",
		"",
	}, "\n")
}

const stuckText = "[server-main] roadmap incelemesi: hedef sistemi bolumunu bugun bitirelim"

func TestDispatchSendsThroughItsOwnHangingPaste(t *testing.T) {
	// The deadlock: the composer holds the very message this record is waiting to
	// deliver, left there by an earlier paste whose Enter never registered. The old
	// Typing gate skipped the record on every pass — measured at four days — so the
	// queue blocked itself. Now the record is handed to Send, which finishes it.
	q := New(t.TempDir())
	q.Now = time.Now
	id, err := q.Enqueue("target", "sender", stuckText)
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane(stuckText)}
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 1 {
		t.Fatalf("the queue skipped its own hanging paste again: sent=%v", target.sent)
	}
	if _, err = os.Stat(filepath.Join(q.done(), id+".json")); err != nil {
		t.Fatalf("record was not closed: %v", err)
	}
}

func TestDispatchStillWaitsForSomeoneElsesText(t *testing.T) {
	q := New(t.TempDir())
	q.Now = time.Now
	id, err := q.Enqueue("target", "sender", stuckText)
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane("kendi yarim kalan sorum burada duruyor")}
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 0 {
		t.Fatalf("a message was delivered into someone's half-written line: %v", target.sent)
	}
	if _, err = os.Stat(filepath.Join(q.pending(), id+".json")); err != nil {
		t.Fatalf("message did not stay pending: %v", err)
	}
}

func TestDispatchClosesAMessageTheTranscriptAlreadyHas(t *testing.T) {
	// Reconciliation: the message reached the agent some other way (hand-delivered,
	// or submitted out of the composer). The transcript is the witness, and it must
	// stop the queue from pasting a second copy.
	q := New(t.TempDir())
	q.Now = time.Now
	var asked []string
	q.Witness = func(to, text string, since time.Time) bool {
		asked = append(asked, to)
		return text == stuckText && since.Before(time.Now().Add(time.Second))
	}
	id, err := q.Enqueue("target", "sender", stuckText)
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: "❯  "}
	var reports []string
	if err = q.Dispatch(context.Background(), target, func(m string) { reports = append(reports, m) }); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 0 {
		t.Fatalf("an already-delivered message was pasted again: %v", target.sent)
	}
	if len(asked) != 1 {
		t.Fatalf("the transcript witness was consulted %d times", len(asked))
	}
	status, err := q.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "FOUND IN TRANSCRIPT") {
		t.Fatalf("status=%q", status)
	}
	if !strings.Contains(strings.Join(reports, "\n"), "transcript") {
		t.Fatalf("reports=%v", reports)
	}
}

func TestWitnessClearsTheSameTextStillHangingInTheComposer(t *testing.T) {
	// The failure mode this closes: the transcript proves the message arrived, so
	// the record is closed — and the very same text is still hanging in the
	// composer from the paste whose Enter never registered. Once the record is
	// gone nothing can recognise that text as ours, it reads as foreign, and the
	// target is blocked forever. Both proofs are in hand at this moment, so the
	// composer is cleared here, out loud.
	q := New(t.TempDir())
	q.Now = time.Now
	q.Witness = func(_, text string, _ time.Time) bool { return text == stuckText }
	id, err := q.Enqueue("target", "sender", stuckText)
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane(stuckText)}
	var reports []string
	if err = q.Dispatch(context.Background(), target, func(m string) { reports = append(reports, m) }); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 0 {
		t.Fatalf("an already-delivered message was pasted again: %v", target.sent)
	}
	if _, err = os.Stat(filepath.Join(q.done(), id+".json")); err != nil {
		t.Fatalf("record was not closed: %v", err)
	}
	if len(target.cleared) != 1 {
		t.Fatalf("the hanging text was left behind: cleared=%v", target.cleared)
	}
	if bptmux.Typing(target.pane) {
		t.Fatalf("composer is still holding text after the record closed: %q", target.pane)
	}
	if !strings.Contains(strings.Join(reports, "\n"), "C-u ile temizlendi") {
		t.Fatalf("the clearing was not reported: %v", reports)
	}

	// And the target is usable again: the next message goes in normally instead of
	// queueing forever behind text nobody can identify.
	q.Witness = func(string, string, time.Time) bool { return false }
	if _, err := q.Enqueue("target", "sender", "[server-main] sonraki mesaj normal gitmeli"); err != nil {
		t.Fatal(err)
	}
	if err := q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 1 {
		t.Fatalf("the target stayed blocked after the cleanup: sent=%v", target.sent)
	}
}

func TestWitnessLeavesAComposerItCannotProveAlone(t *testing.T) {
	// Same witness, but the composer holds someone ELSE's text. No proof that it is
	// ours, so not a key is pressed on it.
	q := New(t.TempDir())
	q.Now = time.Now
	q.Witness = func(string, string, time.Time) bool { return true }
	if _, err := q.Enqueue("target", "sender", stuckText); err != nil {
		t.Fatal(err)
	}
	foreign := composerPane("kendi yarim kalan sorum burada duruyor")
	target := &fakeTarget{alive: true, pane: foreign}
	if err := q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if target.pane != foreign {
		t.Fatalf("someone else's composer was touched: %q", target.pane)
	}
}

func TestDispatchRecordsWhyAMessageIsWaiting(t *testing.T) {
	// Risk-1/3 visibility: a composer bp deliberately refuses to touch must not
	// leave the message waiting in silence. `bp qstat` used to say "is still busy"
	// at an idle agent for four days.
	cases := []struct {
		name   string
		pane   string
		reason string
	}{
		{"unreadable paste chip", composerPane("[Pasted text #1 +12 lines]"), bptmux.BlockedByPasteChip},
		{"someone else's text", composerPane("kendi yarim kalan sorum burada duruyor"), bptmux.BlockedByForeignText},
		{"short unidentifiable fragment", composerPane("/rename wor"), bptmux.BlockedByForeignText},
		{"working pane", "✻ Working… (23s · Esc to interrupt)\n" + composerPane(""), bptmux.BlockedByBusyPane},
	}
	for _, tc := range cases {
		q := New(t.TempDir())
		q.Now = time.Now
		id, err := q.Enqueue("target", "sender", stuckText)
		if err != nil {
			t.Fatal(err)
		}
		target := &fakeTarget{alive: true, pane: tc.pane}
		if err := q.Dispatch(context.Background(), target, nil); err != nil {
			t.Fatal(err)
		}
		if len(target.sent) != 0 {
			t.Fatalf("%s: message was delivered: %v", tc.name, target.sent)
		}
		rows, err := q.List()
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s: list=%v err=%v", tc.name, rows, err)
		}
		if rows[0].Reason != tc.reason {
			t.Errorf("%s: reason=%q, want %q", tc.name, rows[0].Reason, tc.reason)
		}
		status, err := q.Status(id)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(status, tc.reason) || strings.Contains(status, "is still busy") {
			t.Errorf("%s: qstat=%q, want the reason instead of \"is still busy\"", tc.name, status)
		}
		if !strings.Contains(status, "bp peek target") {
			t.Errorf("%s: qstat does not say how to look: %q", tc.name, status)
		}
	}
}

func TestDispatchDropsAStaleReasonWhenThePaneFreesUp(t *testing.T) {
	q := New(t.TempDir())
	q.Now = time.Now
	if _, err := q.Enqueue("target", "sender", stuckText); err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane("kendi yarim kalan sorum")}
	if err := q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	rows, _ := q.List()
	if len(rows) != 1 || rows[0].Reason == "" {
		t.Fatalf("reason was not recorded: %v", rows)
	}
	// The record must survive the rewrite intact — id, target, sender and text.
	if rows[0].To != "target" || rows[0].From != "sender" || rows[0].Msg != stuckText {
		t.Fatalf("rewriting the reason damaged the record: %+v", rows[0])
	}
	target.pane = composerPane("")
	if err := q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 1 {
		t.Fatalf("sent=%v", target.sent)
	}
}

func TestDispatchDeliversWhenTheTranscriptHasNothing(t *testing.T) {
	q := New(t.TempDir())
	q.Now = time.Now
	q.Witness = func(string, string, time.Time) bool { return false }
	if _, err := q.Enqueue("target", "sender", stuckText); err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: "❯  "}
	if err := q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 1 {
		t.Fatalf("a witness that found nothing blocked the delivery: %v", target.sent)
	}
}

func TestPendingForAndCloseDelivered(t *testing.T) {
	q := New(t.TempDir())
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.Local)
	q.Now = func() time.Time { return now }
	first, err := q.Enqueue("target", "sender", stuckText)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Millisecond)
	if _, err := q.Enqueue("other", "sender", "baska hedefin mesaji"); err != nil {
		t.Fatal(err)
	}
	texts := q.PendingFor("target")
	if len(texts) != 1 || texts[0] != stuckText {
		t.Fatalf("PendingFor=%v", texts)
	}
	id, ok := q.CloseDelivered("target", stuckText, "delivered (composer)")
	if !ok || id != first {
		t.Fatalf("CloseDelivered=%q,%v want %q", id, ok, first)
	}
	if _, err := os.Stat(filepath.Join(q.pending(), first+".json")); !os.IsNotExist(err) {
		t.Fatalf("record stayed pending: %v", err)
	}
	if _, ok := q.CloseDelivered("target", stuckText, "delivered (composer)"); ok {
		t.Fatal("a second close reported success for a record that is gone")
	}
}

func TestDispatchKeepsProvenFailurePendingAndClosesUnverified(t *testing.T) {
	t.Run("not ready stays pending", func(t *testing.T) {
		// Expired login / foreign composer: the message provably did not land,
		// so it must stay queued for the next pass, with the reason reported.
		q := New(t.TempDir())
		q.Now = time.Now
		id, err := q.Enqueue("target", "sender", "hello")
		if err != nil {
			t.Fatal(err)
		}
		target := &fakeTarget{alive: true, pane: "❯  ", sendErr: fmt.Errorf("%w: login expired", bptmux.ErrNotReady)}
		var reports []string
		if err = q.Dispatch(context.Background(), target, func(m string) { reports = append(reports, m) }); err != nil {
			t.Fatal(err)
		}
		if _, err = os.Stat(filepath.Join(q.pending(), id+".json")); err != nil {
			t.Fatalf("message did not stay pending: %v", err)
		}
		if !strings.Contains(strings.Join(reports, "\n"), "not delivered") {
			t.Fatalf("reports=%v", reports)
		}
	})

	t.Run("unverified is closed, never retried", func(t *testing.T) {
		// The keystrokes went in unconfirmed. Leaving the record pending would
		// paste the same message again on the next pass, so it is finished with
		// an honest status instead.
		q := New(t.TempDir())
		q.Now = time.Now
		id, err := q.Enqueue("target", "sender", "hello")
		if err != nil {
			t.Fatal(err)
		}
		target := &fakeTarget{alive: true, pane: "❯  ", sendErr: bptmux.ErrUnverified}
		var reports []string
		if err = q.Dispatch(context.Background(), target, func(m string) { reports = append(reports, m) }); err != nil {
			t.Fatal(err)
		}
		if _, err = os.Stat(filepath.Join(q.pending(), id+".json")); !os.IsNotExist(err) {
			t.Fatalf("unverified message stayed pending (would duplicate), stat err=%v", err)
		}
		status, err := q.Status(id)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(status, "DELIVERED (UNVERIFIED)") {
			t.Fatalf("status=%q", status)
		}
		if !strings.Contains(strings.Join(reports, "\n"), "UNVERIFIED") {
			t.Fatalf("reports=%v", reports)
		}
	})
}
