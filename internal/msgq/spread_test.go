package msgq

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	bptmux "blueprint/internal/tmux"
)

const spreadInterval = 10 * time.Second

func spreadQueue(t *testing.T, clock *time.Time) *Queue {
	t.Helper()
	return fifoQueue(t, clock)
}

func enqueueSpread(t *testing.T, q *Queue, to, text string) (string, time.Time) {
	t.Helper()
	id, slot, err := q.EnqueueSpread(to, "lead", "[ANNOUNCE lead] "+text, spreadInterval, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return id, slot
}

func pendingRecord(t *testing.T, q *Queue, id string) Message {
	t.Helper()
	message, err := read(filepath.Join(q.pending(), id+".json"))
	if err != nil {
		t.Fatalf("%s is not pending: %v", id, err)
	}
	return message
}

func delivered(t *testing.T, q *Queue, id string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(q.done(), id+".json"))
	return err == nil
}

func dispatchOnce(t *testing.T, q *Queue, target *fakeTarget) {
	t.Helper()
	if err := q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
}

func seen(target *fakeTarget, name string) bool {
	for _, session := range target.sessionsSeen {
		if session == name {
			return true
		}
	}
	return false
}

func TestSpreadSlotsStartNowAndFollowTheSchedule(t *testing.T) {
	clock := time.Date(2026, 11, 28, 12, 0, 0, 0, time.Local)
	q := spreadQueue(t, &clock)
	_, first := enqueueSpread(t, q, "worker-1", "release freeze at noon")
	_, second := enqueueSpread(t, q, "worker-2", "release freeze at noon")
	_, third := enqueueSpread(t, q, "worker-3", "release freeze at noon")
	if !first.Equal(clock) || !second.Equal(clock.Add(spreadInterval)) || !third.Equal(clock.Add(2*spreadInterval)) {
		t.Fatalf("slots %v %v %v, want now, +10s, +20s", first, second, third)
	}
	// A second announcement while the first is still draining continues after
	// its last slot instead of starting a second schedule at now.
	clock = clock.Add(5 * time.Second)
	_, fourth := enqueueSpread(t, q, "worker-1", "second announcement")
	_, fifth := enqueueSpread(t, q, "worker-4", "second announcement")
	if !fourth.Equal(third.Add(spreadInterval)) || !fifth.Equal(third.Add(2*spreadInterval)) {
		t.Fatalf("chained slots %v %v, want %v and %v", fourth, fifth, third.Add(spreadInterval), third.Add(2*spreadInterval))
	}
	// A duplicate returns the pending record and its slot.
	if id, slot, err := q.EnqueueSpread("worker-4", "lead", "[ANNOUNCE lead] second announcement", spreadInterval, 10*time.Minute); err != nil || !slot.Equal(fifth) || pendingRecord(t, q, id).Msg != "[ANNOUNCE lead] second announcement" {
		t.Fatalf("duplicate: id=%s slot=%v err=%v", id, slot, err)
	}
	if got := q.NextSlot(spreadInterval); !got.Equal(fifth.Add(spreadInterval)) {
		t.Fatalf("NextSlot=%v, want %v", got, fifth.Add(spreadInterval))
	}
}

func TestSpreadScheduleRestartsAtNowWhenDrained(t *testing.T) {
	clock := time.Date(2026, 11, 28, 12, 0, 0, 0, time.Local)
	q := spreadQueue(t, &clock)
	first, _ := enqueueSpread(t, q, "worker-1", "news")
	target := &fakeTarget{alive: true, pane: composerPane("")}
	dispatchOnce(t, q, target)
	if !delivered(t, q, first) {
		t.Fatal("the first slot is now and was not delivered")
	}
	clock = clock.Add(time.Second)
	if _, slot := enqueueSpread(t, q, "worker-2", "later news"); !slot.Equal(clock) {
		t.Fatalf("slot=%v, want now (%v) on an empty schedule", slot, clock)
	}
}

