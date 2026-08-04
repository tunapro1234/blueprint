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
