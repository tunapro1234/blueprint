package tmux

import (
	"strings"
)

// This file holds the BOX view of a composer: the whole multi-line content of a
// TUI's input box, as opposed to composerContent()'s single rendered row.
//
// Why both exist. composerContent/Typing answer one narrow question — "is there
// text on the final prompt row" — and every gate in this package has been tuned
// against that answer for months (ghost text, NBSP padding, Codex prompts, panes
// with no box at all). Widening them in place would change the meaning of every
// existing decision. The box is therefore added as a RICHER, strictly optional
// view: it is used where a decision needs the FULL text (is this hanging paste
// exactly the message we queued, or a truncated version of it?), and every
// caller must fall back to the single-row behavior when the box cannot be read.
// Nothing is ever assumed about a pane whose structure we do not recognise.

// composerStatusMarkers are the signatures of the status/footer line a Claude
// Code pane draws directly BELOW its composer box: the vim mode indicator and
// the permission-mode footer. They anchor the box from below, the same way
// AuthExpired anchors the footer region.
//
// Matched with dim segments PRESERVED (only ANSI colour is stripped): the footer
// is usually dim-rendered and StripDim would delete the very line we look for.
var composerStatusMarkers = []string{"-- INSERT --", "-- NORMAL --", "bypass permissions"}

// composerBorderRunes are the two horizontal rules Claude Code draws its composer
// box with.
const composerBorderRunes = "─━"

// composerBorderMin is how many border characters a row must carry before this
// package will call it a composer border.
//
// Measured against all 29 live panes on this machine (read-only captures,
// 2026-08-11): every real border carries at least 46 border characters — the
// narrowest was a 63-column pane whose LABELLED top row had 46. The pure bottom
// borders ran 63-158. Six is therefore an order of magnitude below anything real,
// while still refusing prose: a line has to both begin and end with a rule AND
// carry six of them, which ordinary text and transcript prose do not.
const composerBorderMin = 6

// isComposerBoxBorder is the composer box's OWN border test, and the reason it
// exists is a bug that would have shipped as dead code.
//
// The shared isComposerBorder accepts only a pure run of ─/━. Live panes do not
// draw that on top: the TOP border carries the agent's name inside it —
//
//	────────────────────────────── probot-main ──
//
// — so on 26 of 29 live panes the top border was invisible to a pure-run test,
// composerBox found only ONE border above the footer and returned ok=false on
// EVERY pane, including the three that had text hanging in the composer. The
// entire match/damaged/foreign decision was unreachable.
//
// This test accepts a row that BEGINS and ENDS with a rule and carries at least
// composerBorderMin of them, with anything in between (the agent name, a counter,
// any future decoration). isComposerBorder itself is deliberately left alone:
// composerTrail, statusFooter and AuthExpired all depend on its exact meaning,
// and all three look for the PURE bottom border, which still matches.
//
// Only ANSI colour is stripped, never dim segments: a border may be dim-rendered
// (it is drawn in 38;5;244 on these panes, but that is a build detail), and
// StripDim would delete the very row being tested.
func isComposerBoxBorder(line string) bool {
	core := stripSpace(ansiSeq.ReplaceAllString(line, ""))
	runes := []rune(core)
	if len(runes) == 0 {
		return false
	}
	if !strings.ContainsRune(composerBorderRunes, runes[0]) || !strings.ContainsRune(composerBorderRunes, runes[len(runes)-1]) {
		return false
	}
	n := 0
	for _, r := range runes {
		if strings.ContainsRune(composerBorderRunes, r) {
			n++
		}
	}
	return n >= composerBorderMin
}

// composerDialogMarkers are the footer hints a MODAL picker draws instead of the
// composer's own status line (the /remote-control menu, a file picker, a
// permission prompt). While one is up the composer is not addressable at all:
// text goes into the menu, not the prompt. Measured live on compec-site
// (2026-08-11), where a picker was open: the "-- INSERT --" footer was gone and
// the bottom line read "Enter to select · Tab/Arrow keys to navigate · Esc to
// cancel". Such a pane must read as "no box", so every caller falls back to
// treating it as in use and nothing is ever typed into it.
var composerDialogMarkers = []string{"Enter to select", "Esc to cancel"}

