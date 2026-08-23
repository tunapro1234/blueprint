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
	calls   int
	sendErr error // when set, Send returns it instead of recording the delivery
	// cleared records every ClearDelivered call and, when the pane provably holds
	// one of the texts, empties it — the way the real client's C-u loop does.
	cleared   [][]string
	submitted []string
	submitErr error
	clearErr  error
	// forced lists the deliveries that came through SendForce — the door that may
	// type into a working pane.
	forced []string
	// sessions, when non-nil, overrides alive per session name.
	sessions map[string]bool
}

func (f *fakeTarget) HasSession(_ context.Context, name string) bool {
	// sessions, when set, answers per name — the notice-home tests need a world
	// where the TARGET exists but the sender's label does not (the real shape:
	// the bridge signs "whatsapp", its session is "server-whatsapp").
	if f.sessions != nil {
		return f.sessions[name]
	}
	return f.alive
}
func (f *fakeTarget) Capture(context.Context, string) (string, error) { return f.pane, nil }
func (f *fakeTarget) Send(_ context.Context, to, text string) error {
	// calls counts every ATTEMPT, including the failing ones: the retry bounds and
	// the never-paste-again flag are about how often bp touches a pane, which a
	// list of successful deliveries cannot show.
	f.calls++
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, to+":"+text)
	return nil
}

// SendForce records the same way Send does, plus the fact that it was the FORCE
// door: a test that expects a message to jump a working pane must be able to
// prove it did not sneak in through the ordinary one.
func (f *fakeTarget) SendForce(ctx context.Context, to, text string) error {
	f.forced = append(f.forced, to+":"+text)
	return f.Send(ctx, to, text)
}

func (f *fakeTarget) CaptureAnsi(ctx context.Context, session string) (string, error) {
	return f.Capture(ctx, session)
}

// SubmitStuck mirrors the real client: it presses Enter only on a composer
// holding the text EXACTLY, and reports whether the box then cleared. The
// fake's submitted list lets a test assert that dispatch finished its own
// hanging paste instead of waiting the witness window out.
func (f *fakeTarget) SubmitStuck(_ context.Context, _ string, texts []string) (bool, error) {
	if f.submitErr != nil {
		return false, f.submitErr
	}
	for _, text := range texts {
		if composerPane(text) != f.pane {
			continue
		}
		f.submitted = append(f.submitted, text)
		f.pane = composerPane("")
		return true, nil
	}
	return false, nil
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

	t.Run("unverified with no witness is closed, never retried", func(t *testing.T) {
		// The keystrokes went in unconfirmed AND nothing could ever settle the
		// record (CanWitness is unset here, as it is for a text too short to
		// identify). Leaving it pending would paste the same message again on the
		// next pass, so it is finished with an honest status instead.
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

// witnessable is a message long enough for the transcript witness to identify,
// which is the condition for holding an unconfirmed delivery open instead of
// closing it blind.
const witnessable = "[ders-main] tek mesaj uc kere teslim edildi; bu kaydin kapanmasi transcript tanigina bagli"

// The bridge signs its messages "whatsapp" but lives in the session
// "server-whatsapp": a notice queued to the LABEL targets a session that does
// not exist and would be cancelled as "target closed" on the next pass — the
// notification channel swallowing its own notifications. Found by
// server-whatsapp on 2026-08-21, before the first notice was ever lost.
func TestNoticeFromThePlumbingReachesItsHome(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	q := New(t.TempDir())
	q.Now = func() time.Time { return now }
	id, err := q.EnqueueUnverified("target", "whatsapp", witnessable)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(witnessWindow + time.Minute)
	target := &fakeTarget{pane: "❯  ", sessions: map[string]bool{"target": true, "server-whatsapp": true}}
	if err := q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	messages, err := q.List()
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages=%v err=%v", messages, err)
	}
	if messages[0].To != "server-whatsapp" || !strings.Contains(messages[0].Msg, id) {
		t.Fatalf("notice went to %q, want the plumbing's HOME session: %+v", messages[0].To, messages[0])
	}
}

// A sender whose label resolves to NO session gets its warning in the LOG, not
// as a record queued into the void: an undeliverable notification is not a
// notification, and pretending otherwise hides exactly the failures the notice
// channel exists to surface.
func TestUndeliverableNoticeIsLoudInsteadOfQueued(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	q := New(t.TempDir())
	q.Now = func() time.Time { return now }
	if _, err := q.EnqueueUnverified("target", "ghost-sender", witnessable); err != nil {
		t.Fatal(err)
	}
	now = now.Add(witnessWindow + time.Minute)
	target := &fakeTarget{pane: "❯  ", sessions: map[string]bool{"target": true}}
	var reports []string
	if err := q.Dispatch(context.Background(), target, func(m string) { reports = append(reports, m) }); err != nil {
		t.Fatal(err)
	}
	if messages, err := q.List(); err != nil || len(messages) != 0 {
		t.Fatalf("a doomed notice was queued anyway: %v (err=%v)", messages, err)
	}
	if joined := strings.Join(reports, "\n"); !strings.Contains(joined, "TESLIM EDILEMIYOR") {
		t.Fatalf("no loud line about the undeliverable notice:\n%s", joined)
	}
}

// heldQueue is a queue whose transcript witness can identify long messages but
// finds nothing yet — the state every unconfirmed delivery starts in.
func heldQueue(t *testing.T, now *time.Time) *Queue {
	t.Helper()
	q := New(t.TempDir())
	q.Now = func() time.Time { return *now }
	q.CanWitness = func(text string) bool { return len([]rune(text)) >= 24 }
	return q
}

func TestDispatchHoldsAnUnverifiedDeliveryForTheWitness(t *testing.T) {
	// The old behavior closed this record on the spot as "delivered (unverified)".
	// If the text never landed, that DROPS a message in silence; if it did land,
	// the transcript would have closed the record anyway. So the record is kept
	// open and marked never-paste-again: nothing can duplicate the message, and
	// the witness gets the chance to settle it.
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.Local)
	q := heldQueue(t, &now)
	id, err := q.Enqueue("target", "sender", witnessable)
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: "❯  ", sendErr: bptmux.ErrUnverified}
	var reports []string
	if err = q.Dispatch(context.Background(), target, func(m string) { reports = append(reports, m) }); err != nil {
		t.Fatal(err)
	}
	record, err := read(filepath.Join(q.pending(), id+".json"))
	if err != nil {
		t.Fatalf("record was closed instead of held: %v", err)
	}
	if !record.NoRepaste || record.Reason != unverifiedReason {
		t.Fatalf("record=%+v", record)
	}
	status, err := q.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "yeniden paste edilmeyecek") || !strings.Contains(status, "transcript tanigi") {
		t.Fatalf("status=%q", status)
	}
	if !strings.Contains(strings.Join(reports, "\n"), "UNVERIFIED") {
		t.Fatalf("reports=%v", reports)
	}

	// Next pass: the record must not be pasted again, whatever the pane says.
	target.sendErr = nil
	now = now.Add(time.Minute)
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if target.calls != 1 {
		t.Fatalf("a never-paste-again record was sent again: %d attempts", target.calls)
	}

	// And the witness closes it, exactly as for any other delivered message.
	q.Witness = func(_, text string, _ time.Time) bool { return text == witnessable }
	now = now.Add(time.Minute)
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	status, err = q.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "FOUND IN TRANSCRIPT") {
		t.Fatalf("status=%q", status)
	}
}

