package tmux

import (
	"context"
	"fmt"
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
	// capture is what every capture-pane returns. It defaults to the Claude
	// readiness screen; a Hermes open needs the Hermes one instead, and the
	// existing-session branch now reads it too (a live Hermes pane reports
	// "python", so the screen is half of the agent test).
	capture string
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
			if h.capture != "" {
				return []byte(h.capture), nil
			}
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

func TestOpenLaunchesHermesAndWaitsForItsIdleComposer(t *testing.T) {
	// The Codex precedent: a plain command override, no --resume, and none of the
	// Claude-only slash commands afterwards (/rename and /remote-control would be
	// typed into the Hermes composer as literal text). Readiness is the idle
	// placeholder — the one screen that proves the TUI has booted AND is not
	// mid-turn, so the onboarding paste can land.
	h := &openHarness{paneCommand: "zsh", capture: hermesPane(hermesIdleRow)}
	opts := OpenOptions{Hermes: true, Resume: true, NoPrompt: true}
	if err := openClient(h).Open(context.Background(), "agent", "/srv/agent", opts, nil); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(h.mutations, "\n")
	if !strings.Contains(joined, "send-keys -t =agent: hermes Enter") {
		t.Fatalf("hermes was not launched: %s", joined)
	}
	for _, forbidden := range []string{"claude", "--resume", "/rename", "/remote-control"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("a Hermes open sent %q: %s", forbidden, joined)
		}
	}
}

func TestOpenLeavesALiveHermesPaneAlone(t *testing.T) {
	// A running Hermes reports "python". On the command alone Open would neither
	// recognise it as an agent nor accept it as a reusable shell, so it would
	// refuse with "pane runs \"python\"" — and an operator following that advice
	// would close a working agent.
	h := &openHarness{paneCommand: "python", capture: hermesPane(hermesIdleRow)}
	if err := openClient(h).Open(context.Background(), "agent", "/srv/agent", OpenOptions{NoPrompt: true}, nil); err != nil {
		t.Fatal(err)
	}
	if h.newSessions != 0 || len(h.mutations) != 0 {
		t.Fatalf("a live Hermes pane was touched: new=%d mutations=%v", h.newSessions, h.mutations)
	}
}

func TestOpenStillRefusesAPythonPaneThatIsNotHermes(t *testing.T) {
	h := &openHarness{paneCommand: "python", capture: "epoch 12/50  loss 0.42\n"}
	err := openClient(h).Open(context.Background(), "agent", "/srv/agent", OpenOptions{NoPrompt: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "python") {
		t.Fatalf("err=%v, want a refusal naming the foreign program", err)
	}
	if h.newSessions != 0 || len(h.mutations) != 0 {
		t.Fatalf("a foreign python pane was touched: new=%d mutations=%v", h.newSessions, h.mutations)
	}
}

func TestCodexResumeLaunchPreservesThreadAndSettings(t *testing.T) {
	id := "01a0617e-29f9-79a3-be66-72ea1dec4718"
	for _, remote := range []string{"", "unix://"} {
		h := &openHarness{paneCommand: "zsh", capture: modernCodexPane("Ask Codex to do anything")}
		opts := OpenOptions{Codex: true, Resume: true, ResumeID: id, Remote: remote, NoSandbox: remote != "", NoPrompt: true}
		if err := openClient(h).Open(context.Background(), "agent", "/srv/agent", opts, nil); err != nil {
			t.Fatal(err)
		}
		launch := strings.Join(h.mutations, "\n")
		if !strings.Contains(launch, "resume '"+id+"'") {
			t.Fatalf("lost thread: %s", launch)
		}
		for _, bad := range []string{"model_reasoning_effort", "--model", "service_tier", "/rename", "/remote-control"} {
			if strings.Contains(launch, bad) {
				t.Fatalf("overrode thread settings: %s", launch)
			}
		}
	}
}

func TestRemoteSandboxRefusalPrecedesAnyMutation(t *testing.T) {
	h := &openHarness{paneCommand: "zsh"}
	err := openClient(h).Open(context.Background(), "agent", "/srv/agent", OpenOptions{Codex: true, Remote: "unix://"}, nil)
	if err == nil || len(h.mutations) > 0 {
		t.Fatalf("sandbox escape was not refused: %v %v", err, h.mutations)
	}
}

func TestFreshCodexOnboardingNeedsNoTranscriptOrPostStartPaste(t *testing.T) {
	for _, noPrompt := range []bool{false, true} {
		h := &openHarness{paneCommand: "zsh", capture: "◦ Working (1s • esc to interrupt)\n" + modernCodexPane("Ask Codex to do anything")}
		c := openClient(h)
		original := c.exec
		pastes := 0
		c.exec = func(ctx context.Context, in []byte, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "has-session" {
				return nil, fmt.Errorf("no session")
			}
			if len(args) > 0 && (args[0] == "load-buffer" || args[0] == "paste-buffer") {
				pastes++
			}
			return original(ctx, in, args...)
		}
		var warnings []string
		err := c.Open(context.Background(), "agent", t.TempDir(), OpenOptions{Codex: true, NoPrompt: noPrompt}, func(s string) { warnings = append(warnings, s) })
		if err != nil || pastes != 0 || len(warnings) != 0 {
			t.Fatal(err, pastes, warnings)
		}
		launch := strings.Join(h.mutations, "\n")
		present := strings.Contains(launch, "bp agent.")
		if present == noPrompt {
			t.Fatalf("onboarding arg missing or --no-prompt ignored: %s", launch)
		}
		if strings.Contains(launch, "--model") || strings.Contains(launch, "model_reasoning_effort") {
			t.Fatal("settings changed")
		}
	}
}
