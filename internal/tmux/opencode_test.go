package tmux

import (
	"strings"
	"testing"
)

// openCodePane renders the TUI the way blueprint-ox-test did (100x30,
// 2026-08-26): transcript rows share the composer's rail character, so every
// fixture here carries some above the composer — that overlap is exactly what
// the reader has to survive.
func openCodePane(composer []string, busy bool) string {
	lines := []string{
		"  ┃  onceki mesajim, transcript'te ayni rail ile ciziliyor",
		"",
		"     ▣  Build · Ox Alpha (stealth)",
		"",
		"  ┃",
	}
	for _, row := range composer {
		lines = append(lines, "  ┃  "+row)
	}
	lines = append(lines,
		"  ┃",
		"  ┃  Build · Ox Alpha (stealth) OpenRouter",
		"  ╹"+strings.Repeat("▀", 60),
	)
	if busy {
		lines = append(lines, "   ⬝⬝⬝⬝⬝■■■  esc interrupt               tab agents  ctrl+p commands")
	} else {
		lines = append(lines, "  tab agents  ctrl+p commands")
	}
	lines = append(lines, "", "  /srv/compec/mail-ox:master                                 1.18.23", "")
	return strings.Join(lines, "\n")
}

func TestOpenCodeRecognition(t *testing.T) {
	pane := openCodePane([]string{"Ask anything... \"What is the tech stack of this project?\""}, false)
	if !OpenCodePane(pane) {
		t.Fatal("a live opencode screen was not recognised")
	}
	if !IsAgentPane("opencode", pane) {
		t.Fatal("opencode pane is not an agent pane")
	}
	// The command alone must not be enough, and no other TUI may be claimed.
	if IsAgentPane("opencode", "  $ ls\n  bp  README.md\n") {
		t.Fatal("a shell pane was claimed as opencode on the command name alone")
	}
	if OpenCodePane(claudePane("❯ ")) {
		t.Fatal("a Claude pane was read as opencode")
	}
}

// The busy signature is two characters away from Claude's: opencode writes
// "esc interrupt", Claude "esc to interrupt". Without its own branch every
// working opencode pane reads idle, and a message lands mid-turn.
func TestOpenCodeBusy(t *testing.T) {
	if !Busy(openCodePane([]string{"Ask anything..."}, true)) {
		t.Fatal("a working opencode pane read as idle")
	}
	if Busy(openCodePane([]string{"Ask anything..."}, false)) {
		t.Fatal("an idle opencode pane read as busy")
	}
}

// The composer reader, including the two things that make it non-obvious: the
// placeholder must read EMPTY (an idle composer that reads as occupied blocks
// its agent's queue behind text nobody typed), and the transcript above uses
// the same rail, so the reader must anchor on the bottom edge.
func TestOpenCodeComposerReader(t *testing.T) {
	idle := openCodePane([]string{"Ask anything... \"What is the tech stack of this project?\""}, false)
	if Typing(idle) {
		t.Fatal("the idle placeholder read as typed text")
	}
	if box, ok := composerBoxText(idle); ok && box != "" {
		t.Fatalf("idle composer box=%q, want empty", box)
	}

	message := "[server-main] bu mesaj composer'da duruyor ve satirlara sariliyor, tam olarak boyle gorunuyor."
	rows := []string{message[:56], message[56:]}
	held := openCodePane(rows, false)
	text, ours := StuckPaste(held, []string{message})
	if !ours || text != message {
		t.Fatalf("StuckPaste ours=%v text=%q, want our own message back", ours, text)
	}
	if _, ours := StuckPaste(held, []string{"bambaska bir mesaj, yeterince uzun olsun diye uzatiyorum"}); ours {
		t.Fatal("a stranger's text was claimed as ours")
	}
}

// A bracketed multi-line paste renders as "[Pasted ~3 lines]": the composer's
// content cannot be read, and every verdict has to know that before it decides
// whose text is in the box.
func TestOpenCodeChipIsUnreadable(t *testing.T) {
	pane := openCodePane([]string{"[Pasted ~3 lines]"}, false)
	if !openCodePasteChip(pane) || !pasteChip(pane) {
		t.Fatal("opencode's paste chip was not recognised")
	}
	if _, ours := StuckPaste(pane, []string{"satir bir\nsatir iki\nsatir uc"}); ours {
		t.Fatal("an unreadable chip was claimed as our text at the gate")
	}
	if got := ComposerContentBlockReason(pane, nil); got != BlockedByPasteChip {
		t.Fatalf("block reason=%q, want the paste-chip refusal", got)
	}
}
