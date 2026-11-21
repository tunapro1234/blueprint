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

// liveMentionText is q915605609's text (2026-11-21), stored before the
// trailing-mention rule: it ends in "@pr", with no safe-form space.
const liveMentionText = "[server-whatsapp] [kopru] GEC TESLIMAT (15:01Z, ilk teslimat dogrulanamadi, transcriptte yok): [WA:VBAI/PR 26-27|Utku#ACBDD74A83942B40F25C85243FE60D0D] @pr"

// writeLegacyRecord puts a record on disk exactly as an older bp wrote it,
// bypassing intake neutralization.
func writeLegacyRecord(t *testing.T, q *Queue, message Message) string {
	t.Helper()
	if err := os.MkdirAll(q.pending(), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(q.pending(), message.ID+".json")
	if err := writePending(path, message); err != nil {
		t.Fatal(err)
	}
	return path
}

func dispatchPass(t *testing.T, q *Queue, target Target, now *time.Time) {
	t.Helper()
	*now = now.Add(30 * time.Second)
	if err := q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
}

func TestPastedMentionRecordIsClearedAndResentBeforeALaterForce(t *testing.T) {
	// The live sequence. An ordinary record was pasted and could not be
	// verified; the picker completed its "@pr" to "@probot-egitim" in the
	// composer. Fourteen minutes later a forced record arrived. Before the fix
	// the forced record became the head, was refused for "composer contains
	// foreign text", and the pasted record never got its recovery pass.
	now := time.Date(2026, 11, 20, 15, 1, 0, 0, time.Local)
	q := heldQueue(t, &now)
	pasted := now
	writeLegacyRecord(t, q, Message{
		ID: "q915605609", To: "server-necati-vbaipr", From: "server-whatsapp", Msg: liveMentionText,
		TS: float64(pasted.UnixNano()) / 1e9, NoRepaste: true, Reason: unverifiedReason,
		UnverifiedAt:   float64(pasted.Add(5*time.Second).UnixNano()) / 1e9,
		AttemptBinding: q.Binding("server-necati-vbaipr"),
	})
	now = now.Add(14 * time.Minute)
	forced, err := q.EnqueueForce("server-necati-vbaipr", "whatsapp", "[wa] Utku: second message for the group")
	if err != nil {
		t.Fatal(err)
	}
	completed := strings.TrimSuffix(liveMentionText, "@pr") + "@probot-egitim "
	target := &fakeTarget{alive: true, pane: composerPane(completed)}

	// Pass 1: the pasted record is the head, its completed copy is cleared.
	dispatchPass(t, q, target, &now)
	if len(target.cleared) != 1 || len(target.submitted) != 0 || len(target.sent) != 0 {
		t.Fatalf("pass 1: cleared=%v submitted=%v sent=%v", target.cleared, target.submitted, target.sent)
	}
	record, err := q.Record("q915605609")
	if err != nil || record.NoRepaste || record.TornClears != 1 || record.Reason != tornClearedReason {
		t.Fatalf("pass 1: record=%+v err=%v", record, err)
	}
	if reason := q.Reason(forced); !strings.Contains(reason, "q915605609 is ahead") {
		t.Fatalf("pass 1: forced record reason=%q, want it queued behind the pasted record", reason)
	}

	// Pass 2: resent in the safe form; the forced record waits for the next pass.
	dispatchPass(t, q, target, &now)
	// Pass 3: the forced record.
	dispatchPass(t, q, target, &now)

	want := []string{
		"server-necati-vbaipr:" + liveMentionText + " ",
		"server-necati-vbaipr:[wa] Utku: second message for the group",
	}
	if strings.Join(target.sent, "\n") != strings.Join(want, "\n") {
		t.Fatalf("deliveries=%q, want %q", target.sent, want)
	}
	if len(target.forced) != 1 || len(target.cleared) != 1 {
		t.Fatalf("forced=%v cleared=%v", target.forced, target.cleared)
	}
	if status, ok := q.Finished("q915605609"); !ok || status != "delivered" {
		t.Fatalf("pasted record status=%q closed=%v", status, ok)
	}
	if status, ok := q.Finished(forced); !ok || status != "delivered" {
		t.Fatalf("forced record status=%q closed=%v", status, ok)
	}
}

func TestEnqueueStoresTrailingMentionInSafeFormAndDeduplicates(t *testing.T) {
	q := New(t.TempDir())
	original := "[sender] please check this with @pr"
	id, err := q.Enqueue("target", "sender", original)
	if err != nil {
		t.Fatal(err)
	}
	record, err := q.Record(id)
	if err != nil || record.Msg != original+" " {
		t.Fatalf("stored message=%q err=%v, want the safe form", record.Msg, err)
	}
	duplicate, err := q.EnqueueUnique("target", "sender", original, false, time.Minute)
	if err != nil || duplicate != id {
		t.Fatalf("duplicate channel=%q err=%v, want %q", duplicate, err, id)
	}
	if found, ok := q.RecentIdentical("target", original, time.Minute); !ok || found.ID != id {
		t.Fatalf("RecentIdentical=%+v ok=%v", found, ok)
	}
}

func TestTrailingMentionIsDeliveredInSafeFormAndWitnessed(t *testing.T) {
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.Local)
	q := heldQueue(t, &now)
	text := "[server-whatsapp] [kopru] message for the group, see @pr"
	// Claude Code stores the submitted text without the trailing space, and the
	// transcript witness trims what it looks for (book.transcriptTextProbes).
	transcript := ""
	q.Witness = func(_ string, wire string, _ time.Time) bool {
		return transcript != "" && strings.TrimSpace(wire) == transcript
	}
	id, err := q.Enqueue("target", "sender", text)
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane(""), sendErr: fmt.Errorf("%w: test", bptmux.ErrUnverified)}
	dispatchPass(t, q, target, &now)
	if target.calls != 1 {
		t.Fatalf("send attempts=%d", target.calls)
	}
	record, err := q.Record(id)
	if err != nil || !record.NoRepaste || record.Wire() != text+" " {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	transcript = text
	dispatchPass(t, q, target, &now)
	if status, ok := q.Finished(id); !ok || status != "delivered (found in transcript)" {
		t.Fatalf("status=%q closed=%v", status, ok)
	}
	if target.calls != 1 {
		t.Fatalf("the witnessed record was pasted again: %d attempts", target.calls)
	}
}

