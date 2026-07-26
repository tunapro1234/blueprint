package tokens

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSummaryCombinesModelsExceptAgentDetail(t *testing.T) {
	root := t.TempDir()
	day := "2026-07-26"
	writeLines(t, filepath.Join(root, "daily", "2026.jsonl"),
		DailyRecord{Day: day, Agent: "blueprint", Src: "claude", Model: "opus", Requests: 2, Out: 20},
		DailyRecord{Day: day, Agent: "blueprint", Src: "claude", Model: "fable", Requests: 3, Out: 30},
		DailyRecord{Day: day, Agent: "blueprint", Src: "codex", Model: "gpt", Requests: 1, Out: 10},
	)
	period := DayRange(time.Date(2026, 7, 26, 0, 0, 0, 0, istanbul))
	defaultView, err := Summary(root, period, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(defaultView.Rows) != 2 {
		t.Fatalf("default rows=%+v, want one per agent+src", defaultView.Rows)
	}
	for _, row := range defaultView.Rows {
		if row.Model != "" {
			t.Fatalf("default row leaked model: %+v", row)
		}
		if row.Src == "claude" && (row.Requests != 5 || row.Out != 50) {
			t.Fatalf("claude models not combined: %+v", row)
		}
	}
	detail, err := Summary(root, period, "blueprint")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Rows) != 3 {
		t.Fatalf("detail rows=%+v, want model breakdown", detail.Rows)
	}
}

func TestFootprintCachesForSixHours(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	config := Config{
		StoreDir: filepath.Join(root, "store"), ClaudeRoot: filepath.Join(root, "claude"), CodexRoot: filepath.Join(root, "codex"),
		Now: func() time.Time { return now }, Log: os.Stderr,
	}
	for _, path := range []string{filepath.Join(config.ClaudeRoot, "a"), filepath.Join(config.CodexRoot, "b")} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("12345"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	first, err := Footprint(config)
	if err != nil {
		t.Fatal(err)
	}
	if first.Claude != 5 || first.Codex != 5 || first.Total != 10 {
		t.Fatalf("first footprint=%+v", first)
	}
	if err := os.WriteFile(filepath.Join(config.ClaudeRoot, "more"), []byte("1234567"), 0o644); err != nil {
		t.Fatal(err)
	}
	cached, err := Footprint(config)
	if err != nil || cached.Claude != 5 {
		t.Fatalf("cached footprint=%+v err=%v", cached, err)
	}
	now = now.Add(footprintTTL + time.Minute)
	refreshed, err := Footprint(config)
	if err != nil || refreshed.Claude != 12 {
		t.Fatalf("refreshed footprint=%+v err=%v", refreshed, err)
	}
}
