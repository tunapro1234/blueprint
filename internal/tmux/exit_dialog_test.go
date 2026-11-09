package tmux

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// exitPrompt renders the prompt with the escape codes Claude Code 2.1.32x
// draws (captured 10 Nov 2026), with the cursor on the given option.
func exitPrompt(cursor int, work ...string) string {
	var b strings.Builder
	b.WriteString("● earlier conversation\n\n\x1b[38;5;153m" + strings.Repeat("▔", 40) + "\n")
	b.WriteString("\x1b[39m   \x1b[1m\x1b[38;5;153mBackground work is running\x1b[0m\n")
	b.WriteString("   \x1b[38;5;246mThe following will stop when you exit:\x1b[39m\n\n")
	for _, item := range work {
		kind, rest, _ := strings.Cut(item, " · ")
		b.WriteString("   \x1b[1m" + kind + "\x1b[0m\x1b[38;5;246m · " + rest + "\x1b[39m\n")
	}
	b.WriteString("\n")
	for index, label := range []string{"Exit and stop tasks", "Move to background and exit", "Stay"} {
		number := string(rune('1' + index))
		if index+1 == cursor {
			b.WriteString("   \x1b[38;5;153m❯\x1b[39m \x1b[38;5;246m" + number + ". \x1b[38;5;153m" + label + "\x1b[39m\n")
		} else {
			b.WriteString("     \x1b[38;5;246m" + number + ". \x1b[39m" + label + "\n")
		}
	}
	b.WriteString("\n   \x1b[3m\x1b[38;5;246mEnter to confirm · Esc to cancel\x1b[0m\n")
	return b.String()
}

func TestExitDialogReadsWorkAndCursor(t *testing.T) {
	work := []string{"shell · exec python3 daemon.py >> state/daemon.…", "scheduled task · 7,37 * * * * · follow-up round"}
	for cursor := 1; cursor <= 3; cursor++ {
		got, at, ok := ExitDialog(exitPrompt(cursor, work...))
		if !ok || at != cursor || !reflect.DeepEqual(got, work) {
			t.Fatalf("cursor %d: work=%q at=%d ok=%v", cursor, got, at, ok)
		}
	}
	if _, at, ok := ExitDialog(exitPrompt(0, work[0])); !ok || at != 0 {
		t.Fatalf("prompt without a visible cursor: at=%d ok=%v", at, ok)
	}
	plain := ansiSeq.ReplaceAllString(exitPrompt(1, work[0]), "")
	for name, pane := range map[string]string{
		"quoted above a composer": plain + "\n────\n❯ \n────\n  ⏵⏵ bypass permissions on\n",
		"another option list":     strings.Replace(plain, "3. Stay", "3. Cancel", 1),
		"two cursors":             strings.Replace(plain, "  3. Stay", "❯ 3. Stay", 1),
		"no header":               strings.Replace(plain, "The following will stop when you exit:", "Something else:", 1),
		"empty":                   "",
		"idle composer":           "────\n❯ \n────\n",
	} {
		if _, _, ok := ExitDialog(pane); ok {
			t.Fatalf("%s was read as the exit prompt", name)
		}
	}
	if !paneDialog(exitPrompt(1, work[0])) {
		t.Fatal("the delivery path does not treat the exit prompt as a dialog")
	}
}

// exitPromptPane is a pane whose prompt reacts to keys the way Claude Code's
// does. frozen keeps the cursor where it is, to model a prompt that ignores
// Down.
type exitPromptPane struct {
	cursor       int
	open, frozen bool
	keys         []string
	command      string
}

func (p *exitPromptPane) client() *Client {
	return &Client{Sleep: func(time.Duration) {}, Now: time.Now, exec: func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
		switch args[0] {
		case "display-message":
			return []byte(p.command + "\n"), nil
		case "capture-pane":
			if !p.open {
				return []byte("────\n❯ \n────\n"), nil
			}
			return []byte(exitPrompt(p.cursor, "shell · sleep 600")), nil
		case "send-keys":
			key := args[len(args)-1]
			p.keys = append(p.keys, key)
			switch {
			case key == "Down" && !p.frozen && p.cursor < 3:
				p.cursor++
			case key == "Enter" && p.cursor != 3:
				panic("Enter pressed while the cursor was on option " + string(rune('0'+p.cursor)))
			case key == "Enter":
				p.open = false
			}
			return nil, nil
		}
		return nil, errors.New("unexpected tmux call: " + strings.Join(args, " "))
	}}
}

func TestStayInExitDialogConfirmsOnlyStay(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pane    exitPromptPane
		keys    []string
		wantErr error
		open    bool
	}{
		{"from-the-default", exitPromptPane{cursor: 1, open: true, command: "claude"}, []string{"Down", "Down", "Enter"}, nil, false},
		{"from-the-middle", exitPromptPane{cursor: 2, open: true, command: "claude"}, []string{"Down", "Enter"}, nil, false},
		{"already-on-stay", exitPromptPane{cursor: 3, open: true, command: "claude"}, []string{"Enter"}, nil, false},
		{"cursor-unreadable", exitPromptPane{cursor: 0, open: true, command: "claude"}, nil, ErrDialog, true},
		{"cursor-does-not-move", exitPromptPane{cursor: 1, open: true, frozen: true, command: "claude"}, []string{"Down", "Down", "Down", "Down"}, ErrDialog, true},
		{"no-prompt", exitPromptPane{cursor: 1, command: "claude"}, nil, ErrUnverified, false},
		{"not-an-agent-pane", exitPromptPane{cursor: 1, open: true, command: "vim"}, nil, ErrNotAgent, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pane := tc.pane
			err := pane.client().StayInExitDialog(context.Background(), "worker")
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("err=%v, want %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(pane.keys, tc.keys) || pane.open != tc.open {
				t.Fatalf("keys=%v open=%v, want %v open=%v", pane.keys, pane.open, tc.keys, tc.open)
			}
		})
	}
}