func TestMixedLegacyPendingRecordsDrainInOrder(t *testing.T) {
	// Records written by an older bp: no safe-form space, and one of them
	// ends in a mention. They must drain in send order, the mention one in the
	// safe form, without duplicates.
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.Local)
	q := heldQueue(t, &now)
	texts := []string{
		"[ada] first legacy message without any mention",
		"[ada] second legacy message, ask @pr",
		"[ada] third legacy message mentions @pr in the middle",
	}
	for i, text := range texts {
		ts := now.Add(time.Duration(i) * time.Second)
		writeLegacyRecord(t, q, Message{ID: fmt.Sprintf("q00000000%d", i), To: "target", From: "ada", Msg: text,
			TS: float64(ts.UnixNano()) / 1e9})
	}
	forced, err := q.EnqueueForce("target", "wa", "[wa] Tuna: forced after the legacy records @me")
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane("")}
	for pass := 0; pass < 6; pass++ {
		dispatchPass(t, q, target, &now)
	}
	want := []string{
		"target:[wa] Tuna: forced after the legacy records @me ",
		"target:" + texts[0],
		"target:" + texts[1] + " ",
		"target:" + texts[2],
	}
	if strings.Join(target.sent, "\n") != strings.Join(want, "\n") {
		t.Fatalf("deliveries=%q, want %q", target.sent, want)
	}
	if status, ok := q.Finished(forced); !ok || status != "delivered" {
		t.Fatalf("forced status=%q closed=%v", status, ok)
	}
}

