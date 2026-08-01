package tmux

import (
	"context"
	"strings"
	"testing"
	"time"
)

// openHarness scripts the tmux calls Open makes, keyed by subcommand. The
// pane command decides which branch of the existing-session logic runs.
type openHarness struct {
	paneCommand string
	mutations   []string
	newSessions int
}

func openClient(h *openHarness) *Client {
	client := New()
	client.Sleep = func(time.Duration) {}
	client.exec = func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "has-session"):
			return nil, nil
		case strings.HasPrefix(joined, "list-panes -t"):
			return []byte("1\t" + h.paneCommand + "\t100\n"), nil
		case strings.HasPrefix(joined, "new-session"):
			h.newSessions++
			return nil, nil
		case strings.HasPrefix(joined, "send-keys"):
			h.mutations = append(h.mutations, joined)
			return nil, nil
		case strings.HasPrefix(joined, "capture-pane"):
			return []byte("bypass permissions\n"), nil
		default:
			return nil, nil
		}
	}
	return client
}

func TestOpenNoopsWhenAgentAlreadyRunning(t *testing.T) {
	h := &openHarness{paneCommand: "claude"}
	if err := openClient(h).Open(context.Background(), "agent", "/srv/agent", OpenOptions{NoPrompt: true}, nil); err != nil {
		t.Fatal(err)
	}
	if h.newSessions != 0 || len(h.mutations) != 0 {
		t.Fatalf("live agent session was touched: new=%d mutations=%v", h.newSessions, h.mutations)
	}
}

func TestOpenRelaunchesAgentInDeadShellSession(t *testing.T) {
	h := &openHarness{paneCommand: "zsh"}
	if err := openClient(h).Open(context.Background(), "agent", "/srv/agent", OpenOptions{NoPrompt: true}, nil); err != nil {
		t.Fatal(err)
	}
	if h.newSessions != 0 {
		t.Fatal("reused session must not create a new one")
	}
	joined := strings.Join(h.mutations, "\n")
	for _, want := range []string{"C-u", "cd '/srv/agent'", "claude --dangerously-skip-permissions"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("mutations missing %q:\n%s", want, joined)
		}
	}
}

func TestOpenRefusesSessionRunningForeignProgram(t *testing.T) {
	h := &openHarness{paneCommand: "vim"}
	err := openClient(h).Open(context.Background(), "agent", "/srv/agent", OpenOptions{NoPrompt: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "vim") {
		t.Fatalf("err=%v, want refusal naming the foreign program", err)
	}
	if h.newSessions != 0 || len(h.mutations) != 0 {
		t.Fatalf("foreign pane was touched: new=%d mutations=%v", h.newSessions, h.mutations)
	}
}

func TestIsShellCommand(t *testing.T) {
	for _, cmd := range []string{"zsh", "bash", "sh", "dash", "fish"} {
		if !isShellCommand(cmd) {
			t.Errorf("isShellCommand(%q) = false, want true", cmd)
		}
	}
	for _, cmd := range []string{"claude", "codex", "bwrap", "vim", "tmux", "node", ""} {
		if isShellCommand(cmd) {
			t.Errorf("isShellCommand(%q) = true, want false", cmd)
		}
	}
}

func TestRemoteControlMenuDetection(t *testing.T) {
	menu := "Remote Control\n❯ Continue\n  Show QR code\n  Disconnect this session\nEnter to select · Esc to cancel\n"
	if !RemoteControlMenu(menu) {
		t.Fatal("full RC menu not detected")
	}
	if !RemoteControlMenu("Remote Control\n❯ Continue\nEnter to select\n") {
		t.Fatal("RC menu without Disconnect line not detected")
	}
	// Other pickers also print "Enter to select" — they must not match.
	if RemoteControlMenu("Resume from summary\n❯ Resume full session\nEnter to select\n") {
		t.Fatal("resume picker misdetected as RC menu")
	}
	if RemoteControlMenu("❯ \n") {
		t.Fatal("plain composer misdetected as RC menu")
	}
}

func TestCommandsPrefersActivePane(t *testing.T) {
	client := New()
	client.exec = func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
		if got := strings.Join(args, " "); !strings.HasPrefix(got, "list-panes -a -F") {
			t.Fatalf("tmux args=%q", got)
		}
		return []byte("agent\t0\tzsh\nagent\t1\tclaude\nother\t1\tzsh\n"), nil
	}
	commands, err := client.Commands(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if commands["agent"] != "claude" || commands["other"] != "zsh" {
		t.Fatalf("commands=%v, want active panes to win", commands)
	}
}