// composerBoxMaxRows bounds the upward search for the box's top border. A real
// composer never grows anywhere near this tall; the bound exists so a stray
// ─── run somewhere in the transcript cannot be mistaken for a top border and
// turn transcript rows into "composer content".
const composerBoxMaxRows = 60

// composerBoxScrollRows is the interior height above which a box is assumed to be
// SCROLLING — showing a window into its content rather than all of it.
//
// The number is deliberately high, and the reason is measured: two live panes
// (compec-outreach, probot-fon, 2026-08-11) were holding 14-row composer boxes
// with the transcript still visible above them, so 14 rows is plainly not
// evidence of a scroll. An earlier guess of 8 would have thrown the box away on
// exactly the multi-line pastes this change exists for.
//
// Erring high here is the DANGEROUS direction (a scrolled box read as damaged
// gets cleared, re-pasted, and queued forever), so it is paired with the
// structural signal in composerBoxScrolled, which does not depend on a number at
// all.
const composerBoxScrollRows = 30

// composerBoxScrolled reports whether the box may be showing only part of its
// content, in which case a SHORT read is not evidence of damage.
//
// Two signals, either one enough:
//   - the box starts at the very top of the capture (top <= 1): it has consumed
//     the screen, so there is no room left for it to grow and the TUI must be
//     scrolling inside it;
//   - an implausible interior height (composerBoxScrollRows).
func composerBoxScrolled(box string, top int) bool {
	return top <= 1 || composerBoxRows(box) >= composerBoxScrollRows
}

// isComposerStatus reports whether the line is the status/footer row a Claude
// Code pane draws under its composer box.
func isComposerStatus(line string) bool {
	clean := ansiSeq.ReplaceAllString(line, "")
	for _, marker := range composerStatusMarkers {
		if strings.Contains(clean, marker) {
			return true
		}
	}
	return false
}

// composerBox returns the FULL rendered content of the composer box: every row
// strictly between the box's two border rows, with the prompt marker stripped
// from the first row and dim/ANSI removed, interior blank rows preserved, joined
// by newlines.
//
// The live structure it is written against (verified on a real pane, 2026-08-11):
//
//	───────────────────────────      <- top border
//	❯ first row, carries the marker
//	  continuation row, no marker
//	                                 <- blank rows occur INSIDE the box
//	  another row
//	───────────────────────────      <- bottom border
//	  -- INSERT -- ⏵⏵ bypass permissions on (shift+tab to cycle)
//	  ⧉  session-name
//
// The top border normally carries the agent's name inside it (see
// isComposerBoxBorder) and the bottom one is a pure rule.
//
// ok is false whenever that structure is not found, and the caller must then
// fall back to the single-row composerContent behavior — never to an assumption:
//
//   - no status/footer line (a Codex pane, whose footer reads "gpt-5.6-sol low ·
//     /tmp", or an unfamiliar build);
//   - a modal picker is open (composerDialogMarkers): the composer is not
//     addressable and nothing may be typed into that pane at all;
//   - only ONE border above the status line;
//   - no prompt marker on the first interior row, or a marker on a later one —
//     continuation rows never carry one, so that is not a composer box;
//   - a top border further than composerBoxMaxRows above the bottom one.
func composerBox(pane string) (string, bool) {
	box, _, ok := composerBoxAt(pane)
	return box, ok
}

// composerBoxAt is composerBox plus the row index of the box's TOP border, which
// is what tells a complete view apart from a scrolling one (composerBoxScrolled).
//
// Two readers, tried in order. The Claude one (claudeComposerBoxAt) is unchanged
// and still decides for every pane it recognises. Only when it finds nothing does
// the Hermes reader get a look, and that one refuses outright unless the screen
// is provably Hermes — so no existing verdict about a Claude or Codex pane can
// change shape here. Hermes needs its own reader because it anchors its box the
// other way round (status row ABOVE, no "-- INSERT --" footer at all); see
// hermesComposerBox.
func composerBoxAt(pane string) (string, int, bool) {
	if box, top, ok := claudeComposerBoxAt(pane); ok {
		return box, top, true
	}
	// Codex before Hermes, and both only on their own proven screens: each
	// reader refuses outright unless the capture shows its TUI, so adding one
	// cannot change any verdict about the other two.
	if box, top, ok := codexComposerBox(pane); ok {
		return box, top, true
	}
	return hermesComposerBox(pane)
}