func TestHeldAnnouncementWaitsForItsSlotWithoutAnAttempt(t *testing.T) {
	clock := time.Date(2026, 11, 28, 12, 0, 0, 0, time.Local)
	q := spreadQueue(t, &clock)
	first, _ := enqueueSpread(t, q, "worker-1", "news")
	second, slot := enqueueSpread(t, q, "worker-2", "news")
	target := &fakeTarget{alive: true, pane: composerPane("")}
	dispatchOnce(t, q, target)
	if !delivered(t, q, first) {
		t.Fatal("the due slot was not delivered")
	}
	if seen(target, "worker-2") {
		t.Fatal("a held line was probed")
	}
	held := pendingRecord(t, q, second)
	if held.Attempts != 0 || held.Reason != "" || held.NoRepaste || held.AttemptBinding != "" {
		t.Fatalf("a held record was touched: %+v", held)
	}
	if until, ok := held.HeldUntil(clock); !ok || !until.Equal(slot) {
		t.Fatalf("HeldUntil=%v,%v want %v", until, ok, slot)
	}
	status, err := q.Status(second)
	if err != nil || !strings.Contains(status, "PENDING (SCHEDULED): worker-2") || !strings.Contains(status, slot.Format("15:04:05")) {
		t.Fatalf("status=%q err=%v", status, err)
	}
	clock = slot.Add(-time.Millisecond)
	dispatchOnce(t, q, target)
	if delivered(t, q, second) {
		t.Fatal("delivered before its slot")
	}
	clock = slot
	dispatchOnce(t, q, target)
	if !delivered(t, q, second) || len(target.sent) != 2 || !strings.HasPrefix(target.sent[1], "worker-2:") {
		t.Fatalf("not delivered at its slot: %v", target.sent)
	}
}

func TestHeldAnnouncementRidesAlongWithALaterMessage(t *testing.T) {
	// Announcement first, then an ordinary message for the same agent: the
	// ordinary one releases the line, and the line runs in send order.
	clock := time.Date(2026, 11, 28, 12, 0, 0, 0, time.Local)
	q := spreadQueue(t, &clock)
	enqueueSpread(t, q, "worker-1", "news")
	announcement, slot := enqueueSpread(t, q, "worker-2", "news")
	clock = clock.Add(time.Second)
	normal, err := q.Enqueue("worker-2", "lead", "[lead] direct question")
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	q.DeferCold = func(Message) (bool, error) { calls++; return false, nil }
	target := &fakeTarget{alive: true, pane: composerPane("")}
	dispatchOnce(t, q, target)
	clock = clock.Add(time.Second)
	dispatchOnce(t, q, target)
	if clock.After(slot) {
		t.Fatal("fixture reached the slot")
	}
	if !delivered(t, q, announcement) || !delivered(t, q, normal) {
		t.Fatalf("both should be delivered before the slot: %v", target.sent)
	}
	var line []string
	for _, sent := range target.sent {
		if strings.HasPrefix(sent, "worker-2:") {
			line = append(line, sent)
		}
	}
	if len(line) != 2 || !strings.Contains(line[0], "ANNOUNCE") || !strings.Contains(line[1], "direct question") {
		t.Fatalf("worker-2 line out of send order: %v", line)
	}
	if calls != 1 {
		// worker-1's due slot is asked; a released ride-along never is.
		t.Fatalf("DeferCold asked %d times, want only for worker-1's slot", calls)
	}
}

func TestEarlierMessageIsUnaffectedByAHeldAnnouncement(t *testing.T) {
	clock := time.Date(2026, 11, 28, 12, 0, 0, 0, time.Local)
	q := spreadQueue(t, &clock)
	normal, err := q.Enqueue("worker-2", "lead", "[lead] earlier instruction")
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	enqueueSpread(t, q, "worker-1", "news")
	announcement, slot := enqueueSpread(t, q, "worker-2", "news")
	target := &fakeTarget{alive: true, pane: composerPane("")}
	dispatchOnce(t, q, target)
	if !delivered(t, q, normal) || delivered(t, q, announcement) {
		t.Fatalf("the earlier message must go first and alone: %v", target.sent)
	}
	// With the earlier message gone the announcement is held again until its
	// slot: being released once does not make it due.
	clock = clock.Add(time.Second)
	dispatchOnce(t, q, target)
	if delivered(t, q, announcement) {
		t.Fatal("delivered before its slot once the ride-along had left")
	}
	clock = slot
	dispatchOnce(t, q, target)
	if !delivered(t, q, announcement) {
		t.Fatal("not delivered at its slot")
	}
}

func TestHeldAnnouncementNeverAffectsAnotherTarget(t *testing.T) {
	clock := time.Date(2026, 11, 28, 12, 0, 0, 0, time.Local)
	q := spreadQueue(t, &clock)
	enqueueSpread(t, q, "worker-1", "news")
	held, _ := enqueueSpread(t, q, "worker-2", "news")
	other, err := q.Enqueue("worker-3", "lead", "[lead] unrelated")
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane("")}
	dispatchOnce(t, q, target)
	if !delivered(t, q, other) {
		t.Fatal("another target's message was held")
	}
	if delivered(t, q, held) || seen(target, "worker-2") {
		t.Fatal("another target's message released a held line")
	}
}

