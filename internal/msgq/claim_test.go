package msgq

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaimTakesTheTargetLineInOrderAndClosesItVerified(t *testing.T) {
	q := newBoundTestQueue(t.TempDir())
	first, _ := q.Enqueue("worker", "lead", "first instruction")
	second, _ := q.Enqueue("worker", "lead", "second instruction")
	other, _ := q.Enqueue("someone-else", "lead", "not for the worker")
	got, err := q.Claim("worker", StatusDeliveredHook, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != first || got[1].ID != second {
		t.Fatalf("claimed %+v, want %s then %s", got, first, second)
	}
	for _, id := range []string{first, second} {
		status, ok := q.Finished(id)
		if !ok || status != StatusDeliveredHook || !IsVerifiedDelivery(status) {
			t.Fatalf("%s finished=%v status=%q", id, ok, status)
		}
	}
	if _, err := os.Stat(filepath.Join(q.pending(), other+".json")); err != nil {
		t.Fatalf("another target's record was touched: %v", err)
	}
	// Nothing left: a second claim is empty, and a dispatch pass has nothing to paste.
	if again, err := q.Claim("worker", StatusDeliveredHook, 0); err != nil || len(again) != 0 {
		t.Fatalf("second claim = %+v, %v", again, err)
	}
	target := &fakeTarget{alive: true, pane: composerPane(""), sessions: map[string]bool{"worker": true}}
	if err := q.DispatchTargets(context.Background(), target, []string{"worker"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.sent) != 0 {
		t.Fatalf("dispatch pasted a claimed record: %q", target.sent)
	}
}

func TestClaimStopsAtARecordThatMayAlreadyBeInTheAgent(t *testing.T) {
	q := newBoundTestQueue(t.TempDir())
	held, err := enqueueBoundUnverified(t, q, "worker", "lead", "pasted but not witnessed")
	if err != nil {
		t.Fatal(err)
	}
	behind, _ := q.Enqueue("worker", "lead", "queued behind it")
	got, err := q.Claim("worker", StatusDeliveredHook, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("claim overtook a NoRepaste head: %+v", got)
	}
	for _, id := range []string{held, behind} {
		if _, err := os.Stat(filepath.Join(q.pending(), id+".json")); err != nil {
			t.Fatalf("%s left the queue: %v", id, err)
		}
	}
}

func TestClaimHonoursMax(t *testing.T) {
	q := newBoundTestQueue(t.TempDir())
	for _, text := range []string{"one", "two", "three"} {
		if _, err := q.Enqueue("worker", "lead", "message "+text); err != nil {
			t.Fatal(err)
		}
	}
	got, err := q.Claim("worker", StatusDeliveredHook, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !strings.HasSuffix(got[1].Msg, "two") {
		t.Fatalf("claimed %+v", got)
	}
	rest, _ := q.Claim("worker", StatusDeliveredHook, 0)
	if len(rest) != 1 || !strings.HasSuffix(rest[0].Msg, "three") {
		t.Fatalf("rest %+v", rest)
	}
}
