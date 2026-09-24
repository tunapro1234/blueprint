package book

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAwaitingUserClaudeIgnoresToolResultsAndMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	rows := []any{
		map[string]any{"type": "user", "timestamp": "2026-09-24T10:00:00Z", "message": map[string]any{"content": "question"}},
		map[string]any{"type": "assistant", "timestamp": "2026-09-24T10:00:01Z", "message": map[string]any{"stop_reason": "tool_use"}},
		map[string]any{"type": "user", "timestamp": "2026-09-24T10:00:02Z", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "content": "ok"}}}},
		map[string]any{"type": "assistant", "timestamp": "2026-09-24T10:00:03Z", "message": map[string]any{"stop_reason": "end_turn"}},
		map[string]any{"type": "system", "subtype": "turn_duration", "timestamp": "2026-09-24T10:00:04Z"},
		map[string]any{"type": "last-prompt", "timestamp": "2026-09-24T10:00:05Z"},
	}
	writeAttentionRows(t, path, rows)
	want := time.Date(2026, 9, 24, 10, 0, 3, 0, time.UTC)
	if awaiting, at := AwaitingUser(path, "claude"); !awaiting || !at.Equal(want) {
		t.Fatalf("awaiting=%v at=%v, want true at %v", awaiting, at, want)
	}
	rows = append(rows, map[string]any{"type": "user", "timestamp": "2026-09-24T10:01:00Z", "message": map[string]any{"content": "follow-up"}})
	writeAttentionRows(t, path, rows)
	if awaiting, at := AwaitingUser(path, "claude"); awaiting || !at.Equal(want) {
		t.Fatalf("new prompt: awaiting=%v replyAt=%v, want false and latest reply %v", awaiting, at, want)
	}
}

func TestAwaitingUserCodexRequiresCompletedTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	rows := []any{
		map[string]any{"type": "response_item", "timestamp": "2026-09-24T10:00:00Z", "payload": map[string]any{"type": "message", "role": "user"}},
		map[string]any{"type": "event_msg", "timestamp": "2026-09-24T10:00:01Z", "payload": map[string]any{"type": "task_started"}},
		map[string]any{"type": "response_item", "timestamp": "2026-09-24T10:00:02Z", "payload": map[string]any{"type": "message", "role": "assistant"}},
	}
	writeAttentionRows(t, path, rows)
	if awaiting, _ := AwaitingUser(path, "codex"); awaiting {
		t.Fatal("in-progress assistant output reported as awaiting user")
	}
	rows = append(rows, map[string]any{"type": "event_msg", "timestamp": "2026-09-24T10:00:03Z", "payload": map[string]any{"type": "task_complete"}})
	writeAttentionRows(t, path, rows)
	if awaiting, at := AwaitingUser(path, "codex"); !awaiting || at.IsZero() {
		t.Fatalf("awaiting=%v at=%v, want completed reply", awaiting, at)
	}
	rows = append(rows,
		map[string]any{"type": "response_item", "timestamp": "2026-09-24T10:01:00Z", "payload": map[string]any{"type": "message", "role": "user"}},
		map[string]any{"type": "event_msg", "timestamp": "2026-09-24T10:01:01Z", "payload": map[string]any{"type": "task_started"}},
	)
	writeAttentionRows(t, path, rows)
	wantReply := time.Date(2026, 9, 24, 10, 0, 2, 0, time.UTC)
	if awaiting, at := AwaitingUser(path, "codex"); awaiting || !at.Equal(wantReply) {
		t.Fatalf("in-progress next turn: awaiting=%v replyAt=%v, want false and latest reply %v", awaiting, at, wantReply)
	}
}

func writeAttentionRows(t *testing.T, path string, rows []any) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
