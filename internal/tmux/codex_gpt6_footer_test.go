package tmux

import (
	"context"
	"testing"
)

// gpt6Pane mirrors a live GPT-6 Codex pane captured on 2026-09-24 (82 columns):
// a bold coloured prompt marker, a message wrapped over three rows, and a
// footer whose model is a capitalised display name followed by the cwd and the
// thread title.
func gpt6Pane(composer ...string) string {
	pane := "\x1b[2m  12:28 AM\x1b[0m\n\n\n"
	for i, row := range composer {
		if i == 0 {
			pane += "\x1b[1m\x1b[38;5;215m›\x1b[0m " + row + "\n"
			continue
		}
		pane += "  " + row + "\n"
	}
	return pane + "\n  \x1b[38;5;223mGPT-6-Luna max\x1b[39m · \x1b[38;5;151m/work/project\x1b[39m · \x1b[38;5;211mthread title\x1b[39m"
}

func TestIssue16GPT6FooterKeepsWrappedComposerVisible(t *testing.T) {
	msg := "[lead] Your task is ready: read /work/project/brief.md and follow it. Ask the lead if anything is unclear."
	pane := gpt6Pane(
		"[lead] Your task is ready: read /work/project/brief.md and follow",
		"it. Ask the lead if anything is unclear.",
	)
	if !CodexPane(pane) {
		t.Fatal("GPT-6 pane with a full composer is not recognised as Codex")
	}
	if !composerHoldsMessage(pane, stripSpace(msg)) {
		box, _, ok := codexComposerBox(pane)
		t.Fatalf("pasted message not found in composer: ok=%v box=%q", ok, box)
	}
	empty := gpt6Pane("\x1b[2mAsk Codex to do anything\x1b[0m")
	h := &sendHarness{captures: []string{empty}, activities: []string{"target\t900\n"}}
	if got := testClient(h).submit(context.Background(), "=target:", "target", msg, &pane); got != sendVerified || countEnter(h.mutations) != 1 {
		t.Fatalf("result=%v keys=%v", got, h.mutations)
	}
}

func TestIssue16GPT6ForeignDraftIsNotOurs(t *testing.T) {
	pane := gpt6Pane("half typed user draft")
	if !CodexPane(pane) {
		t.Fatal("GPT-6 pane holding a user draft read as non-Codex; bp would paste behind it")
	}
	if composerHoldsMessage(pane, stripSpace("[lead] something else")) {
		t.Fatal("user draft mistaken for our message")
	}
}