func TestUnsettledRecordClosesAndTellsTheSender(t *testing.T) {
	// The witness never spoke. After witnessWindow the queue stops waiting, closes
	// the record with the honest status for the road it took, and tells the SENDER
	// once — an unknown nobody hears about is how a message goes missing quietly.
	for _, tc := range []struct {
		name     string
		sendErr  error
		status   string
		fastFwd  time.Duration
		wantSend int
	}{
		{"unverified injection", bptmux.ErrUnverified, "DELIVERED (UNVERIFIED)", witnessWindow + time.Minute, 1},
		{"three unverifiable attempts", fmt.Errorf("%w: %s", bptmux.ErrNotReady, "composer'da baska metin var"), "NOT DELIVERED (VERIFICATION FAILED)", witnessWindow + time.Minute, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.Local)
			q := heldQueue(t, &now)
			id, err := q.Enqueue("target", "sender", witnessable)
			if err != nil {
				t.Fatal(err)
			}
			target := &fakeTarget{alive: true, pane: "❯  ", sendErr: tc.sendErr}
			// Enough passes (each past its backoff, all of them still inside
			// witnessWindow) to reach the never-paste-again state by whichever road
			// this case takes.
			for pass := 0; pass < 3; pass++ {
				if err = q.Dispatch(context.Background(), target, nil); err != nil {
					t.Fatal(err)
				}
				now = now.Add(3 * time.Minute)
			}
			if target.calls != tc.wantSend {
				t.Fatalf("send attempts=%d, want %d", target.calls, tc.wantSend)
			}
			now = now.Add(tc.fastFwd)
			var reports []string
			if err = q.Dispatch(context.Background(), target, func(m string) { reports = append(reports, m) }); err != nil {
				t.Fatal(err)
			}
			status, err := q.Status(id)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(status, tc.status) {
				t.Fatalf("status=%q, want %s", status, tc.status)
			}
			// The notice: one new pending record, addressed to the sender, from bp,
			// naming the id, the target, how to look and the head of the message.
			messages, err := q.List()
			if err != nil {
				t.Fatal(err)
			}
			if len(messages) != 1 {
				t.Fatalf("expected exactly one notice, got %d: %+v", len(messages), messages)
			}
			notice := messages[0]
			if notice.To != "sender" || notice.From != "bp" {
				t.Fatalf("notice=%+v", notice)
			}
			for _, want := range []string{id, "target hedefine", "bp peek target", witnessable[:60]} {
				if !strings.Contains(notice.Msg, want) {
					t.Fatalf("notice %q does not mention %q", notice.Msg, want)
				}
			}
			if !strings.Contains(strings.Join(reports, "\n"), "haberdar edildi") {
				t.Fatalf("reports=%v", reports)
			}
			// Once only: the notice record itself must not spawn another one, and a
			// second pass must not repeat this one.
			if err = q.Dispatch(context.Background(), &fakeTarget{alive: true, pane: "❯  "}, nil); err != nil {
				t.Fatal(err)
			}
			if messages, err = q.List(); err != nil || len(messages) > 1 {
				t.Fatalf("the notice was repeated: %+v (err=%v)", messages, err)
			}
		})
	}
}

func TestUnsettledRecordKeepsQuietWhenThereIsNobodyToTell(t *testing.T) {
	// A notice is worth sending only to a real sender who is not the target: bp
	// telling itself, or an agent being told about the message it sent to itself,
	// is noise at best and a loop at worst.
	for _, tc := range []struct{ name, from, to string }{
		{"no sender recorded", "", "target"},
		{"bp is its own sender", "bp", "target"},
		{"sender is the target", "target", "target"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.Local)
			q := heldQueue(t, &now)
			if _, err := q.Enqueue(tc.to, tc.from, witnessable); err != nil {
				t.Fatal(err)
			}
			target := &fakeTarget{alive: true, pane: "❯  ", sendErr: bptmux.ErrUnverified}
			if err := q.Dispatch(context.Background(), target, nil); err != nil {
				t.Fatal(err)
			}
			now = now.Add(witnessWindow + time.Minute)
			if err := q.Dispatch(context.Background(), target, nil); err != nil {
				t.Fatal(err)
			}
			messages, err := q.List()
			if err != nil {
				t.Fatal(err)
			}
			if len(messages) != 0 {
				t.Fatalf("a notice was queued anyway: %+v", messages)
			}
		})
	}
}

func TestProvenFailureBacksOffAndStopsAtThreeAttempts(t *testing.T) {
	// The measured loop (q163159804): the same message pasted into the same pane
	// every 30 seconds because the screen kept returning "provably not delivered".
	// Now each failure costs a doubling pause, and the third one ends the pasting
	// for good — whatever the screen claims after that, bp waits for the
	// transcript instead of adding another copy.
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.Local)
	q := heldQueue(t, &now)
	id, err := q.Enqueue("target", "sender", witnessable)
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: "❯  ", sendErr: fmt.Errorf("%w: %s", bptmux.ErrNotReady, "composer'da baska metin var: paste hic girmemis")}
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	record, err := read(filepath.Join(q.pending(), id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if record.Attempts != 1 || record.NextTry == 0 || record.Reason != "composer'da baska metin var: paste hic girmemis" {
		t.Fatalf("record=%+v", record)
	}
	status, err := q.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "sonraki deneme ~") {
		t.Fatalf("status=%q", status)
	}
	// Inside the backoff window nothing is pasted again.
	now = now.Add(notReadyBackoff / 2)
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if target.calls != 1 {
		t.Fatalf("the backoff was ignored: %d attempts", target.calls)
	}
	// Past it, the retries resume — and stop at the third. The advances stay well
	// inside witnessWindow so this measures the attempt ceiling alone.
	for pass := 0; pass < 3; pass++ {
		now = now.Add(3 * time.Minute)
		if err = q.Dispatch(context.Background(), target, nil); err != nil {
			t.Fatal(err)
		}
	}
	if target.calls != notReadyAttemptMax {
		t.Fatalf("send attempts=%d, want the %d-attempt ceiling", target.calls, notReadyAttemptMax)
	}
	if record, err = read(filepath.Join(q.pending(), id+".json")); err != nil {
		t.Fatal(err)
	}
	if !record.NoRepaste || record.Reason != exhaustedReason {
		t.Fatalf("record=%+v", record)
	}
}

