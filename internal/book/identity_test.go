package book

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"blueprint/internal/identity"
)

const identityRoot = "11111111-1111-1111-1111-111111111111"
const identityOther = "22222222-2222-2222-2222-222222222222"
const identityChild = "33333333-3333-3333-3333-333333333333"

func threadMeta(t *testing.T, home, id string, source any) string {
	t.Helper()
	dir := filepath.Join(home, "sessions/2026/09/05")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-test-"+id+".jsonl")
	data, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": id, "cwd": "/same/cwd", "source": source}})
	if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestThreadIdentityRequiresUniquePinAndMetadata(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "agentbook.json")
	data := `{"orchestrator":"server-main","agents":[{"name":"server-main","folder":"/same/cwd","identityThreadId":"` + identityRoot + `"},{"name":"astra","folder":"/same/cwd","identityThreadId":"` + identityOther + `"}]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	threadMeta(t, home, identityRoot, "cli")
	threadMeta(t, home, identityOther, "appServer")
	threadMeta(t, home, identityChild, map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": identityOther}}})
	for _, tc := range []struct {
		id, label, parent string
		authority         bool
	}{
		{identityRoot, "server-main", "", true}, {identityOther, "astra", "", true},
		{identityChild, "astra/subagent:" + identityChild, "astra", false},
		{"44444444-4444-4444-4444-444444444444", "codex?:44444444-4444-4444-4444-444444444444", "", false},
	} {
		got := ThreadIdentity(context.Background(), []string{path}, home, tc.id)
		if got.Label != tc.label || got.Parent != tc.parent || got.Authoritative() != tc.authority {
			t.Fatalf("%s: %+v", tc.id, got)
		}
	}
	// Same cwd is deliberately irrelevant; a second pin to the same ID is ambiguous.
	if err := os.WriteFile(path, []byte(`{"orchestrator":"server-main","agents":[{"name":"server-main","identityThreadId":"`+identityRoot+`"},{"name":"other","identityThreadId":"`+identityRoot+`"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := ThreadIdentity(context.Background(), []string{path}, home, identityRoot); got.Certain || got.Label == "server-main" {
		t.Fatal(got)
	}
	// A loop or corrupted ID must never produce the parent's identity.
	threadMeta(t, home, identityChild, map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": identityChild}}})
	if got := ThreadIdentity(context.Background(), []string{path}, home, identityChild); got.Certain {
		t.Fatal(got)
	}
	if got := ThreadIdentity(context.Background(), []string{path}, home, "../../server-main"); got.Label != identity.Unknown {
		t.Fatal(got)
	}
}
