package tmux

import "testing"

func TestTypingUsesOnlyLastPrompt(t *testing.T) {
	pane := "❯ eski mesaj\ncevap\n  ❯ \u00a0  \t\n"
	if Typing(pane) {
		t.Fatal("empty final composer was reported as typing")
	}
	pane += "çıktı\n  ❯ yeni mesaj\n"
	if !Typing(pane) {
		t.Fatal("non-empty final composer was not reported as typing")
	}
}

func TestTypingNoPrompt(t *testing.T) {
	if Typing("normal output\n❯\u00a0\nmore output") {
		t.Fatal("NBSP-only composer must be empty")
	}
}

func TestBusyCaseInsensitive(t *testing.T) {
	if !Busy("Esc to interrupt") || Busy("ready") {
		t.Fatal("busy marker parsing failed")
	}
}

func TestTailBytesMatchesShellTail(t *testing.T) {
	got := tailBytes("abcdefghijklmnopqrstuvwxyz0123456789ABCDE", 40)
	if got != "bcdefghijklmnopqrstuvwxyz0123456789ABCDE" {
		t.Fatalf("unexpected tail: %q", got)
	}
}
