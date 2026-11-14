package tokens

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeRawDay(t *testing.T, storeDir, day string, compressed bool, records ...RawRecord) {
	t.Helper()
	dir := filepath.Join(storeDir, "raw")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, day+".jsonl")
	if compressed {
		path += ".gz"
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var writer interface {
		Write([]byte) (int, error)
	} = file
	var gz *gzip.Writer
	if compressed {
		gz = gzip.NewWriter(file)
		writer = gz
	}
	for _, record := range records {
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(append(data, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	if gz != nil {
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentUsage(t *testing.T) {
	storeDir := t.TempDir()
	if _, _, err := AgentUsage(storeDir, "worker", time.Now().Add(-time.Hour), time.Now()); !errors.Is(err, ErrNotCollected) {
		t.Fatalf("store without a collection: want ErrNotCollected, got %v", err)
	}
	// 2026-09-24 22:30 UTC is already 2026-09-25 in Istanbul, so a window
	// starting three hours earlier spans two Istanbul day files.
	base := time.Date(2026, 9, 24, 22, 30, 0, 0, time.UTC)
	ts := func(offset time.Duration) string { return base.Add(offset).In(istanbul).Format(time.RFC3339Nano) }
	writeRawDay(t, storeDir, "2026-09-24", true,
		RawRecord{TS: ts(-4 * time.Hour), Agent: "worker", Requests: 1, In: 1000},
		RawRecord{TS: ts(-2 * time.Hour), Agent: "worker", Requests: 1, In: 7, Out: 3},
	)
	writeRawDay(t, storeDir, "2026-09-25", false,
		RawRecord{TS: ts(time.Minute), Agent: "worker", Requests: 1, In: 10, Out: 20, CacheWrite: 30, CacheRead: 40},
		RawRecord{TS: ts(2 * time.Minute), Agent: "Explore-1", Owner: "worker", Kind: "subagent", Requests: 1, In: 100},
		RawRecord{TS: ts(3 * time.Minute), Agent: "other", Requests: 1, In: 5000},
		RawRecord{TS: ts(time.Hour), Agent: "worker", Requests: 1, In: 9000},
	)
	if err := os.WriteFile(filepath.Join(storeDir, "checkpoints.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	usage, asOf, err := AgentUsage(storeDir, "worker", base.Add(-3*time.Hour), base.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{Requests: 3, In: 117, Out: 23, CacheWrite: 30, CacheRead: 40, Seen: true}
	if usage != want {
		t.Fatalf("usage = %#v, want %#v", usage, want)
	}
	if info, _ := os.Stat(filepath.Join(storeDir, "checkpoints.json")); !asOf.Equal(info.ModTime()) {
		t.Fatalf("asOf %s is not the last collection time", asOf)
	}
	if usage, _, err := AgentUsage(storeDir, "worker", base, base); err != nil || usage != (Usage{}) {
		t.Fatalf("empty interval: %#v err=%v", usage, err)
	}
	if usage, _, err := AgentUsage(storeDir, "nobody", base.Add(-time.Hour), base.Add(time.Hour)); err != nil || usage != (Usage{}) {
		t.Fatalf("unknown agent: %#v err=%v", usage, err)
	}
	// Records outside the interval but in the searched days still show the
	// name is attributed; a name with no record at all is not Seen.
	if usage, _, err := AgentUsage(storeDir, "worker", base.Add(10*time.Minute), base.Add(20*time.Minute)); err != nil || usage != (Usage{Seen: true}) {
		t.Fatalf("quiet interval of a known agent: %#v err=%v", usage, err)
	}
	if usage, _, err := AgentUsage(storeDir, "Explore-1", base.Add(-time.Hour), base.Add(time.Hour)); err != nil || usage.Seen {
		t.Fatalf("a subagent label rolls up to its owner and is not an agent name: %#v err=%v", usage, err)
	}
	// An interval early on the next Istanbul day (2026-09-26 01:30) still
	// sees the previous day's records.
	if usage, _, err := AgentUsage(storeDir, "other", base.Add(24*time.Hour), base.Add(25*time.Hour)); err != nil || !usage.Seen || usage.In != 0 {
		t.Fatalf("previous-day records do not count as Seen: %#v err=%v", usage, err)
	}
	// A replaced day file is read again rather than served from the cache.
	writeRawDay(t, storeDir, "2026-09-25", false,
		RawRecord{TS: ts(time.Minute), Agent: "worker", Requests: 1, In: 10, Out: 20, CacheWrite: 30, CacheRead: 40},
		RawRecord{TS: ts(2 * time.Minute), Agent: "Explore-1", Owner: "worker", Kind: "subagent", Requests: 1, In: 100},
		RawRecord{TS: ts(4 * time.Minute), Agent: "worker", Requests: 1, Out: 1},
	)
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(filepath.Join(storeDir, "raw", "2026-09-25.jsonl"), later, later); err != nil {
		t.Fatal(err)
	}
	if usage, _, err := AgentUsage(storeDir, "worker", base.Add(-3*time.Hour), base.Add(30*time.Minute)); err != nil || usage.Requests != 4 || usage.Out != 24 {
		t.Fatalf("replaced day file not re-read: %#v err=%v", usage, err)
	}
}
