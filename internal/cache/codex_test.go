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

// Same tail-window trap as the Claude transcript, with more headroom to lose:
// codex rollout records reach 7.1 MB in this fleet against a 500 KB window.
func TestReadCodexWidensTailPastRecordLargerThanWindow(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	path := filepath.Join(home, "sessions", "2026", "07", "29", "rollout-big.jsonl")
	writeRollout(t, path, "/srv/server-crash", now.Add(-8*time.Minute), 177_990, 258_400)

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	row := map[string]any{
		"timestamp": now.Add(-time.Minute).UTC().Format(time.RFC3339Nano),
		"type":      "response_item",
		"payload":   map[string]any{"type": "function_call_output", "output": strings.Repeat("z", tailSize*2)},
	}
	if err := json.NewEncoder(file).Encode(row); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	state := ReadCodex(home, "/srv/server-crash")
	if !state.Known || state.CtxTokens != 177_990 || state.Window != 258_400 {
		t.Fatalf("state=%+v, want the token_count behind the oversized record", state)
	}
}

func TestCodexMetricsSurviveToolOutputAndPartialWrites(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "sessions/2026/09/05/rollout-session.jsonl")
	now := time.Now()
	writeRollout(t, path, "/work", now.Add(-time.Minute), 123456, 258400)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, row := range []any{
		map[string]any{"type": "turn_context", "payload": map[string]any{"model": "gpt-6-astra", "effort": "high", "service_tier": "priority"}},
		map[string]any{"type": "response_item", "timestamp": now.Format(time.RFC3339Nano), "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "please check this"}}}},
		map[string]any{"type": "event_msg", "timestamp": now.Format(time.RFC3339Nano), "payload": map[string]any{"type": "task_started"}},
	} {
		if err := enc.Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	// Many COMPLETE rows used to stop readTail widening, hiding token_count.
	for i := 0; i < 20; i++ {
		_ = enc.Encode(map[string]any{"type": "response_item", "payload": map[string]any{"type": "function_call_output", "output": strings.Repeat("x", 64000)}})
	}
	_, _ = f.WriteString(`{"type":"event_msg","payload":{"type":"token_count","info":`)
	_ = f.Close()
	got := ReadCodex(home, "/work")
	if !got.Known || got.CtxTokens != 123456 || got.Model != "gpt-6-astra" || got.Effort != "high" || !got.Busy || got.LastHumanAge < 0 {
		t.Fatalf("lost metrics: %+v", got)
	}
	if got.Age < 50*time.Second {
		t.Fatalf("tool activity reset usage age: %v", got.Age)
	}
}

func TestCodexIgnoresExecAndSubagentSessions(t *testing.T) {
	home := t.TempDir()
	day := filepath.Join(home, "sessions/2026/09/05")
	writeRollout(t, filepath.Join(day, "rollout-owner.jsonl"), "/work", time.Now().Add(-time.Minute), 123, 0)
	for name, source := range map[string]any{"exec": "exec", "child": map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": "owner"}}}} {
		row := map[string]any{"type": "session_meta", "payload": map[string]any{"cwd": "/work", "source": source}}
		data, _ := json.Marshal(row)
		_ = os.WriteFile(filepath.Join(day, "rollout-"+name+".jsonl"), append(data, '\n'), 0600)
	}
	if got := ReadCodex(home, "/work"); !got.Known || got.CtxTokens != 123 {
		t.Fatalf("selected helper: %+v", got)
	}
	if _, ok := CodexPath(home, "/work", "missing"); ok {
		t.Fatal("missing pinned thread fell back to another session")
	}
}

func TestReverseRolloutSkipsGiantRecordWithoutLosingEarlierRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	data := "first\n" + strings.Repeat("x", maxTailSize+65536) + "\nlast\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	var rows []string
	ScanCodexReverse(path, func(row []byte) bool {
		rows = append(rows, string(row))
		return true
	})
	if strings.Join(rows, ",") != "last,first" {
		t.Fatalf("wrong rows after giant record: count=%d", len(rows))
	}
}
