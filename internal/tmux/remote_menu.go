package tmux

import (
	"strings"

	"blueprint/internal/harness"
)

// BlockedByRemoteMenu is the Remote Control modal (Disconnect this session /
// Show QR code / Continue) left open in a Claude pane. The composer is hidden
// while it is up, so the pane used to read as "composer contains foreign text"
// (ders-main, 5 Nov 2026). bp never answers it: Enter there picks an entry.
const BlockedByRemoteMenu = "Remote Control menu is open (Disconnect / Show QR / Continue) — a human must close it"

// remoteMenuTailRows bounds where the live menu may sit: the heading, the URL
// paragraph, three entries and the footer fit well inside it.
const remoteMenuTailRows = 30

// OpenRemoteControlMenu reports whether the Remote Control menu is the live
// bottom of the pane: its heading, its Disconnect entry and its "Enter to
// select" footer, in that order, with the footer among the last rows. A menu
// that was answered and scrolled up into history does not count.
func OpenRemoteControlMenu(pane string) bool {
	lines := trimTrailingBlank(strings.Split(ansiSeq.ReplaceAllString(pane, ""), "\n"))
	from := len(lines) - remoteMenuTailRows
	if from < 0 {
		from = 0
	}
	heading, item, footer := -1, -1, -1
	for i := from; i < len(lines); i++ {
		text := strings.TrimSpace(lines[i])
		switch {
		case text == "Remote Control":
			heading, item, footer = i, -1, -1
		case heading >= 0 && strings.Contains(text, harness.ClaudeScreen.RemoteControlDisconnectItem):
			item = i
		case item >= 0 && strings.Contains(text, harness.ClaudeScreen.RemoteControlMenu):
			footer = i
		}
	}
	return heading >= 0 && item > heading && footer > item && len(lines)-footer <= 3
}