func claudeComposerBoxAt(pane string) (string, int, bool) {
	lines := strings.Split(pane, "\n")
	status := -1
	for i, line := range lines {
		if isComposerStatus(line) {
			status = i // the LAST match: the live footer, never a transcript quote
		}
	}
	if status < 0 {
		return "", -1, false
	}
	// A modal picker anywhere in the footer region means the composer is not the
	// thing on screen: no box, and nothing may be typed into this pane.
	for _, line := range lines[status:] {
		clean := ansiSeq.ReplaceAllString(line, "")
		for _, marker := range composerDialogMarkers {
			if strings.Contains(clean, marker) {
				return "", -1, false
			}
		}
	}
	// Walk up from the status line: blank rows may separate it from the border.
	i := status - 1
	for i >= 0 && stripSpace(StripDim(lines[i])) == "" {
		i--
	}
	if i < 0 || !isComposerBoxBorder(lines[i]) {
		return "", -1, false
	}
	bottom, top := i, -1
	for j := bottom - 1; j >= 0 && bottom-j <= composerBoxMaxRows; j-- {
		if isComposerBoxBorder(lines[j]) {
			top = j
			break
		}
	}
	if top < 0 {
		return "", -1, false
	}
	rows := lines[top+1 : bottom]
	if len(rows) == 0 || !promptLine.MatchString(rows[0]) {
		return "", -1, false
	}
	out := make([]string, 0, len(rows))
	for k, row := range rows {
		clean := StripDim(row)
		if k == 0 {
			clean = promptLine.ReplaceAllString(clean, "")
		} else if promptLine.MatchString(row) {
			// A second prompt marker means these rows are not one box.
			return "", -1, false
		}
		out = append(out, strings.TrimRight(clean, " \t\u00a0"))
	}
	return strings.Join(out, "\n"), top, true
}

// composerBoxText returns the whitespace-stripped full box content. Whitespace
// is removed rather than collapsed, exactly as everywhere else in this package,
// so a legitimate WRAP (the same text rendered across several rows, with the
// continuation indent) compares equal to the message that produced it.
func composerBoxText(pane string) (string, bool) {
	box, ok := composerBox(pane)
	if !ok {
		return "", false
	}
	return stripSpace(box), true
}

// composerJudgeText returns the box content only while the box is a COMPLETE
// view of the composer: readable, and short enough that it cannot have scrolled.
// It is what a verdict about whose text this is may be based on. A tall box still
// tells us plenty (see classifyPaste, which uses the full box to recognise our
// own hanging paste), but it must never be the ground for declaring a composer
// FOREIGN — the rows we cannot see could hold the rest of our own message.
func composerJudgeText(pane string) (string, bool) {
	box, top, ok := composerBoxAt(pane)
	if !ok || composerBoxScrolled(box, top) {
		return "", false
	}
	return stripSpace(box), true
}

// composerBoxRows counts the interior rows of the box, for the scroll guard.
func composerBoxRows(box string) int {
	if box == "" {
		return 0
	}
	return strings.Count(box, "\n") + 1
}

// pasteVerdict says whose text a NON-EMPTY composer is holding, judged against
// the messages bp itself is responsible for.
type pasteVerdict int

const (
	// pasteForeign: not ours, or not readable as ours. Someone's half-written
	// line, an unfamiliar UI, or a paste chip whose content cannot be read. The
	// conservative verdict, and the default whenever anything is in doubt: the
	// caller must leave the composer completely alone.
	pasteForeign pasteVerdict = iota
	// pasteExact: the box holds one of our messages, whole. This is OUR OWN
	// unsubmitted paste — the state the 2026-08-01..08-11 deadlock left behind,
	// where bp read its own hanging text as "the agent is busy" and queued
	// behind itself for four days. It may be finished with Enter.
	pasteExact
	// pasteDamaged: the box holds a MUTILATED version of one of our messages
	// (truncated, or sharing a long prefix/suffix). Pressing Enter here is how a
	// silently truncated message gets delivered — the operator measured a
	// re-paste that began mid-word with ~200 leading characters missing. It must
	// be cleared and re-pasted, never submitted.
	pasteDamaged
)

