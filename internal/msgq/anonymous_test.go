package msgq

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAnonymousLegacyRecordNeverSubmitsButPastProofCanSettle(t *testing.T) {
	for _, sender := range []string{"", "bilinmiyor"} {
		for _, recovery := range []bool{false, true} {
			t.Run(sender+map[bool]string{false: "/new", true: "/recovery"}[recovery], func(t *testing.T) {
				q := newBoundTestQueue(t.TempDir())
				if err := os.MkdirAll(q.pending(), 0755); err != nil {
					t.Fatal(err)
				}
				message := Message{ID: "qlegacy", To: "target", From: sender, Msg: stuckText, TS: float64(q.Now().Unix()), NoRepaste: recovery, AttemptBinding: q.Binding("target")}
				data, err := json.Marshal(message)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(q.pending(), message.ID+".json"), data, 0600); err != nil {
					t.Fatal(err)
				}
				target := &fakeTarget{alive: true, pane: composerPane("")}
				if recovery {
					target.pane = composerPane(stuckText)
				}
				if err := q.Dispatch(context.Background(), target, nil); err != nil {
					t.Fatal(err)
				}
				record, err := q.Record(message.ID)
				if err != nil || record.Status != "" || record.From != sender || record.Msg != stuckText || !strings.Contains(record.Reason, "anonymous delivery blocked") {
					t.Fatal(record, err)
				}
				if target.calls != 0 || len(target.submitted) != 0 || len(target.cleared) != 0 {
					t.Fatal("legacy anonymous message caused terminal input")
				}
				q.Witness = func(string, string, time.Time) bool { return true }
				if err := q.Dispatch(context.Background(), target, nil); err != nil {
					t.Fatal(err)
				}
				record, err = q.Record(message.ID)
				if err != nil || record.Status != "delivered (found in transcript)" || target.calls != 0 || len(target.submitted) != 0 || len(target.cleared) != 0 {
					t.Fatal(record, err)
				}
			})
		}
	}
}

func TestQueueRejectsAnonymousIngress(t *testing.T) {
	for _, sender := range []string{"", "bilinmiyor", "  "} {
		q := New(t.TempDir())
		if _, err := q.Enqueue("target", sender, stuckText); err == nil {
			t.Fatalf("accepted anonymous sender %q", sender)
		}
	}
}
