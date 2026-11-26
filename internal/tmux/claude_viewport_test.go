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
	rows := composerRows(tt.composer, composerContentWidth(tt.width))
	if len(rows) > tt.visibleRows {
		// Claude anchors the cursor at the end after a paste. Once the buffer
		// shrinks to fit, displaying all rows naturally anchors the view at top.
		rows = rows[len(rows)-tt.visibleRows:]
	}
	rendered := make([]string, len(rows))
	for i, row := range rows {
		row = strings.TrimSuffix(row, "\n")
		if i == 0 {
			rendered[i] = "❯ " + row
		} else {
			rendered[i] = "  " + row
		}
	}
	pane := claudePane(rendered...)
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

// composerRows renders a composer buffer the way Claude does: every logical
// line starts a new row and is word-wrapped on its own. Each row keeps the
// newline that ends its logical line, so joining the rows restores the text.
func composerRows(text string, width int) []string {
	lines := strings.Split(text, "\n")
	var rows []string
	for i, line := range lines {
		wrapped := wordWrap(line, width)
		if i < len(lines)-1 {
			wrapped[len(wrapped)-1] += "\n"
		}
		rows = append(rows, wrapped...)
	}
	return rows
}

func (tt *claudeViewportTerminal) clearWrappedRow() {
	rows := composerRows(tt.composer, composerContentWidth(tt.width))
	if len(rows) <= 1 {
		tt.composer = ""
		return
	}
	// The row above keeps its newline; deleting the emptied row removes it.
	tt.composer = strings.TrimSuffix(strings.Join(rows[:len(rows)-1], ""), "\n")
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

// Measured 2026-11-26 on Claude Code 2.1.321 in a 79x18 pane: a two-line
// message whose short first line (about 33 cells without spaces) is the only
// row the 4-row composer viewport hides. Moving the cursor up revealed the
// first line intact, but the hidden part was under half a row, so the tail was
// not accepted: the paste was called mangled, cleared, re-pasted, called
// mangled again and queued as "paste entered the composer incorrectly".
// shortHeadMessage is a synthetic text with the same geometry: a 34-cell first
// line, then one logical line that word-wraps to exactly 4 rows at width 79.
// The em dash and the accented letters keep rune-versus-byte handling covered.
const shortHeadMessage = "[relay-bot] Morning round, 09:00 note:\n" +
	"Zoë | ops | 00:07-08:07 | soldered four encoder cables for the café crew, " +
	"whether they were shipped is unclear, some may be wired wrong (the first " +
	"one was right, it was tested) — the owner said they will check it later, " +
	"follow-up stays with the owner [follow-up]"

// shortHeadCapture is the composer viewport written out row by row in the
// shape Claude 2.1.321 draws it (prompt marker plus NBSP on the first visible
// row, a two-space indent on the rest, trailing spaces trimmed), so the matcher
// is also tested against rows the test helpers did not generate.
var shortHeadCapture = []string{
	"❯\u00a0Zoë | ops | 00:07-08:07 | soldered four encoder cables for the café crew,",
	"  whether they were shipped is unclear, some may be wired wrong (the first",
	"  one was right, it was tested) — the owner said they will check it later,",
	"  follow-up stays with the owner [follow-up]",
}

func TestClaudeComposerTailHidesShortFirstLine(t *testing.T) {
	const width = 79
	// The literal rows must be what the helpers render for the same viewport.
	rendered := (&claudeViewportTerminal{width: width, visibleRows: 4, composer: shortHeadMessage}).pane()
	if literal := claudePane(shortHeadCapture...); captureRows(rendered) != captureRows(literal) {
		t.Fatalf("literal capture does not match the rendered viewport:\n%s\n---\n%s", literal, rendered)
	}
	pane := claudePane(shortHeadCapture...)
	if !claudeComposerTailMatches(pane, shortHeadMessage, width) {
		t.Fatal("the viewport hiding exactly the short first line must match as our tail")
	}
	if !claudeComposerTailMatches(rendered, shortHeadMessage, width) {
		t.Fatal("the rendered viewport hiding the short first line must match as our tail")
	}
	if got := testClient(&sendHarness{}).pasteIntegrity(pane, shortHeadMessage, width); got != pasteIntact {
		t.Fatalf("pasteIntegrity(short first line hidden)=%v, want pasteIntact", got)
	}
	// Several short lines hidden at once are still whole lines.
	tinyHeads := "a\nb\n" + strings.SplitN(shortHeadMessage, "\n", 2)[1]
	if !claudeComposerTailMatches(pane, tinyHeads, width) {
		t.Fatal("hidden whole short lines must match as our tail")
	}
}

func TestClaudeComposerTailShortLossStaysDamage(t *testing.T) {
	const width = 79
	tinyHead := "abc\n" + strings.SplitN(shortHeadMessage, "\n", 2)[1]
	cases := []struct {
		name, message string
		rows          []string
	}{
		// A tear that lost part of the first line, not the whole of it.
		{"head lost mid-line", shortHeadMessage, append([]string{"❯\u00a0round, 09:00 note:"}, indentRows(shortHeadCapture)...)},
		// The whole short first line AND the opening word of the second are
		// gone: under half a row in all, and not starting at a line.
		{"second line lost its head", tinyHead, append([]string{"❯\u00a0| ops | 00:07-08:07 | soldered four encoder cables for the café crew,"}, shortHeadCapture[1:]...)},
		// The rows after the newline, but missing a middle row.
		{"middle row lost", shortHeadMessage, []string{shortHeadCapture[0], shortHeadCapture[1], shortHeadCapture[3]}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pane := claudePane(tc.rows...)
			if claudeComposerTailMatches(pane, tc.message, width) {
				t.Fatal("a damaged paste must not be accepted as a clipped tail")
			}
			if got := testClient(&sendHarness{}).pasteIntegrity(pane, tc.message, width); got == pasteIntact {
				t.Fatal("pasteIntegrity accepted a damaged paste as intact")
			}
		})
	}
}

// captureRows normalizes a pane row by row the way a capture reads: the NBSP
// after the prompt marker as a space, trailing spaces trimmed.
func captureRows(pane string) string {
	rows := strings.Split(strings.ReplaceAll(pane, "\u00a0", " "), "\n")
	for i, row := range rows {
		rows[i] = strings.TrimRight(row, " ")
	}
	return strings.Join(rows, "\n")
}

func indentRows(rows []string) []string {
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = "  " + strings.TrimPrefix(row, "❯\u00a0")
	}
	return out
}

func TestClaudeClippedShortFirstLineDeliversOnce(t *testing.T) {
	terminal := &claudeViewportTerminal{width: 79, visibleRows: 4}
	if rows := len(composerRows(shortHeadMessage, composerContentWidth(79))); rows != 5 {
		t.Fatalf("fixture renders %d rows, want 5 (one hidden)", rows)
	}
	if err := terminal.client().Send(context.Background(), "target", shortHeadMessage); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if terminal.pastes != 1 || countClaudeEnter(terminal.keys) != 1 {
		t.Fatalf("pastes=%d Enter=%d keys=%v, want one paste and one Enter", terminal.pastes, countClaudeEnter(terminal.keys), terminal.keys)
	}
	if len(terminal.submitted) != 1 || terminal.submitted[0] != shortHeadMessage {
		t.Fatalf("submitted = %q, want one complete message", terminal.submitted)
	}
}
