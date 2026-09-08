package tmux

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeProcessSessionChecksLivePIDAndIgnoresNestedAgent(t *testing.T) {
	for _, mode := range []string{"valid", "reused-pid", "wrong-cwd", "wrong-id", "noninteractive", "missing", "continued", "historical-continuation"} {
		t.Run(mode, func(t *testing.T) {
			proc, sessions := t.TempDir(), filepath.Join(t.TempDir(), "sessions")
			put := func(path, data string) {
				t.Helper()
				if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(path, []byte(data), 0600); e != nil {
					t.Fatal(e)
				}
			}
			put(filepath.Join(proc, "10/comm"), "zsh\n")
			put(filepath.Join(proc, "10/task/10/children"), "11")
			put(filepath.Join(proc, "11/comm"), "claude\n")
			put(filepath.Join(proc, "11/task/11/children"), "12")
			put(filepath.Join(proc, "12/comm"), "claude\n")
			fields := strings.Fields(strings.Repeat("0 ", 20))
			fields[19] = "777"
			put(filepath.Join(proc, "11/stat"), "11 (claude) "+strings.Join(fields, " "))
			if e := os.Symlink("/work", filepath.Join(proc, "11/cwd")); e != nil {
				t.Fatal(e)
			}
			id := "4bd47c4f-e853-4eff-bdd0-f3b8444531f8"
			r := map[string]any{"pid": 11, "sessionId": id, "cwd": "/work", "procStart": "777", "kind": "interactive"}
			switch mode {
			case "reused-pid":
				r["procStart"] = "old"
			case "wrong-cwd":
				r["cwd"] = "/other"
			case "wrong-id":
				r["sessionId"] = "../../other"
			case "noninteractive":
				r["kind"] = "subagent"
			}
			expected := id
			if mode == "continued" || mode == "historical-continuation" {
				start := time.Now().Add(-time.Minute)
				r["startedAt"] = start.UnixMilli()
				at := start.Add(time.Second)
				next := "b9c94862-2cdf-4b04-b490-679f6f83065d"
				expected = next
				if mode == "historical-continuation" {
					at = start.Add(-time.Second)
					expected = id
				}
				data, _ := json.Marshal(map[string]any{"type": "continued-in", "sessionId": id, "continuedInSessionId": next, "timestamp": at.UTC().Format(time.RFC3339Nano)})
				projects := filepath.Join(filepath.Dir(sessions), "projects", "-work")
				put(filepath.Join(projects, id+".jsonl"), string(data)+"\n")
				put(filepath.Join(projects, next+".jsonl"), `{"type":"user","sessionId":"`+next+`","cwd":"/work"}`+"\n")
			}
			if mode != "missing" {
				data, _ := json.Marshal(r)
				put(filepath.Join(sessions, "11.json"), string(data))
			}
			// A nested agent must never be considered as the pane's conversation.
			put(filepath.Join(sessions, "12.json"), `{"pid":12,"sessionId":"invalid"}`)
			got, err := claudeProcessSession(proc, sessions, 10, "/work")
			if mode == "valid" || mode == "continued" || mode == "historical-continuation" {
				if got != expected || err != nil {
					t.Fatal(got, err)
				}
			} else if mode == "missing" {
				if got != "" || err != nil {
					t.Fatal(got, err)
				}
			} else if got != "" || err == nil {
				t.Fatal(mode, got, err)
			}
		})
	}
}

func TestClaudeBindingExplainsDuplicateAndMissingPin(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "-work")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	ids := []string{"4bd47c4f-e853-4eff-bdd0-f3b8444531f8", "f9e902f0-1111-1111-1111-111111111111"}
	for _, id := range ids {
		if e := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte("{\"type\":\"custom-title\",\"customTitle\":\"agent\"}\n"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	if _, err := ResolveSessionPath(root, "/work", "agent", ""); err == nil || !strings.Contains(err.Error(), ids[0]) || !strings.Contains(err.Error(), ids[1]) {
		t.Fatal(err)
	}
	if p, err := ResolveSessionPath(root, "/work", "agent", ids[0]); err != nil || !strings.HasSuffix(p, ids[0]+".jsonl") {
		t.Fatal(p, err)
	}
	if _, err := ResolveSessionPath(root, "/work", "agent", "aaaaaaaa-1111-1111-1111-111111111111"); err == nil || !strings.Contains(err.Error(), "pin") {
		t.Fatal(err)
	}
}
