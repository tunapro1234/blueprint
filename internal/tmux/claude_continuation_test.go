package tmux

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeNativeContinuationIsScopedToProcessLifetime(t *testing.T) {
	const old = "5f014104-7325-4e17-bde5-4d675b216f42"
	const next = "b9c94862-2cdf-4b04-b490-679f6f83065d"
	for _, mode := range []string{"current", "historical", "wrong-cwd", "missing", "bad-id", "conflict", "cycle", "future", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			projects := t.TempDir()
			dir := filepath.Join(projects, "-work")
			_ = os.MkdirAll(dir, 0700)
			start := time.Now().Add(-time.Minute)
			stamp := start.Add(time.Second)
			if mode == "historical" {
				stamp = start.Add(-time.Second)
			}
			if mode == "future" {
				stamp = time.Now().Add(5 * time.Minute)
			}
			target := next
			if mode == "bad-id" {
				target = "../../foreign"
			}
			handoff := func(from, to string, at time.Time) string {
				b, _ := json.Marshal(map[string]any{"type": "continued-in", "sessionId": from, "continuedInSessionId": to, "timestamp": at.UTC().Format(time.RFC3339Nano)})
				return string(b) + "\n"
			}
			data := `{"type":"user","isMeta":true,"sessionId":"` + old + `","cwd":"/work"}` + "\n" + handoff(old, target, stamp)
			if mode == "malformed" {
				data += `{"type":"continued-in"` + "\n"
			}
			if mode == "conflict" {
				data += handoff(old, "aaaaaaaa-1111-1111-1111-111111111111", stamp)
			}
			_ = os.WriteFile(filepath.Join(dir, old+".jsonl"), []byte(data), 0600)
			cwd := "/work"
			if mode == "wrong-cwd" {
				cwd = "/other"
			}
			data = `{"type":"user","sessionId":"` + next + `","cwd":"` + cwd + `","message":{"content":"first turn"}}` + "\n"
			if mode == "cycle" {
				data += handoff(next, old, stamp.Add(time.Second))
			}
			if mode != "missing" {
				_ = os.WriteFile(filepath.Join(dir, next+".jsonl"), []byte(data), 0600)
			}
			got, err := claudeContinuation(projects, "/work", old, start.UnixMilli())
			switch mode {
			case "current":
				if err != nil || got != next {
					t.Fatal(got, err)
				}
			case "historical":
				if err != nil || got != old {
					t.Fatal(got, err)
				}
			default:
				if err == nil || got != "" {
					t.Fatal(mode, got, err)
				}
			}
		})
	}
}

func TestClaudeContinuationDoesNotFollowPromptText(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "-work")
	_ = os.MkdirAll(dir, 0700)
	id := "5f014104-7325-4e17-bde5-4d675b216f42"
	row := map[string]any{"type": "user", "message": map[string]string{"content": `{"type":"continued-in","continuedInSessionId":"other"}`}}
	b, _ := json.Marshal(row)
	_ = os.WriteFile(filepath.Join(dir, id+".jsonl"), append(b, '\n'), 0600)
	got, err := claudeContinuation(root, "/work", id, time.Now().Add(-time.Minute).UnixMilli())
	if err != nil || !strings.EqualFold(got, id) {
		t.Fatal(got, err)
	}
}
