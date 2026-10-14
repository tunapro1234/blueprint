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

// TestRenamedAgentResolvesByProcessNotTitle locks the fix for the server-main
// false alarm (2026-10-14): a Claude agent whose orchestration name no longer
// equals the customTitle recorded in its transcript (renamed 2026-10-07:
// registry "server-main", transcript still "server-main-claude") must still
// resolve a session file. The title probe (ResumeSessionPath, keyed on the
// name) cannot bridge that divergence; the process binding (pane PID -> child
// claude -> sessions/<pid>.json -> sessionId) does, and it is what the status
// reader and the pane-sanity watchdog now use.
func TestRenamedAgentResolvesByProcessNotTitle(t *testing.T) {
	proc, sessions := t.TempDir(), filepath.Join(t.TempDir(), "sessions")
	projects := filepath.Join(filepath.Dir(sessions), "projects")
	put := func(path, data string) {
		t.Helper()
		if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(path, []byte(data), 0600); e != nil {
			t.Fatal(e)
		}
	}
	const (
		registryName = "server-main"        // the agentbook key the watchdog keys on
		transTitle   = "server-main-claude" // the stale customTitle in the transcript
		id           = "e4355d7d-a888-414e-bd56-78a309328326"
	)
	// Fake pane process tree: pane shell -> interactive claude writing `id`.
	put(filepath.Join(proc, "10/comm"), "zsh\n")
	put(filepath.Join(proc, "10/task/10/children"), "11")
	put(filepath.Join(proc, "11/comm"), "claude\n")
	fields := strings.Fields(strings.Repeat("0 ", 20))
	fields[19] = "777"
	put(filepath.Join(proc, "11/stat"), "11 (claude) "+strings.Join(fields, " "))
	if e := os.Symlink("/srv", filepath.Join(proc, "11/cwd")); e != nil {
		t.Fatal(e)
	}
	r := map[string]any{"pid": 11, "sessionId": id, "cwd": "/srv", "procStart": "777", "kind": "interactive"}
	data, _ := json.Marshal(r)
	put(filepath.Join(sessions, "11.json"), string(data))
	// The transcript carries the OLD title, not the current registry name.
	put(filepath.Join(projects, mungeProjectPath("/srv"), id+".jsonl"),
		`{"type":"custom-title","customTitle":"`+transTitle+`","sessionId":"`+id+`"}`+"\n")

	// Title probe keyed on the registry name finds nothing — the old watchdog's
	// exact failure.
	if path, ok := ResumeSessionPath(projects, "/srv", registryName); ok {
		t.Fatalf("title probe unexpectedly resolved %q for name %q", path, registryName)
	}
	// It would have matched the stale title, which is precisely why keying on a
	// mutable display name is wrong.
	if _, ok := ResumeSessionPath(projects, "/srv", transTitle); !ok {
		t.Fatal("sanity: title probe should match the stale transcript title")
	}
	// Process binding resolves regardless of the title divergence.
	got, err := claudeProcessSession(proc, sessions, 10, "/srv")
	if err != nil || got != id {
		t.Fatalf("process binding: got %q err %v, want %q", got, err, id)
	}
	if _, err := ClaudeSessionPathForID(projects, "/srv", got); err != nil {
		t.Fatalf("ClaudeSessionPathForID(%q): %v", got, err)
	}
}
