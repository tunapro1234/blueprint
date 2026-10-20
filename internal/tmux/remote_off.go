package tmux

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"blueprint/internal/harness"
)

// ErrRemoteOffUnconfirmed reports that bp could not confirm on screen that a
// session's Remote Control was turned off. The pane may still be connected.
var ErrRemoteOffUnconfirmed = errors.New("remote control not confirmed off")

var (
	remoteStoppedLine = harness.ClaudeScreen.RemoteControlStopped
	remoteBannerRow   = harness.ClaudeScreen.RemoteControlBanner
)

// RemoteControlMenuSelection is the /remote-control menu entry under the
// cursor, or "" when no menu is on screen. Only rows below the menu's own
// "Remote Control" heading count, so the composer's prompt marker in older
// scrollback is never read as the cursor.
func RemoteControlMenuSelection(pane string) string {
	if !RemoteControlMenu(pane) {
		return ""
	}
	lines := strings.Split(pane, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "Remote Control" {
			start = i
		}
	}
	if start < 0 {
		return ""
	}
	cursor := harness.ClaudeScreen.RemoteControlMenuCursor
	for _, line := range lines[start+1:] {
		text := strings.TrimSpace(line)
		if !strings.HasPrefix(text, cursor) {
			continue
		}
		item := strings.TrimSpace(strings.TrimPrefix(text, cursor))
		if strings.HasPrefix(item, harness.ClaudeScreen.RemoteControlDisconnectItem) {
			return harness.ClaudeScreen.RemoteControlDisconnectItem
		}
		return item
	}
	return ""
}

// RemoteControlBannerConnected reports whether the startup banner at the top
// of a session's history shows the session connected to Remote Control
// ("/path · /rc"). It reads only the first rows, where the banner is printed.
func RemoteControlBannerConnected(history string) bool {
	rows := 0
	for _, line := range strings.Split(history, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if remoteBannerRow.MatchString(line) {
			return true
		}
		if rows++; rows >= 12 {
			return false
		}
	}
	return false
}

// remoteMenuChecks bounds (in one-second reads) how long a /remote-control
// menu or status line may take to render; connecting can be slow.
const remoteMenuChecks = 20

// DisconnectRemoteControl turns Remote Control off in an idle Claude session
// and confirms it on screen. /remote-control opens the Disconnect/QR/Continue
// menu only while the session is connected; in a session that is not, the
// same command CONNECTS it, so that case is told apart and the command is
// sent a second time to reach the menu. wasOff reports that the session was
// not connected before (bp connected it for a moment to disconnect it).
//
// A working pane or a composer holding text is never touched: the error is
// ErrBusy or ErrTyping and nothing was sent.
func (c *Client) DisconnectRemoteControl(ctx context.Context, session string) (wasOff bool, err error) {
	pane, err := c.Capture(ctx, session)
	if err != nil {
		return false, err
	}
	// The pane's status lines are not trusted to say "already off": a resumed
	// session replays its old "Remote Control disconnected." line while it may
	// have reconnected on its own. Only the menu proves the current state.
	if !RemoteControlMenu(pane) {
		if err := c.sendRemoteControlCommand(ctx, session); err != nil {
			return false, err
		}
		menu, activated, err := c.waitRemoteControlReply(ctx, session, countActive(pane))
		if err != nil {
			return false, err
		}
		if !menu {
			if !activated {
				return false, fmt.Errorf("%w: /remote-control showed neither its menu nor a status line", ErrRemoteOffUnconfirmed)
			}
			wasOff = true
			current, err := c.Capture(ctx, session)
			if err != nil {
				return wasOff, err
			}
			if err := c.sendRemoteControlCommand(ctx, session); err != nil {
				return wasOff, err
			}
			if menu, _, err = c.waitRemoteControlReply(ctx, session, countActive(current)); err != nil {
				return wasOff, err
			}
			if !menu {
				return wasOff, fmt.Errorf("%w: Remote Control was connected by the check and its menu did not open", ErrRemoteOffUnconfirmed)
			}
		}
	}
	return wasOff, c.chooseDisconnect(ctx, session)
}

// sendRemoteControlCommand submits /remote-control, refusing a working pane or
// a non-empty composer.
func (c *Client) sendRemoteControlCommand(ctx context.Context, session string) error {
	err := c.Send(ctx, session, "/remote-control")
	if errors.Is(err, ErrUnverified) {
		// The slash-command menu can swallow the first Enter; finish it only
		// while the composer still holds exactly this command.
		c.finishStartupCommand(ctx, session, "/remote-control")
		return nil
	}
	return err
}

// waitRemoteControlReply waits for the menu (the session was connected) or a
// new "/remote-control is active" line (the command just connected it).
func (c *Client) waitRemoteControlReply(ctx context.Context, session string, activeBefore int) (menu, activated bool, err error) {
	for check := 0; check < remoteMenuChecks; check++ {
		c.Sleep(time.Second)
		if err := ctx.Err(); err != nil {
			return false, false, err
		}
		pane, err := c.Capture(ctx, session)
		if err != nil {
			continue
		}
		if RemoteControlMenu(pane) {
			return true, false, nil
		}
		if countActive(pane) > activeBefore {
			// The menu can still follow a status line; give it a moment.
			c.Sleep(2 * time.Second)
			if pane, err := c.Capture(ctx, session); err == nil && RemoteControlMenu(pane) {
				return true, false, nil
			}
			return false, true, nil
		}
	}
	return false, false, nil
}

// chooseDisconnect moves the open menu's cursor to "Disconnect this session",
// selects it and confirms the session reports Remote Control off. It presses
// Enter only with the cursor on that entry.
func (c *Client) chooseDisconnect(ctx context.Context, session string) error {
	target := harness.ClaudeScreen.RemoteControlDisconnectItem
	for moves := 0; ; moves++ {
		pane, err := c.Capture(ctx, session)
		if err != nil {
			return err
		}
		selected := RemoteControlMenuSelection(pane)
		if selected == target {
			break
		}
		if !RemoteControlMenu(pane) {
			return fmt.Errorf("%w: the menu closed before Disconnect was selected", ErrRemoteOffUnconfirmed)
		}
		if moves >= 4 {
			return fmt.Errorf("%w: cursor stayed on %q (menu left open, bp peek %s)", ErrRemoteOffUnconfirmed, selected, session)
		}
		// The entry is the first row; Up never wraps past it.
		if _, err := c.run(ctx, nil, "send-keys", "-t", "="+session+":", "Up"); err != nil {
			return err
		}
		c.Sleep(500 * time.Millisecond)
	}
	if err := c.PressEnter(ctx, session); err != nil {
		return err
	}
	for check := 0; check < 10; check++ {
		c.Sleep(time.Second)
		pane, err := c.Capture(ctx, session)
		if err != nil {
			continue
		}
		if state, _, _ := RemoteControlStatus(pane); state == RemoteOff && !RemoteControlMenu(pane) {
			return nil
		}
	}
	return fmt.Errorf("%w: no \"Remote Control disconnected\" line after selecting Disconnect", ErrRemoteOffUnconfirmed)
}

func countActive(pane string) int {
	n := 0
	for _, line := range strings.Split(pane, "\n") {
		if remoteActiveLine.MatchString(line) {
			n++
		}
	}
	return n
}
