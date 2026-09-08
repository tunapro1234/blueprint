package book

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blueprint/internal/cache"
)

func TestLocalRuntimeNeedsLaunchBindingNotCWDTitleOrNewestFile(t *testing.T) {
	t.Run("claude", func(t *testing.T) {
		harness := "claude"
		dir := t.TempDir()
		id := "11111111-1111-1111-1111-111111111111"
		filename := id + ".jsonl"
		path := filepath.Join(dir, filename)
		stamp := time.Now().UTC().Format(time.RFC3339Nano)
		rows := []any{map[string]any{"type": "assistant", "effort": "medium", "timestamp": stamp, "message": map[string]any{"model": "claude-fable-5-1", "stop_reason": "end_turn"}}, map[string]any{"type": "system", "subtype": "turn_duration", "timestamp": stamp}}
		var data []byte
		for _, row := range rows {
			b, _ := json.Marshal(row)
			data = append(data, append(b, '\n')...)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		count := 30000
		o := cache.LocalObservation{SessionID: id, TranscriptPath: path, CWD: dir, Window: 200000, Context: &count, ObservedAt: time.Now().UTC()}
		b := &cache.LocalBinding{Path: filepath.Join(dir, "observation.json"), PID: 123, Harness: harness}
		dump, _ := json.Marshal(o)
		if err := os.WriteFile(b.Path, dump, 0600); err != nil {
			t.Fatal(err)
		}
		a := &cache.Activity{State: "unknown", ObservedAt: time.Now().UTC()}
		s := localRuntime(b, 123, a)
		if a.State != "idle" || a.ThreadID != id || s.Model == "" {
			t.Fatalf("state=%+v activity=%+v", s, a)
		}
		if harness == "claude" && (s.Window != 200000 || s.CtxTokens != 30000 || s.Effort != "medium") {
			t.Fatalf("Claude metrics: %+v", s)
		}
		a = &cache.Activity{State: "unknown", ObservedAt: time.Now().UTC()}
		s = localRuntime(b, 124, a)
		if a.State != "unknown" || s.Model != "" || !strings.Contains(a.Reason, "PID") {
			t.Fatal("reused pane accepted", s, a)
		}
	})
}

func TestLocalCodexMetadataRejectsAnotherThreadAndSubagent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	for _, source := range []any{"cli", map[string]any{"subagent": map[string]string{"parent_thread_id": "parent"}}} {
		data, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": "id", "cwd": dir, "source": source}})
		if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		o := cache.LocalObservation{SessionID: "id", CWD: dir, TranscriptPath: path}
		want := false
		if s, ok := source.(string); ok && s == "cli" {
			want = true
		}
		if localCodexMeta(o) != want {
			t.Fatal(source)
		}
		o.SessionID = "other"
		if localCodexMeta(o) {
			t.Fatal("different thread accepted")
		}
	}
}

func TestManualCompactCompletionClearsStaleLocalTurn(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-time.Hour).Format(time.RFC3339Nano)
	stamp := now.Format(time.RFC3339Nano)
	prompt := `{"type":"user","timestamp":"` + old + `","message":{"content":"old task"}}`
	boundary := `{"type":"system","subtype":"compact_boundary","timestamp":"` + stamp + `","compactMetadata":{"trigger":"manual"}}`
	summary := `{"type":"user","isCompactSummary":true,"timestamp":"` + stamp + `","message":{"content":"summary"}}`
	done := `{"type":"user","timestamp":"` + stamp + `","message":{"content":"<local-command-stdout>\u001b[2mCompacted (ctrl+o to see full summary)\u001b[22m</local-command-stdout>"}}`
	newer := `{"type":"user","timestamp":"` + now.Add(time.Millisecond).Format(time.RFC3339Nano) + `","message":{"content":"new task"}}`
	for _, tc := range []struct {
		name string
		rows []string
		want string
	}{
		{"manual", []string{prompt, boundary, summary, done}, "idle"},
		{"auto", []string{prompt, strings.ReplaceAll(boundary, "manual", "auto"), summary, done}, "unknown"},
		{"no completion", []string{prompt, boundary, summary}, "unknown"},
		{"no summary", []string{prompt, boundary, done}, "unknown"},
		{"new work", []string{prompt, boundary, summary, done, newer}, "working"},
		{"new work before echo", []string{prompt, boundary, summary, newer, done}, "working"},
		{"sidechain", []string{prompt, boundary, summary, strings.Replace(done, `"type":"user"`, `"type":"user","isSidechain":true`, 1)}, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			id := "11111111-1111-1111-1111-111111111111"
			path := filepath.Join(dir, id+".jsonl")
			if err := os.WriteFile(path, []byte(strings.Join(tc.rows, "\n")+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			obs := cache.LocalObservation{SessionID: id, TranscriptPath: path, CWD: dir, ObservedAt: now}
			b := &cache.LocalBinding{Path: filepath.Join(dir, "observation.json"), PID: 123, Harness: "claude"}
			data, _ := json.Marshal(obs)
			if err := os.WriteFile(b.Path, data, 0600); err != nil {
				t.Fatal(err)
			}
			a := &cache.Activity{State: "unknown", ObservedAt: now}
			localRuntime(b, 123, a)
			if a.State != tc.want {
				t.Fatalf("%+v; want %s", a, tc.want)
			}
		})
	}
}
