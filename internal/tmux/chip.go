package tmux

import (
	"context"
	"regexp"
	"strconv"

	"blueprint/internal/messagetext"
)

// claudeChipLabel is a Claude Code paste chip as the whitespace-stripped box
// reads it. Claude labels a collapsed paste "[Pasted text #N +K lines]", where
// K counts the line breaks in the pasted text (\r\n, \r or \n), and drops the
// "+K lines" part when K is 0 ("[Pasted text #N]", a long single-line paste).
// Read from Claude Code 2.1.332; the pattern is anchored so the box may hold
// nothing else.
var claudeChipLabel = regexp.MustCompile(`^\[Pastedtext#\d+(?:\+(\d+)lines)?\]$`)

var lineBreak = regexp.MustCompile(`\r\n|\r|\n`)

// ClaudeChipFor reports whether a Claude composer holds nothing but a paste
// chip whose line count is the one Claude gives this text. It is the evidence
// that lets bp finish its OWN collapsed paste; the caller must add the rest
// (same session as the paste, idle pane), because a chip does not show its
// content and the count alone cannot tell two pastes of equal length apart.
//
// Claude only: the Codex chip's count is capped and unreliable, and the
// OpenCode chip has no ownership evidence in this package.
func ClaudeChipFor(pane, text string) bool {
	box, top, ok := claudeComposerBoxAt(pane)
	if !ok || composerBoxScrolled(pane, box, top) || !claudePasteChip(pane) {
		return false
	}
	match := claudeChipLabel.FindStringSubmatch(stripSpace(box))
	if match == nil {
		return false
	}
	lines := 0
	if match[1] != "" {
		n, err := strconv.Atoi(match[1])
		if err != nil || n == 0 {
			return false
		}
		lines = n
	}
	return lines == len(lineBreak.FindAllString(text, -1))
}

// SubmitStuckChip is SubmitStuck for a paste Claude collapsed into a chip:
// one Enter when the composer holds only a chip that ClaudeChipFor accepts for
// text, with every other refusal of SubmitStuck. Its answers are SubmitStuck's,
// so an Enter the screen has not shown yet is ErrSubmitUnconfirmed.
func (c *Client) SubmitStuckChip(ctx context.Context, session, text string) (bool, error) {
	if err := messagetext.Validate(text); err != nil {
		return false, err
	}
	if err := c.requireAgentPane(ctx, session); err != nil {
		return false, err
	}
	pane, err := c.CaptureAnsi(ctx, session)
	if err != nil {
		return false, err
	}
	if paneDialog(pane) {
		return false, ErrDialog
	}
	if Busy(pane) {
		return false, ErrBusy
	}
	if !ClaudeChipFor(pane, text) {
		return false, nil
	}
	return c.pressStuckEnter(ctx, session)
}