func TestHeldRecordsSurviveANewQueueInstance(t *testing.T) {
	clock := time.Date(2026, 11, 28, 12, 0, 0, 0, time.Local)
	q := spreadQueue(t, &clock)
	enqueueSpread(t, q, "worker-1", "news")
	held, slot := enqueueSpread(t, q, "worker-2", "news")

	restarted := newBoundTestQueue(q.Root)
	restarted.Now = func() time.Time { return clock }
	if next, ok := restarted.NextRelease(clock); !ok || !next.Equal(slot) {
		t.Fatalf("NextRelease=%v,%v want %v", next, ok, slot)
	}
	target := &fakeTarget{alive: true, pane: composerPane("")}
	dispatchOnce(t, restarted, target)
	if delivered(t, restarted, held) {
		t.Fatal("a restart released the hold")
	}
	clock = slot
	if _, ok := restarted.NextRelease(clock); ok {
		t.Fatal("NextRelease reported a slot at or before after")
	}
	dispatchOnce(t, restarted, target)
	if !delivered(t, restarted, held) {
		t.Fatal("not delivered at its slot after a restart")
	}
}

func TestMixedOldAndNewRecordsLoadTogether(t *testing.T) {
	clock := time.Date(2026, 11, 28, 12, 0, 0, 0, time.Local)
	q := spreadQueue(t, &clock)
	enqueueSpread(t, q, "worker-1", "news")
	held, _ := enqueueSpread(t, q, "worker-2", "news")
	legacy := `{"id":"q1","to":"worker-3","from":"lead","msg":"record written by an older bp","ts":1795000000.0}` + "\n"
	if err := os.WriteFile(filepath.Join(q.pending(), "q1.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err := q.List()
	if err != nil || len(rows) != 3 {
		t.Fatalf("List=%d rows, err=%v", len(rows), err)
	}
	for _, row := range rows {
		if row.ID == "q1" {
			if row.Kind != "" || row.NotBefore != 0 {
				t.Fatalf("legacy record gained schedule fields: %+v", row)
			}
			if _, isHeld := row.HeldUntil(clock); isHeld {
				t.Fatal("a legacy record reads as held")
			}
		}
	}
	data, err := os.ReadFile(filepath.Join(q.pending(), held+".json"))
	if err != nil || !strings.Contains(string(data), `"kind":"announce"`) || !strings.Contains(string(data), `"notBefore":`) {
		t.Fatalf("new record on disk = %s, %v", data, err)
	}
	target := &fakeTarget{alive: true, pane: composerPane("")}
	dispatchOnce(t, q, target)
	if !delivered(t, q, "q1") || delivered(t, q, held) {
		t.Fatalf("sent=%v", target.sent)
	}
}

func TestDueAnnouncementForAColdTargetGoesToTheSpool(t *testing.T) {
	clock := time.Date(2026, 11, 28, 12, 0, 0, 0, time.Local)
	q := spreadQueue(t, &clock)
	cold, _ := enqueueSpread(t, q, "worker-1", "news")
	warm, slot := enqueueSpread(t, q, "worker-2", "news")
	var asked []string
	q.DeferCold = func(m Message) (bool, error) {
		asked = append(asked, m.To)
		return m.To == "worker-1", nil
	}
	target := &fakeTarget{alive: true, pane: composerPane("")}
	dispatchOnce(t, q, target)
	if len(target.sent) != 0 {
		t.Fatalf("a cold target was woken: %v", target.sent)
	}
	record, err := q.Record(cold)
	if err != nil || record.Status != StatusDeferredCold || !IsTerminalDelivery(record.Status) {
		t.Fatalf("cold record=%+v err=%v", record, err)
	}
	clock = slot
	dispatchOnce(t, q, target)
	if !delivered(t, q, warm) || len(target.sent) != 1 {
		t.Fatalf("the warm target was not delivered: %v", target.sent)
	}
	if strings.Join(asked, ",") != "worker-1,worker-2" {
		t.Fatalf("asked=%v", asked)
	}
}

func TestDeferColdFailureHoldsTheRecord(t *testing.T) {
	clock := time.Date(2026, 11, 28, 12, 0, 0, 0, time.Local)
	q := spreadQueue(t, &clock)
	id, _ := enqueueSpread(t, q, "worker-1", "news")
	q.DeferCold = func(Message) (bool, error) { return false, errors.New("spool is read-only") }
	target := &fakeTarget{alive: true, pane: composerPane("")}
	dispatchOnce(t, q, target)
	if len(target.sent) != 0 {
		t.Fatal("pasted although the warm check failed")
	}
	if record := pendingRecord(t, q, id); !strings.Contains(record.Reason, "spool is read-only") || record.Attempts != 0 {
		t.Fatalf("record=%+v", record)
	}
}

func TestRunReleasesDeliversAtTheSlot(t *testing.T) {
	q := newBoundTestQueue(t.TempDir())
	interval := 1500 * time.Millisecond
	first, _, err := q.EnqueueSpread("worker-1", "lead", "[ANNOUNCE lead] news", interval, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, slot, err := q.EnqueueSpread("worker-2", "lead", "[ANNOUNCE lead] news", interval, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane("")}
	dispatchOnce(t, q, target) // the command's own pass takes the first slot
	if !delivered(t, q, first) || delivered(t, q, second) {
		t.Fatalf("sent=%v", target.sent)
	}
	// An ordinary record for another target, queued after the command's pass:
	// a release pass must neither probe nor touch it.
	other, err := q.Enqueue("worker-3", "lead", "[lead] unrelated question")
	if err != nil {
		t.Fatal(err)
	}
	probedBefore := len(target.sessionsSeen)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		q.RunReleases(ctx, target, 50*time.Millisecond, nil)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for !delivered(t, q, second) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	wg.Wait()
	record, err := q.Record(second)
	if err != nil || !IsVerifiedDelivery(record.Status) {
		t.Fatalf("second slot not released: %+v %v", record, err)
	}
	if finished := fromUnixSeconds(record.Finished); finished.Before(slot) {
		t.Fatalf("released at %v, before its slot %v", finished, slot)
	}
	for _, session := range target.sessionsSeen[probedBefore:] {
		if session != "worker-2" {
			t.Fatalf("the release pass probed %s; only the due target may be touched (%v)", session, target.sessionsSeen[probedBefore:])
		}
	}
	untouched := pendingRecord(t, q, other)
	if untouched.Attempts != 0 || untouched.Reason != "" || untouched.AttemptBinding != "" || untouched.NoRepaste {
		t.Fatalf("the release pass touched another target's record: %+v", untouched)
	}
}

func TestDueReleasesListsOnlyTargetsWhoseSlotCame(t *testing.T) {
	clock := time.Date(2026, 11, 28, 12, 0, 0, 0, time.Local)
	q := spreadQueue(t, &clock)
	_, first := enqueueSpread(t, q, "worker-1", "news")
	_, second := enqueueSpread(t, q, "worker-2", "news")
	enqueueSpread(t, q, "worker-3", "news")
	if _, err := q.Enqueue("worker-4", "lead", "[lead] ordinary"); err != nil {
		t.Fatal(err)
	}
	due := q.dueReleases(first, second)
	if len(due) != 1 {
		t.Fatalf("due=%v, want only worker-2", due)
	}
	if _, ok := due["worker-2"]; !ok {
		t.Fatalf("due=%v, want worker-2", due)
	}
	if due := q.dueReleases(second.Add(time.Hour), second.Add(2*time.Hour)); len(due) != 0 {
		t.Fatalf("due=%v after the last slot", due)
	}
}

func TestNextReleaseReadsNothingWithoutASchedule(t *testing.T) {
	q := newBoundTestQueue(t.TempDir())
	if _, err := q.Enqueue("worker-1", "lead", "[lead] ordinary"); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.NextRelease(time.Now().Add(-time.Hour)); ok {
		t.Fatal("a queue without spread records reported a release")
	}
	if _, err := os.Stat(filepath.Join(q.Root, slotMarkFile)); !os.IsNotExist(err) {
		t.Fatalf("ordinary enqueue wrote the slot hint: %v", err)
	}
}

func TestEnqueueSpreadRejectsANonPositiveInterval(t *testing.T) {
	q := newBoundTestQueue(t.TempDir())
	if _, _, err := q.EnqueueSpread("worker-1", "lead", "[ANNOUNCE lead] news", 0, time.Minute); err == nil {
		t.Fatal("accepted a zero interval")
	}
}

func TestWitnessWindowOfAHeldAnnouncementStartsAtThePaste(t *testing.T) {
	// Queued at T, slot at T+30m, pasted unverified at the slot: the witness
	// gets its full window from the paste, not from T.
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.Local)
	q := heldQueue(t, &now)
	interval := 30 * time.Minute
	if _, _, err := q.EnqueueSpread("worker-0", "sender", "[ANNOUNCE sender] first slot", interval, time.Minute); err != nil {
		t.Fatal(err)
	}
	id, slot, err := q.EnqueueSpread("worker-1", "sender", witnessable, interval, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: "❯  "}
	dispatchOnce(t, q, target)
	now = slot
	target.sendErr = bptmux.ErrUnverified
	dispatchOnce(t, q, target)
	if record := pendingRecord(t, q, id); !record.NoRepaste || record.UnverifiedAt == 0 {
		t.Fatalf("record=%+v", record)
	}
	target.sendErr = nil
	now = slot.Add(time.Minute)
	dispatchOnce(t, q, target)
	if record := pendingRecord(t, q, id); record.Status != "" {
		t.Fatalf("settled one minute after the paste: %+v", record)
	}
	now = slot.Add(witnessWindow + time.Minute)
	dispatchOnce(t, q, target)
	if record, err := q.Record(id); err != nil || record.Status != StatusUnconfirmed {
		t.Fatalf("not settled after the window: %+v %v", record, err)
	}
}
