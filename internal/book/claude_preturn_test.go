package book

import (
	"blueprint/internal/cache"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudePreTurnRequiresWholeBoundMetadataOnlyTranscript(t *testing.T) {
	const id = "5f014104-7325-4e17-bde5-4d675b216f42"
	base := `{"type":"custom-title","sessionId":"` + id + `","customTitle":"writer"}` + "\n" + `{"type":"user","sessionId":"` + id + `","cwd":"/work","message":{"content":"<command-name>/model</command-name><command-message>model</command-message>"}}` + "\n"
	for _, tc := range []struct {
		name, extra string
		want        bool
	}{
		{"model only", "", true},
		{"completed command", `{"type":"user","message":{"content":"<local-command-stdout>Set model</local-command-stdout>"}}` + "\n", true},
		{"actual user", `{"type":"user","message":{"content":"do work"}}` + "\n", false},
		{"assistant unknown stop", `{"type":"assistant","message":{"stop_reason":null}}` + "\n", false},
		{"manual compact", `{"type":"system","subtype":"compact_boundary"}` + "\n", false},
		{"handoff", `{"type":"continued-in","continuedInSessionId":"other"}` + "\n", false},
		{"unknown event", `{"type":"future-event"}` + "\n", false},
		{"partial tail", `{"type":`, false},
		{"wrong session", `{"type":"mode","sessionId":"other"}` + "\n", false},
		{"large transcript", strings.Repeat(" ", 129*1024), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), id+".jsonl")
			_ = os.WriteFile(path, []byte(base+tc.extra), 0600)
			if got := claudePreTurn(path, id, "/work"); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestClaudePreTurnDoesNotOverrideBusyDraftModalStaleOrWeakBinding(t *testing.T) {
	id := "5f014104-7325-4e17-bde5-4d675b216f42"
	path := filepath.Join(t.TempDir(), id+".jsonl")
	_ = os.WriteFile(path, []byte(`{"type":"custom-title","sessionId":"`+id+`"}`+"\n"), 0600)
	pane := "──────────────────── writer ──\n❯\u00a0\n────────────────────────────────────────\n  -- INSERT -- ⏵⏵ bypass permissions on (shift+tab to cycle)"
	for _, mode := range []string{"ready", "working", "draft", "modal", "stale", "weak", "missing-box"} {
		t.Run(mode, func(t *testing.T) {
			a := &cache.Activity{State: "unknown", Reason: "no readable decisive turn event", ThreadID: id, TranscriptPath: path, Binding: "claude-process-session"}
			screen := pane
			switch mode {
			case "working":
				screen = "✻ Working… (23s · esc to interrupt)\n" + pane
			case "draft":
				screen = strings.Replace(pane, "❯\u00a0", "❯ user draft", 1)
			case "modal":
				screen = pane + "\nEsc to cancel"
			case "stale":
				a.Reason = "turn evidence stale"
			case "weak":
				a.Binding = "claude-session-name"
			case "missing-box":
				screen = "❯ "
			}
			applyClaudePreTurn(cache.State{Runtime: "claude"}, a, "/work", screen)
			if (a.State == "idle") != (mode == "ready") {
				t.Fatal(mode, a)
			}
		})
	}
}
