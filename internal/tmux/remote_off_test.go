package tmux

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Captured from Claude Code 2.1.304 after /remote-control in a connected
// session: the cursor starts on Continue, Disconnect is the first row.
const rcMenuOnContinue = `❯ /remote-control
  /remote-control is active · Continue here, on your phone, or at https://claude.ai/code/session_01WX

   Remote Control

   This session is available in the Claude mobile app and at https://claude.ai/code/session_01WX.

     Disconnect this session
     Show QR code   Scan with your phone to open this session
   ❯ Continue

   Enter to select · Esc to continue
`

const rcMenuOnDisconnect = `❯ /remote-control

   Remote Control

   This session is available in the Claude mobile app and at https://claude.ai/code/session_01WX.

   ❯ Disconnect this session
     Show QR code   Scan with your phone to open this session
     Continue

   Enter to select · Esc to continue
`

func TestRemoteControlMenuSelection(t *testing.T) {
	if got := RemoteControlMenuSelection(rcMenuOnContinue); got != "Continue" {
		t.Fatalf("selection = %q, want Continue", got)
	}
	if got := RemoteControlMenuSelection(rcMenuOnDisconnect); got != "Disconnect this session" {
		t.Fatalf("selection = %q, want Disconnect this session", got)
	}
	// The composer prompt above the menu is not the menu cursor.
	if got := RemoteControlMenuSelection("❯ /remote-control\n  /remote-control is active · x\n"); got != "" {
		t.Fatalf("selection without a menu = %q", got)
	}
}

func TestRemoteControlStatusOff(t *testing.T) {
	pane := "❯ /remote-control\n  /remote-control is active · Continue here, on your phone, or at https://claude.ai/code/session_01WX\n❯ /remote-control\n  ⎿  Remote Control disconnected.\n"
	state, url, _ := RemoteControlStatus(pane)
	if state != RemoteOff || url != "" {
		t.Fatalf("state=%v url=%q, want RemoteOff without a URL", state, url)
	}
	// Reconnecting after the disconnect is active again.
	pane += "❯ /remote-control\n  /remote-control is active · Continue here, on your phone, or at https://claude.ai/code/session_02YZ\n"
	if state, url, _ := RemoteControlStatus(pane); state != RemoteActive || url != "https://claude.ai/code/session_02YZ" {
		t.Fatalf("state=%v url=%q after reconnect", state, url)
	}
	// A dropped bridge is still told apart from a chosen disconnect.
	if state, _, _ := RemoteControlStatus("● Remote Control disconnected - account changed\n"); state != RemoteDisconnected {
		t.Fatalf("drop read as %v", state)
	}
}

func TestRemoteControlBannerConnected(t *testing.T) {
	on := " ▐▛███▛█   Claude Code v2.1.304\n▝▜██████▀  Opus 5.5 with high effort · Claude Team\n ▝▝   ▝▝   /srv/project · /rc\n\n❯ hi\n"
	off := " ▐▛███▛█   Claude Code v2.1.304\n▝▜██████▀  Opus 5.5 with high effort · Claude Team\n ▝▝   ▝▝   /srv/project\n\n❯ hi\n"
	if !RemoteControlBannerConnected(on) {
		t.Fatal("connected banner not detected")
	}
	if RemoteControlBannerConnected(off) {
		t.Fatal("plain banner read as connected")
	}
	// The marker deep in the conversation is not the banner.
	deep := off
	for i := 0; i < 20; i++ {
		deep += "line\n"
	}
	if RemoteControlBannerConnected(deep + "  quoting /x · /rc\n") {
		t.Fatal("marker below the banner rows counted")
	}
}

// A connected session opens the menu as soon as /remote-control is submitted.
// bp must press Enter exactly once before navigating: a second Enter would
// select "Continue" and close the menu (server-necati panes, 20 Oct).
func TestDisconnectRemoteControlSendsOneEnterBeforeMenu(t *testing.T) {
	const idle = "❯ \n"
	pane := idle
	var calls []string
	client := New()
	client.Sleep = func(time.Duration) {}
	client.exec = func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		switch {
		case strings.HasPrefix(joined, "display-message") && strings.Contains(joined, "#{pane_current_command}"):
			return []byte("claude\n"), nil
		case strings.HasPrefix(joined, "capture-pane"):
			return []byte(pane), nil
		case joined == "send-keys -t =s: Enter" && pane == idle:
			pane = rcMenuOnContinue
		case joined == "send-keys -t =s: Enter" && RemoteControlMenuSelection(pane) == "Continue":
			pane = idle // the regression: Continue closes the menu
		case joined == "send-keys -t =s: Enter" && RemoteControlMenuSelection(pane) != "":
			pane = "❯ /remote-control\n  ⎿  Remote Control disconnected.\n❯ \n"
		case joined == "send-keys -t =s: Up":
			pane = rcMenuOnDisconnect
		}
		return nil, nil
	}
	wasOff, err := client.DisconnectRemoteControl(context.Background(), "s")
	if err != nil || wasOff {
		t.Fatalf("wasOff=%v err=%v\ncalls=%q", wasOff, err, calls)
	}
	enters := 0
	for _, call := range calls {
		if strings.HasPrefix(call, "send-keys") && strings.HasSuffix(call, " Enter") {
			enters++
		}
	}
	if enters != 2 {
		t.Fatalf("Enter pressed %d times, want 2 (submit + Disconnect)\ncalls=%q", enters, calls)
	}
}
