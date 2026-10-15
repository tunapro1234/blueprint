package tokens

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestFleetSummaryKeepsModelAndSplitsSub(t *testing.T) {
	root := t.TempDir()
	day := "2026-07-26"
	writeLines(t, filepath.Join(root, "daily", "2026.jsonl"),
		// Same agent, same model, main + subagent on the same day: the main
		// totals must fold in Sub*, and Sub* must carry only the subagent part.
		DailyRecord{Day: day, Agent: "blueprint", Src: "claude", Model: "claude-opus-4-8", Requests: 5, In: 500, Out: 1000, CacheWrite: 20, CacheRead: 200},
		DailyRecord{Day: day, Agent: "blueprint", Src: "claude", Model: "claude-opus-4-8", Kind: "subagent", Requests: 2, In: 200, Out: 400, CacheWrite: 10, CacheRead: 100},
		// A different model on the same agent stays its own row (never collapsed).
		DailyRecord{Day: day, Agent: "blueprint", Src: "claude", Model: "claude-haiku-5-5", Requests: 1, Out: 50},
	)
	period := DayRange(time.Date(2026, 7, 26, 0, 0, 0, 0, istanbul))
	rows, err := FleetSummary(root, period)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%+v, want one per model", rows)
	}
	var opus *FleetRow
	for i := range rows {
		if rows[i].Model == "claude-opus-4-8" {
			opus = &rows[i]
		}
	}
	if opus == nil {
		t.Fatalf("opus row missing: %+v", rows)
	}
	if opus.Requests != 7 || opus.Out != 1400 || opus.In != 700 || opus.CacheWrite != 30 || opus.CacheRead != 300 {
		t.Fatalf("opus totals should include subagent: %+v", opus)
	}
	if opus.SubRequests != 2 || opus.SubOut != 400 || opus.SubIn != 200 || opus.SubCacheWrite != 10 || opus.SubCacheRead != 100 {
		t.Fatalf("opus subagent split wrong: %+v", opus)
	}
}

func TestFleetSummaryExcludesOutOfRange(t *testing.T) {
	root := t.TempDir()
	writeLines(t, filepath.Join(root, "daily", "2026.jsonl"),
		DailyRecord{Day: "2026-07-26", Agent: "a", Src: "claude", Model: "claude-opus-4-8", Out: 10},
		DailyRecord{Day: "2026-07-20", Agent: "a", Src: "claude", Model: "claude-opus-4-8", Out: 99},
	)
	period := DayRange(time.Date(2026, 7, 26, 0, 0, 0, 0, istanbul))
	rows, err := FleetSummary(root, period)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Out != 10 {
		t.Fatalf("out-of-range day leaked in: %+v", rows)
	}
}

func TestRowCostUSDByModelAndCacheMultipliers(t *testing.T) {
	// Opus: in 5 / out 25 per 1M; cache read x0.1, cache write x1.25 of input.
	row := FleetRow{Model: "claude-opus-4-8", In: 1_000_000, Out: 1_000_000, CacheRead: 1_000_000, CacheWrite: 1_000_000}
	want := 5.0 + 25.0 + (5.0 * 0.1) + (5.0 * 1.25) // 36.75
	if got := RowCostUSD(row); math.Abs(got-want) > 1e-9 {
		t.Fatalf("opus cost=%v, want %v", got, want)
	}
	// Haiku: in 1 / out 5 per 1M.
	haiku := FleetRow{Model: "claude-haiku-5-5", In: 2_000_000, Out: 1_000_000}
	if got := RowCostUSD(haiku); math.Abs(got-(2.0+5.0)) > 1e-9 {
		t.Fatalf("haiku cost=%v, want 7", got)
	}
	// Non-Claude model prices to zero (counts still travel, dollars do not).
	if got := RowCostUSD(FleetRow{Model: "gpt-6-luna", In: 9_000_000, Out: 9_000_000}); got != 0 {
		t.Fatalf("non-claude cost=%v, want 0", got)
	}
}

func TestClaudeModelRateFamilies(t *testing.T) {
	for _, test := range []struct {
		model   string
		in, out float64
		ok      bool
	}{
		{"claude-opus-4-8", 5, 25, true},
		{"claude-sonnet-5-5", 3, 15, true},
		{"claude-haiku-5-5", 1, 5, true},
		{"claude-fable-5-1", 10, 50, true},
		{"gpt-6-luna", 0, 0, false},
		{"", 0, 0, false},
	} {
		in, out, ok := ClaudeModelRate(test.model)
		if in != test.in || out != test.out || ok != test.ok {
			t.Errorf("%q = (%v,%v,%v), want (%v,%v,%v)", test.model, in, out, ok, test.in, test.out, test.ok)
		}
	}
}
