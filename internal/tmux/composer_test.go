package tmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures below reproduce the STRUCTURE of live Claude Code panes, measured
// against all 29 sessions on this machine with read-only captures (2026-08-11).
// Every piece of TEXT here is synthetic: real pane content is private mail.
//
// What the measurement changed: the TOP border carries the agent's NAME inside it
// ("---- probot-main --") while the bottom one is a pure rule. Fixtures built with
// two PURE borders passed happily while composerBox returned ok=false on all 29
// real panes — the fixtures were the reason the bug stayed invisible, so they now
// carry the label.
const (
	boxBorderTop    = "\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500 probot-main \u2500\u2500"
	boxBorderBottom = "\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500"
	boxStatus       = "  -- INSERT -- \u23f5\u23f5 bypass permissions on (shift+tab to cycle)    /rc"
	boxChip         = "  \u29c9  fon-panosu"
	// emptyRow is how an empty composer renders on these panes: the prompt marker
	// plus a single NBSP (0xa0) — a two-character row, no spaces at all.
	emptyRow = "\u276f\u00a0"
)

// claudePane wraps composer rows in the full live structure, with a couple of
// transcript rows above it (one of them a rule, so the upward border search is
// exercised against a pane that has border-looking lines in its scrollback — the
// NEAREST border above the box must win).
func claudePane(rows ...string) string {
	lines := []string{"  agent: onceki turdan kalan cikti", "  \u2500\u2500\u2500\u2500\u2500\u2500\u2500 ozet \u2500\u2500\u2500\u2500\u2500\u2500\u2500"}
	lines = append(lines, boxBorderTop)
	lines = append(lines, rows...)
	lines = append(lines, boxBorderBottom, boxStatus, boxChip, "")
	return strings.Join(lines, "\n")
}

// screenFillingPane is a composer that has consumed the whole capture: its top
// border is the first row, so the box has no room left to grow and the TUI must be
// scrolling inside it.
func screenFillingPane(rows ...string) string {
	lines := []string{boxBorderTop}
	lines = append(lines, rows...)
	lines = append(lines, boxBorderBottom, boxStatus, boxChip, "")
	return strings.Join(lines, "\n")
}

// collapsedPane is a composer with only ONE border above the status line.
func collapsedPane() string {
	return strings.Join([]string{
		"  agent: onceki turdan kalan cikti",
		emptyRow,
		boxBorderBottom,
		boxStatus,
		boxChip,
		"",
	}, "\n")
}

// pickerPane is the state measured live on compec-site: a modal picker is open,
// the "-- INSERT --" footer is GONE, and the bottom line is the picker's own hint.
// The composer is not addressable at all — keys go into the menu — so no box may be
// read and nothing may be sent to that pane.
func pickerPane() string {
	return strings.Join([]string{
		"  agent: onceki turdan kalan cikti",
		boxBorderBottom,
		"  Hangi dosyayi acalim?",
		"\u276f 1. birinci secenek",
		"  2. ikinci secenek",
		boxBorderBottom,
		"  Enter to select \u00b7 Tab/Arrow keys to navigate \u00b7 Esc to cancel",
		"",
	}, "\n")
}

// pickerOverStatusPane is the hybrid: a picker is up while the permission footer is
// still on screen, so the status anchor matches. The picker hint in the footer
// region must veto the box anyway.
func pickerOverStatusPane() string {
	return strings.Join([]string{
		"  agent: onceki turdan kalan cikti",
		boxBorderTop,
		"\u276f 1. birinci secenek",
		boxBorderBottom,
		boxStatus,
		"  Enter to select \u00b7 Esc to cancel",
		"",
	}, "\n")
}

func TestComposerBoxReadsTheWholeBox(t *testing.T) {
	pane := claudePane(
		"❯ -konusma taslaginda altinci bolum atlanmis",
		"  -takim tanitimi daha basa alinabilir",
		"",
		"  - teknik soru yoktu",
	)
	box, ok := composerBox(pane)
	if !ok {
		t.Fatalf("box not found in a live-shaped pane:\n%s", pane)
	}
	rows := strings.Split(box, "\n")
	if len(rows) != 4 {
		t.Fatalf("box rows=%d, want 4 (interior blank row preserved): %q", len(rows), box)
	}
	if rows[2] != "" {
		t.Fatalf("interior blank row was not preserved: %q", box)
	}
	if strings.Contains(box, "❯") {
		t.Fatalf("prompt marker was not stripped: %q", box)
	}
	if strings.Contains(box, "ozet") || strings.Contains(box, "INSERT") {
		t.Fatalf("box leaked rows from outside the borders: %q", box)
	}
	want := stripSpace("-konusma taslaginda altinci bolum atlanmis-takim tanitimi daha basa alinabilir- teknik soru yoktu")
	if got := stripSpace(box); got != want {
		t.Fatalf("box text=%q, want %q", got, want)
	}
}

func TestComposerBoxSingleRowMatchesTodaysReading(t *testing.T) {
	// The one-row case must agree with composerContent, so the richer view can
	// never disagree with the narrow one where both apply.
	pane := claudePane("❯ deploy the new bar chips")
	box, ok := composerBox(pane)
	if !ok {
		t.Fatal("single-row box not found")
	}
	if stripSpace(box) != composerContent(pane) {
		t.Fatalf("box=%q composerContent=%q", stripSpace(box), composerContent(pane))
	}
}

