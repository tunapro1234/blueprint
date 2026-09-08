package book

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalLineageHintsNeverGrantAuthority(t *testing.T) {
	const root = "11111111-1111-1111-1111-111111111111"
	const child = "22222222-2222-2222-2222-222222222222"
	const other = "33333333-3333-3333-3333-333333333333"
	home := t.TempDir()
	dir := filepath.Join(home, "sessions/2026/09/08")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{root, child, other} {
		var source any = "cli"
		if id == child {
			source = map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": root}}}
		}
		data, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": id, "source": source}})
		if err := os.WriteFile(filepath.Join(dir, "rollout-"+id+".jsonl"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{root, child, other, "missing", "../../escape"} {
		who := localLineageHint(home, "local-codex", root, id)
		if who.Authoritative() || who.Certain {
			t.Fatalf("hint acquired authority: %+v", who)
		}
		switch id {
		case root:
			if who.Label != "local-codex?" || who.Parent != "" {
				t.Fatal(who)
			}
		case child:
			if who.Label != "local-codex/subagent:"+child+"?" || who.Parent != "local-codex" {
				t.Fatal(who)
			}
		default:
			if who.Label != "" {
				t.Fatal(who)
			}
		}
	}
}
