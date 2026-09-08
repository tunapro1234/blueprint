package msgq

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestTransportReplayAcrossQueueInstances(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q := New(root)
			if _, e := q.EnqueueOnce("peer:channel", "agent", "external:alice@laptop", "[external:alice@laptop] hello"); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	q := New(root)
	records, e := q.List()
	if e != nil || len(records) != 1 {
		t.Fatalf("records=%v %v", records, e)
	}
	if _, e = q.EnqueueOnce("peer:channel", "agent", "external:alice@laptop", "different"); e == nil {
		t.Fatal("content collision accepted")
	}
	if _, ok := q.CloseDelivered("agent", records[0].Msg, "delivered"); !ok {
		t.Fatal("finish")
	}
	replay, e := New(root).EnqueueOnce("peer:channel", "agent", records[0].From, records[0].Msg)
	if e != nil || replay.Status != "delivered" {
		t.Fatalf("replay=%+v %v", replay, e)
	}
	pending, _ := q.List()
	if len(pending) != 0 {
		t.Fatal("replayed completed message")
	}
}

func TestTransportMessageUsesOrdinaryBusyAndDraftGuards(t *testing.T) {
	q := New(t.TempDir())
	m, e := q.EnqueueOnce("peer:channel", "agent", "external:alice@laptop", "[external:alice@laptop] hello")
	if e != nil {
		t.Fatal(e)
	}
	busy := true
	q.RuntimeBlock = func(string, bool) string {
		if busy {
			return "runtime working"
		}
		return ""
	}
	target := &fakeTarget{alive: true, pane: composerPane("")}
	if e = q.Dispatch(context.Background(), target, nil); e != nil {
		t.Fatal(e)
	}
	if target.calls != 0 || q.Reason(m.ID) != "runtime working" {
		t.Fatalf("busy gate bypassed calls=%d reason=%s", target.calls, q.Reason(m.ID))
	}
	busy = false
	target.pane = composerPane("user draft")
	if e = q.Dispatch(context.Background(), target, nil); e != nil {
		t.Fatal(e)
	}
	if target.calls != 0 {
		t.Fatal("user draft interrupted")
	}
	target.pane = composerPane("")
	if e = q.Dispatch(context.Background(), target, nil); e != nil {
		t.Fatal(e)
	}
	if target.calls != 1 || len(target.forced) != 0 {
		t.Fatalf("calls=%d forced=%v", target.calls, target.forced)
	}
}

func TestTransportCorruptReceiptFailsClosed(t *testing.T) {
	q := New(t.TempDir())
	m, e := q.EnqueueOnce("key", "a", "b", "c")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(q.pending(), m.ID+".json"), []byte("{"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = q.EnqueueOnce("key", "a", "b", "c"); e == nil {
		t.Fatal("corrupt receipt overwritten")
	}
}

func TestTransportCrashAfterDoneBeforePendingUnlink(t *testing.T) {
	q := New(t.TempDir())
	m, e := q.EnqueueOnce("peer:channel", "agent", "external:a@b", "hello")
	if e != nil {
		t.Fatal(e)
	}
	pendingPath := filepath.Join(q.pending(), m.ID+".json")
	original, e := os.ReadFile(pendingPath)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := q.CloseDelivered("agent", "hello", "delivered"); !ok {
		t.Fatal("finish")
	}
	if e = os.WriteFile(pendingPath, original, 0600); e != nil {
		t.Fatal(e)
	} // interrupted unlink
	target := &fakeTarget{alive: true, pane: composerPane("")}
	if e = q.Dispatch(context.Background(), target, nil); e != nil {
		t.Fatal(e)
	}
	if target.calls != 0 {
		t.Fatal("terminal message replayed after crash")
	}
	replay, e := q.EnqueueOnce("peer:channel", "agent", "external:a@b", "hello")
	if e != nil || replay.Status != "delivered" {
		t.Fatalf("%+v %v", replay, e)
	}
}
