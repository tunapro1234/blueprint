package usagecli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// readAllSamples is the previous whole-file reader, kept as the reference
// that the tail reader must agree with.
func readAllSamples(t *testing.T, path string) []Sample {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var samples []Sample
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var s Sample
		if err := json.Unmarshal(scanner.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		samples = append(samples, s)
	}
	return samples
}

func writeHistory(t *testing.T, path string, samples []Sample) {
	t.Helper()
	var b strings.Builder
	for _, s := range samples {
		line, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLatestTailMatchesWholeHistory(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	for round := 0; round < 400; round++ {
		n := 1 + rng.Intn(120)
		samples := make([]Sample, n)
		at := base
		for i := range samples {
			at = at.Add(time.Duration(rng.Intn(8)) * time.Hour)
			ts := at.Format(time.RFC3339)
			if rng.Intn(25) == 0 {
				ts = "garbage"
			}
			pick := func() any {
				if rng.Intn(3) == 0 {
					return nil
				}
				return float64(rng.Intn(100))
			}
			samples[i] = sample(ts, pick(), pick(), pick(), pick())
			if rng.Intn(4) == 0 {
				samples[i].Codex5 = nil
			}
		}
		path := filepath.Join(dir, fmt.Sprintf("h%d.jsonl", round))
		writeHistory(t, path, samples)
		got, err := Latest(path)
		if err != nil {
			t.Fatal(err)
		}
		if want := resolve(readAllSamples(t, path)); !reflect.DeepEqual(got, want) {
			t.Fatalf("round %d: tail %+v, whole %+v", round, got, want)
		}
	}
}

func TestLatestSeesAppendedRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	rows := []Sample{sample("2026-09-01T00:00:00Z", 1.0, 2.0, 3.0, nil)}
	writeHistory(t, path, rows)
	first, err := Latest(path)
	if err != nil || first.TS != rows[0].TS {
		t.Fatalf("first = %+v, %v", first, err)
	}
	rows = append(rows, sample("2026-09-01T01:00:00Z", 4.0, 5.0, 6.0, nil))
	writeHistory(t, path, rows)
	second, err := Latest(path)
	if err != nil || second.TS != rows[1].TS || second.Codex.Codex5 != 6.0 {
		t.Fatalf("second = %+v, %v", second, err)
	}
}

func TestLatestKeepsEmptyAndMalformedErrors(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(empty, []byte("\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Latest(empty); err == nil {
		t.Fatal("empty history resolved")
	}
	torn := filepath.Join(dir, "torn.jsonl")
	if err := os.WriteFile(torn, []byte(`{"ts":"2026-09-01T00:00:00Z"}`+"\n"+`{"ts":"2026-`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Latest(torn); err == nil {
		t.Fatal("malformed newest row resolved")
	}
}

// TestLatestLiveHistoryMatches reads a real history file read-only when one is
// named, e.g. BP_USAGE_HISTORY=/path/history.jsonl.
func TestLatestLiveHistoryMatches(t *testing.T) {
	path := os.Getenv("BP_USAGE_HISTORY")
	if path == "" {
		t.Skip("BP_USAGE_HISTORY not set")
	}
	got, err := Latest(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := resolve(readAllSamples(t, path)); !reflect.DeepEqual(got, want) {
		t.Fatalf("tail %+v, whole %+v", got, want)
	}
}
