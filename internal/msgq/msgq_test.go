package msgq

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeTarget struct {
	alive bool
	pane  string
	sent  []string
}

func (f *fakeTarget) HasSession(context.Context, string) bool         { return f.alive }
func (f *fakeTarget) Capture(context.Context, string) (string, error) { return f.pane, nil }
func (f *fakeTarget) Send(_ context.Context, to, text string) error {
	f.sent = append(f.sent, to+":"+text)
	return nil
}

func TestEnqueueListAndStatus(t *testing.T) {
	q := New(t.TempDir())
	now := time.Date(2026, 7, 10, 10, 0, 0, 123456789, time.Local)
	q.Now = func() time.Time { return now }
	id, err := q.Enqueue("hedef", "gonderen", "merhaba")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "q") {
		t.Fatalf("bad id: %s", id)
	}
	rows, err := q.List()
	if err != nil || len(rows) != 1 || rows[0].Msg != "merhaba" {
		t.Fatalf("list=%v err=%v", rows, err)
	}
	q.Now = func() time.Time { return now.Add(12 * time.Second) }
	status, _ := q.Status(id)
	if status != "BEKLIYOR: hedef hala musait degil (12 sn kuyrukta)" {
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
	id, err := q.Enqueue("hedef", "gonderen", "satir1\nsatir2")
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: "❯ kullanici yaziyor"}
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
	if !strings.HasPrefix(status, "ILETILDI: hedef") {
		t.Fatalf("status=%q", status)
	}
}

func TestDispatchCancelsClosedTarget(t *testing.T) {
	q := New(t.TempDir())
	id, err := q.Enqueue("kapali", "gonderen", "mesaj")
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Dispatch(context.Background(), &fakeTarget{}, nil); err != nil {
		t.Fatal(err)
	}
	status, _ := q.Status(id)
	if !strings.HasPrefix(status, "IPTAL (HEDEF KAPALI): kapali") {
		t.Fatalf("status=%q", status)
	}
}
