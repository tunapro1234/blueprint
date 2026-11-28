package tmux

import (
	"context"
	"errors"
	"fmt"
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

// ErrNotCleared means an erase key went in and the composer still showed
// content afterwards. Nothing more was pressed.
var ErrNotCleared = errors.New("composer is not empty after the erase; nothing more was pressed")

// chipClearReads is how many times ClearStuckChip looks for the empty box
// after its one C-u before it gives up. A loaded pane can take more than one
// settle window to redraw; looking again presses nothing.
const chipClearReads = 5

// ClearStuckChip erases bp's own paste that Claude collapsed into a chip, for
// an operator who has decided it must not be delivered (bp qclear). It presses
// C-u once, never Escape, and only when the composer holds nothing but a chip
// that ClaudeChipFor accepts for text, the pane is idle, shows no dialog and
// no client is typing into it. It then reads the screen until the box is
// empty. When it stays filled the answer is ErrNotCleared and no second key
// goes in.
//
// One C-u removes the whole chip: measured on Claude Code 2.1.332 with the vim
// editor mode in INSERT and in NORMAL mode, and a character typed afterwards
// showed alone in the box, so no hidden text was left behind (2026-11-28).
func (c *Client) ClearStuckChip(ctx context.Context, session, text string) (bool, error) {
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
	activity, err := c.clientActivity(ctx, session)
	if err != nil {
		return false, err
	}
	if !activity.IsZero() && c.Now().Sub(activity) < clientIdleWindow {
		return false, ErrTyping
	}
	if _, err := c.run(ctx, nil, "send-keys", "-t", "="+session+":", "C-u"); err != nil {
		return false, err
	}
	after := pane
	for read := 0; read < chipClearReads; read++ {
		c.Sleep(composerSettleWindow)
		if after, err = c.CaptureAnsi(ctx, session); err != nil {
			return false, fmt.Errorf("%w: the screen could not be read after C-u: %v", ErrNotCleared, err)
		}
		if ComposerEmpty(after) {
			return true, nil
		}
	}
	return false, clearStopped(ErrNotCleared, after)
}

// RequireAgentPane refuses a pane that is not running an agent CLI (ErrNotAgent).
func (c *Client) RequireAgentPane(ctx context.Context, session string) error {
	return c.requireAgentPane(ctx, session)
}

// ComposerComplete reports whether the pane shows its composer box whole:
// readable and not scrolled, so what it holds can be judged.
func ComposerComplete(pane string) bool {
	_, ok := composerJudgeText(pane)
	return ok
}
