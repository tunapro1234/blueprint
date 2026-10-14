package tokens

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A day's raw tier is kept for 14 days, but its daily and hourly rollups are
// kept far longer (90-400 days). Subagent and workflow transcripts are
// discovered in bulk with no checkpoint, so a first scan must not emit records
// for a day whose raw has already aged out: recomputing that day from its
// raw-only records would overwrite the retained main-agent totals. Such old
// records are skipped; the retained rollup is left intact.
func TestSubagentBeyondRawWindowLeavesOldRollupIntact(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, istanbul)
	config := testConfig(t, now)
	proj, sess := "-srv-test", "sess1"
	oldTS := "2026-09-01T08:00:00Z" // >14 days before now

	// A main transcript recorded its usage back when the day was fresh.
	writeLines(t, filepath.Join(config.ClaudeRoot, proj, sess+".jsonl"),
		claudeUser("u1", "", "p1", oldTS, "prompt", false),
		claudeAssistant("a1", "u1", "req-main-old", oldTS, 100),
	)
	if _, err := Collect(config); err != nil {
		t.Fatal(err)
	}
	// Simulate 14-day raw GC: the raw day is gone, the daily rollup remains.
	for _, p := range []string{"2026-09-01.jsonl", "2026-09-01.jsonl.gz"} {
		_ = os.Remove(filepath.Join(config.StoreDir, "raw", p))
	}

	// A later backfill discovers a subagent transcript whose records fall on
	// that same aged-out day.
	writeLines(t, filepath.Join(config.ClaudeRoot, proj, sess, "subagents", "agent-aaa.jsonl"),
		claudeSubAssistant("s1", "req-sub-old", "msg-s1", sess, oldTS, 10, 0, 0, 40),
	)
	if _, err := Collect(config); err != nil {
		t.Fatal(err)
	}

	var daily []DailyRecord
	if err := readJSONLines(filepath.Join(config.StoreDir, "daily", "2026.jsonl"), &daily); err != nil {
		t.Fatal(err)
	}
	var mainOut, subOut int64
	for _, r := range daily {
		if r.Day != "2026-09-01" {
			continue
		}
		if r.Kind == "" {
			mainOut += r.Out
		} else {
			subOut += r.Out
		}
	}
	if mainOut != 100 {
		t.Fatalf("retained main daily was overwritten: mainOut=%d, want 100", mainOut)
	}
	if subOut != 0 {
		t.Fatalf("subagent usage older than the raw window must be skipped: subOut=%d, want 0", subOut)
	}
}

// Within the raw-retention window the day still has complete raw, so a subagent
// record is counted and its day is recomputed in full (main plus subagent).
func TestSubagentWithinRawWindowIsCounted(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, istanbul)
	config := testConfig(t, now)
	proj, sess := "-srv-test", "sess1"
	ts := "2026-10-10T08:00:00Z" // within 14 days of now

	writeLines(t, filepath.Join(config.ClaudeRoot, proj, sess+".jsonl"),
		claudeUser("u1", "", "p1", ts, "prompt", false),
		claudeAssistant("a1", "u1", "req-main", ts, 100),
	)
	writeLines(t, filepath.Join(config.ClaudeRoot, proj, sess, "subagents", "agent-aaa.jsonl"),
		claudeSubAssistant("s1", "req-sub", "msg-s1", sess, ts, 10, 0, 0, 40),
	)
	if _, err := Collect(config); err != nil {
		t.Fatal(err)
	}
	var daily []DailyRecord
	if err := readJSONLines(filepath.Join(config.StoreDir, "daily", "2026.jsonl"), &daily); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int64{}
	for _, r := range daily {
		if r.Day == "2026-10-10" {
			kinds[r.Kind] += r.Out
		}
	}
	if kinds[""] != 100 || kinds["subagent"] != 40 {
		t.Fatalf("within-window split=%+v, want main=100 subagent=40", kinds)
	}
}
