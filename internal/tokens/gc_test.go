package tokens

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBudgetDeletesOldestRawAndReportsPath(t *testing.T) {
	root := t.TempDir()
	oldDay := "2026-07-25"
	newDay := "2026-07-26"
	for _, day := range []string{oldDay, newDay} {
		month := day[:7]
		year := day[:4]
		if err := os.MkdirAll(filepath.Join(root, "raw"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "raw", day+".jsonl"), []byte(strings.Repeat(day, 30)), 0o644); err != nil {
			t.Fatal(err)
		}
		writeLines(t, filepath.Join(root, "prompts", day+".jsonl"), PromptRecord{Day: day})
		writeLines(t, filepath.Join(root, "hourly", month+".jsonl"), HourlyRecord{Day: day})
		writeLines(t, filepath.Join(root, "daily", year+".jsonl"), DailyRecord{Day: day})
	}
	size, err := storeSize(root)
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	config := Config{StoreDir: root, BudgetBytes: size - 1, HardLimitBytes: size * 2, Log: &log}.normalized()
	stats, err := enforceBudget(config)
	if err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(root, "raw", oldDay+".jsonl")
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("oldest raw was not deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "raw", newDay+".jsonl")); err != nil {
		t.Fatalf("newest raw unexpectedly deleted: %v", err)
	}
	if stats.Deleted != 1 || !strings.Contains(log.String(), oldPath) || !strings.Contains(log.String(), "store budget") {
		t.Fatalf("stats=%+v log=%q", stats, log.String())
	}
}

func TestContextPruneAgeCapAndMissingSources(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, istanbul)
	livePath := "/logs/live.jsonl"
	missingPath := "/logs/gone.jsonl"
	nodes := []nodeRef{{UUID: "old", Ref: promptRef{ID: "old"}, TS: now.AddDate(0, 0, -15).Format(time.RFC3339)}}
	for index := 0; index < 2105; index++ {
		node := nodeRef{UUID: "n" + strconv.Itoa(index), Ref: promptRef{ID: "p"}}
		if index%2 == 0 {
			node.TS = now.Add(-time.Hour).Format(time.RFC3339)
		}
		nodes = append(nodes, node)
	}
	contexts := contextFile{Sources: map[string]sourceContext{
		livePath:    {Nodes: nodes},
		missingPath: {Nodes: []nodeRef{{UUID: "gone", Ref: promptRef{ID: "gone"}}}},
	}}
	var log bytes.Buffer
	pruneContexts(&contexts, map[string]bool{livePath: true}, now, &log)
	if _, exists := contexts.Sources[missingPath]; exists {
		t.Fatal("missing source context was retained")
	}
	got := contexts.Sources[livePath].Nodes
	if len(got) != 2000 {
		t.Fatalf("kept %d nodes, want 2000", len(got))
	}
	for _, node := range got {
		if node.UUID == "old" {
			t.Fatal("node older than 14 days was retained")
		}
	}
	if got[0].UUID != "n105" || !strings.Contains(log.String(), "pruned 107 context nodes and 1 missing sources") {
		t.Fatalf("unexpected cap/order or log: first=%s log=%q", got[0].UUID, log.String())
	}
}

func TestSeenDailyFilesAndRetention(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, istanbul)
	config := testConfig(t, now)
	path := filepath.Join(config.ClaudeRoot, "-srv-test", "session.jsonl")
	writeLines(t, path,
		claudeUser("u", "", "p", "2026-07-26T08:00:00Z", "prompt", false),
		claudeAssistant("a", "u", "request", "2026-07-26T08:01:00Z", 1),
	)
	legacy := filepath.Join(config.StoreDir, "seen", "2026.jsonl")
	writeLines(t, legacy, struct {
		Key string `json:"key"`
	}{"legacy"})
	if _, err := Collect(config); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy yearly seen file remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(config.StoreDir, "seen", "2026-07-26.jsonl")); err != nil {
		t.Fatalf("daily seen file missing: %v", err)
	}

	old := filepath.Join(config.StoreDir, "seen", "2026-06-26.jsonl")
	recent := filepath.Join(config.StoreDir, "seen", "2026-06-27.jsonl")
	writeLines(t, old, struct {
		Key string `json:"key"`
	}{"old"})
	writeLines(t, recent, struct {
		Key string `json:"key"`
	}{"recent"})
	if _, err := GC(config); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("30-day seen file remains: %v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("29-day seen file removed: %v", err)
	}
}
