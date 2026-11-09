package tmux

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrBackgroundWork reports that the CLI would not exit because the session
// still runs background work, and that bp chose to stay.
var ErrBackgroundWork = errors.New("background work is running")

var exitDialogOption = regexp.MustCompile(`^(❯\s*)?([123])\.\s+(.+)$`)

// ExitDialog recognizes the prompt Claude Code shows for /exit while the
// session still has background work (shells, scheduled tasks):
//
//	Background work is running
//	The following will stop when you exit:
//	shell · <command>
//	❯ 1. Exit and stop tasks
//	  2. Move to background and exit
//	  3. Stay
//	Enter to confirm · Esc to cancel
//
// It returns the listed work and the option the cursor is on (0 when the
// cursor cannot be read). Only a prompt that ends the pane counts, so the same
// words quoted in a conversation above the composer do not.
func ExitDialog(pane string) (work []string, cursor int, ok bool) {
	var lines []string
	for _, line := range strings.Split(ansiSeq.ReplaceAllString(pane, ""), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	// header, at least one item, three options, footer
	if len(lines) < 6 || !strings.HasPrefix(lines[len(lines)-1], "Enter to confirm") {
		return nil, 0, false
	}
	options := lines[len(lines)-4 : len(lines)-1]
	for index, want := range []string{"Exit and stop tasks", "Move to background and exit", "Stay"} {
		match := exitDialogOption.FindStringSubmatch(options[index])
		if match == nil || match[2] != string(rune('1'+index)) || match[3] != want {
			return nil, 0, false
		}
		if match[1] != "" {
			if cursor != 0 {
				return nil, 0, false
			}
			cursor = index + 1
		}
	}
	for index := len(lines) - 5; index >= 0; index-- {
		if lines[index] == "The following will stop when you exit:" {
			return work, cursor, true
		}
		work = append([]string{lines[index]}, work...)
	}
	return nil, 0, false
}

// StayInExitDialog answers the exit prompt with "Stay", so the session and its
// background work keep running. It moves the cursor with Down and presses
// Enter only on a capture that shows the cursor on "3. Stay"; every key is
// preceded by a fresh read of the prompt, and a prompt it cannot read is left
// exactly as it is. "Exit and stop tasks" is never confirmed.
func (c *Client) StayInExitDialog(ctx context.Context, session string) error {
	if err := c.requireAgentPane(ctx, session); err != nil {
		return err
	}
	target := "=" + session + ":"
	for moves := 0; ; moves++ {
		pane, err := c.CaptureAnsi(ctx, session)
		if err != nil {
			return err
		}
		_, cursor, ok := ExitDialog(pane)
		if !ok {
			return fmt.Errorf("%w: no exit prompt on screen", ErrUnverified)
		}
		if cursor == 0 || moves > 3 {
			return fmt.Errorf("%w: cannot move the exit prompt to Stay; left open", ErrDialog)
		}
		if cursor != 3 {
			if _, err := c.run(ctx, nil, "send-keys", "-t", target, "Down"); err != nil {
				return err
			}
			c.Sleep(composerSettleWindow)
			continue
		}
		if _, err := c.run(ctx, nil, "send-keys", "-t", target, "Enter"); err != nil {
			return err
		}
		for wait := 0; wait < 10; wait++ {
			c.Sleep(composerSettleWindow)
			after, err := c.CaptureAnsi(ctx, session)
			if err != nil {
				return err
			}
			if _, _, still := ExitDialog(after); !still {
				return nil
			}
		}
		return fmt.Errorf("%w: exit prompt still open after choosing Stay", ErrDialog)
	}
}
