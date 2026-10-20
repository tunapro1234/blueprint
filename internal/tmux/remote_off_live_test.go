//go:build rclive

package tmux

import (
	"context"
	"os"
	"testing"
	"time"
)

// Run against a throwaway Claude session on an isolated tmux server:
// BP_RCLIVE_TMUX=<wrapper calling tmux -S sock> BP_RCLIVE_SESSION=<name> go test -tags rclive -run Live
func TestLiveDisconnectRemoteControl(t *testing.T) {
	bin, session := os.Getenv("BP_RCLIVE_TMUX"), os.Getenv("BP_RCLIVE_SESSION")
	if bin == "" || session == "" {
		t.Skip("set BP_RCLIVE_TMUX and BP_RCLIVE_SESSION")
	}
	c := &Client{Bin: bin, Sleep: time.Sleep, Now: time.Now}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	wasOff, err := c.DisconnectRemoteControl(ctx, session)
	t.Logf("wasOff=%v err=%v", wasOff, err)
	if err != nil {
		t.Fatal(err)
	}
	pane, _ := c.Capture(ctx, session)
	if state, _, _ := RemoteControlStatus(pane); state != RemoteOff || RemoteControlMenu(pane) {
		t.Fatalf("state %v after disconnect:\n%s", state, pane)
	}
}

// BP_RCLIVE_RESUME=<claude session id> BP_RCLIVE_DIR=<cwd>: the session was
// connected when its process died; Open with Remote Control off must
// disconnect the bridge it reattaches.
func TestLiveResumeDisconnectsRemoteControl(t *testing.T) {
	bin, session := os.Getenv("BP_RCLIVE_TMUX"), os.Getenv("BP_RCLIVE_SESSION")
	id, dir := os.Getenv("BP_RCLIVE_RESUME"), os.Getenv("BP_RCLIVE_DIR")
	if bin == "" || session == "" || id == "" || dir == "" {
		t.Skip("set BP_RCLIVE_TMUX, BP_RCLIVE_SESSION, BP_RCLIVE_RESUME, BP_RCLIVE_DIR")
	}
	c := &Client{Bin: bin, Sleep: time.Sleep, Now: time.Now}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var warnings []string
	if err := c.Open(ctx, session, dir, OpenOptions{Resume: true, ResumeID: id, NoPrompt: true}, func(s string) { warnings = append(warnings, s) }); err != nil {
		t.Fatal(err)
	}
	t.Logf("warnings: %q", warnings)
	pane, _ := c.Capture(ctx, session)
	history, _ := c.CaptureHistory(ctx, session, 5000)
	if state, _, _ := RemoteControlStatus(pane); state != RemoteOff || RemoteControlBannerConnected(history) {
		t.Fatalf("state %v banner-connected %v after resume:\n%s", state, RemoteControlBannerConnected(history), pane)
	}
}
