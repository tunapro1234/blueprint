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
	usageLimited       bool
	limitAfterEnter    bool
}

func (tt *claudeViewportTerminal) pane() string {
	if tt.composer == "" {
		pane := claudePane(emptyRow)
		if tt.usageLimited {
			return claudePaneWithUsageLimit(pane,
				"  ⚠ Usage limit reached · limit resets 5:40pm · clau.de/wrap-up · /upgrade to keep using …",
				"  ⚠ While you wait, start a new cloud session by claiming a $250 credit",
				"  ⚠ /low-priority to continue now at lower priority · uses your weekly limit",
			)
		}
		return pane
	}
	rows := wordWrap(tt.composer, composerContentWidth(tt.width))
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
	pane := claudePane(composerRows...)
	if !tt.usageLimited {
		return pane
	}
	return claudePaneWithUsageLimit(pane,
		"  ⚠ Usage limit reached · limit resets 5:40pm · clau.de/wrap-up · /upgrade to keep using …",
		"  ⚠ While you wait, start a new cloud session by claiming a $250 credit",
		"  ⚠ /low-priority to continue now at lower priority · uses your weekly limit",
	)
}

// wordWrap breaks rows after the last space that fits, as Claude does, and
// falls back to a hard break inside a long word. The space stays on the row it
// ends, so joining the rows restores the text.
func wordWrap(text string, width int) []string {
	var rows []string
	runes := []rune(text)
	for len(runes) > width {
		cut := width
		for i := width; i > 0; i-- {
			if runes[i] == ' ' {
				cut = i + 1
				break
			}
		}
		rows = append(rows, string(runes[:cut]))
		runes = runes[cut:]
	}
	return append(rows, string(runes))
}

func (tt *claudeViewportTerminal) clearWrappedRow() {
	rows := wordWrap(tt.composer, composerContentWidth(tt.width))
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
				if tt.limitAfterEnter {
					tt.usageLimited = true
				} else {
					tt.submitted = append(tt.submitted, tt.composer)
					tt.composer = ""
				}
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

// q382452178 (2026-09-27, probot-egitim-writer at 59x23): a 346-character
// message with ordinary spaces. Stripped of its spaces it would fit the six
// visible rows, so judging the viewport from the stripped text called our own
// clipped paste foreign text and queued it as never entered.
func TestClaudeClippedSpacedMessageDeliversOnce(t *testing.T) {
	message := "[probot-egitim] Starter Bot 1.7, 1.8, 1.9 yazimi. Brief: " +
		"/srv/probot/egitim/mufredat/araclar/starter-bot-1-7-1-9-brief-2026-09-27.md " +
		"(once onu, sonra oradaki Once oku listesini oku). 1.4-1.6 entegre edildi ve " +
		"dev yayinda, koordinator duzeltmeleri brief icinde. Sira 1.7, 1.8, 1.9. Her " +
		"derste teslim hazir mesaji, sonunda damitma ve skill-yedekle."
	terminal := &claudeViewportTerminal{width: 59, visibleRows: 6}
	if rows := len(wordWrap(message, composerContentWidth(59))); rows <= terminal.visibleRows {
		t.Fatalf("fixture wraps to %d rows, want a clipped viewport", rows)
	}
	if err := terminal.client().Send(context.Background(), "target", message); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if terminal.pastes != 1 || countClaudeEnter(terminal.keys) != 1 {
		t.Fatalf("pastes=%d Enter=%d keys=%v, want one paste and one Enter", terminal.pastes, countClaudeEnter(terminal.keys), terminal.keys)
	}
	if len(terminal.submitted) != 1 || terminal.submitted[0] != message {
		t.Fatalf("submitted = %q, want one complete message", terminal.submitted)
	}
}

func TestClaudeClippedOwnRemnantIsClearedBeforeOneDelivery(t *testing.T) {
	message := strings.Repeat("0123456789", 45) + "END!"
	const width = 59
	const visibleRows = 6
	leftover := message[:visibleRows*composerContentWidth(width)]
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

// q319700699 (2026-09-29, blueprint at 64x31, Claude 2.1.284): a 597-character
// message that Claude wraps at 60 cells into 11 rows and shows as a 10-row
// tail. Counted at character wrap across 62 cells it fit in 10 rows, so the clipped view of our own paste was
// called damaged, repaired, called damaged again and left in the composer.
const financeMessage = "[probot-finance] probot-finance (Claude, /srv/probot/finance) — " +
	"yardım: Tuna laptopundaki para-main agentı (kimliği \"para-main@tuna-laptop\") " +
	"bana [external:para-main@tuna-laptop] önekiyle mesaj attı ama ben ona geri " +
	"yazamıyorum: SendMessage \"para-main@tuna-laptop\" -> \"bare teammate name olmalı\", " +
	"\"para-main\" -> \"reachable değil\"; ListAgents ve bp status da görmüyor. Ona " +
	"nasıl cevap gönderirim? (bp p2p / bp msg para-main@tuna-laptop / bp attach ... " +
	"hangisi doğru, bağlantı kurulu mu?) Cevabı bp msg probot-finance ile at. " +
	"İletilecek metin hazır: bütçe formatı + equity kuralları, veri/rakam yok."

func TestWrappedMessageRowsCountsWordWrap(t *testing.T) {
	rows, err := wrappedMessageRows(financeMessage, 64)
	if err != nil {
		t.Fatal(err)
	}
	if want := len(wordWrap(financeMessage, composerContentWidth(64))); rows != want {
		t.Fatalf("wrappedMessageRows=%d, want the word-wrapped %d", rows, want)
	}
	if rows <= 10 {
		t.Fatalf("wrappedMessageRows=%d, want more than the 10 rows Claude showed", rows)
	}
	cases := []struct {
		message string
		width   int
		want    int
	}{
		{"", 14, 1},
		{"short", 14, 1},
		{"0123456789", 14, 1},
		{"0123456789a", 14, 2},
		{"aaaa bbbb cc", 14, 2},
		{"aaaa bbb c", 14, 1},
		{strings.Repeat("x", 25), 14, 3},
		{"one\ntwo", 14, 2},
	}
	for _, tc := range cases {
		got, err := wrappedMessageRows(tc.message, tc.width)
		if err != nil || got != tc.want {
			t.Errorf("wrappedMessageRows(%q, %d)=%d, %v; want %d", tc.message, tc.width, got, err, tc.want)
		}
	}
	if _, err := wrappedMessageRows("x", composerIndent+composerRightMargin); err == nil {
		t.Fatal("a pane with no content columns must be an error")
	}
}

func TestClaudeClippedWordWrappedMessageDeliversOnce(t *testing.T) {
	for _, visibleRows := range []int{8, 10, 11} {
		t.Run(strconv.Itoa(visibleRows), func(t *testing.T) {
			terminal := &claudeViewportTerminal{width: 64, visibleRows: visibleRows}
			if err := terminal.client().Send(context.Background(), "target", financeMessage); err != nil {
				t.Fatalf("Send() error = %v", err)
			}
			if terminal.pastes != 1 || countClaudeEnter(terminal.keys) != 1 {
				t.Fatalf("pastes=%d Enter=%d keys=%v, want one paste and one Enter", terminal.pastes, countClaudeEnter(terminal.keys), terminal.keys)
			}
			if len(terminal.submitted) != 1 || terminal.submitted[0] != financeMessage {
				t.Fatalf("submitted = %q, want one complete message", terminal.submitted)
			}
		})
	}
}
