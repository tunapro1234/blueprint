package audit

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestAppendReadFilter(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	events := []Event{
		{Time: base, Kind: "p2p.msg.accepted", Peer: "mami"},
		{Time: base.Add(time.Minute), Kind: "p2p.msg.rejected", Severity: Warn, Peer: "mami", Reason: "target is not exposed"},
		{Time: base.Add(2 * time.Minute), Kind: "module.enable", Target: "bar"},
	}
	for _, ev := range events {
		if err := Append(dir, ev); err != nil {
			t.Fatal(err)
		}
	}
	all, err := Read(dir, Filter{})
	if err != nil || len(all) != 3 || all[0].Severity != Info {
		t.Fatalf("all = %+v, %v", all, err)
	}
	if got, _ := Read(dir, Filter{Kind: "p2p."}); len(got) != 2 {
		t.Fatalf("kind filter = %d", len(got))
	}
	if got, _ := Read(dir, Filter{Severity: Warn}); len(got) != 1 || got[0].Reason != "target is not exposed" {
		t.Fatalf("severity filter = %+v", got)
	}
	if got, _ := Read(dir, Filter{Limit: 1}); len(got) != 1 || got[0].Target != "bar" {
		t.Fatalf("limit = %+v", got)
	}
	if st, _ := os.Stat(Path(dir)); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", st.Mode())
	}
}

func TestFieldsAreClipped(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("ü", 1000)
	if err := Append(dir, Event{Kind: "x", Reason: long, Fields: map[string]string{"k": long}}); err != nil {
		t.Fatal(err)
	}
	got, _ := Read(dir, Filter{})
	if len(got) != 1 || len(got[0].Reason) > maxField+3 || !strings.HasSuffix(got[0].Fields["k"], "...") {
		t.Fatalf("not clipped: %d", len(got[0].Reason))
	}
}

func TestMissingLogReadsEmpty(t *testing.T) {
	got, err := Read(t.TempDir(), Filter{})
	if err != nil || got != nil {
		t.Fatalf("%v %v", got, err)
	}
}