func TestReadKeepsWorkingForRecordsWithoutTheNewFields(t *testing.T) {
	// Back-compat: records written before attempts/noRepaste/notified/nextTry
	// existed must keep loading, and must behave like a fresh, retryable record.
	q := New(t.TempDir())
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.Local)
	q.Now = func() time.Time { return now }
	if err := os.MkdirAll(q.pending(), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"id":"q1","to":"target","from":"sender","msg":"eski kayit, yeni alanlari yok","ts":1755255600.0}` + "\n"
	if err := os.WriteFile(filepath.Join(q.pending(), "q1.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	message, err := read(filepath.Join(q.pending(), "q1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if message.Attempts != 0 || message.NoRepaste || message.Notified || message.NextTry != 0 {
		t.Fatalf("legacy record did not read as a plain pending message: %+v", message)
	}
	target := &fakeTarget{alive: true, pane: "❯  "}
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 1 {
		t.Fatalf("a legacy record was not delivered: %v", target.sent)
	}
}

func TestDispatchTreatsALateBusyPaneAsAnOrdinaryWait(t *testing.T) {
	// The pane began its turn between the capture and the paste, so Send refused
	// to inject anything (ErrBusy). Nothing landed and nothing was proven about
	// the message: it stays pending under the plain busy reason, and it must NOT
	// count as a failed attempt — that budget exists for verdicts, not for waits.
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.Local)
	q := heldQueue(t, &now)
	id, err := q.Enqueue("target", "sender", witnessable)
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: "❯  ", sendErr: bptmux.ErrBusy}
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	record, err := read(filepath.Join(q.pending(), id+".json"))
	if err != nil {
		t.Fatalf("a busy pane closed the record: %v", err)
	}
	if record.Reason != bptmux.BlockedByBusyPane || record.Attempts != 0 || record.NoRepaste {
		t.Fatalf("record=%+v", record)
	}
}

func TestDispatchWaitsForAnOpenTurnTheScreenCannotSee(t *testing.T) {
	// The pane is idle by every screen signal there is — an empty composer, no
	// spinner — and the agent is nonetheless mid-turn, streaming an answer. That
	// combination is not hypothetical: it was measured for 147 uninterrupted
	// seconds on 2026-08-15, and it is the whole reason for the second gate.
	// Nothing may be typed while it holds, and the record must say why.
	q := New(t.TempDir())
	q.Now = time.Now
	turnOpen := true
	var asked []string
	q.TurnOpen = func(to string) bool {
		asked = append(asked, to)
		return turnOpen
	}
	id, err := q.Enqueue("target", "sender", "streaming sirasinda gelen mesaj")
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane("")}
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 0 || target.calls != 0 {
		t.Fatalf("message was typed into a pane that was mid-turn: sent=%v calls=%d", target.sent, target.calls)
	}
	if len(asked) != 1 || asked[0] != "target" {
		t.Fatalf("the transcript gate was asked about %v", asked)
	}
	rows, err := q.List()
	if err != nil || len(rows) != 1 {
		t.Fatalf("list=%v err=%v", rows, err)
	}
	// The reason is the SAME one a visibly busy pane produces: which gate noticed
	// is bp's business, not the operator's.
	if rows[0].Reason != bptmux.BlockedByBusyPane {
		t.Fatalf("reason=%q, want %q", rows[0].Reason, bptmux.BlockedByBusyPane)
	}
	// The turn ends; the very next pass delivers, with no further nudging.
	turnOpen = false
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 1 {
		t.Fatalf("sent=%v", target.sent)
	}
	if _, err = os.Stat(filepath.Join(q.done(), id+".json")); err != nil {
		t.Fatalf("delivered record was not moved to done: %v", err)
	}
}

// --- strict per-target FIFO --------------------------------------------------

// fifoQueue returns a queue whose clock the caller drives, so a test can mint records
// whose ids and send times DISAGREE — the state the 2026-08-17 reordering needed.
func fifoQueue(t *testing.T, clock *time.Time) *Queue {
	t.Helper()
	q := New(t.TempDir())
	q.Now = func() time.Time { return *clock }
	return q
}

func TestDispatchDeliversInSendOrderNotInIDOrder(t *testing.T) {
	// The measured incident, in miniature. Ids are minted from the nanoseconds
	// WITHIN the second, so a message sent at 10:43 can be called "q950734611" while
	// one sent at 12:55 is "q436531039". Sorted by file name — which is what
	// Dispatch used to do — the 12:55 message went first, and probot-outreach
	// received a "DUR/IPTAL" correction AFTER the instruction it cancelled.
	clock := time.Date(2026, 8, 17, 10, 43, 30, 950734611, time.Local)
	q := fifoQueue(t, &clock)
	first, err := q.Enqueue("target", "sender", "[probot-business] birinci talimat: kosuyu baslat")
	if err != nil {
		t.Fatal(err)
	}
	clock = time.Date(2026, 8, 17, 12, 55, 13, 436531039, time.Local)
	second, err := q.Enqueue("target", "sender", "[probot-business] DUR, onceki talimati IPTAL ET")
	if err != nil {
		t.Fatal(err)
	}
	if second >= first {
		t.Fatalf("fixture is not the incident: ids %s then %s sort in send order by accident", first, second)
	}
	target := &fakeTarget{alive: true, pane: composerPane("")}
	// Two passes, because a target takes at most one paste per pass.
	for pass := 0; pass < 2; pass++ {
		clock = clock.Add(30 * time.Second)
		if err = q.Dispatch(context.Background(), target, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(target.sent) != 2 {
		t.Fatalf("sent=%v", target.sent)
	}
	if !strings.Contains(target.sent[0], "birinci talimat") || !strings.Contains(target.sent[1], "IPTAL") {
		t.Fatalf("delivered out of send order: %v", target.sent)
	}
}

func TestDispatchDeliversOneMessagePerTargetPerPass(t *testing.T) {
	// Atomicity, as measured from the other side: op-main was handed three messages
	// inside a single second (13:03:37), because one pass pasted them back to back.
	// Nothing verifies that the first message left the composer within one pass —
	// only the NEXT pass's capture and witness can — so a pass delivers once per
	// target and the rest of the line waits, with a reason that says so.
	clock := time.Date(2026, 8, 17, 13, 3, 37, 0, time.Local)
	q := fifoQueue(t, &clock)
	first, err := q.Enqueue("target", "sender", "[server-main] birinci mesaj")
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	second, err := q.Enqueue("target", "sender", "[server-main] ikinci mesaj")
	if err != nil {
		t.Fatal(err)
	}
	// A third message for a DIFFERENT target proves the rule is per target, not a
	// global one-message-per-pass throttle.
	clock = clock.Add(time.Second)
	if _, err = q.Enqueue("other", "sender", "[server-main] baska hedefe"); err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane("")}
	clock = clock.Add(30 * time.Second)
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 2 {
		t.Fatalf("want one delivery per target in one pass, got %v", target.sent)
	}
	if _, err = os.Stat(filepath.Join(q.done(), first+".json")); err != nil {
		t.Fatalf("the head of the line was not delivered: %v", err)
	}
	waiting, err := read(filepath.Join(q.pending(), second+".json"))
	if err != nil {
		t.Fatalf("the second message did not stay pending: %v", err)
	}
	if !strings.Contains(waiting.Reason, first) {
		t.Fatalf("reason=%q, want it to name %s", waiting.Reason, first)
	}
	clock = clock.Add(30 * time.Second)
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 3 || !strings.Contains(target.sent[2], "ikinci mesaj") {
		t.Fatalf("the next pass did not deliver the second message: %v", target.sent)
	}
}

func TestDispatchKeepsAYoungMessageBehindABlockedHead(t *testing.T) {
	// The head of the line could not be delivered (a proven failure put it in
	// backoff), and the composer is wide open. Nothing younger may overtake it: an
	// instruction and its correction arriving in the wrong order is worse than both
	// arriving late.
	clock := time.Date(2026, 8, 17, 10, 43, 0, 0, time.Local)
	q := fifoQueue(t, &clock)
	head, err := q.Enqueue("target", "sender", "[probot-business] birinci talimat: kosuyu baslat")
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(12 * time.Second)
	young, err := q.Enqueue("target", "sender", "[probot-business] DUR, onceki talimati IPTAL ET")
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane(""), sendErr: fmt.Errorf("%w: login expired", bptmux.ErrNotReady)}
	clock = clock.Add(30 * time.Second)
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	// One attempt only: the head's. The young record was not even offered to Send.
	if target.calls != 1 {
		t.Fatalf("a young record was pasted past a blocked head: %d attempts", target.calls)
	}
	waiting, err := read(filepath.Join(q.pending(), young+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Reason != headOfLineReason(head) {
		t.Fatalf("reason=%q, want %q", waiting.Reason, headOfLineReason(head))
	}
	// The pane recovers and the backoff expires: the order still holds, one per pass.
	target.sendErr = nil
	clock = clock.Add(2 * time.Minute)
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(30 * time.Second)
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 2 || !strings.Contains(target.sent[0], "birinci talimat") || !strings.Contains(target.sent[1], "IPTAL") {
		t.Fatalf("sent=%v", target.sent)
	}
}

func TestWitnessClosesAYoungRecordWhileTheHeadWaits(t *testing.T) {
	// The head-of-line rule governs ACTIONS, never proof. The witness reports a
	// delivery that already happened, so closing a young record with it cannot
	// reorder anything — and refusing to would leave records open for messages the
	// agent demonstrably has.
	clock := time.Date(2026, 8, 17, 10, 43, 0, 0, time.Local)
	q := fifoQueue(t, &clock)
	head, err := q.Enqueue("target", "sender", "[server-main] beklemede kalacak olan mesaj")
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	young, err := q.Enqueue("target", "sender", stuckText)
	if err != nil {
		t.Fatal(err)
	}
	q.Witness = func(_, text string, _ time.Time) bool { return text == stuckText }
	// A working pane: the head cannot move at all this pass.
	target := &fakeTarget{alive: true, pane: "✻ Working… (23s · Esc to interrupt)\n" + composerPane("")}
	clock = clock.Add(30 * time.Second)
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 0 {
		t.Fatalf("something was pasted into a working pane: %v", target.sent)
	}
	if _, err = os.Stat(filepath.Join(q.done(), young+".json")); err != nil {
		t.Fatalf("the witness could not close a young record: %v", err)
	}
	if _, err = os.Stat(filepath.Join(q.pending(), head+".json")); err != nil {
		t.Fatalf("the head was not left pending: %v", err)
	}
}

func TestWitnessKeepsTheRecordUntilTheHangingCopyIsCleared(t *testing.T) {
	// The last link of the duplicate chain (op-main, 2026-08-17: the same 441
	// characters at 13:03:37 and 13:07:27). The transcript proves the message
	// arrived, so the record used to close on the spot — and the copy still hanging
	// in the composer was then unattributable, so the agent's next turn submitted
	// it. Now the cleanup comes FIRST: a copy that could not be cleared keeps the
	// record open, which is the only thing that can still identify that text as ours.
	clock := time.Date(2026, 8, 17, 13, 3, 37, 0, time.Local)
	q := fifoQueue(t, &clock)
	id, err := q.Enqueue("target", "sender", stuckText)
	if err != nil {
		t.Fatal(err)
	}
	q.Witness = func(_, text string, _ time.Time) bool { return text == stuckText }
	target := &fakeTarget{alive: true, pane: composerPane(stuckText), clearErr: bptmux.ErrBusy}
	var reports []string
	clock = clock.Add(30 * time.Second)
	if err = q.Dispatch(context.Background(), target, func(m string) { reports = append(reports, m) }); err != nil {
		t.Fatal(err)
	}
	record, err := read(filepath.Join(q.pending(), id+".json"))
	if err != nil {
		t.Fatalf("record was closed while its copy was still hanging: %v", err)
	}
	if !record.Cleanup || record.Reason != cleanupReason {
		t.Fatalf("record=%+v", record)
	}
	if !strings.Contains(strings.Join(reports, "\n"), "temizlenemedi") {
		t.Fatalf("reports=%v", reports)
	}
	// While in that state the record is invisible to the send path: its text must
	// never be submitted again, so nothing may press Enter on the copy.
	if texts := q.PendingFor("target"); len(texts) != 0 {
		t.Fatalf("a delivered copy was offered to the send path: %v", texts)
	}
	status, err := q.Status(id)
	if err != nil || !strings.Contains(status, "temizlik bekliyor") {
		t.Fatalf("status=%q err=%v", status, err)
	}

	// The pane frees up: the copy is erased and the record closes for real.
	target.clearErr = nil
	clock = clock.Add(30 * time.Second)
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(q.done(), id+".json")); err != nil {
		t.Fatalf("record was not closed after the cleanup: %v", err)
	}
	if len(target.sent) != 0 || target.calls != 0 {
		t.Fatalf("the delivered text was pasted again: %v", target.sent)
	}
	if bptmux.Typing(target.pane) {
		t.Fatalf("the copy is still in the composer: %q", target.pane)
	}
}

func TestWitnessStopsWaitingForACleanupThatNeverHappens(t *testing.T) {
	// A record cannot be held forever either: if the pane never frees up, the
	// witnessWindow ceiling closes the record and says so out loud, so the operator
	// hears about a composer only a human can clear.
	clock := time.Date(2026, 8, 17, 13, 0, 0, 0, time.Local)
	q := fifoQueue(t, &clock)
	id, err := q.Enqueue("target", "sender", stuckText)
	if err != nil {
		t.Fatal(err)
	}
	q.Witness = func(string, string, time.Time) bool { return true }
	target := &fakeTarget{alive: true, pane: composerPane(stuckText), clearErr: bptmux.ErrBusy}
	clock = clock.Add(30 * time.Second)
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(witnessWindow)
	var reports []string
	if err = q.Dispatch(context.Background(), target, func(m string) { reports = append(reports, m) }); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(q.done(), id+".json")); err != nil {
		t.Fatalf("record was held past the ceiling: %v", err)
	}
	if !strings.Contains(strings.Join(reports, "\n"), "elle bak") {
		t.Fatalf("the uncleared copy was not reported: %v", reports)
	}
}

func TestRecentIdenticalOnlyMatchesTheSameTextInsideTheWindow(t *testing.T) {
	// The lookup behind bp msg's duplicate guard. Same target, same bytes, recent
	// enough: those three together mean "already on the way".
	clock := time.Date(2026, 8, 17, 13, 3, 37, 0, time.Local)
	q := fifoQueue(t, &clock)
	text := "[probot-business] BUSINESS → OP-MAIN — ayni metin ucuncu kez gonderiliyor"
	id, err := q.Enqueue("op-main", "probot-business", text)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(33 * time.Second)
	if found, ok := q.RecentIdentical("op-main", text, 10*time.Minute); !ok || found.ID != id {
		t.Fatalf("found=%+v ok=%v", found, ok)
	}
	if _, ok := q.RecentIdentical("op-main", text+" (farkli)", 10*time.Minute); ok {
		t.Fatal("a different text was reported as already in flight")
	}
	if _, ok := q.RecentIdentical("baska-hedef", text, 10*time.Minute); ok {
		t.Fatal("another target's queue was consulted")
	}
	clock = clock.Add(11 * time.Minute)
	if _, ok := q.RecentIdentical("op-main", text, 10*time.Minute); ok {
		t.Fatal("a record older than the window still blocks a resend")
	}
}

func TestDispatchWaitsWhenAnotherBpHoldsThePane(t *testing.T) {
	// The cross-process half of the merge fix, from the queue's side. Another bp is
	// inside the critical section for this pane (bp open flushing a digest, bp msg
	// pasting a message), so the pass must not capture or type: it records the reason
	// and leaves the message queued.
	defer func(previous time.Duration) { bptmux.PaneLockWait = previous }(bptmux.PaneLockWait)
	bptmux.PaneLockWait = 200 * time.Millisecond
	q := New(t.TempDir())
	q.Now = time.Now
	id, err := q.Enqueue("target", "sender", "[server-main] kilit tutulurken gelen mesaj")
	if err != nil {
		t.Fatal(err)
	}
	release, err := bptmux.AcquirePaneLock(q.Root, "target")
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane("")}
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 0 || target.calls != 0 {
		t.Fatalf("typed into a pane another bp was holding: %v", target.sent)
	}
	record, err := read(filepath.Join(q.pending(), id+".json"))
	if err != nil {
		t.Fatalf("message was not kept: %v", err)
	}
	if record.Reason != bptmux.BlockedByPaneLock {
		t.Fatalf("reason=%q, want %q", record.Reason, bptmux.BlockedByPaneLock)
	}
	// The other bp finishes: the very next pass delivers.
	release()
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 1 {
		t.Fatalf("sent=%v", target.sent)
	}
}

// --- forced delivery into a busy pane ----------------------------------------

// forcedFollowUp is the SECOND forced message in a conversation: long enough for
// the witness to identify, and sharing nothing with the first one, so a composer
// holding one of them can never be mistaken for holding the other.
const forcedFollowUp = "[wa] Tuna: ikinci mesaj — birincisini gorunce haber ver, bekliyorum burada"

// busyForceQueue is the state a forced message exists for: a target whose turn is
// open in its own transcript while the screen shows an idle, empty composer. That
// is the window a WhatsApp message from Tuna used to sit out for a whole turn.
func busyForceQueue(t *testing.T, clock *time.Time, busy *bool) *Queue {
	t.Helper()
	q := heldQueue(t, clock)
	q.TurnOpen = func(string) bool { return *busy }
	return q
}

func TestForcedRecordIsDeliveredWhileTheTargetIsBusy(t *testing.T) {
	// The whole point of the flag: the busy gate is skipped for the forced record
	// and for nothing else. The ordinary message queued a second EARLIER stays
	// where it is.
	now := time.Date(2026, 8, 21, 11, 0, 0, 0, time.Local)
	busy := true
	q := busyForceQueue(t, &now, &busy)
	normal, err := q.Enqueue("target", "ada", "[ada] siradan mesaj, sirasini bekler")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	forced, err := q.EnqueueForce("target", "wa", "[wa] Tuna: acil, hemen bakar misin")
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane("")}
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 1 || !strings.Contains(target.sent[0], "acil") {
		t.Fatalf("sent=%v, want only the forced message", target.sent)
	}
	if _, err = os.Stat(filepath.Join(q.done(), forced+".json")); err != nil {
		t.Fatalf("the forced record was not closed as delivered: %v", err)
	}
	waiting, err := read(filepath.Join(q.pending(), normal+".json"))
	if err != nil {
		t.Fatalf("the ordinary message did not stay pending: %v", err)
	}
	if !strings.Contains(waiting.Reason, forced) {
		t.Fatalf("reason=%q, want it to name the forced record %s", waiting.Reason, forced)
	}
}

func TestForcedRecordsKeepSendOrderAheadOfNormalTraffic(t *testing.T) {
	// Priority is per LINE, not per message: forced records go to the front of
	// their target's queue and keep send order among themselves, and one pass
	// still pastes at most once into a pane.
	now := time.Date(2026, 8, 21, 11, 0, 0, 0, time.Local)
	busy := true
	q := busyForceQueue(t, &now, &busy)
	if _, err := q.Enqueue("target", "ada", "[ada] siradan mesaj, sirasini bekler"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, err := q.EnqueueForce("target", "wa", "[wa] Tuna: birinci forced mesaj"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, err := q.EnqueueForce("target", "wa", "[wa] Tuna: ikinci forced mesaj"); err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane("")}
	now = now.Add(30 * time.Second)
	if err := q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 1 {
		t.Fatalf("a pass pasted more than once into one pane: %v", target.sent)
	}
	now = now.Add(30 * time.Second)
	if err := q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	// The turn ends, so the ordinary message can finally move — behind both
	// forced ones.
	busy = false
	now = now.Add(30 * time.Second)
	if err := q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 3 {
		t.Fatalf("sent=%v", target.sent)
	}
	for index, want := range []string{"birinci forced", "ikinci forced", "siradan mesaj"} {
		if !strings.Contains(target.sent[index], want) {
			t.Fatalf("delivery order=%v, want %q at %d", target.sent, want, index)
		}
	}
	// And only the forced ones went through the door that may type into a working
	// pane: the flag is a property of the record, not of the pass it rode in.
	if len(target.forced) != 2 {
		t.Fatalf("forced=%v, want exactly the two forced messages", target.forced)
	}
}

func TestForcedRecordStillWaitsForSomeoneElsesComposer(t *testing.T) {
	// The flag overrides "the agent is working", never "a human is in the middle
	// of a line". Tuna's rule stands above it: what somebody typed is theirs.
	now := time.Date(2026, 8, 21, 11, 0, 0, 0, time.Local)
	busy := true
	q := busyForceQueue(t, &now, &busy)
	forced, err := q.EnqueueForce("target", "wa", "[wa] Tuna: acil, hemen bakar misin")
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane("elle yazilmis yarim satir, bitmedi")}
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if target.calls != 0 {
		t.Fatalf("a forced message was pasted on top of somebody's composer: %d attempts", target.calls)
	}
	record, err := read(filepath.Join(q.pending(), forced+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if record.Reason != bptmux.BlockedByForeignText {
		t.Fatalf("reason=%q, want %q", record.Reason, bptmux.BlockedByForeignText)
	}
	// And the status says both things: forced, and still waiting for a reason the
	// operator can act on.
	status, err := q.Status(forced)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "PENDING (FORCE)") || !strings.Contains(status, bptmux.BlockedByForeignText) {
		t.Fatalf("status=%q", status)
	}
}

func TestFollowerForcedMessageWaitsForTheWitnessThenForTheCooldown(t *testing.T) {
	// Two forced messages in a row is the dangerous shape: pasting the second into
	// the same TUI window while the first may still be sitting there unsubmitted is
	// the 2026-08-17 merge condition itself. So the follower waits for the witness —
	// and, because the witness can stay silent forever, no longer than forceCooldown
	// after the first text reached the pane.
	now := time.Date(2026, 8, 21, 11, 0, 0, 0, time.Local)
	busy := true
	q := busyForceQueue(t, &now, &busy)
	first, err := q.EnqueueForce("target", "wa", witnessable)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	second, err := q.EnqueueForce("target", "wa", forcedFollowUp)
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane(""), sendErr: bptmux.ErrUnverified}
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	head, err := read(filepath.Join(q.pending(), first+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !head.NoRepaste || head.ForcedAt == 0 {
		t.Fatalf("head=%+v, want a never-paste-again record that recorded when it reached the pane", head)
	}
	// Thirty seconds later the composer is clear again and the witness has said
	// nothing. The follower still waits: nothing has confirmed that the first
	// message actually reached the agent.
	target.sendErr = nil
	now = now.Add(30 * time.Second)
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if target.calls != 1 {
		t.Fatalf("the follower overtook an unconfirmed forced message: %d attempts", target.calls)
	}
	waiting, err := read(filepath.Join(q.pending(), second+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(waiting.Reason, first) {
		t.Fatalf("reason=%q, want it to name the record it is behind (%s)", waiting.Reason, first)
	}
	// Past the ceiling: the follower goes. The first record is NOT abandoned — it
	// stays in the witness channel with its own window.
	now = now.Add(61 * time.Second)
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 1 || !strings.Contains(target.sent[0], "ikinci mesaj") {
		t.Fatalf("sent=%v, want the follower delivered after the cooldown", target.sent)
	}
	if _, err = os.Stat(filepath.Join(q.pending(), first+".json")); err != nil {
		t.Fatalf("the unconfirmed record was dropped instead of left to the witness: %v", err)
	}
}

func TestFollowerForcedMessageGoesAsSoonAsTheWitnessSpeaks(t *testing.T) {
	// The other end of the same rule: the ceiling is a fallback, not the schedule.
	// Once the transcript proves the first message arrived, the follower does not
	// wait out ninety seconds for nothing.
	now := time.Date(2026, 8, 21, 11, 0, 0, 0, time.Local)
	busy := true
	q := busyForceQueue(t, &now, &busy)
	if _, err := q.EnqueueForce("target", "wa", witnessable); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, err := q.EnqueueForce("target", "wa", forcedFollowUp); err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane(""), sendErr: bptmux.ErrUnverified}
	if err := q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	target.sendErr = nil
	q.Witness = func(_, text string, _ time.Time) bool { return text == witnessable }
	now = now.Add(30 * time.Second)
	if err := q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 1 || !strings.Contains(target.sent[0], "ikinci mesaj") {
		t.Fatalf("sent=%v, want the follower delivered as soon as the witness settled the head", target.sent)
	}
}

func TestFollowerForcedMessageStillRefusesAComposerHoldingTheEarlierPaste(t *testing.T) {
	// The ceiling releases the WAIT, never the composer gates. Here the first
	// forced message is still hanging in the composer when the ninety seconds are
	// up: from this record's side that text is foreign (its own record is out of
	// the way, so nothing offers it as ours), and foreign text is never pasted on
	// top of. If this ever fails, the safety is what must be fixed — not the
	// ceiling shortened.
	now := time.Date(2026, 8, 21, 11, 0, 0, 0, time.Local)
	busy := true
	q := busyForceQueue(t, &now, &busy)
	if _, err := q.EnqueueForce("target", "wa", witnessable); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	second, err := q.EnqueueForce("target", "wa", forcedFollowUp)
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane(""), sendErr: bptmux.ErrUnverified}
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	// The unconfirmed paste is exactly what it warned about: still in the box.
	target.pane = composerPane(witnessable)
	target.sendErr = nil
	now = now.Add(91 * time.Second)
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if target.calls != 1 {
		t.Fatalf("the follower was pasted on top of the first forced message: %d attempts", target.calls)
	}
	waiting, err := read(filepath.Join(q.pending(), second+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Reason != bptmux.BlockedByForeignText {
		t.Fatalf("reason=%q, want %q", waiting.Reason, bptmux.BlockedByForeignText)
	}
}

func TestOrdinaryRecordNeverOvertakesAForcedOne(t *testing.T) {
	// The ceiling is for forced records only. An ordinary message behind an
	// unconfirmed forced one waits for it however long that takes, on an idle pane
	// and with the cooldown long gone.
	now := time.Date(2026, 8, 21, 11, 0, 0, 0, time.Local)
	busy := true
	q := busyForceQueue(t, &now, &busy)
	forced, err := q.EnqueueForce("target", "wa", witnessable)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	normal, err := q.Enqueue("target", "ada", "[ada] siradan mesaj, sirasini bekler")
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane(""), sendErr: bptmux.ErrUnverified}
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	target.sendErr = nil
	busy = false
	now = now.Add(120 * time.Second)
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if target.calls != 1 {
		t.Fatalf("an ordinary message overtook an unsettled forced one: %d attempts", target.calls)
	}
	waiting, err := read(filepath.Join(q.pending(), normal+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(waiting.Reason, forced) {
		t.Fatalf("reason=%q, want it to name the forced record %s", waiting.Reason, forced)
	}
}

func TestForceFieldsAreOptionalOnDiskAndSurviveARoundTrip(t *testing.T) {
	// Back-compat both ways: a record written before the flag existed reads as an
	// ordinary one, and an ordinary record still writes neither key.
	q := New(t.TempDir())
	now := time.Date(2026, 8, 21, 11, 0, 0, 0, time.Local)
	q.Now = func() time.Time { return now }
	if err := os.MkdirAll(q.pending(), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"id":"q1","to":"target","from":"sender","msg":"eski kayit, force alanlari yok","ts":1755255600.0}` + "\n"
	if err := os.WriteFile(filepath.Join(q.pending(), "q1.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	old, err := read(filepath.Join(q.pending(), "q1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if old.ForceBusy || old.ForcedAt != 0 {
		t.Fatalf("legacy record did not read as ordinary: %+v", old)
	}
	plain, err := q.Enqueue("target", "sender", "siradan mesaj")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(q.pending(), plain+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "forceBusy") || strings.Contains(string(data), "forcedAt") {
		t.Fatalf("an ordinary record wrote the force keys: %s", data)
	}
	forced, err := q.EnqueueForce("target", "wa", "[wa] Tuna: acil")
	if err != nil {
		t.Fatal(err)
	}
	record, err := read(filepath.Join(q.pending(), forced+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !record.ForceBusy || record.Reason != forceReason {
		t.Fatalf("record=%+v", record)
	}
	status, err := q.Status(forced)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "PENDING (FORCE)") || !strings.Contains(status, forceReason) {
		t.Fatalf("status=%q", status)
	}
}

// busyComposerPane is composerPane with the live spinner on the row just above
// the box: a pane whose SCREEN says the agent is working, which is where a busy
// agent spends most of its time (tool calls and thinking, not streaming).
func busyComposerPane(text string) string {
	return strings.Replace(composerPane(text), "  agent: onceki turdan kalan cikti",
		"✻ Working… (23s · esc to interrupt)", 1)
}

func TestForcedRecordIsPastedIntoAPaneWhoseSpinnerIsRunning(t *testing.T) {
	// The screen-busy plane of the same rule. The transcript gate (streaming) was
	// never the whole story: a working agent shows its spinner most of the time,
	// and a forced message that waited for the spinner to stop would be exactly
	// the "late WhatsApp message" the flag exists to end.
	now := time.Date(2026, 8, 21, 11, 0, 0, 0, time.Local)
	busy := false // the transcript says nothing; the SCREEN is the busy signal here
	q := busyForceQueue(t, &now, &busy)
	forced, err := q.EnqueueForce("target", "wa", witnessable)
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: busyComposerPane("")}
	if !bptmux.Busy(target.pane) {
		t.Fatal("fixture is not a busy pane: the test would prove nothing")
	}
	// A working pane redraws, so the screen cannot confirm the paste. That is the
	// ORDINARY outcome of a forced delivery, not a fault.
	target.sendErr = bptmux.ErrUnverified
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.forced) != 1 {
		t.Fatalf("the forced message did not go through the force door: forced=%v sent=%v", target.forced, target.sent)
	}
	record, err := read(filepath.Join(q.pending(), forced+".json"))
	if err != nil {
		t.Fatalf("the unconfirmed forced record was closed instead of held: %v", err)
	}
	if !record.NoRepaste || record.Reason != unverifiedReason || record.ForcedAt == 0 {
		t.Fatalf("record=%+v, want it waiting for the transcript witness", record)
	}
}

