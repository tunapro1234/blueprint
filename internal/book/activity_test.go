package book

import (
	bptmux "blueprint/internal/tmux"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func runtimePane(t *testing.T, command, pane string) *bptmux.Client {
	t.Helper()
	dir := t.TempDir()
	screen := filepath.Join(dir, "screen")
	if err := os.WriteFile(screen, []byte(pane), 0600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$1\" in\nlist-panes) printf '1\\t" + command + "\\t0\\n';;\ncapture-pane) cat '" + screen + "';;\n*) exit 1;;\nesac\n"
	bin := filepath.Join(dir, "tmux")
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	c := bptmux.New()
	c.Bin = bin
	return c
}

func writeRuntimeCodex(t *testing.T, home, id, cwd, event string, stamp time.Time) string {
	t.Helper()
	dir := filepath.Join(home, "sessions/2026/09/05")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "rollout-"+id+".jsonl")
	f, e := os.Create(p)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	rows := []any{map[string]any{"type": "session_meta", "payload": map[string]any{"id": id, "cwd": cwd, "source": "cli"}}, map[string]any{"type": "event_msg", "timestamp": stamp, "payload": map[string]any{"type": event}}, map[string]any{"type": "event_msg", "timestamp": stamp, "payload": map[string]any{"type": "token_count", "info": map[string]any{"last_token_usage": map[string]int{"total_tokens": 987654}}}}}
	for _, row := range rows {
		if e := json.NewEncoder(f).Encode(row); e != nil {
			t.Fatal(e)
		}
	}
	return p
}

func TestRuntimeNeverBindsNewestCwdOrInheritedThread(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	id := "11111111-1111-1111-1111-111111111111"
	t.Setenv("CODEX_THREAD_ID", id)
	writeRuntimeCodex(t, home, id, "/work", "task_started", time.Now())
	c := runtimePane(t, "codex", "› Ask Codex to do anything\n gpt-6-astra high · /work\n")
	s, _ := RuntimeState(context.Background(), c, Agent{Name: "different-agent", Folder: "/work"})
	if s.Activity.State != "unknown" || s.Activity.ThreadID != "" || s.Known || !s.Activity.DeliveryBlocked {
		t.Fatalf("guessed thread/state: %+v %+v", s, s.Activity)
	}
}

func TestRuntimeWorkingImmediatelyAboveComposer(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	c := runtimePane(t, "node", "◦ Working (2h 4m 9s • esc to interrupt)\n› Ask Codex to do anything\n gpt-6-astra high · /work\n")
	s, _ := RuntimeState(context.Background(), c, Agent{Name: "blueprint", Folder: "/work"})
	if s.Activity.State != "working" || s.Activity.Source != "pane" || s.Activity.ThreadID != "" || s.Runtime != "codex" {
		t.Fatalf("lost visible work: %+v %+v", s, s.Activity)
	}
}

func TestRuntimeStaleOpenIsUnknownAndOldClosedIsIdle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	id := "11111111-1111-1111-1111-111111111111"
	c := runtimePane(t, "codex", "› Ask Codex to do anything\n gpt-6-astra high · /work\n")
	agent := Agent{Name: "agent", Folder: "/work", IdentityThreadID: id}
	for _, event := range []string{"task_started", "task_complete"} {
		p := writeRuntimeCodex(t, home, id, "/work", event, time.Now().Add(-2*time.Hour))
		_ = os.Chtimes(p, time.Now(), time.Now()) // Metadata touch is not a new turn.
		s, _ := RuntimeState(context.Background(), c, agent)
		want := "unknown"
		if event == "task_complete" {
			want = "idle"
		}
		if s.Activity.State != want || !s.Known || time.Since(s.UsageAt) < time.Hour {
			t.Fatalf("mtime substituted for event time: %+v %+v", s, s.Activity)
		}
	}
}

func TestClosedTranscriptConflictsWithWorkingPane(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	id := "11111111-1111-1111-1111-111111111111"
	writeRuntimeCodex(t, home, id, "/work", "task_complete", time.Now())
	c := runtimePane(t, "codex", "◦ Working (4s • esc to interrupt)\n› Ask Codex to do anything\n gpt-6-astra high · /work\n")
	s, _ := RuntimeState(context.Background(), c, Agent{Name: "agent", Folder: "/work", IdentityThreadID: id})
	if s.Activity.State != "unknown" || !s.Activity.DeliveryBlocked || s.Activity.TurnBusy == nil || *s.Activity.TurnBusy {
		t.Fatalf("contradictory evidence became certainty: %+v", s.Activity)
	}
}