func TestComposerBoxRefusesUnfamiliarStructures(t *testing.T) {
	cases := []struct {
		name string
		pane string
	}{
		{"only one border above the status line", collapsedPane()},
		{"no status footer", "  transcript\n" + boxBorderTop + "\n\u276f yazi\n" + boxBorderBottom + "\n"},
		{"codex pane", "  \u203a \n  gpt-5.6-sol low \u00b7 /tmp\n"},
		{"codex pane with text", "\u203a taslak mesaj\n  gpt-5.6-sol low \u00b7 /tmp\n"},
		{"no composer at all", "just some output\n"},
		{"marker on a continuation row", "  x\n" + boxBorderTop + "\n\u276f birinci\n\u276f ikinci\n" + boxBorderBottom + "\n" + boxStatus + "\n"},
		{"no marker on the first row", "  x\n" + boxBorderTop + "\n  birinci\n" + boxBorderBottom + "\n" + boxStatus + "\n"},
		// Measured live on compec-site: a modal picker is open. Reading a box there
		// would treat menu rows as composer content, and typing into that pane sends
		// keys into the menu.
		{"modal picker instead of a composer", pickerPane()},
		{"modal picker over a surviving status footer", pickerOverStatusPane()},
	}
	for _, tc := range cases {
		if box, ok := composerBox(tc.pane); ok {
			t.Errorf("%s: box was read as %q, want ok=false", tc.name, box)
		}
	}
	// Every one of those must also read as "do not touch" through the fallback: a
	// picker pane is treated as in use, and the collapsed one as empty.
	if composerFilled(collapsedPane()) {
		t.Fatal("collapsed empty composer reported as filled")
	}
	for _, pane := range []string{pickerPane(), pickerOverStatusPane()} {
		if _, ours := StuckPaste(pane, []string{"1. birinci secenek", stuckMessage}); ours {
			t.Fatal("a picker row was claimed as our own paste")
		}
		if got := ComposerBlockReason(pane, []string{stuckMessage}); got != BlockedByForeignText {
			t.Fatalf("picker pane reason=%q, want %q", got, BlockedByForeignText)
		}
	}
}

func TestComposerBoxReadsTheLabelledTopBorder(t *testing.T) {
	// The bug that would have shipped: on 26 of 29 live panes the TOP border carries
	// the agent's name, so a pure-run border test found only the bottom one and the
	// whole decision table was dead code. The label must not hide the border, and
	// prose must still not look like one.
	for _, line := range []string{
		boxBorderTop,
		boxBorderBottom,
		"\u2500\u2500\u2500\u2500\u2500\u2500 kavram-outreach \u2500\u2500",
		"\u2500\u2500\u2500\u2500\u2500\u2500\u2500 12 satir daha \u2500\u2500\u2500",
		"  \u2500\u2500\u2500\u2500\u2500\u2500 probot-main \u2500\u2500  ",                   // padded, as captured
		"\u001b[38;5;244m\u2500\u2500\u2500\u2500\u2500\u2500 op-main \u2500\u2500\u001b[39m", // coloured, as captured
	} {
		if !isComposerBoxBorder(line) {
			t.Errorf("border not recognised: %q", line)
		}
	}
	for _, line := range []string{
		"",
		"  duz metin",
		"\u276f \u2500\u2500\u2500 kullanici kendi yazdi \u2500\u2500\u2500", // starts with the prompt marker
		"bir \u2500 iki \u2500 uc",       // dashes inside prose
		"\u2500\u2500 kisa \u2500\u2500", // only 4 border runes
		"  -- INSERT -- \u23f5\u23f5 bypass permissions on",
	} {
		if isComposerBoxBorder(line) {
			t.Errorf("false border: %q", line)
		}
	}
	// isComposerBorder is shared with composerTrail/statusFooter/AuthExpired and
	// must keep its old, strict meaning: the labelled row is NOT a pure border.
	if isComposerBorder(stripSpace(boxBorderTop)) {
		t.Fatal("the shared pure-run border test was widened")
	}
	if !isComposerBorder(stripSpace(boxBorderBottom)) {
		t.Fatal("the shared border test no longer matches a pure rule")
	}
}

func TestComposerBoxOnAnEmptyLiveComposer(t *testing.T) {
	// Measured on probot-main: an empty composer's first row is the marker plus a
	// single NBSP. The box must exist (both borders are there) and read as EMPTY.
	pane := claudePane(emptyRow)
	box, ok := composerBox(pane)
	if !ok {
		t.Fatalf("no box on an empty live-shaped composer:\n%s", pane)
	}
	if stripSpace(box) != "" {
		t.Fatalf("empty composer read as %q", box)
	}
	if composerFilled(pane) || Typing(pane) {
		t.Fatal("empty composer reported as filled")
	}
	if got := ComposerBlockReason(pane, nil); got != "" {
		t.Fatalf("empty composer blocks delivery: %q", got)
	}
}

func TestComposerBoxPrefersTheNearestBorderAboveTheBox(t *testing.T) {
	// The transcript can hold rule-shaped rows (this fixture has one). The upward
	// search must stop at the box's own top border, not wander into the scrollback.
	pane := claudePane("\u276f tek satir")
	box, top, ok := composerBoxAt(pane)
	if !ok || stripSpace(box) != "teksatir" {
		t.Fatalf("box=%q ok=%v", box, ok)
	}
	if top != 2 {
		t.Fatalf("top border index=%d, want 2 (the box's own border)", top)
	}
	if composerBoxScrolled(box, top) {
		t.Fatal("a box with transcript above it was called scrolled")
	}
}

func TestClassifyPasteTellsOursFromForeign(t *testing.T) {
	msg := "[server-main] roadmap incelemesi: hedef sistemi bolumunu bugun bitirmemiz gerekiyor, ozellikle hiyerarsik atama kismini"
	other := "[ada] tamamen baska bir konu: dashboard renkleri"
	cases := []struct {
		name    string
		pane    string
		verdict pasteVerdict
		match   string
	}{
		{"exact", claudePane("❯ " + msg), pasteExact, msg},
		{"exact across a wrap", claudePane("❯ "+msg[:40], "  "+msg[40:]), pasteExact, msg},
		{"exact match on a second candidate", claudePane("❯ " + other), pasteExact, other},
		{"truncated from the front", claudePane("❯ " + msg[60:]), pasteDamaged, msg},
		{"truncated at the end", claudePane("❯ " + msg[:70]), pasteDamaged, msg},
		{"middle chunk missing", claudePane("❯ " + msg[:45] + msg[80:]), pasteDamaged, msg},
		{"foreign line", claudePane("❯ kendi yarim kalan sorum burada duruyor"), pasteForeign, ""},
		{"short foreign line that our text contains", claudePane("❯ roadmap"), pasteForeign, ""},
		{"claude paste chip is not readable", claudePane("❯ [Pasted text #1 +12 lines]"), pasteForeign, ""},
		{"codex chip is not readable", claudePane("❯ [Pasted Content 1024 chars]"), pasteForeign, ""},
		{"empty composer", claudePane(emptyRow), pasteForeign, ""},
		{"unreadable structure", "❯ " + msg + "\n", pasteForeign, ""},
	}
	for _, tc := range cases {
		verdict, match := classifyPaste(tc.pane, []string{msg, other})
		if verdict != tc.verdict || match != tc.match {
			t.Errorf("%s: verdict=%d match=%q, want verdict=%d match=%q", tc.name, verdict, match, tc.verdict, tc.match)
		}
	}
}

