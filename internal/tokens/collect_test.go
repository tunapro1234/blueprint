package tokens

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testConfig(t *testing.T, now time.Time) Config {
	t.Helper()
	root := t.TempDir()
	return Config{
		StoreDir:       filepath.Join(root, "store"),
		ClaudeRoot:     filepath.Join(root, "claude"),
		CodexRoot:      filepath.Join(root, "codex"),
		Now:            func() time.Time { return now },
		BudgetBytes:    1 << 30,
		HardLimitBytes: 2 << 30,
		Log:            &bytes.Buffer{},
	}
}

func writeLines(t *testing.T, path string, rows ...any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	for _, row := range rows {
		if err := encoder.Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, data.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendLines(t *testing.T, path string, rows ...any) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, row := range rows {
		if err := encoder.Encode(row); err != nil {
			t.Fatal(err)
		}
	}
}

func claudeUser(uuid, parent, promptID, timestamp string, content any, meta bool) map[string]any {
	return map[string]any{
		"type": "user", "uuid": uuid, "parentUuid": parent, "promptId": promptID,
		"timestamp": timestamp, "isMeta": meta, "message": map[string]any{"content": content},
	}
}

func claudeAssistant(uuid, parent, requestID, timestamp string, out int64) map[string]any {
	return map[string]any{
		"type": "assistant", "uuid": uuid, "parentUuid": parent, "requestId": requestID,
		"sessionId": "session", "timestamp": timestamp,
		"message": map[string]any{
			"model": "claude-test", "content": []any{},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": out, "cache_creation_input_tokens": 2, "cache_read_input_tokens": 3},
		},
	}
}

func TestClaudePromptAttributionAndDedup(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, istanbul)
	config := testConfig(t, now)
	path := filepath.Join(config.ClaudeRoot, "-srv-test", "session.jsonl")
	ts := "2026-07-26T08:00:00Z"
	writeLines(t, path,
		claudeUser("u1", "", "prompt-1", ts, "the real prompt", false),
		claudeAssistant("a1", "u1", "request-duplicate", ts, 11),
		claudeAssistant("a1-copy", "u1", "request-duplicate", ts, 999),
		claudeUser("meta", "a1", "", ts, "metadata", true),
		claudeAssistant("a2", "meta", "request-2", ts, 12),
		claudeUser("local", "a2", "", ts, "<local-command-stdout>noise", false),
		claudeAssistant("a3", "local", "request-3", ts, 13),
		claudeUser("command", "a3", "", ts, "<command-name>noise", false),
		claudeAssistant("a4", "command", "request-4", ts, 14),
		claudeUser("tool", "a4", "", ts, []any{map[string]any{"type": "tool_result", "content": "noise"}}, false),
		claudeAssistant("a5", "tool", "request-5", ts, 15),
	)

	stats, err := Collect(config)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Added != 5 || stats.Deduped != 1 {
		t.Fatalf("Collect stats=%+v, want 5 added and 1 duplicate", stats)
	}
	rows, err := readRawDay(config.StoreDir, "2026-07-26")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("raw rows=%d, want 5", len(rows))
	}
	for _, row := range rows {
		if row.PromptID != "prompt-1" || row.Prompt != "the real prompt" {
			t.Errorf("row attributed to %q %q, want prompt-1", row.PromptID, row.Prompt)
		}
		if row.Out == 999 {
			t.Error("duplicate request was counted")
		}
	}
}

func TestCodexUsesLastTokenUsageDelta(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, istanbul)
	config := testConfig(t, now)
	path := filepath.Join(config.CodexRoot, "2026", "07", "26", "rollout-test.jsonl")
	writeLines(t, path,
		map[string]any{"type": "session_meta", "timestamp": "2026-07-26T08:00:00Z", "payload": map[string]any{"cwd": "/srv/test", "originator": "codex", "model": "gpt-test"}},
		map[string]any{"type": "event_msg", "timestamp": "2026-07-26T08:01:00Z", "payload": map[string]any{
			"type": "token_count", "info": map[string]any{
				"total_token_usage": map[string]any{"input_tokens": 9000, "output_tokens": 8000, "reasoning_output_tokens": 7000},
				"last_token_usage":  map[string]any{"input_tokens": 101, "cached_input_tokens": 11, "cache_write_input_tokens": 12, "output_tokens": 13, "reasoning_output_tokens": 17},
			},
		}},
	)
	if _, err := Collect(config); err != nil {
		t.Fatal(err)
	}
	rows, err := readRawDay(config.StoreDir, "2026-07-26")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("raw rows=%d, want 1", len(rows))
	}
	row := rows[0]
	if row.In != 101 || row.Out != 30 || row.CacheRead != 11 || row.CacheWrite != 12 {
		t.Fatalf("delta row=%+v", row)
	}
}

