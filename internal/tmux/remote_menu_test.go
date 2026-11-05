package tmux

import (
	"strings"
	"testing"
)

func TestOpenRemoteControlMenuIsItsOwnReason(t *testing.T) {
	for name, pane := range map[string]string{"cursor on Continue": rcMenuOnContinue, "cursor on Disconnect": rcMenuOnDisconnect} {
		if !OpenRemoteControlMenu(pane) {
			t.Fatalf("%s: menu not recognised", name)
		}
		if got := ComposerContentBlockReason(pane, nil); got != BlockedByRemoteMenu {
			t.Fatalf("%s: reason=%q, want %q", name, got, BlockedByRemoteMenu)
		}
	}
}

func TestAnsweredRemoteControlMenuInHistoryIsNotOpen(t *testing.T) {
	pane := rcMenuOnContinue + "  ⎿  Remote Control disconnected.\n" + claudePane(emptyRow)
	if OpenRemoteControlMenu(pane) {
		t.Fatal("a menu answered earlier and followed by the composer read as open")
	}
	if got := ComposerContentBlockReason(pane, nil); got != "" {
		t.Fatalf("reason=%q, want empty", got)
	}
}

func TestStrayKeypressIsNamedInTheReason(t *testing.T) {
	got := ComposerContentBlockReason(claudePane("❯ n"), nil)
	if !strings.HasPrefix(got, BlockedByForeignText) || !strings.Contains(got, `only "n"`) || !strings.Contains(got, "stray keypress") {
		t.Fatalf("reason=%q, want foreign text naming the stray \"n\"", got)
	}
	if got := ComposerContentBlockReason(claudePane("❯ /rename wor"), nil); got != BlockedByForeignText {
		t.Fatalf("longer text reason=%q, want plain %q", got, BlockedByForeignText)
	}
}
