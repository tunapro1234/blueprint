package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheAgeAndLastHuman(t *testing.T) {
	root, folder, agent := t.TempDir(), "/srv/project", "ada"
	dir := filepath.Join(root, "-srv-project")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.jsonl")
	now := time.Now()
	lines := []any{
		map[string]any{"type": "custom-title", "customTitle": agent},
		user(now.Add(-30*time.Hour), "real old message"),
		user(now.Add(-4*time.Hour), "[ANNOUNCE ada] automated"),
		user(now.Add(-3*time.Hour), "[ ada DUYURU (27 Tem)]\nautomated"),
		user(now.Add(-2*time.Hour), "health-watch: automated"),
		user(now.Add(-90*time.Minute), "[usage-policy] automated"),
		user(now.Add(-80*time.Minute), "[3 birikmis duyuru — 26-27 Tem]\nautomated"),
		usage(now.Add(-59*time.Minute), 150_000, 70_000),
	}
	writeJSONL(t, path, lines)
	state := Read(root, folder, agent)
	if !state.Known || state.Age < 58*time.Minute || state.Age >= 60*time.Minute {
		t.Fatalf("warm state=%+v", state)
	}
	if state.CtxTokens != 220_000 {
		t.Fatalf("ctx=%d, want 220000", state.CtxTokens)
	}
	if state.LastHumanAge < 29*time.Hour || state.LastHumanAge > 31*time.Hour {
		t.Fatalf("last human age=%v, want about 30h", state.LastHumanAge)
	}

	lines = append(lines, user(now.Add(-20*time.Minute), "actual user message"), usage(now.Add(-61*time.Minute), 200_000, 50_000))
	writeJSONL(t, path, lines)
	state = Read(root, folder, agent)
	if !state.Known || state.Age < 60*time.Minute {
		t.Fatalf("cold state=%+v", state)
	}
	if state.LastHumanAge < 19*time.Minute || state.LastHumanAge > 21*time.Minute {
		t.Fatalf("last human age=%v, want about 20m", state.LastHumanAge)
	}
}

func user(timestamp time.Time, text string) any {
	return map[string]any{
		"type":      "user",
		"timestamp": timestamp.Format(time.RFC3339Nano),
		"message":   map[string]any{"role": "user", "content": text},
	}
}

func usage(timestamp time.Time, read, creation int) any {
	return map[string]any{
		"type":      "assistant",
		"timestamp": timestamp.Format(time.RFC3339Nano),
		"message": map[string]any{
			"role":  "assistant",
			"usage": map[string]any{"cache_read_input_tokens": read, "cache_creation_input_tokens": creation},
		},
	}
}

func writeJSONL(t *testing.T, path string, rows []any) {
	t.Helper()
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
}
