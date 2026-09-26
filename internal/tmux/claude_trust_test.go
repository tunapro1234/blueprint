package tmux

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Reconstructed startup fixture: strings checked against Claude 2.1.263.
// No native CLI, credentials, trust settings or model calls are used.
const claudeTrustFixture = `Accessing workspace:

 /srv/agent

 Quick safety check: Is this a project you created or one you trust? (Like your own
 code, a well-known open source project, or work from your team).

 ❯ 1. Yes, I trust this folder
   2. No, exit

 Enter to confirm · Esc to cancel
`

// Claude 2.1.283 trust screen as captured from a live pane: no option
// numbers, the refusal first and selected.
const claudeTrustFixture283 = `CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=agent command claude --dangerously-skip-permissions
[insert root@host agent]# CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=agent

────────────────────────────────────────
 Accessing workspace:

 /srv/agent

 Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source
 project, or work from your team). If not, take a moment to review what's in this folder first.

 Claude Code'll be able to read, edit, and execute files here.

 Security guide

 ❯ No, exit
   Yes, I trust this folder

 Enter to confirm · Esc to cancel
`

func TestClaudeTrustModal(t *testing.T) {
	moved := strings.Replace(strings.Replace(claudeTrustFixture283, "❯ No, exit", "  No, exit", 1), "  Yes, I trust", "❯ Yes, I trust", 1)
	for _, tc := range []struct {
		name, pane string
		want       claudeTrustAction
	}{
		{"startup", claudeTrustFixture, claudeTrustConfirm},
		{"restricted alternative", strings.ReplaceAll(claudeTrustFixture, "No, exit", "No, continue without these permissions"), claudeTrustConfirm},
		{"wrong directory", strings.ReplaceAll(claudeTrustFixture, "/srv/agent", "/srv/other"), claudeTrustNone},
		{"words in composer", "❯ Quick safety check trust this folder directory", claudeTrustNone},
		{"quoted modal in composer", "──────────\n❯ " + claudeTrustFixture + "──────────\n", claudeTrustNone},
		{"historical modal", claudeTrustFixture + "❯ user draft\n", claudeTrustNone},
		{"refusal selected", strings.ReplaceAll(claudeTrustFixture, "❯ 1.", "  1."), claudeTrustNone},
		{"missing footer", strings.ReplaceAll(claudeTrustFixture, "Enter to confirm · Esc to cancel", ""), claudeTrustNone},
		{"unselected approval", strings.ReplaceAll(claudeTrustFixture, "❯ 1.", "1."), claudeTrustNone},
		{"numbered refusal selected above nothing", strings.Replace(strings.ReplaceAll(claudeTrustFixture, "❯ 1.", "  1."), "  2. No", "❯ 2. No", 1), claudeTrustNone},
		{"2.1.283 refusal first", claudeTrustFixture283, claudeTrustSelect},
		{"2.1.283 restricted refusal", strings.ReplaceAll(claudeTrustFixture283, "No, exit", "No, continue without these permissions"), claudeTrustSelect},
		{"2.1.283 approval selected", moved, claudeTrustConfirm},
		{"2.1.283 wrong directory", strings.ReplaceAll(claudeTrustFixture283, "/srv/agent", "/srv/other"), claudeTrustNone},
		{"2.1.283 missing footer", strings.ReplaceAll(claudeTrustFixture283, "Enter to confirm · Esc to cancel", ""), claudeTrustNone},
		{"2.1.283 approval not adjacent", strings.Replace(claudeTrustFixture283, "   Yes, I trust this folder", "   Other\n   Yes, I trust this folder", 1), claudeTrustNone},
		{"2.1.283 in composer", "──────────\n❯ " + claudeTrustFixture283 + "──────────\n", claudeTrustNone},
		{"2.1.283 rule inside", strings.Replace(claudeTrustFixture283, " Security guide", " ──────────\n Security guide", 1), claudeTrustNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeTrustModal(tc.pane, "/srv/agent"); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// claudeTrustTerminal models the 2.1.283 modal: the first key after it is
// drawn is ignored, Down moves the selection with wraparound, and Enter on the
// approval opens the composer.
type claudeTrustTerminal struct {
	keys     []string
	approval bool
	ignore   int
}

func (tt *claudeTrustTerminal) screen() string {
	if tt.approval {
		return strings.Replace(strings.Replace(claudeTrustFixture283, "❯ No, exit", "  No, exit", 1), "  Yes, I trust", "❯ Yes, I trust", 1)
	}
	return claudeTrustFixture283
}

func (tt *claudeTrustTerminal) press(key string) (done bool) {
	tt.keys = append(tt.keys, key)
	if tt.ignore > 0 {
		tt.ignore--
		return false
	}
	switch key {
	case "Down":
		tt.approval = !tt.approval
	case "Enter":
		return tt.approval
	}
	return false
}

func openThroughClaudeTrust(t *testing.T, tt *claudeTrustTerminal) error {
	t.Helper()
	h := &openHarness{paneCommand: "zsh"}
	c := openClient(h)
	base := c.exec
	open := false
	c.exec = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
		if args[0] == "capture-pane" {
			if open {
				return []byte("bypass permissions on\n"), nil
			}
			return []byte(tt.screen()), nil
		}
		if len(args) == 4 && args[0] == "send-keys" && args[2] == "=agent:" && (args[3] == "Down" || args[3] == "Enter") {
			open = open || tt.press(args[3])
		}
		return base(ctx, input, args...)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	return c.Open(ctx, "agent", "/srv/agent", OpenOptions{NoPrompt: true}, nil)
}

// A Down lost to the modal's input guard is repeated after re-reading the
// screen, never blind: a second Down would wrap back to "No, exit".
func TestOpenClaudeTrustMovesOffDefaultRefusal(t *testing.T) {
	tt := &claudeTrustTerminal{ignore: 1}
	openThroughClaudeTrust(t, tt)
	if got := strings.Join(tt.keys, " "); !strings.HasPrefix(got, "Down Down Enter") {
		t.Fatalf("trust keys = %q, want Down Down Enter", got)
	}
	if !tt.approval {
		t.Fatal("selection left on the refusal")
	}
}

func TestOpenClaudeTrustBoundsKeys(t *testing.T) {
	tt := &claudeTrustTerminal{ignore: 1 << 30}
	err := openThroughClaudeTrust(t, tt)
	if err == nil || !strings.Contains(err.Error(), "trust prompt") {
		t.Fatalf("open error = %v, want trust prompt failure", err)
	}
	if len(tt.keys) != claudeTrustMaxKeys {
		t.Fatalf("trust keys = %v, want %d", tt.keys, claudeTrustMaxKeys)
	}
}

func TestOpenClaudeTrustSelection(t *testing.T) {
	for _, tc := range []struct {
		name, pane string
		failKey    bool
		wantKeys   int
	}{
		{"modal", claudeTrustFixture, false, 1},
		{"composer", "❯ trust this folder directory", false, 0},
		{"send failure", claudeTrustFixture, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &openHarness{paneCommand: "zsh"}
			c := openClient(h)
			base := c.exec
			keys := 0
			stop := errors.New("stop after startup selection")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.exec = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
				if args[0] == "capture-pane" {
					return []byte(tc.pane), nil
				}
				if strings.Join(args, " ") == "send-keys -t =agent: Enter" {
					keys++
					if tc.failKey {
						return nil, stop
					}
					cancel()
				}
				return base(ctx, input, args...)
			}
			err := c.Open(ctx, "agent", "/srv/agent", OpenOptions{NoPrompt: true}, nil)
			if err == nil || keys != tc.wantKeys {
				t.Fatalf("err=%v, bare Enter count=%d, want %d", err, keys, tc.wantKeys)
			}
			if tc.failKey && !errors.Is(err, stop) {
				t.Fatalf("lost send error: %v", err)
			}
		})
	}
}