// pasteRelatedMin is the number of (whitespace-stripped) characters two texts
// must share before "related" means anything. Below it, an overlap is
// coincidence: a human's "ok" is a substring of half the messages on this fleet,
// and treating it as our damaged paste would erase what they were writing. Short
// messages (`/compact`) therefore only ever match EXACTLY, which is the correct
// and safe behavior for them.
const pasteRelatedMin = 32

// classifyPaste judges a non-empty composer against the texts bp is responsible
// for (the message about to be sent, plus any queue records pending for this
// target) and returns the matching text.
//
// Order matters: an exact match is looked for across ALL candidates before any
// damage test runs, so a message that is a clean substring of another queued one
// is never reported as damage.
func classifyPaste(pane string, texts []string) (pasteVerdict, string) {
	// A paste chip stands IN PLACE of the text, so the composer's content cannot
	// be read at all. Right after our own paste that is evidence of ours
	// (classifyComposer treats it so), but at the PRE-SEND gate it proves
	// nothing: it may be a human's own large paste, and pressing Enter on that
	// would submit someone else's work. Foreign, deliberately.
	if codexPasteChip(pane) || claudePasteChip(pane) {
		return pasteForeign, ""
	}
	got, ok := composerBoxText(pane)
	if !ok || got == "" {
		return pasteForeign, ""
	}
	for _, text := range texts {
		if stripSpace(text) == got {
			return pasteExact, text
		}
	}
	for _, text := range texts {
		want := stripSpace(text)
		if !relatedPaste(got, want) {
			continue
		}
		// A near-match on a message carrying WIDE characters is not a damaged
		// paste — it is a lossy RENDER. Measured 2026-08-23: a message with
		// "✍️ / ✅" in it came back from the screen with a letter missing
		// ("gorunmez" -> "grunmez"), because a double-width glyph shifts the
		// TUI's column accounting and a character is overwritten. The bytes in
		// the composer's own buffer are fine — the agents that received these
		// messages quoted them back correctly — so clearing and re-pasting
		// cannot help: the second render loses a character too, which is
		// exactly what bp did until now (probot-outreach lost most of two
		// seven-pane batches to this, and both batches happened to be the
		// message that warned about emoji).
		//
		// So: if the difference could be explained by the wide characters, the
		// composer is treated as holding OUR text. Only then — a near-match on
		// a plain-ASCII message stays "damaged", where re-pasting really can
		// fix a torn paste.
		if hasWideRunes(text) {
			return pasteExact, text
		}
		return pasteDamaged, text
	}
	return pasteForeign, ""
}

// relatedPaste reports whether got looks like a MUTILATED render of want: one
// contains the other, or they share a long common prefix or suffix. Both sides
// must carry at least pasteRelatedMin characters, so a short line can never be
// dragged into a damage verdict by a coincidental overlap.
// hasWideRunes reports whether a text carries characters a terminal may render
// wider than one column — emoji, variation selectors, CJK. Their width is where
// screen text and sent text stop being comparable character by character.
func hasWideRunes(text string) bool {
	for _, r := range text {
		switch {
		case r == 0xFE0F || r == 0x200D: // variation selector-16, ZWJ
			return true
		case r >= 0x1F000 && r <= 0x1FAFF: // emoji planes
			return true
		case r >= 0x2600 && r <= 0x27BF: // misc symbols and dingbats (✍ ✅ ❯)
			return true
		case r >= 0x1100 && r <= 0x11FF, r >= 0x2E80 && r <= 0xA4CF: // CJK ranges
			return true
		case r >= 0xAC00 && r <= 0xD7A3, r >= 0xF900 && r <= 0xFAFF:
			return true
		}
	}
	return false
}

func relatedPaste(got, want string) bool {
	g, w := []rune(got), []rune(want)
	if len(g) < pasteRelatedMin || len(w) < pasteRelatedMin {
		return false
	}
	if strings.Contains(want, got) || strings.Contains(got, want) {
		return true
	}
	return commonPrefix(g, w) >= pasteRelatedMin || commonSuffix(g, w) >= pasteRelatedMin
}

func commonPrefix(a, b []rune) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