func TestInFlightRecordWaitingOnlyForTheWitnessLetsAForceRecordPass(t *testing.T) {
	// A pasted record whose binding changed (the agent restarted) cannot be
	// recovered and waits for the witness, with nothing of it in the composer.
	// Before in-flight records were sorted first, a force record went ahead of
	// it; it still must, or a WhatsApp message would wait out the 15-minute
	// witness window. Ordinary records keep waiting behind it.
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.Local)
	q := heldQueue(t, &now)
	writeLegacyRecord(t, q, Message{
		ID: "q100000000", To: "target", From: "ada", Msg: "[ada] pasted before the agent restarted",
		TS: float64(now.UnixNano()) / 1e9, NoRepaste: true, Reason: unverifiedReason,
		UnverifiedAt: float64(now.UnixNano()) / 1e9, AttemptBinding: "claude:old-thread:1",
	})
	now = now.Add(time.Second)
	ordinary, err := q.Enqueue("target", "ada", "[ada] ordinary message behind it")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	forced, err := q.EnqueueForce("target", "wa", "[wa] Tuna: urgent message")
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane("")}
	dispatchPass(t, q, target, &now)
	if len(target.sent) != 1 || !strings.Contains(target.sent[0], "urgent message") {
		t.Fatalf("sent=%v, want the forced record delivered past the witness wait", target.sent)
	}
	if _, ok := q.Finished(forced); !ok {
		t.Fatal("forced record not closed")
	}
	dispatchPass(t, q, target, &now)
	if len(target.sent) != 1 {
		t.Fatalf("the ordinary record overtook the in-flight record: %v", target.sent)
	}
	if reason := q.Reason(ordinary); !strings.Contains(reason, "q100000000 is ahead") {
		t.Fatalf("ordinary reason=%q", reason)
	}
}

func TestInFlightRecordInSettleGraceHoldsAForceRecord(t *testing.T) {
	// Right after an unverified paste the composer may still show the text, so
	// nothing may be pasted on top of it, forced or not.
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.Local)
	q := heldQueue(t, &now)
	writeLegacyRecord(t, q, Message{
		ID: "q200000000", To: "target", From: "ada", Msg: "[ada] pasted a moment ago and unverified",
		TS: float64(now.UnixNano()) / 1e9, NoRepaste: true, Reason: unverifiedReason,
		UnverifiedAt: float64(now.UnixNano()) / 1e9, AttemptBinding: q.Binding("target"),
	})
	forced, err := q.EnqueueForce("target", "wa", "[wa] Tuna: arrives right after")
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane("")}
	now = now.Add(10 * time.Second)
	if err := q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 0 {
		t.Fatalf("a force record was pasted inside the settle grace: %v", target.sent)
	}
	if reason := q.Reason(forced); !strings.Contains(reason, "q200000000 is ahead") {
		t.Fatalf("forced reason=%q", reason)
	}
}

func TestClearedInFlightRecordFacingABusyPaneLetsAForceRecordPass(t *testing.T) {
	// The torn copy is gone, the resend waits for the turn to end. A force
	// record exists to reach a working agent, so it does not wait with it.
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.Local)
	busy := true
	q := busyForceQueue(t, &now, &busy)
	writeLegacyRecord(t, q, Message{
		ID: "q300000000", To: "target", From: "ada", Msg: "[ada] cleared and waiting to be sent again",
		TS: float64(now.UnixNano()) / 1e9, TornClears: 1, Reason: tornClearedReason,
	})
	now = now.Add(time.Second)
	if _, err := q.EnqueueForce("target", "wa", "[wa] Tuna: reach the working agent"); err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane("")}
	dispatchPass(t, q, target, &now)
	if len(target.forced) != 1 || len(target.sent) != 1 {
		t.Fatalf("forced=%v sent=%v, want the forced record through", target.forced, target.sent)
	}
	busy = false
	dispatchPass(t, q, target, &now)
	if len(target.sent) != 2 || !strings.Contains(target.sent[1], "cleared and waiting") {
		t.Fatalf("sent=%v, want the cleared record resent once the turn ended", target.sent)
	}
}
