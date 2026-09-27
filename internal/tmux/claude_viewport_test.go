package tmux

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

type claudeViewportTerminal struct {
	width, visibleRows int
	composer, payload  string
	submitted          []string
	keys               []string
	pastes             int
	clears             int
}

func (tt *claudeViewportTerminal) pane() string {
	if tt.composer == "" {
		return claudePane(emptyRow)
	}
	rows := wrapText(tt.composer, tt.width-2)
	if len(rows) > tt.visibleRows {
		// Claude anchors the cursor at the end after a paste. Once the buffer
		// shrinks to fit, displaying all rows naturally anchors the view at top.
		rows = rows[len(rows)-tt.visibleRows:]
	}
	composerRows := make([]string, len(rows))
	for i, row := range rows {
		if i == 0 {
			composerRows[i] = "❯ " + row
		} else {
			composerRows[i] = "  " + row
		}
	}
	return claudePane(composerRows...)
}

func (tt *claudeViewportTerminal) clearWrappedRow() {
	rows := wrapText(tt.composer, tt.width-2)
	if len(rows) <= 1 {
		tt.composer = ""
		return
	}
	tt.composer = strings.Join(rows[:len(rows)-1], "")
}

func (tt *claudeViewportTerminal) client() *Client {
	c := New()
	c.Sleep = func(time.Duration) {}
	c.Now = func() time.Time { return time.Unix(1000, 0) }
	c.exec = func(_ context.Context, data []byte, args ...string) ([]byte, error) {
		switch args[0] {
		case "display-message":
			format := args[len(args)-1]
			switch format {
			case "#{pane_current_command}":
				return []byte("claude\n"), nil
			case "#{pane_width}":
				return []byte(strconv.Itoa(tt.width) + "\n"), nil
			default:
				return nil, fmt.Errorf("unexpected display-message format %q", format)
			}
		case "list-clients":
			return nil, nil
		case "capture-pane":
			return []byte(tt.pane()), nil
		case "load-buffer":
			tt.payload = string(data)
		case "paste-buffer":
			tt.pastes++
			tt.composer += tt.payload
		case "send-keys":
			key := args[len(args)-1]
			tt.keys = append(tt.keys, key)
			switch key {
			case "C-u":
				tt.clears++
				tt.clearWrappedRow()
			case "Enter":
				tt.submitted = append(tt.submitted, tt.composer)
				tt.composer = ""
			}
		case "delete-buffer":
		default:
			return nil, fmt.Errorf("unexpected tmux call: %v", args)
		}
		return nil, nil
	}
	return c
}

func TestClaudeComposerTailViewportMatcher(t *testing.T) {
	want := strings.Repeat("abcdefgh", 11) + "TAIL-0123456789"
	tail := want[len(want)-48:]
	cases := []struct {
		name string
		got  string
		want bool
	}{
		{"tail substring", tail, true},
		{"middle substring without tail", want[16:64], false},
		{"foreign text", strings.Repeat("foreign text ", 5), false},
		{"short suffix", want[len(want)-12:], false},
		// Clipping is proven only when at least half a row is hidden; a head
		// that lost a few characters is damage, not a viewport.
		{"lost head", want[3:], false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pane := claudePane("❯ " + tc.got)
			if got := claudeComposerTailMatches(pane, want, 59); got != tc.want {
				t.Fatalf("claudeComposerTailMatches()=%v, want %v", got, tc.want)
			}
		})
	}
	if got := testClient(&sendHarness{}).pasteIntegrity(claudePane("❯ "+tail), want, 59); got != pasteIntact {
		t.Fatalf("pasteIntegrity(tail viewport)=%v, want pasteIntact", got)
	}
	if claudeComposerTailMatches(claudePane("❯ "+tail), want, 0) {
		t.Fatal("a tail without a measured pane width must not match")
	}
}

func TestClaudeClippedComposerDeliversOnce(t *testing.T) {
	message := strings.Repeat("0123456789", 45) + "END!"
	terminal := &claudeViewportTerminal{width: 59, visibleRows: 6}
	if err := terminal.client().Send(context.Background(), "target", message); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if terminal.pastes != 1 {
		t.Fatalf("paste count = %d, want 1", terminal.pastes)
	}
	if countClaudeEnter(terminal.keys) != 1 {
		t.Fatalf("Enter count = %d, want 1 (%v)", countClaudeEnter(terminal.keys), terminal.keys)
	}
	if len(terminal.submitted) != 1 || terminal.submitted[0] != message {
		t.Fatalf("submitted = %q, want one complete message", terminal.submitted)
	}
}

func TestClaudeClippedOwnRemnantIsClearedBeforeOneDelivery(t *testing.T) {
	message := strings.Repeat("0123456789", 45) + "END!"
	const width = 59
	const visibleRows = 6
	leftover := message[:visibleRows*(width-2)]
	terminal := &claudeViewportTerminal{width: width, visibleRows: visibleRows, composer: leftover}
	if err := terminal.client().Send(context.Background(), "target", message); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if terminal.clears != visibleRows {
		t.Fatalf("C-u count = %d, want %d to clear the remnant", terminal.clears, visibleRows)
	}
	if terminal.pastes != 1 || countClaudeEnter(terminal.keys) != 1 {
		t.Fatalf("pastes=%d Enter=%d keys=%v, want one paste and one Enter", terminal.pastes, countClaudeEnter(terminal.keys), terminal.keys)
	}
	if len(terminal.submitted) != 1 || terminal.submitted[0] != message {
		t.Fatalf("submitted = %q, want one complete message", terminal.submitted)
	}
	if terminal.composer != "" {
		t.Fatalf("composer left with text %q", terminal.composer)
	}
}

func TestClaudeClippedForeignDraftIsUntouched(t *testing.T) {
	message := strings.Repeat("0123456789", 45) + "END!"
	terminal := &claudeViewportTerminal{
		width: 59, visibleRows: 6, composer: strings.Repeat("foreign draft ", 40),
	}
	if err := terminal.client().Send(context.Background(), "target", message); err == nil {
		t.Fatal("Send() succeeded with a foreign draft in the clipped composer")
	}
	if terminal.pastes != 0 || len(terminal.keys) != 0 || len(terminal.submitted) != 0 {
		t.Fatalf("foreign draft was touched: pastes=%d keys=%v submitted=%q", terminal.pastes, terminal.keys, terminal.submitted)
	}
}

func countClaudeEnter(keys []string) int {
	count := 0
	for _, key := range keys {
		if key == "Enter" {
			count++
		}
	}
	return count
}
