package book

import (
	"blueprint/internal/cache"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bptmux "blueprint/internal/tmux"
)

func TestCodexWriterObservationOnlyForEmbeddedPane(t *testing.T) {
	for _, tc := range []struct {
		name  string
		info  bptmux.CodexProcess
		agent Agent
		want  bool
	}{
		{"fresh", bptmux.CodexProcess{}, Agent{}, true},
		{"argv", bptmux.CodexProcess{ThreadID: "thread"}, Agent{}, true},
		{"identity pin", bptmux.CodexProcess{}, Agent{IdentityThreadID: "thread"}, true},
		{"resume pin", bptmux.CodexProcess{}, Agent{Launch: &bptmux.OpenOptions{Codex: true, ResumeID: "thread"}}, true},
		{"remote argv", bptmux.CodexProcess{Remote: "unix://"}, Agent{}, false},
		{"stale remote book with observed embedded CLI", bptmux.CodexProcess{Observed: true}, Agent{Launch: &bptmux.OpenOptions{Codex: true, Remote: "unix://"}}, true},
		{"remote book", bptmux.CodexProcess{}, Agent{Launch: &bptmux.OpenOptions{Codex: true, Remote: "unix://"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsCodexWriterBinding(tc.info, tc.agent); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	// No process/lock is not evidence of an idle agent, even with a familiar cwd.
	a := &cache.Activity{State: "unknown", ObservedAt: time.Now().UTC()}
	codexRuntime(context.Background(), 0, Agent{Folder: "/srv/blueprint"}, a)
	if a.State != "unknown" || a.ThreadID != "" {
		t.Fatalf("invented binding: %+v", a)
	}
}

func TestClaudeRuntimeRejectsForeignPinAndBindsDuplicateTitlesByProcess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	dir := filepath.Join(home, "projects", "-work")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	id := "69632f85-5244-4ec1-866c-31aa4adef0be"
	row := "{\"type\":\"custom-title\",\"customTitle\":\"agent\"}\n" + `{"type":"assistant","timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","message":{"role":"assistant","model":"claude-opus-5","stop_reason":"end_turn","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":100}}}` + "\n"
	if e := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(row), 0600); e != nil {
		t.Fatal(e)
	}
	agent := Agent{Name: "agent", Folder: "/work", Launch: &bptmux.OpenOptions{Codex: true, ResumeID: "01a07246-1e79-73b0-9b69-74e791e68208"}}
	observe := func(liveID string) *cache.Activity {
		a := &cache.Activity{State: "unknown", ObservedAt: time.Now()}
		claudeRuntimeBound(agent, a, liveID)
		return a
	}
	if a := observe(""); a.ThreadID != id {
		t.Fatalf("Codex pin filtered Claude: %+v", a)
	}
	other := "aaaaaaaa-1111-1111-1111-111111111111"
	if e := os.WriteFile(filepath.Join(dir, other+".jsonl"), []byte(row), 0600); e != nil {
		t.Fatal(e)
	}
	if a := observe(""); a.ThreadID != "" || !strings.Contains(a.Reason, "ambiguous") {
		t.Fatal(a)
	}
	if a := observe(id); a.ThreadID != id || a.Binding != "claude-process-session" {
		t.Fatal(a)
	}
	agent.Launch = &bptmux.OpenOptions{ResumeID: other}
	agent.Name = "stale-name-before-rename"
	if a := observe(id); a.ThreadID != id || a.State != "idle" {
		t.Fatal(a)
	}
	agent.Launch.ResumeID = "bbbbbbbb-1111-1111-1111-111111111111"
	if a := observe(""); a.ThreadID != "" || !strings.Contains(a.Reason, "pin") {
		t.Fatal(a)
	}
	if got := launchThread(agent, true); got != "" {
		t.Fatal("Claude pin used for Codex", got)
	}
}

func TestRemoteRetiredArgvNeedsExplicitPinAndLiveServer(t *testing.T) {
	const oldID = "01a0617e-29f9-79a3-be66-72ea1dec4718"
	const currentID = "01a0715e-a5c0-7171-adf3-c95343ce6d5b"
	for _, mode := range []string{"remote-pin", "embedded-pin", "remote-launch-only", "conflicting-pins"} {
		t.Run(mode, func(t *testing.T) {
			info := bptmux.CodexProcess{Home: t.TempDir(), ThreadID: oldID, Remote: "unix:///missing-bp-runtime-test.sock"}
			agent := Agent{Name: "server-main", Folder: "/srv", IdentityThreadID: currentID, Launch: &bptmux.OpenOptions{Codex: true, ResumeID: currentID}}
			switch mode {
			case "embedded-pin":
				info.Remote = ""
			case "remote-launch-only":
				agent.IdentityThreadID = ""
			case "conflicting-pins":
				agent.Launch.ResumeID = oldID
			}
			a := &cache.Activity{State: "unknown", DeliveryBlocked: true}
			s := readCodexRuntime(context.Background(), info, agent, a)
			want := "conflicting thread bindings"
			if mode == "remote-pin" {
				want = "app-server unavailable"
			}
			if a.State != "unknown" || a.Reason != want || s.Known {
				t.Fatalf("unverified argv replacement: %+v %+v", s, a)
			}
		})
	}
}

func TestRemoteTranscriptUsesVerifiedPathDespiteBirthCwd(t *testing.T) {
	home := t.TempDir()
	id := "01a0711e-1b8b-76a2-954a-76f9e1a44ceb"
	path := writeRuntimeCodex(t, home, id, "/srv", "task_complete", time.Now())
	if !remoteTranscript(home, id, path) {
		t.Fatal("server-selected rollout with an older birth cwd rejected")
	}
	if remoteTranscript(home, "01a0715e-a5c0-7171-adf3-c95343ce6d5b", path) || remoteTranscript(filepath.Join(home, "other-home"), id, path) {
		t.Fatal("wrong thread or out-of-home path accepted")
	}
}

func TestRuntimeReadsCodexInsteadOfStaleClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	id := "01a070c4-3f56-7213-b8ad-19fa7d6f5e4d"
	day := filepath.Join(home, "sessions/2026/09/05")
	if err := os.MkdirAll(day, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(day, "rollout-"+id+".jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Format(time.RFC3339Nano)
	for _, row := range []any{
		map[string]any{"type": "session_meta", "payload": map[string]any{"id": id, "cwd": "/work", "source": "cli"}},
		map[string]any{"type": "turn_context", "payload": map[string]any{"model": "gpt-6-astra", "effort": "high"}},
		map[string]any{"type": "event_msg", "timestamp": now, "payload": map[string]any{"type": "token_count", "info": map[string]any{"last_token_usage": map[string]any{"total_tokens": 43210}}}},
		map[string]any{"type": "event_msg", "timestamp": now, "payload": map[string]any{"type": "task_started"}},
	} {
		if err := json.NewEncoder(f).Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	_ = f.Close()
	fake := filepath.Join(home, "tmux")
	script := "#!/bin/sh\ncase \"$1\" in\nlist-panes) printf '1\\tnode\\t0\\n';;\ncapture-pane) printf '› Ask Codex to do anything\\n  gpt-6-astra high · /work\\n';;\n*) exit 1;;\nesac\n"
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	client := bptmux.New()
	client.Bin = fake
	agent := Agent{Name: "agent", Folder: "/work", Launch: &bptmux.OpenOptions{Codex: true, ResumeID: id}}
	state, codex := RuntimeState(context.Background(), client, agent)
	if !codex || !state.Known || !state.Busy || state.CtxTokens != 43210 || state.Model != "gpt-6-astra" || state.ThreadID != id {
		t.Fatalf("bad embedded runtime: %+v codex=%v", state, codex)
	}
	// A disconnected remote must retain its last measurement and block delivery.
	agent.Launch.Remote = "unix:///nonexistent/bp-test.sock"
	remote, codex := RuntimeState(context.Background(), client, agent)
	if !codex || !remote.Busy || remote.RuntimeError == "" || remote.CtxTokens != state.CtxTokens {
		t.Fatalf("lost remote fallback/protection: %+v", remote)
	}
}