func TestDuplicateRuntimeBindingsBlockDeliveryAndKeepThreadEvidence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	id := "11111111-1111-1111-1111-111111111111"
	writeRuntimeCodex(t, home, id, "/work", "task_complete", time.Now())
	c := runtimePane(t, "codex", "› Ask Codex to do anything\n gpt-6-astra high · /work\n")
	fleet := Fleet{Agents: map[string]Agent{"one": {Name: "one", Folder: "/work", IdentityThreadID: id}, "two": {Name: "two", Folder: "/work", IdentityThreadID: id}}}
	s := RuntimeFor(context.Background(), c, fleet, "one")
	if s.Activity.State != "unknown" || !s.Known || s.Activity.Binding != "" || s.Activity.ThreadID != id || len(s.Activity.BindingConflicts) != 1 {
		t.Fatalf("ambiguous metrics: %+v %+v", s, s.Activity)
	}
}

func TestClaudeRuntimeMapsOpMainAndIgnoresCompactMetadata(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	project := filepath.Join(root, "projects", "-srv-outpost")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(project, "11111111-1111-1111-1111-111111111111.jsonl")
	stamp := time.Now().Add(-time.Hour).Format(time.RFC3339Nano)
	text := `{"type":"custom-title","customTitle":"op-main"}` + "\n" + `{"type":"system","subtype":"turn_duration","timestamp":"` + stamp + `"}` + "\n" + `{"type":"user","isCompactSummary":true,"timestamp":"` + time.Now().Format(time.RFC3339Nano) + `","message":{"content":"summary"}}` + "\n" + `{"type":"bridge-session"}` + "\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	c := runtimePane(t, "claude", "────────────────────────\n❯ \n────────────────────────\n -- INSERT --\n")
	s, _ := RuntimeState(context.Background(), c, Agent{Name: "op-main", Folder: "/srv/outpost"})
	if s.Activity.State != "idle" || s.Activity.TranscriptPath != path || s.Runtime != "claude" {
		t.Fatalf("false post-compact activity: %+v %+v", s, s.Activity)
	}
	if err := os.WriteFile(filepath.Join(project, "other.jsonl"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	s, _ = RuntimeState(context.Background(), c, Agent{Name: "op-main", Folder: "/srv/outpost"})
	if s.Activity.State != "unknown" || s.Activity.Binding != "" {
		t.Fatalf("selected ambiguous session: %+v", s.Activity)
	}
}

func TestDisconnectedRemoteCannotUseFrozenWorkingFrame(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	id := "11111111-1111-1111-1111-111111111111"
	writeRuntimeCodex(t, home, id, "/work", "task_started", time.Now())
	c := runtimePane(t, "codex", "◦ Working (4s • esc to interrupt)\n› Ask Codex to do anything\n gpt-6-astra high · /work\n")
	s, _ := RuntimeState(context.Background(), c, Agent{Name: "agent", Folder: "/work", Launch: &bptmux.OpenOptions{Codex: true, ResumeID: id, Remote: "unix:///nonexistent-bp-runtime.sock"}})
	if s.Activity.State != "unknown" || !s.Activity.DeliveryBlocked || s.Activity.TurnBusy != nil {
		t.Fatalf("frozen screen became work proof: %+v", s.Activity)
	}
}

func TestTornAndDuplicatedRolloutsNeverAuthorizeIdle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	id := "11111111-1111-1111-1111-111111111111"
	p := writeRuntimeCodex(t, home, id, "/work", "task_complete", time.Now())
	c := runtimePane(t, "codex", "› Ask Codex to do anything\n gpt-6-astra high · /work\n")
	agent := Agent{Name: "agent", Folder: "/work", IdentityThreadID: id}
	f, e := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = f.WriteString(`{"type":"event_msg","payload":`)
	_ = f.Close()
	s, _ := RuntimeState(context.Background(), c, agent)
	if s.Activity.State != "unknown" {
		t.Fatalf("torn record ignored: %+v", s.Activity)
	}
	p = writeRuntimeCodex(t, home, id, "/work", "task_complete", time.Now())
	data, _ := os.ReadFile(p)
	if e := os.WriteFile(filepath.Join(filepath.Dir(p), "copy-"+id+".jsonl"), data, 0600); e != nil {
		t.Fatal(e)
	}
	s, _ = RuntimeState(context.Background(), c, agent)
	if s.Activity.State != "unknown" || s.Known || s.Activity.TranscriptPath != "" {
		t.Fatalf("duplicate chosen by mtime: %+v", s.Activity)
	}
}
