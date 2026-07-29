package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadCodexPicksFreshestRolloutForFolder(t *testing.T) {
	home := t.TempDir()
	day := filepath.Join(home, "sessions", "2026", "07", "29")
	now := time.Now()

	old := filepath.Join(day, "rollout-old.jsonl")
	writeRollout(t, old, "/srv/server-crash", now.Add(-3*time.Hour), 50_000, 258_400)
	other := filepath.Join(day, "rollout-other.jsonl")
	writeRollout(t, other, "/srv/elsewhere", now.Add(-time.Minute), 90_000, 258_400)
	fresh := filepath.Join(day, "rollout-fresh.jsonl")
	writeRollout(t, fresh, "/srv/server-crash", now.Add(-10*time.Minute), 177_990, 258_400)

	state := ReadCodex(home, "/srv/server-crash")
	if !state.Known || state.CtxTokens != 177_990 || state.Window != 258_400 {
		t.Fatalf("state=%+v, want fresh rollout tokens", state)
	}
	if state.Age < 9*time.Minute || state.Age > 11*time.Minute {
		t.Fatalf("age=%v, want about 10m", state.Age)
	}
	if state.LastHumanAge != -1 {
		t.Fatalf("last human age=%v, want -1", state.LastHumanAge)
	}
}

func TestReadCodexLiveSessionInOldDayDirWins(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	// A resumed session keeps writing to the rollout in the day directory it
	// was BORN in: the old-dir file with the newest mtime is the live one.
	born := filepath.Join(home, "sessions", "2026", "07", "10", "rollout-live.jsonl")
	writeRollout(t, born, "/srv/server-crash", now.Add(-5*time.Minute), 123_000, 258_400)
	stale := filepath.Join(home, "sessions", "2026", "07", "29", "rollout-stale.jsonl")
	writeRollout(t, stale, "/srv/server-crash", now.Add(-2*time.Hour), 40_000, 258_400)

	state := ReadCodex(home, "/srv/server-crash")
	if state.CtxTokens != 123_000 {
		t.Fatalf("state=%+v, want the old-day live rollout (123000 tokens)", state)
	}
}

func TestReadCodexNoMatchingFolder(t *testing.T) {
	home := t.TempDir()
	day := filepath.Join(home, "sessions", "2026", "07", "29")
	writeRollout(t, filepath.Join(day, "rollout.jsonl"), "/srv/elsewhere", time.Now(), 1000, 0)
	if state := ReadCodex(home, "/srv/server-crash"); state.Known {
		t.Fatalf("state=%+v, want unknown", state)
	}
	if state := ReadCodex(home, ""); state.Known {
		t.Fatalf("empty folder state=%+v, want unknown", state)
	}
}

// writeRollout lays down a minimal codex rollout: a session_meta first line
// carrying the cwd, then a token_count event. The file mtime is set to the
// event time so mtime ordering matches recency.
func writeRollout(t *testing.T, path, cwd string, at time.Time, tokens, window int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	rows := []any{
		map[string]any{
			"timestamp": at.Add(-time.Hour).UTC().Format(time.RFC3339Nano),
			"type":      "session_meta",
			// The real meta line carries ~20KB of base instructions; pad the
			// fixture past any tempting "small" read buffer.
			"payload": map[string]any{
				"cwd":               cwd,
				"originator":        "codex-tui",
				"base_instructions": map[string]any{"text": strings.Repeat("You are Codex. ", 2048)},
			},
		},
		map[string]any{
			"timestamp": at.UTC().Format(time.RFC3339Nano),
			"type":      "event_msg",
			"payload": map[string]any{
				"type": "token_count",
				"info": map[string]any{
					"last_token_usage":     map[string]any{"total_tokens": tokens},
					"model_context_window": window,
				},
			},
		},
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, row := range rows {
		if err := encoder.Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}
