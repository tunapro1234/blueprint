package cache

import (
	"path/filepath"
	"testing"
	"time"
)

func TestClaudeTTLComesFromLatestWriteNotGlobalHour(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hour, five int
		elapsed    time.Duration
		want       string
	}{
		{"probot-egitim-36m", 3743, 0, 36 * time.Minute, "warm~"},
		{"five-minute-expired", 0, 3743, 36 * time.Minute, "cold~"},
		{"five-minute-recent", 0, 3743, time.Minute, "warm~"},
		{"hour-expired", 3743, 0, 61 * time.Minute, "cold~"},
		{"mixed-not-known", 3000, 743, time.Minute, "age"},
		{"read-hit-without-ttl", 0, 0, time.Minute, "age"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			row := usage(time.Now().Add(-tc.elapsed), 86350, tc.hour+tc.five).(map[string]any)
			m := row["message"].(map[string]any)
			m["usage"].(map[string]any)["cache_creation"] = map[string]int{"ephemeral_1h_input_tokens": tc.hour, "ephemeral_5m_input_tokens": tc.five}
			writeJSONL(t, path, []any{row})
			s := ReadClaudePath(path)
			if got, _ := s.CacheHint(); got != tc.want {
				t.Fatalf("got %s, state %+v", got, s)
			}
			// A compact snapshot has token metrics but proves no cache write.
			writeJSONL(t, path, []any{row, map[string]any{"type": "system", "subtype": "compact_boundary", "timestamp": time.Now().Format(time.RFC3339Nano), "compactMetadata": map[string]int{"postTokens": 1000}}})
			if got, _ := ReadClaudePath(path).CacheHint(); got != "age" {
				t.Fatal("compact inherited cache TTL", got)
			}
		})
	}
}

func TestStreamedMessageDoesNotRefreshCacheClock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	now := time.Now()
	var rows []any
	for _, elapsed := range []time.Duration{6 * time.Minute, time.Minute} {
		row := usage(now.Add(-elapsed), 100, 20).(map[string]any)
		m := row["message"].(map[string]any)
		m["id"] = "same-response"
		m["usage"].(map[string]any)["cache_creation"] = map[string]int{"ephemeral_5m_input_tokens": 20}
		rows = append(rows, row)
	}
	writeJSONL(t, path, rows)
	s := ReadClaudePath(path)
	if got, _ := s.CacheHint(); got != "cold~" || s.Age > 2*time.Minute {
		t.Fatalf("last chunk renewed cache: %+v", s)
	}
}

func TestModelNameAndRecentTokensCannotProveCodexTTL(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.5", "claude-fable-5-1"} {
		s := State{Known: true, Model: model, Age: 36 * time.Minute}
		if got, _ := s.CacheHint(); got != "age" {
			t.Fatal(model, got)
		}
	}
}