func TestRelatedPasteIgnoresShortOverlaps(t *testing.T) {
	// A human's short line may not be dragged into a damage verdict, however well
	// it overlaps: erasing what someone is writing is the worst outcome here.
	long := strings.Repeat("plan taslagi ", 20)
	for _, short := range []string{"ok", "evet", "plan taslagi", "/compact"} {
		if relatedPaste(stripSpace(short), stripSpace(long)) {
			t.Errorf("%q was treated as a damaged render of a long message", short)
		}
		if relatedPaste(stripSpace(long), stripSpace(short)) {
			t.Errorf("a long line was treated as a damaged render of %q", short)
		}
	}
}

// --- the deadlock ---------------------------------------------------------

const stuckMessage = "[server-main] roadmap incelemesi: hedef sistemi bolumunu bugun bitirelim, hiyerarsik atama kismi eksik"

func TestSendFinishesItsOwnHangingPaste(t *testing.T) {
	// The incident: an earlier paste's Enter never registered and the message hung
	// in the composer, which every later delivery read as "the agent is busy".
	// The text is OURS and whole, so it is finished with Enter — never pasted
	// again, never queued behind itself.
	h := &sendHarness{
		captures: []string{
			claudePane("❯ " + stuckMessage), // pre-send gate: exactly our message
			claudePane(emptyRow),            // Enter submitted it
		},
		activities: []string{"target\t900\n", "target\t900\n"},
	}
	finished, err := testClient(h).SendWithPending(context.Background(), "target", stuckMessage, nil)
	if err != nil {
		t.Fatalf("err=%v, want a normal delivery", err)
	}
	if got := countEnter(h.mutations); got != 1 {
		t.Fatalf("expected exactly 1 Enter, got %d: %v", got, h.mutations)
	}
	if countInjections(h.mutations) != 0 {
		t.Fatalf("the hanging text was pasted a second time: %v", h.mutations)
	}
	if len(finished) != 0 {
		t.Fatalf("finished=%v, want empty (the message was our own, not a queue record)", finished)
	}
	assertNoEscape(t, h.mutations)
}

func TestSendFinishesAQueuedMessageHangingInTheComposer(t *testing.T) {
	// Same state, but the hanging text is an EARLIER queued message, not the one
	// we are sending. It is submitted (it was meant for this agent), its queue
	// record is reported back so the caller can close it — the missing step that
	// let a hand-delivered message be pasted twice — and our own message then goes
	// in normally.
	queued := "[ada] " + strings.Repeat("onceki kuyruk mesaji ", 6)
	h := &sendHarness{
		captures: []string{
			claudePane("❯ " + queued), // gate: a pending record, whole
			claudePane(emptyRow),      // Enter submitted it
			claudePane(emptyRow),      // readyToSend pass 1 (capture is stale after keys)
			claudePane(emptyRow),      // readyToSend pass 2
			claudePane("❯ " + stuckMessage),
			claudePane(emptyRow),
		},
		activities: []string{
			"target\t900\n", "target\t900\n", "target\t900\n",
			"target\t900\n", "target\t900\n", "target\t900\n",
		},
	}
	finished, err := testClient(h).SendWithPending(context.Background(), "target", stuckMessage, []string{queued})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(finished) != 1 || finished[0] != queued {
		t.Fatalf("finished=%v, want the queued text so its record gets closed", finished)
	}
	if got := countEnter(h.mutations); got != 2 {
		t.Fatalf("expected 2 Enter presses (the hanging one, then ours), got %d: %v", got, h.mutations)
	}
	if countInjections(h.mutations) != 1 {
		t.Fatalf("expected our message pasted once, got: %v", h.mutations)
	}
	assertNoEscape(t, h.mutations)
}

func TestSendClearsAndRepastesADamagedHangingPaste(t *testing.T) {
	// The dangerous state the operator measured: the hanging text is OURS but
	// starts mid-word, ~200 leading characters missing. Pressing Enter here
	// delivers a silently truncated message, so Enter is never pressed: the
	// composer is cleared with C-u and the whole message is pasted again.
	truncated := stuckMessage[55:]
	h := &sendHarness{
		captures: []string{
			claudePane("❯ " + truncated),    // gate: ours, damaged
			claudePane(emptyRow),            // after C-u: clean
			claudePane(emptyRow),            // readyToSend pass 1
			claudePane(emptyRow),            // readyToSend pass 2
			claudePane("❯ " + stuckMessage), // the re-paste landed whole -> Enter
			claudePane(emptyRow),            // cleared -> verified
		},
		activities: []string{
			"target\t900\n", "target\t900\n", "target\t900\n",
			"target\t900\n", "target\t900\n",
		},
	}
	if _, err := testClient(h).SendWithPending(context.Background(), "target", stuckMessage, nil); err != nil {
		t.Fatalf("err=%v, want the re-pasted message to be delivered", err)
	}
	joined := strings.Join(h.mutations, "\n")
	clear, enter := strings.Index(joined, "C-u"), strings.Index(joined, "Enter")
	if clear < 0 {
		t.Fatalf("the damaged text was not cleared: %v", h.mutations)
	}
	if enter < 0 || enter < clear {
		t.Fatalf("Enter was pressed on the damaged text: %v", h.mutations)
	}
	if got := countEnter(h.mutations); got != 1 {
		t.Fatalf("expected exactly 1 Enter (after the re-paste), got %d: %v", got, h.mutations)
	}
	if countInjections(h.mutations) != 1 {
		t.Fatalf("expected exactly 1 re-paste, got: %v", h.mutations)
	}
	assertNoEscape(t, h.mutations)
}

