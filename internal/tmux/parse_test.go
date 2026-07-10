package tmux

import "testing"

func TestTypingUsesOnlyLastPrompt(t *testing.T) {
	pane := "❯ old message\nresponse\n  ❯ \u00a0  \t\n"
	if Typing(pane) {
		t.Fatal("empty final composer was reported as typing")
	}
	pane += "output\n  ❯ new message\n"
	if !Typing(pane) {
		t.Fatal("non-empty final composer was not reported as typing")
	}
}

func TestTypingRecognizesCodexPrompt(t *testing.T) {
	if Typing("output\n  › \u00a0 \t\n") {
		t.Fatal("empty Codex composer was reported as typing")
	}
	if !Typing("output\n  › draft message\n") {
		t.Fatal("non-empty Codex composer was not reported as typing")
	}
}

func TestTypingNoPrompt(t *testing.T) {
	if Typing("normal output\n❯\u00a0\nmore output") {
		t.Fatal("NBSP-only composer must be empty")
	}
}

func TestBusyRequiresLiveIndicator(t *testing.T) {
	// Live indicators: spinner timer or the ⏵ footer.
	if !Busy("✻ Working… (23s · Esc to interrupt)") {
		t.Fatal("spinner line should be busy")
	}
	if !Busy("⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt") {
		t.Fatal("footer line should be busy")
	}
	// Quoted prose, bare phrase and background-shell footers are NOT busy.
	if Busy(`transcript quoting "esc to interrupt" in a rule message`) ||
		Busy("Esc to interrupt") ||
		Busy("⏵⏵ bypass permissions on · 2 shells · esc to interrupt") ||
		Busy("ready") {
		t.Fatal("false positive busy")
	}
}

func TestTailBytesMatchesShellTail(t *testing.T) {
	got := tailBytes("abcdefghijklmnopqrstuvwxyz0123456789ABCDE", 40)
	if got != "bcdefghijklmnopqrstuvwxyz0123456789ABCDE" {
		t.Fatalf("unexpected tail: %q", got)
	}
}
