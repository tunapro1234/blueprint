package tmux

import (
	"context"
	"strings"
	"testing"
)

// Codex 0.157 first-run folder trust screen as captured from a live pane.
const codexTrustPane = `  Folder access
  /work/new
  Trust this folder? Codex can read, edit, and run files here, subject to your permission
  settings. Folder settings can run code automatically, even without a model request. Continue
  only if you trust these files. Your trust decision will be saved.
› 1. Trust and continue
  2. Back to Agent Command Center
  enter continue · esc back
`

func TestCodexTrustModal(t *testing.T) {
	for _, tc := range []struct {
		name, pane, dir string
		want            bool
	}{
		{"requested folder", codexTrustPane, "/work/new", true},
		{"other folder", codexTrustPane, "/work/other", false},
		{"back selected", strings.Replace(strings.Replace(codexTrustPane, "› 1. Trust", "  1. Trust", 1), "  2. Back", "› 2. Back", 1), "/work/new", false},
		{"no footer", strings.Replace(codexTrustPane, "enter continue · esc back", "", 1), "/work/new", false},
		{"composer", "› trust this folder /work/new\n", "/work/new", false},
	} {
		if got := codexTrustModal(tc.pane, tc.dir); got != tc.want {
			t.Errorf("%s: codexTrustModal=%v want %v", tc.name, got, tc.want)
		}
	}
	if !codexTrustScreen(codexTrustPane) {
		t.Error("trust screen not recognized as a trust screen")
	}
	if codexTrustScreen("› explain why we trust this folder\n") {
		t.Error("composer text mentioning trust taken for a trust screen")
	}
}

// The trust screen's "› 1. Trust and continue" is not a Codex composer. For a
// folder other than the requested one, open must neither answer the prompt
// nor report the agent open.
func TestCodexOpenDoesNotTreatForeignTrustScreenAsReady(t *testing.T) {
	h := &launchHarness{capture: strings.Replace(codexTrustPane, "/work/new", "/work/elsewhere", 1)}
	var progress []string
	err := h.client().Open(context.Background(), "agent", "/work/new", OpenOptions{Codex: true, NoPrompt: true, Progress: func(s string) { progress = append(progress, s) }}, nil)
	if err == nil || !strings.Contains(err.Error(), "trust prompt") {
		t.Fatalf("open over an unanswered trust screen: err=%v", err)
	}
	for _, p := range progress {
		if p == "harness ready" {
			t.Fatal("reported harness ready while the trust screen was showing")
		}
	}
	for _, call := range h.calls {
		if call == "send-keys -t =agent: Enter" {
			t.Fatalf("answered a trust screen for another folder: %v", h.calls)
		}
	}
	if h.exists {
		t.Fatal("session left blocked on the trust screen")
	}
}

// The requested folder's trust screen is answered once, and the agent is
// ready only when the real composer appears.
func TestCodexOpenAnswersRequestedFolderTrust(t *testing.T) {
	h := &launchHarness{capture: codexTrustPane}
	client := h.client()
	exec := client.exec
	enters := 0
	client.exec = func(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "send-keys -t =agent: Enter" {
			enters++
			h.capture = "╭────────────╮\n│ >_ OpenAI Codex │\n╰────────────╯\n\n› \n\n  gpt-6-luna max · /work/new\n"
		}
		return exec(ctx, stdin, args...)
	}
	if err := client.Open(context.Background(), "agent", "/work/new", OpenOptions{Codex: true, NoPrompt: true}, nil); err != nil {
		t.Fatalf("open: %v", err)
	}
	if enters != 1 {
		t.Fatalf("trust screen answered %d times, want 1", enters)
	}
}