func TestSendQueuesForeignComposerWithoutTouchingIt(t *testing.T) {
	// Someone else's half-written line: not one key may be sent, and the caller
	// must be told to queue exactly as before.
	h := &sendHarness{
		captures:   []string{claudePane("❯ kendi yarim kalan sorum burada duruyor")},
		activities: []string{"target\t900\n"},
	}
	_, err := testClient(h).SendWithPending(context.Background(), "target", stuckMessage, nil)
	if !errors.Is(err, ErrTyping) {
		t.Fatalf("err=%v, want ErrTyping (queue it)", err)
	}
	if len(h.mutations) != 0 {
		t.Fatalf("a foreign composer was touched: %v", h.mutations)
	}
}

func TestSendLeavesTheComposerAloneWhileSomeoneIsTyping(t *testing.T) {
	// The composer holds exactly our message, but an attached client pressed a key
	// moments ago. Ownership is not enough: while a human is at the keyboard,
	// nothing is pressed.
	h := &sendHarness{
		captures:   []string{claudePane("❯ " + stuckMessage)},
		activities: []string{"target\t1000\n"},
	}
	_, err := testClient(h).SendWithPending(context.Background(), "target", stuckMessage, nil)
	if !errors.Is(err, ErrTyping) {
		t.Fatalf("err=%v, want ErrTyping", err)
	}
	if len(h.mutations) != 0 {
		t.Fatalf("keys were sent while a client was active: %v", h.mutations)
	}
}

func TestSendDoesNotTouchAHangingPasteOnABusyPane(t *testing.T) {
	busy := "✻ Working… (23s · Esc to interrupt)\n" + claudePane("❯ "+stuckMessage)
	h := &sendHarness{
		captures:   []string{busy},
		activities: []string{},
	}
	_, err := testClient(h).SendWithPending(context.Background(), "target", stuckMessage, nil)
	// ErrBusy, not ErrTyping (2026-08-15): a working pane is now refused by NAME
	// before anything is pasted, so the caller can say "pane calisiyor" instead of
	// blaming a composer. Both are queueing outcomes, so nothing downstream
	// changes; only the reason the operator is shown does.
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err=%v, want ErrBusy", err)
	}
	if len(h.mutations) != 0 {
		t.Fatalf("keys were sent into a working pane: %v", h.mutations)
	}
}

// --- integrity of our own paste ------------------------------------------

func TestSendQueuesWhenItsOwnPasteLandsMangledTwice(t *testing.T) {
	// The paste arrives damaged. It is cleared and pasted once more; the second
	// attempt is still damaged, so nothing is submitted and the caller is told the
	// message did NOT land (ErrNotReady -> queued), rather than delivering a
	// mangled message.
	mangled := stuckMessage[:45] + stuckMessage[80:]
	h := &sendHarness{
		captures: []string{
			claudePane(emptyRow),       // gate: empty, reused as readyToSend pass 1
			claudePane(emptyRow),       // readyToSend pass 2
			claudePane("❯ " + mangled), // post-paste: damaged -> repair
			claudePane(emptyRow),       // after C-u
			claudePane("❯ " + mangled), // the re-paste is damaged too
			claudePane("❯ " + mangled), // still-frame check: same calm screen -> the verdict is proof
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	}
	_, err := testClient(h).SendWithPending(context.Background(), "target", stuckMessage, nil)
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("err=%v, want ErrNotReady so the message is queued", err)
	}
	if got := countEnter(h.mutations); got != 0 {
		t.Fatalf("Enter was pressed on a mangled paste: %v", h.mutations)
	}
	if countInjections(h.mutations) != 2 {
		t.Fatalf("expected the one repair re-paste, got: %v", h.mutations)
	}
	assertNoEscape(t, h.mutations)
}

// --- a moving pane never produces proof ------------------------------------

// busyPane wraps composer rows in a pane that is mid-turn, the way a live
// Claude Code footer renders it while a turn runs.
func busyPane(rows ...string) string {
	return "✻ Working… (23s · Esc to interrupt)\n" + claudePane(rows...)
}

func TestSendRefusesToPasteIntoAWorkingPane(t *testing.T) {
	// The state that started q163159804 (2026-08-15): the composer is EMPTY and
	// the agent is mid-turn. The old path let that through — the stuck-paste gate
	// only skipped itself and readyToSend never asks about Busy — so the message
	// was pasted into a running turn and then "verified" against a redrawing
	// screen. Nothing may be pressed or pasted here; the caller queues.
	h := &sendHarness{captures: []string{busyPane(emptyRow)}}
	_, err := testClient(h).SendWithPending(context.Background(), "target", stuckMessage, nil)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err=%v, want ErrBusy", err)
	}
	if len(h.mutations) != 0 {
		t.Fatalf("keys or paste went into a working pane: %v", h.mutations)
	}
}

func TestSendDowngradesAVerdictReadOffAMovingPane(t *testing.T) {
	// A "foreign composer" verdict is only proof when the screen it was read from
	// is still there when we look again. A torn frame from a redrawing pane must
	// become ErrUnverified — the caller must NOT paste the message a second time
	// on the strength of it, which is exactly how one message was delivered three
	// times.
	foreign := claudePane("❯ /rename wor")
	for _, tc := range []struct {
		name  string
		again string
		want  error
	}{
		{"composer changed under the verdict", claudePane(emptyRow), ErrUnverified},
		{"pane is working", busyPane("❯ /rename wor"), ErrUnverified},
		{"same still screen", foreign, ErrNotReady},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &sendHarness{
				captures: []string{
					claudePane(emptyRow), // gate / readyToSend pass 1
					claudePane(emptyRow), // readyToSend pass 2
					foreign,              // post-paste verdict frame
					tc.again,             // the still-frame check
				},
				activities: []string{"target\t900\n", "target\t900\n", "target\t900\n"},
			}
			_, err := testClient(h).SendWithPending(context.Background(), "target", stuckMessage, nil)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v, want %v", err, tc.want)
			}
			if got := countEnter(h.mutations); got != 0 {
				t.Fatalf("Enter was pressed on foreign text: %v", h.mutations)
			}
			if countInjections(h.mutations) != 1 {
				t.Fatalf("message was re-injected: %v", h.mutations)
			}
			assertNoEscape(t, h.mutations)
		})
	}
}

