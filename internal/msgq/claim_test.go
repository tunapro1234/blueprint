package msgq

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestClaimFramesExternalAndHoldsOnRenderError proves the hook delivery path
// (Claim) honours the untrusted-input seam exactly as terminal dispatch does:
// an external record is claimed FRAMED (its Wire() carries the guard frame, the
// raw Msg survives on disk), a local record is claimed raw, and a record whose
// body cannot be framed is HELD, never handed to the harness. frameExternal is
// the package test stand-in for the guard renderer (see delivery_seam_test.go).
func TestClaimFramesExternalAndHoldsOnRenderError(t *testing.T) {
	q := newBoundTestQueue(t.TempDir())
	q.Render = frameExternal
	ext, err := q.EnqueueOnceOrigin("peer:h1", "worker", "yigit", "[yigit] from outside", &Origin{Transport: "libp2p"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue("worker", "lead", "local instruction"); err != nil {
		t.Fatal(err)
	}
	got, err := q.Claim("worker", StatusDeliveredHook, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != ext.ID {
		t.Fatalf("claimed %+v, want the external record first then the local one", got)
	}
	if got[0].Wire() != "FRAME<[yigit] from outside>" {
		t.Fatalf("external record was not framed at claim: Wire()=%q", got[0].Wire())
	}
	if got[0].Msg != "[yigit] from outside" {
		t.Fatalf("the raw body must survive: Msg=%q", got[0].Msg)
	}
	if got[1].Wire() != "local instruction" {
		t.Fatalf("a local record must be claimed raw: Wire()=%q", got[1].Wire())
	}
}

func TestClaimHoldsRecordThatCannotBeFramed(t *testing.T) {
	q := newBoundTestQueue(t.TempDir())
	q.Render = func(Message) (string, error) { return "", errors.New("boom") }
	m, err := q.EnqueueOnceOrigin("peer:h2", "worker", "yigit", "[yigit] dangerous", &Origin{Transport: "libp2p"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := q.Claim("worker", StatusDeliveredHook, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a record that cannot be framed must never be handed to the harness; claimed %+v", got)
	}
	if _, err := os.Stat(filepath.Join(q.pending(), m.ID+".json")); err != nil {
		t.Fatalf("the held record must stay pending: %v", err)
	}
}

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
