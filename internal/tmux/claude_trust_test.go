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

func TestClaudeTrustModal(t *testing.T) {
	for _, tc := range []struct {
		name, pane string
		want       bool
	}{
		{"startup", claudeTrustFixture, true},
		{"restricted alternative", strings.ReplaceAll(claudeTrustFixture, "No, exit", "No, continue without these permissions"), true},
		{"wrong directory", strings.ReplaceAll(claudeTrustFixture, "/srv/agent", "/srv/other"), false},
		{"words in composer", "❯ Quick safety check trust this folder directory", false},
		{"quoted modal in composer", "──────────\n❯ " + claudeTrustFixture + "──────────\n", false},
		{"historical modal", claudeTrustFixture + "❯ user draft\n", false},
		{"refusal selected", strings.ReplaceAll(claudeTrustFixture, "❯ 1.", "  1."), false},
		{"missing footer", strings.ReplaceAll(claudeTrustFixture, "Enter to confirm · Esc to cancel", ""), false},
		{"unselected approval", strings.ReplaceAll(claudeTrustFixture, "❯ 1.", "1."), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeTrustModal(tc.pane, "/srv/agent"); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
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
