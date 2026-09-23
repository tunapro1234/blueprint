package msgq

import (
	"strings"
	"testing"
	"time"
)

func TestRenameTargetMovesOnlyPendingChannelsAndReportsIDs(t *testing.T) {
	q := newBoundTestQueue(t.TempDir())
	q.Now = func() time.Time { return time.Unix(100, 0) }
	first, err := q.EnqueueReason("old", "sender", "one", "target closed; waiting for it to reopen")
	if err != nil {
		t.Fatal(err)
	}
	second, err := q.Enqueue("old", "sender", "two")
	if err != nil {
		t.Fatal(err)
	}
	untouched, err := q.Enqueue("other", "sender", "three")
	if err != nil {
		t.Fatal(err)
	}

	changes, err := q.RenameTarget("old", "new")
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 || !strings.Contains(strings.Join(changes, "\n"), first) || !strings.Contains(strings.Join(changes, "\n"), second) {
		t.Fatalf("changes=%v, want both channel IDs", changes)
	}
	rows, err := q.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == first || row.ID == second {
			if row.To != "new" {
				t.Errorf("channel %s target=%q, want new", row.ID, row.To)
			}
		}
		if row.ID == untouched && row.To != "other" {
			t.Errorf("unrelated channel target=%q, want other", row.To)
		}
	}
	status, err := q.Status(first)
	if err != nil || !strings.Contains(status, "new") || strings.Contains(status, "closed") {
		t.Fatalf("qstat=%q err=%v, want new target", status, err)
	}
}