func commonSuffix(a, b []rune) int {
	n := 0
	for n < len(a) && n < len(b) && a[len(a)-1-n] == b[len(b)-1-n] {
		n++
	}
	return n
}

// The reasons a pane cannot take a message right now, phrased for the operator
// rather than for the log: each one names the state AND what to do about it.
// They travel with the queue record so `bp q`/`bp qstat` can say WHY a message
// is still waiting. Silence is the failure we are designing against here — a
// message that sits for four days under "is still busy" is indistinguishable
// from a working agent.
const (
	// BlockedByPasteChip is the case bp cannot resolve by itself: the composer
	// holds a paste rendered as a chip ("[Pasted text #1 +12 lines]"), whose
	// content cannot be read, so it can be neither recognised as ours nor safely
	// submitted. Only a human can tell what it is.
	BlockedByPasteChip = "composer'da okunamayan bir paste var (chip); bp elle dokunmaz"
	// BlockedByForeignText covers someone's half-written line and any remnant too
	// short to identify as ours (under 32 characters) — both are left untouched on
	// purpose, because erasing a human's input is the worse outcome.
	BlockedByForeignText = "composer'da yabanci metin var"
	// BlockedByBusyPane is the ordinary, transient case: the agent is mid-turn.
	BlockedByBusyPane = "pane calisiyor (esc to interrupt)"
	// BlockedByDialog is the pane waiting on a decision only a human may make —
	// today a Hermes permission prompt ("1. Allow once / 4. Deny"). It reads as
	// idle by every other measure (empty composer, no spinner), which is what
	// made it dangerous: a paste there lands on a selection list and its Enter
	// answers the prompt. Unlike BlockedByBusyPane this one does not clear
	// itself — it waits for a person, so `bp q` says so instead of implying
	// patience will fix it.
	BlockedByDialog = "onay/secim ekrani acik — yalnizca insan yanitlar"
)

// ComposerBlockReason names why a pane cannot take a message, or "" when nothing
// blocks it (an empty composer, or one holding text bp recognises as its own and
// can therefore resolve).
//
// It reports rather than decides: no key is pressed and no verdict here changes
// what gets delivered. Its whole purpose is to turn a silent wait into a line the
// operator can act on in one glance.
func ComposerBlockReason(pane string, texts []string) string {
	if Busy(pane) {
		return BlockedByBusyPane
	}
	return ComposerContentBlockReason(pane, texts)
}

// ComposerContentBlockReason is ComposerBlockReason WITHOUT the busy question:
// it names only what the composer itself is holding.
//
// It exists for forced delivery (see Client.SendForce), which overrides "the
// agent is working" and nothing else. The split is the whole point: reading a
// busy pane through ComposerBlockReason answers "pane calisiyor" and never gets
// as far as the box, so a forced message would either wait on the one state it
// is meant to override, or — if the caller ignored the verdict wholesale — paste
// over somebody's half-written line. Callers that force ask this one instead and
// keep every composer refusal intact.
func ComposerContentBlockReason(pane string, texts []string) string {
	// Asked before the composer, because this state has an EMPTY composer: a
	// Hermes permission prompt would otherwise fall straight through as "idle"
	// and take a paste onto its selection list (measured 2026-08-23, see
	// hermesDialog). It is also checked here rather than only in Busy() so that
	// FORCED delivery cannot skip it — force overrides "the agent is working",
	// never "a human is being asked something".
	if hermesDialog(pane) {
		return BlockedByDialog
	}
	if !composerFilled(pane) {
		return ""
	}
	if codexPasteChip(pane) || claudePasteChip(pane) {
		return BlockedByPasteChip
	}
	if _, ours := StuckPaste(pane, texts); ours {
		return ""
	}
	return BlockedByForeignText
}

// StuckPaste reports which of texts a non-empty composer is holding as OUR OWN
// unsubmitted paste (exact or damaged), so a caller can tell its own hanging
// text apart from an agent whose user is typing. ok is false for a foreign,
// unreadable or empty composer — in which case the caller must behave exactly as
// it did before: treat the pane as in use and queue.
//
// It says nothing about whether the pane is BUSY; callers check that themselves
// and must never touch a working pane.
func StuckPaste(pane string, texts []string) (string, bool) {
	verdict, text := classifyPaste(pane, texts)
	return text, verdict != pasteForeign
}