func TestOrdinaryRecordStillWaitsForASpinner(t *testing.T) {
	// The screen gate is untouched for everything that is not forced.
	now := time.Date(2026, 8, 21, 11, 0, 0, 0, time.Local)
	busy := false
	q := busyForceQueue(t, &now, &busy)
	id, err := q.Enqueue("target", "ada", "[ada] siradan mesaj, sirasini bekler")
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: busyComposerPane("")}
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if target.calls != 0 || len(target.forced) != 0 {
		t.Fatalf("an ordinary message was typed into a working pane: calls=%d forced=%v", target.calls, target.forced)
	}
	record, err := read(filepath.Join(q.pending(), id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if record.Reason != bptmux.BlockedByBusyPane {
		t.Fatalf("reason=%q, want %q", record.Reason, bptmux.BlockedByBusyPane)
	}
}

func TestFollowerForcedMessageRefusesAHeldComposerOnAWorkingPaneToo(t *testing.T) {
	// The interlock, on the screen-busy plane: the ninety second ceiling releases
	// the WAIT, never the composer. Here the pane is working AND still holding the
	// first forced paste — which is precisely the state a forced delivery into a
	// redrawing screen can leave behind — so the follower stays out.
	now := time.Date(2026, 8, 21, 11, 0, 0, 0, time.Local)
	busy := false
	q := busyForceQueue(t, &now, &busy)
	if _, err := q.EnqueueForce("target", "wa", witnessable); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	second, err := q.EnqueueForce("target", "wa", forcedFollowUp)
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: busyComposerPane(""), sendErr: bptmux.ErrUnverified}
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.forced) != 1 {
		t.Fatalf("the first forced message did not reach the pane: %v", target.forced)
	}
	// Working pane, first paste still in the box, ceiling long past.
	target.pane = busyComposerPane(witnessable)
	target.sendErr = nil
	now = now.Add(91 * time.Second)
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.forced) != 1 {
		t.Fatalf("the follower was pasted on top of the first forced message: %v", target.forced)
	}
	waiting, err := read(filepath.Join(q.pending(), second+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Reason != bptmux.BlockedByForeignText {
		t.Fatalf("reason=%q, want %q", waiting.Reason, bptmux.BlockedByForeignText)
	}
}

// hermesBusyPane is the live BUSY screen of a Hermes Agent pane, measured
// 2026-08-22 in the tmux session blueprint-hermes-test: a kaomoji spinner frame,
// the status row (model · context% · turn age), and the composer prefixed with the
// caduceus. "msg=interrupt" is Hermes stating what Enter does right now.
func hermesBusyPane() string {
	return strings.Join([]string{
		"╭─ ⚕ Hermes ──────────────────────────────────────╮",
		"onceki cevabin son satiri",
		"╰─────────────────────────────────────────────────╯",
		"  (¬_¬) processing...",
		"",
		" ⚕ x-preview-f-free · 2% · 12m               ─ Say ve /srv dizin...",
		"────────────────────────────────────────────────────────────────────",
		"⚕ ❯ msg=interrupt · /queue · /bg · /steer · Ctrl+C cancel",
		"────────────────────────────────────────────────────────────────────",
		"",
	}, "\n")
}

func TestForcedRecordStillWaitsForABusyHermes(t *testing.T) {
	// The one pane type --force-busy may not jump. On Claude/Codex a forced
	// message is typed into a working pane and interleaves with the turn; on
	// Hermes the same keystrokes CANCEL it — the composer says so itself
	// ("msg=interrupt"), and queueing there needs a /queue prefix, i.e. rewriting
	// the operator's message, which bp does not do. So the record waits, with the
	// ordinary busy reason a human can read in `bp q`.
	now := time.Date(2026, 8, 22, 11, 0, 0, 0, time.Local)
	busy := false // the transcript knows nothing about Hermes: no session file
	q := busyForceQueue(t, &now, &busy)
	forced, err := q.EnqueueForce("target", "wa", witnessable)
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: hermesBusyPane()}
	if !bptmux.Busy(target.pane) {
		t.Fatal("fixture is not a busy Hermes pane: the test would prove nothing")
	}
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if target.calls != 0 || len(target.forced) != 0 {
		t.Fatalf("a forced message interrupted a working Hermes: calls=%d forced=%v", target.calls, target.forced)
	}
	record, err := read(filepath.Join(q.pending(), forced+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if record.Reason != bptmux.BlockedByBusyPane || record.ForcedAt != 0 {
		t.Fatalf("record=%+v, want it waiting like any other busy target", record)
	}
}

// The hole probot-outreach found by hand on 2026-08-23: a delivery bp could not
// verify left its text sitting UNSUBMITTED in the composer. The witness can
// never settle that one — the message is not in the transcript because it was
// never sent — so before this the paste hung until a human pressed Enter.
func TestUnverifiedRecordFinishesItsOwnHangingPaste(t *testing.T) {
	dir := t.TempDir()
	queue := New(dir)
	id, err := queue.EnqueueUnverified("kavram-main", "blueprint", "asili kalan mesaj")
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{sessions: map[string]bool{"kavram-main": true}, pane: composerPane("asili kalan mesaj")}
	queue.Dispatch(context.Background(), target, nil)
	if len(target.submitted) != 1 || target.submitted[0] != "asili kalan mesaj" {
		t.Fatalf("the hanging paste was not finished: %v", target.submitted)
	}
	rows, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("record %s stayed open after its paste was submitted: %+v", id, rows)
	}
	// A composer holding SOMEBODY ELSE's text is never submitted: that would put
	// a human's half-written line into their own agent.
	queue2 := New(t.TempDir())
	if _, err := queue2.EnqueueUnverified("kavram-main", "blueprint", "bizim mesaj"); err != nil {
		t.Fatal(err)
	}
	foreign := &fakeTarget{sessions: map[string]bool{"kavram-main": true}, pane: composerPane("insanin yazdigi bir sey")}
	queue2.Dispatch(context.Background(), foreign, nil)
	if len(foreign.submitted) != 0 {
		t.Fatalf("a foreign composer was submitted: %v", foreign.submitted)
	}
}

// Measured 2026-08-23: an unverified record whose text had LEFT the composer
// held probot-outreach's queue for 200 seconds and told them to press Enter on
// an empty composer. Nothing can finish such a record and the pane it guards is
// free, so it must neither advise an action nor block the messages behind it.
func TestVanishedUnverifiedPasteStopsBlockingAndStopsAdvisingEnter(t *testing.T) {
	queue := New(t.TempDir())
	if _, err := queue.EnqueueUnverified("kavram-main", "blueprint", "kaybolan mesaj"); err != nil {
		t.Fatal(err)
	}
	later, err := queue.Enqueue("kavram-main", "blueprint", "arkadaki mesaj")
	if err != nil {
		t.Fatal(err)
	}
	// The composer is empty: the first record's text is nowhere.
	target := &fakeTarget{sessions: map[string]bool{"kavram-main": true}, pane: composerPane("")}
	queue.Dispatch(context.Background(), target, nil)
	rows, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Reason == hangingPasteReason {
			t.Fatalf("a vanished paste still advises an Enter: %+v", row)
		}
	}
	// The message behind it must have been delivered rather than held.
	if len(target.sent) == 0 || !strings.Contains(target.sent[len(target.sent)-1], "arkadaki mesaj") {
		t.Fatalf("the queue stayed blocked behind a dead record: sent=%v (later=%s)", target.sent, later)
	}
}

// A notice must describe the situation AS IT IS WHEN IT IS SENT. Four of five
// notices in one evening described situations already fixed by hand, and the
// noise hid the one real hanging paste (probot-outreach, 2026-08-23).
func TestNoticeSaysWhetherThePasteIsStillHanging(t *testing.T) {
	hanging := noticeText(Message{ID: "q1", To: "kavram-main", Msg: "asili duran mesaj"}, true)
	if !strings.Contains(hanging, "HALA ASILI") {
		t.Fatalf("a hanging paste was not announced as such: %q", hanging)
	}
	gone := noticeText(Message{ID: "q1", To: "kavram-main", Msg: "asili duran mesaj"}, false)
	if !strings.Contains(gone, "SONRADAN COZULMUS OLABILIR") {
		t.Fatalf("a resolved case was not marked as such: %q", gone)
	}
	// Both keep the parts an operator navigates by.
	for _, text := range []string{hanging, gone} {
		if !strings.Contains(text, "kavram-main hedefine") || !strings.Contains(text, "bp peek kavram-main") {
			t.Fatalf("notice lost its target: %q", text)
		}
	}
}