func TestSendSubmitsWhenTheRepairedPasteMatches(t *testing.T) {
	mangled := stuckMessage[:45] + stuckMessage[80:]
	h := &sendHarness{
		captures: []string{
			claudePane(emptyRow),            // gate / readyToSend pass 1
			claudePane(emptyRow),            // readyToSend pass 2
			claudePane("❯ " + mangled),      // post-paste: damaged -> repair
			claudePane(emptyRow),            // after C-u
			claudePane("❯ " + stuckMessage), // re-paste is whole -> Enter
			claudePane(emptyRow),            // cleared -> verified
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	}
	if _, err := testClient(h).SendWithPending(context.Background(), "target", stuckMessage, nil); err != nil {
		t.Fatalf("err=%v, want a verified delivery", err)
	}
	if got := countEnter(h.mutations); got != 1 {
		t.Fatalf("expected exactly 1 Enter, got %d: %v", got, h.mutations)
	}
	if countInjections(h.mutations) != 2 {
		t.Fatalf("expected paste + one repair paste, got: %v", h.mutations)
	}
	assertNoEscape(t, h.mutations)
}

func TestSendReportsFailureWhenItsPasteIsReplacedByForeignText(t *testing.T) {
	// Not damage — the box holds something with no relation to our message. That
	// is the 2026-08-01 state: the paste never landed. Nothing is cleared (the
	// text is not ours) and nothing is submitted.
	h := &sendHarness{
		captures: []string{
			claudePane(emptyRow),
			claudePane(emptyRow),
			claudePane("❯ /rename wor"), // 12 unrelated characters
			claudePane("❯ /rename wor"), // still-frame check: unchanged, so the verdict stands
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n"},
	}
	_, err := testClient(h).SendWithPending(context.Background(), "target", stuckMessage, nil)
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("err=%v, want ErrNotReady", err)
	}
	if got := countEnter(h.mutations); got != 0 {
		t.Fatalf("Enter was pressed on foreign text: %v", h.mutations)
	}
	if got := countKey(h.mutations, "C-u"); got != 0 {
		t.Fatalf("foreign text was erased: %v", h.mutations)
	}
	if countInjections(h.mutations) != 1 {
		t.Fatalf("message was re-injected: %v", h.mutations)
	}
}

func TestSendTreatsAScreenFillingBoxAsUnreadableRatherThanDamaged(t *testing.T) {
	// A composer that has consumed the whole screen (no transcript row left above
	// its top border) has no room to grow, so the TUI is SCROLLING inside it: the
	// rows we can read are a window, not the content. Reading that as damage would
	// clear, re-paste and queue the same message forever, so the box is treated as
	// unreadable and the existing single-row logic decides — here, an Enter that
	// submits.
	rows := []string{"❯ " + stuckMessage[:20]}
	for i := 0; i < 6; i++ {
		rows = append(rows, "  devam satiri "+strings.Repeat("x", 20))
	}
	h := &sendHarness{
		captures: []string{
			claudePane(emptyRow),
			claudePane(emptyRow),
			screenFillingPane(rows...), // top border at row 0: a scrolling window
			claudePane(emptyRow),
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	}
	if _, err := testClient(h).SendWithPending(context.Background(), "target", stuckMessage, nil); err != nil {
		t.Fatalf("err=%v, want the message submitted rather than repaired forever", err)
	}
	if got := countKey(h.mutations, "C-u"); got != 0 {
		t.Fatalf("a scrolling box was treated as damage and cleared: %v", h.mutations)
	}
	if got := countEnter(h.mutations); got != 1 {
		t.Fatalf("expected 1 Enter, got %d: %v", got, h.mutations)
	}
}

func TestSendVerifiesATallWrappedPasteMeasuredOnLivePanes(t *testing.T) {
	// Two live panes (compec-outreach, probot-fon, 2026-08-11) were holding 14-row
	// composer boxes with the transcript still visible above them. That is a
	// COMPLETE view, so a 14-row render of our own message must verify as intact —
	// an earlier row-count guard threw the box away at 8 rows and disabled the
	// integrity check on exactly these pastes.
	long := strings.Repeat("bizim kendi mesajimizin bir satiri daha ", 14)
	rows := []string{"❯ " + long[:40]}
	for i := 40; i < len(long); i += 40 {
		end := i + 40
		if end > len(long) {
			end = len(long)
		}
		rows = append(rows, "  "+long[i:end])
	}
	if len(rows) < 14 {
		t.Fatalf("fixture only built %d rows", len(rows))
	}
	h := &sendHarness{
		captures: []string{
			claudePane(emptyRow),
			claudePane(emptyRow),
			claudePane(rows...), // the whole message, wrapped across 14 rows
			claudePane(emptyRow),
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	}
	if _, err := testClient(h).SendWithPending(context.Background(), "target", long, nil); err != nil {
		t.Fatalf("err=%v, want a verified delivery", err)
	}
	if got := countKey(h.mutations, "C-u"); got != 0 {
		t.Fatalf("an intact 14-row paste was cleared: %v", h.mutations)
	}
	if got := countEnter(h.mutations); got != 1 {
		t.Fatalf("expected 1 Enter, got %d: %v", got, h.mutations)
	}
}

// --- clearing -------------------------------------------------------------

// assertNoEscape is the standing rule, checked over a whole mutation log: bp may
// never send Escape into an agent pane. On a mid-turn agent it cancels the turn
// and destroys running work, and Busy detection has been wrong in both
// directions before.
func assertNoEscape(t *testing.T, mutations []string) {
	t.Helper()
	for _, m := range mutations {
		if strings.Contains(m, "Escape") {
			t.Fatalf("Escape was sent to a pane: %v", mutations)
		}
	}
}

func TestClearComposerPressesCtrlUAndNeverEscape(t *testing.T) {
	h := &sendHarness{
		captures:   []string{claudePane(emptyRow), claudePane(emptyRow)},
		activities: []string{},
	}
	if err := testClient(h).ClearComposer(context.Background(), "target"); err != nil {
		t.Fatalf("err=%v", err)
	}
	if got := countKey(h.mutations, "C-u"); got != 1 {
		t.Fatalf("expected 1 C-u, got %d: %v", got, h.mutations)
	}
	assertNoEscape(t, h.mutations)
}

func TestClearComposerRefusesVisibleTextWithoutPressingAnything(t *testing.T) {
	h := &sendHarness{
		captures:   []string{claudePane("❯ yarim kalan bir soru")},
		activities: []string{},
	}
	err := testClient(h).ClearComposer(context.Background(), "target")
	if !errors.Is(err, ErrTyping) {
		t.Fatalf("err=%v, want ErrTyping", err)
	}
	if len(h.mutations) != 0 {
		t.Fatalf("keys were sent at a composer holding text: %v", h.mutations)
	}
}

func TestClearComposerRefusesABusyPane(t *testing.T) {
	h := &sendHarness{
		captures:   []string{"✻ Working… (23s · Esc to interrupt)\n" + claudePane(emptyRow)},
		activities: []string{},
	}
	if err := testClient(h).ClearComposer(context.Background(), "target"); !errors.Is(err, ErrBusy) {
		t.Fatalf("err=%v, want ErrBusy", err)
	}
	if len(h.mutations) != 0 {
		t.Fatalf("keys were sent into a working pane: %v", h.mutations)
	}
}

func TestClearWithCtrlURepeatsUntilTheBoxIsEmpty(t *testing.T) {
	// A single C-u clears one line; multi-line content needs it repeated (the
	// operator measured 6-8). Each press is followed by a fresh capture, and the
	// loop stops the moment the box reads empty.
	long := strings.Repeat("bizim kendi mesajimizin satiri ", 6)
	h := &sendHarness{
		captures: []string{
			claudePane("❯ "+long[:120], "  "+long[120:]),
			claudePane("❯ " + long[:120]),
			claudePane("❯ " + long[:60]),
			claudePane(emptyRow),
		},
		activities: []string{},
	}
	if err := testClient(h).clearWithCtrlU(context.Background(), "target", []string{long}); err != nil {
		t.Fatalf("err=%v", err)
	}
	if got := countKey(h.mutations, "C-u"); got != 4 {
		t.Fatalf("expected 4 C-u presses, got %d: %v", got, h.mutations)
	}
	assertNoEscape(t, h.mutations)
}

func TestClearWithCtrlUStopsAtForeignText(t *testing.T) {
	long := strings.Repeat("bizim kendi mesajimizin satiri ", 6)
	h := &sendHarness{
		captures: []string{
			claudePane("❯ " + long[:100]),
			claudePane("❯ birisi simdi yazmaya baslamis"), // not ours -> stop
		},
		activities: []string{},
	}
	err := testClient(h).clearWithCtrlU(context.Background(), "target", []string{long})
	if !errors.Is(err, ErrTyping) {
		t.Fatalf("err=%v, want ErrTyping", err)
	}
	if got := countKey(h.mutations, "C-u"); got != 2 {
		t.Fatalf("expected the loop to stop after the foreign text appeared, got %d presses: %v", got, h.mutations)
	}
	assertNoEscape(t, h.mutations)
}

func TestClearWithCtrlUIsBounded(t *testing.T) {
	long := strings.Repeat("bizim kendi mesajimizin satiri ", 6)
	captures := make([]string, composerClearAttempts+2)
	for i := range captures {
		captures[i] = claudePane("❯ " + long)
	}
	h := &sendHarness{captures: captures}
	if err := testClient(h).clearWithCtrlU(context.Background(), "target", []string{long}); !errors.Is(err, ErrTyping) {
		t.Fatalf("err=%v, want ErrTyping at the bound", err)
	}
	if got := countKey(h.mutations, "C-u"); got != composerClearAttempts {
		t.Fatalf("expected %d C-u presses at the bound, got %d", composerClearAttempts, got)
	}
}

func TestClearDeliveredNeedsProofBeforeErasingAnything(t *testing.T) {
	delivered := stuckMessage
	cases := []struct {
		name    string
		pane    string
		cleared bool
		presses int
	}{
		{"holds the delivered text", claudePane("❯ " + delivered), true, 1},
		{"holds a damaged copy of it", claudePane("❯ " + delivered[55:]), true, 1},
		{"holds someone else's text", claudePane("❯ bambaska bir soru yaziyorum"), false, 0},
		{"holds an unreadable chip", claudePane("❯ [Pasted text #1 +12 lines]"), false, 0},
		{"already empty", claudePane(emptyRow), false, 0},
	}
	for _, tc := range cases {
		h := &sendHarness{captures: []string{tc.pane, claudePane(emptyRow)}}
		cleared, err := testClient(h).ClearDelivered(context.Background(), "target", []string{delivered})
		if err != nil {
			t.Errorf("%s: err=%v", tc.name, err)
			continue
		}
		if cleared != tc.cleared {
			t.Errorf("%s: cleared=%v, want %v", tc.name, cleared, tc.cleared)
		}
		if got := countKey(h.mutations, "C-u"); got != tc.presses {
			t.Errorf("%s: %d C-u presses, want %d: %v", tc.name, got, tc.presses, h.mutations)
		}
		if got := countEnter(h.mutations); got != 0 {
			t.Errorf("%s: Enter was pressed on already-delivered text: %v", tc.name, h.mutations)
		}
		assertNoEscape(t, h.mutations)
	}
}

func TestClearDeliveredRefusesAWorkingPane(t *testing.T) {
	h := &sendHarness{captures: []string{"✻ Working… (23s · Esc to interrupt)\n" + claudePane("❯ "+stuckMessage)}}
	if _, err := testClient(h).ClearDelivered(context.Background(), "target", []string{stuckMessage}); !errors.Is(err, ErrBusy) {
		t.Fatalf("err=%v, want ErrBusy", err)
	}
	if len(h.mutations) != 0 {
		t.Fatalf("keys were sent into a working pane: %v", h.mutations)
	}
}

func TestComposerBlockReasonNamesWhatAHumanMustFix(t *testing.T) {
	cases := []struct {
		name string
		pane string
		want string
	}{
		{"empty", claudePane(emptyRow), ""},
		{"our own hanging paste is not a block", claudePane("❯ " + stuckMessage), ""},
		{"our damaged paste is not a block", claudePane("❯ " + stuckMessage[55:]), ""},
		{"chip", claudePane("❯ [Pasted text #1 +12 lines]"), BlockedByPasteChip},
		{"codex chip", claudePane("❯ [Pasted Content 1024 chars]"), BlockedByPasteChip},
		{"foreign line", claudePane("❯ kendi yarim kalan sorum burada duruyor"), BlockedByForeignText},
		{"short fragment", claudePane("❯ /rename wor"), BlockedByForeignText},
		{"busy", "✻ Working… (23s · Esc to interrupt)\n" + claudePane(emptyRow), BlockedByBusyPane},
	}
	for _, tc := range cases {
		if got := ComposerBlockReason(tc.pane, []string{stuckMessage}); got != tc.want {
			t.Errorf("%s: reason=%q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestComposerBoxAgainstLiveCaptures is the check that fixtures cannot perform,
// and its absence is why a dead decision table nearly shipped: composerBox
// returned ok=false on all 29 live panes while every fixture passed, because the
// fixtures encoded a structure (two pure borders) that no real pane draws.
//
// It is skipped unless PANEDIR points at a directory of pane captures, so it never
// runs in an ordinary `go test ./...`. Produce the captures READ-ONLY, and delete
// them afterwards — they contain private conversations:
//
//	for s in $(tmux list-sessions -F '#{session_name}'); do
//	  tmux capture-pane -t "=$s:" -e -p > "$D/$s.ansi"
//	done
//	PANEDIR=$D go test ./internal/tmux/ -run TestComposerBoxAgainstLiveCaptures -v
//
// It prints STRUCTURE only — never pane text — and fails if the box cannot be read
// on a clear majority of panes, which is what a Claude Code UI change would look
// like. Re-run it after any agent-CLI upgrade.
func TestComposerBoxAgainstLiveCaptures(t *testing.T) {
	dir := os.Getenv("PANEDIR")
	if dir == "" {
		t.Skip("set PANEDIR to a directory of read-only pane captures")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	readable, total := 0, 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		pane := string(data)
		total++
		box, top, ok := composerBoxAt(pane)
		if ok {
			readable++
		}
		t.Logf("%-24s ok=%-5v rows=%-3d chars=%-5d scrolled=%-5v typing=%-5v filled=%-5v busy=%-5v reason=%q",
			entry.Name(), ok, composerBoxRows(box), len(stripSpace(box)), composerBoxScrolled(box, top),
			Typing(pane), composerFilled(pane), Busy(pane), ComposerBlockReason(pane, nil))
	}
	if total == 0 {
		t.Fatal("PANEDIR holds no captures")
	}
	if readable*2 <= total {
		t.Fatalf("composerBox read only %d of %d live panes: the pane structure has drifted and every "+
			"match/damaged/foreign decision is dead code again", readable, total)
	}
}

// --- forced delivery into a working pane -------------------------------------

func TestSendForceDeliversIntoAWorkingPane(t *testing.T) {
	// The one refusal force drops. The pane is mid-turn with an empty composer —
	// the state TestSendRefusesToPasteIntoAWorkingPane pins for the ordinary path —
	// and here the message goes in, is watched being held, and is submitted with a
	// single Enter. On a working Claude Code that Enter puts the message into the
	// CLI's own input queue, which is what the operator asked for: seen at the end
	// of this turn instead of after the next thirty-second dispatch tick.
	h := &sendHarness{
		captures: []string{
			busyPane(emptyRow),            // pre-send gate: working, empty composer
			busyPane(emptyRow),            // readyToSend pass 2
			busyPane("❯ " + stuckMessage), // post-paste: our text is in the box
			busyPane(emptyRow),            // Enter took it
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	}
	if err := testClient(h).SendForce(context.Background(), "target", stuckMessage); err != nil {
		t.Fatalf("err=%v, want the forced delivery to go through", err)
	}
	if got := countInjections(h.mutations); got != 1 {
		t.Fatalf("expected exactly 1 paste, got %d: %v", got, h.mutations)
	}
	if got := countEnter(h.mutations); got != 1 {
		t.Fatalf("expected exactly 1 Enter, got %d: %v", got, h.mutations)
	}
	assertNoEscape(t, h.mutations)
}

// The measured worst case on the WhatsApp side: single messages up to 15,084
// characters (a whole jury-day transcript in one piece), multi-line and full of
// Unicode. The paste must be ATOMIC — one load-buffer carrying every byte, one
// paste-buffer — because a payload split across writes is how a message goes out
// half-delivered.
func TestSendForceCarriesAJuryDaySizedPayloadWhole(t *testing.T) {
	piece := "Tuna'nin juri gunu konusmasi — çok satırlı bölüm №7:\nkarar: […] devam.\n"
	payload := strings.Repeat(piece, 1+15084/len(piece))[:15084]
	h := &sendHarness{
		captures: []string{
			busyPane(emptyRow), // pre-send gate: working, empty composer
			busyPane(emptyRow), // readyToSend pass 2
			busyPane(emptyRow), // post-paste frame does not show it: unverified
			busyPane(emptyRow),
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	}
	err := testClient(h).SendForce(context.Background(), "target", payload)
	if err != nil && !errors.Is(err, ErrUnverified) {
		t.Fatalf("err=%v, want nil or ErrUnverified", err)
	}
	if len(h.payloads) != 1 {
		t.Fatalf("expected exactly 1 load-buffer, got %d", len(h.payloads))
	}
	if got := string(h.payloads[0]); got != payload {
		t.Fatalf("payload arrived damaged: %d bytes of %d", len(got), len(payload))
	}
	if got := countInjections(h.mutations); got != 1 {
		t.Fatalf("expected exactly 1 paste, got %d: %v", got, h.mutations)
	}
}

func TestSendForceReportsUnverifiedWhenTheWorkingScreenCannotShowThePaste(t *testing.T) {
	// The expected shape of most forced deliveries: a redrawing pane hands back a
	// frame that does not show the paste, so nothing may be claimed. It must come
	// back UNVERIFIED — never "sent", and never the ErrNotReady that would make a
	// caller paste the message a second time (the q163159804 loop). The record
	// then waits for the transcript, which is the only witness a moving screen
	// leaves standing.
	h := &sendHarness{
		captures: []string{
			busyPane(emptyRow), // gate
			busyPane(emptyRow), // readyToSend pass 2
			busyPane(emptyRow), // post-paste: the frame shows nothing of ours
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n"},
	}
	err := testClient(h).SendForce(context.Background(), "target", stuckMessage)
	if !errors.Is(err, ErrUnverified) {
		t.Fatalf("err=%v, want ErrUnverified", err)
	}
	if errors.Is(err, ErrNotReady) {
		t.Fatal("a torn frame off a working pane was reported as proof of non-delivery")
	}
	if got := countInjections(h.mutations); got != 1 {
		t.Fatalf("expected exactly 1 paste, got %d: %v", got, h.mutations)
	}
	if got := countEnter(h.mutations); got != 0 {
		t.Fatalf("Enter was pressed on a composer nothing had been seen in: %v", h.mutations)
	}
}

func TestSendForceStillRefusesSomeoneElsesComposer(t *testing.T) {
	// Force overrides the agent's concentration, never a human's input. A working
	// pane whose composer holds somebody's half-written line is left exactly as it
	// is — no paste, no keys — and the caller queues.
	h := &sendHarness{
		captures:   []string{busyPane("❯ /rename wor")},
		activities: []string{"target\t900\n"},
	}
	err := testClient(h).SendForce(context.Background(), "target", stuckMessage)
	if !errors.Is(err, ErrTyping) {
		t.Fatalf("err=%v, want ErrTyping", err)
	}
	if len(h.mutations) != 0 {
		t.Fatalf("a forced message touched a composer holding foreign text: %v", h.mutations)
	}
}

func TestSendForceStillRefusesANonAgentPane(t *testing.T) {
	// The chokepoint guard is not a busy-pane rule and force does not reach it:
	// a session that dropped to a shell would receive the message at a root
	// prompt.
	h := &sendHarness{command: "zsh"}
	if err := testClient(h).SendForce(context.Background(), "target", stuckMessage); !errors.Is(err, ErrNotAgent) {
		t.Fatalf("err=%v, want ErrNotAgent", err)
	}
	if len(h.mutations) != 0 {
		t.Fatalf("keys went into a shell: %v", h.mutations)
	}
}

func TestSendForceStillRefusesAnExpiredLogin(t *testing.T) {
	// Nothing can be delivered to an agent that is not logged in, however urgent
	// the sender is. The verdict is a PROVEN non-delivery, so the queue keeps the
	// message and retries it — the one thing a forced record must not do is
	// disappear here.
	expired := busyPane(emptyRow) + "\x1b[2m● Login expired · Please run /login\x1b[0m\n"
	h := &sendHarness{
		captures:   []string{expired},
		activities: []string{"target\t900\n"},
	}
	err := testClient(h).SendForce(context.Background(), "target", stuckMessage)
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("err=%v, want ErrNotReady", err)
	}
	if len(h.mutations) != 0 {
		t.Fatalf("a forced message was typed at a login prompt: %v", h.mutations)
	}
}

// The trigger three days of hanging pastes came down to (measured 2026-08-23 in
// a lab pane, confirmed against probot-outreach's two failed batches): a message
// carrying emoji renders WRONG. "gorunmez" came back from the screen as
// "grunmez" — a double-width glyph shifts the TUI's column accounting and eats a
// character. The composer's own buffer is intact; only the drawing is lossy, so
// clearing and re-pasting produces the same loss again.
func TestWideRuneMessageIsOursNotDamaged(t *testing.T) {
	sent := "[bp] UYARI: komut satirina ✍️ / ✅ emojisi YAZMA — gorunmez Unicode tasiyor."
	// What the screen gave back: one letter short.
	rendered := strings.Replace(sent, "gorunmez", "grunmez", 1)
	pane := claudePane("❯ " + rendered)
	verdict, text := classifyPaste(pane, []string{sent})
	if verdict != pasteExact || text != sent {
		t.Fatalf("verdict=%v text=%q, want pasteExact — a lossy render of OUR text is not a damaged paste", verdict, text)
	}
	// A plain-ASCII message with the same kind of difference is still damaged:
	// there, re-pasting genuinely repairs a torn paste.
	plain := "[bp] UYARI: komut satirina emoji yazma, gorunmez Unicode tasiyor kardesim."
	plainPane := claudePane("❯ " + strings.Replace(plain, "gorunmez", "grunmez", 1))
	if verdict, _ := classifyPaste(plainPane, []string{plain}); verdict != pasteDamaged {
		t.Fatalf("verdict=%v, want pasteDamaged for a plain-text mismatch", verdict)
	}
	if hasWideRunes(plain) {
		t.Fatal("a plain ASCII message was called wide")
	}
	for _, wide := range []string{"✍️", "✅", "🚀", "日本語"} {
		if !hasWideRunes(wide) {
			t.Fatalf("%q was not recognised as wide", wide)
		}
	}
}
