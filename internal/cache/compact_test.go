package cache

import (
	"path/filepath"
	"testing"
	"time"
)

func TestClaudeCompactReplacesOldContextAndIgnoresReplayedUsage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "session.jsonl")
	now := time.Now().UTC()
	boundary := map[string]any{"type": "system", "subtype": "compact_boundary", "timestamp": now.Add(-time.Minute), "compactMetadata": map[string]any{"postTokens": 12222}}
	rows := []any{user(now.Add(-time.Hour), "human instruction"), usage(now.Add(-2*time.Minute), 300000, 61000), boundary,
		map[string]any{"type": "user", "isCompactSummary": true, "timestamp": now, "message": map[string]any{"content": "compacted conversation"}},
		usage(now.Add(-2*time.Minute), 300000, 61000), // preserved old record after boundary
		map[string]any{"type": "user", "timestamp": now, "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "content": "output"}}}},
	}
	writeJSONL(t, p, rows)
	s := ReadClaudePath(p)
	if !s.Known || s.CtxTokens != 12222 || !s.UsageAt.Equal(now.Add(-time.Minute)) || s.LastHumanAge < 59*time.Minute {
		t.Fatalf("pre-compact usage or synthetic user won: %+v", s)
	}
	rows = append(rows, usage(now, 14000, 500))
	writeJSONL(t, p, rows)
	if s = ReadClaudePath(p); !s.Known || s.CtxTokens != 14500 {
		t.Fatalf("new post-compact measurement lost: %+v", s)
	}
}

func TestClaudeCompactWithoutPostTokensInvalidatesOldContext(t *testing.T) {
	p := filepath.Join(t.TempDir(), "session.jsonl")
	now := time.Now().UTC()
	writeJSONL(t, p, []any{usage(now.Add(-time.Minute), 300000, 61000), map[string]any{"type": "system", "subtype": "compact_boundary", "timestamp": now}})
	if s := ReadClaudePath(p); s.Known || !s.UsageAt.IsZero() {
		t.Fatalf("old context retained without post-compact measurement: %+v", s)
	}
}

func TestCodexCompactionWaitsForNewContextMeasurement(t *testing.T) {
	p := filepath.Join(t.TempDir(), "session.jsonl")
	now := time.Now().UTC()
	token := func(n int, stamp time.Time) any {
		return map[string]any{"type": "event_msg", "timestamp": stamp, "payload": map[string]any{"type": "token_count", "info": map[string]any{"last_token_usage": map[string]int{"total_tokens": n}}}}
	}
	for _, marker := range []any{map[string]any{"type": "compacted", "timestamp": now}, map[string]any{"type": "event_msg", "timestamp": now, "payload": map[string]any{"type": "context_compacted"}}} {
		rows := []any{token(240000, now.Add(-time.Minute)), marker}
		writeJSONL(t, p, rows)
		if s := ReadCodexPath(p); s.Known || !s.UsageAt.IsZero() {
			t.Fatalf("old context shown after compact: %+v", s)
		}
		writeJSONL(t, p, append(rows, token(18000, now.Add(time.Second))))
		if s := ReadCodexPath(p); !s.Known || s.CtxTokens != 18000 {
			t.Fatalf("new context lost after compact: %+v", s)
		}
	}
}