func TestIncrementalOffsetAndTruncate(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, istanbul)
	config := testConfig(t, now)
	path := filepath.Join(config.ClaudeRoot, "-srv-test", "session.jsonl")
	ts := "2026-07-26T08:00:00Z"
	writeLines(t, path,
		claudeUser("u1", "", "p1", ts, strings.Repeat("first prompt ", 80), false),
		claudeAssistant("a1", "u1", "r1", ts, 1),
	)
	first, err := Collect(config)
	if err != nil || first.Added != 1 {
		t.Fatalf("first Collect=%+v, %v", first, err)
	}
	appendLines(t, path, claudeAssistant("a2", "u1", "r2", ts, 2))
	second, err := Collect(config)
	if err != nil || second.Added != 1 {
		t.Fatalf("incremental Collect=%+v, %v", second, err)
	}

	writeLines(t, path,
		claudeUser("u2", "", "p2", ts, "short", false),
		claudeAssistant("a3", "u2", "r3", ts, 3),
	)
	third, err := Collect(config)
	if err != nil || third.Added != 1 {
		t.Fatalf("truncate Collect=%+v, %v", third, err)
	}
	rows, err := readRawDay(config.StoreDir, "2026-07-26")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("raw rows=%d, want 3", len(rows))
	}
	for _, row := range rows {
		if row.Key == "claude:r2" && row.Prompt == "" {
			t.Fatal("incremental attribution lost the persisted prompt preview")
		}
	}
}

func TestRawRetentionPreservesRollups(t *testing.T) {
	eventDay := time.Date(2026, 7, 1, 12, 0, 0, 0, istanbul)
	config := testConfig(t, eventDay)
	path := filepath.Join(config.ClaudeRoot, "-srv-test", "session.jsonl")
	writeLines(t, path,
		claudeUser("u1", "", "p1", "2026-07-01T08:00:00Z", "prompt", false),
		claudeAssistant("a1", "u1", "r1", "2026-07-01T08:01:00Z", 37),
	)
	if _, err := Collect(config); err != nil {
		t.Fatal(err)
	}
	period := DayRange(eventDay)
	beforeDaily, err := Summary(config.StoreDir, period, "")
	if err != nil {
		t.Fatal(err)
	}
	beforeHourly, err := Hours(config.StoreDir, period, "")
	if err != nil {
		t.Fatal(err)
	}
	config.Now = func() time.Time { return eventDay.AddDate(0, 0, 15) }
	if _, err := GC(config); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(config.StoreDir, "raw", "2026-07-01.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("raw file still exists: %v", err)
	}
	afterDaily, _ := Summary(config.StoreDir, period, "")
	afterHourly, _ := Hours(config.StoreDir, period, "")
	if !equalJSON(beforeDaily, afterDaily) || !equalJSON(beforeHourly, afterHourly) {
		t.Fatalf("rollups changed after raw removal\nbefore=%+v %+v\nafter=%+v %+v", beforeDaily, beforeHourly, afterDaily, afterHourly)
	}
}

func equalJSON(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}

// The token store keeps prompt text for 90 days in line-delimited files. A
// multi-line prompt must stay ONE raw record and ONE prompt-rollup record: an
// unescaped newline here would be read back as extra rows with an empty agent,
// silently inflating a usage report instead of failing.
func TestMultiLinePromptStaysOneRecordInStore(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, istanbul)
	config := testConfig(t, now)
	path := filepath.Join(config.ClaudeRoot, "-srv-test", "session.jsonl")
	ts := "2026-07-26T08:00:00Z"
	prompt := "first line\n{\"day\":\"2026-07-26\",\"agent\":\"\",\"in\":999999}\nlast line"
	writeLines(t, path,
		claudeUser("u1", "", "prompt-1", ts, prompt, false),
		claudeAssistant("a1", "u1", "request-1", ts, 11),
	)
	if _, err := Collect(config); err != nil {
		t.Fatal(err)
	}

	for _, file := range []string{
		filepath.Join(config.StoreDir, "raw", "2026-07-26.jsonl"),
		filepath.Join(config.StoreDir, "prompts", "2026-07-26.jsonl"),
	} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if lines := strings.Count(string(data), "\n"); lines != 1 {
			t.Fatalf("%s holds %d lines, want 1: %s", filepath.Base(file), lines, data)
		}
	}

	rows, err := readRawDay(config.StoreDir, "2026-07-26")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Agent == "" || rows[0].In != 10 {
		t.Fatalf("raw rows=%+v, want a single attributed row", rows)
	}
	// The preview collapses whitespace, so no newline reaches the store at all.
	if strings.ContainsAny(rows[0].Prompt, "\n\r") {
		t.Fatalf("stored preview still carries line breaks: %q", rows[0].Prompt)
	}
}
