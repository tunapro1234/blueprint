package tmux

import (
	"strings"
	"testing"
)

// codexPaneWith renders a Codex screen the way the live pane does: transcript,
// a rule line, the composer (first row carries the prompt marker, the rest are
// wrap continuations), a blank row, then the footer.
func codexPaneWith(rows []string) string {
	lines := []string{
		"• Ran 6 commands · ctrl + t to view transcript",
		"",
		strings.Repeat("─", 70),
		"",
	}
	for i, row := range rows {
		if i == 0 {
			lines = append(lines, "› "+row)
			continue
		}
		lines = append(lines, "  "+row)
	}
	lines = append(lines, "", "  gpt-5.6-sol medium fast · /srv/probot/out-codex", "")
	return strings.Join(lines, "\n")
}

// wrapText splits a message the way a narrow pane renders it.
func wrapText(text string, width int) []string {
	var rows []string
	for len(text) > width {
		rows = append(rows, text[:width])
		text = text[width:]
	}
	return append(rows, text)
}

// The q218783035 case (probot-outreach, 2026-08-25): a ~520 character
// plain-ASCII message pasted into probot-out-codex, too long for one rendered
// row and too short for Codex's paste chip. Until Codex got a composer reader,
// bp saw one row of eight — so it could neither verify the paste nor recognise
// the composer as still holding its own text, and told the operator the message
// was gone while it sat on screen.
func TestCodexComposerReadsWrappedPaste(t *testing.T) {
	message := "[probot-outreach] blueprint bp'deki gonderme arizani duzeltti (16ed445): sorun teslimat degil spool'du — bp duyuru eklemek icin spool'u yazma modunda aciyordu, senin sandbox'inda /srv/blueprint salt-okunur oldugu icin daha ilk adimda patliyordu. Artik spool salt-okunursa mesaj duyurusuz TEK BASINA gidiyor. DOGRULAMA RICASI: musait oldugunda bana kisa bir test mesaji at."
	pane := codexPaneWith(wrapText(message, 68))

	if _, ok := composerBoxText(pane); !ok {
		t.Fatal("codex composer unreadable: the pane has no box reader")
	}
	text, ours := StuckPaste(pane, []string{message})
	if !ours || text != message {
		t.Fatalf("StuckPaste ours=%v text=%q, want our own message back", ours, text)
	}
	if got := classifyComposer(pane, stripSpace(message)); got != composerMine {
		t.Fatalf("classifyComposer=%v, want composerMine", got)
	}
}

// An idle Codex composer must read as EMPTY: its placeholder is not a human
// typing, and a pane that reads as occupied blocks its own queue.
func TestCodexIdleComposerReadsEmpty(t *testing.T) {
	pane := codexPaneWith([]string{"Ask Codex to do anything"})
	if Typing(pane) {
		t.Fatal("idle codex composer read as typing")
	}
	if box, ok := composerBoxText(pane); ok && box != "" {
		t.Fatalf("idle codex box=%q, want empty", box)
	}
}

// Someone else's half-written line in a Codex composer must stay FOREIGN: the
// reader exists to let bp finish its OWN paste, never to press Enter on a
// human's unfinished text.
func TestCodexForeignComposerStaysForeign(t *testing.T) {
	pane := codexPaneWith(wrapText("abi sunu bir kontrol eder misin, listedeki numaralarin nitelik alanini yeniden gozden gecirmemiz lazim", 68))
	if _, ours := StuckPaste(pane, []string{"[bp] bambaska bir mesaj, yeterince uzun olsun diye uzatiyorum."}); ours {
		t.Fatal("a stranger's text in a codex composer was claimed as ours")
	}
}
