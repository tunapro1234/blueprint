package tmux

import (
	"regexp"
	"strings"
)

// This file teaches the tmux layer one more agent TUI: the Hermes Agent
// (github.com/NousResearch/hermes-agent, installed on this machine as
// /usr/local/bin/hermes). Everything encoded here was measured on 2026-08-22 in
// the tmux session `blueprint-hermes-test` (200x50), with read-only captures and
// one driven turn; the fixtures in hermes_test.go quote those captures verbatim.
//
// Hermes differs from claude/codex in four ways that matter to this package, and
// each one is why a piece of code below exists rather than a reuse of the
// existing paths:
//
//  1. THE PANE COMMAND LIES. #{pane_current_command} for a Hermes pane is
//     "python" — the runtime, not the program. "python" cannot go into
//     IsAgentCommand's whitelist: every pane on this machine running a script, a
//     REPL or a long-lived job would become a legal target for injected
//     keystrokes, which is precisely the accident that whitelist exists to
//     prevent. So a python pane is an agent pane only when the SCREEN says so
//     (HermesPane), and the two facts are always asked together (IsAgentPane).
//
//  2. THE PLACEHOLDER IS NOT DIM. Claude and Codex render composer placeholders
//     dim (\x1b[2m), which is why StripDim exists and why an empty composer reads
//     empty. Hermes renders "Ask anything, or type / for commands…" in ITALIC
//     (\x1b[3m) plus colour 38;5;136 — measured byte-for-byte:
//     "\x1b[38;5;230m❯ \x1b[3m\x1b[38;5;136mAsk anything, or type / for commands…\x1b[0m".
//     StripDim leaves it standing, so without hermesPlaceholderOnly below EVERY
//     idle Hermes pane reads as "somebody is typing": Typing() true, composerFilled
//     true, and every message to that agent queues forever behind a composer that
//     is in fact empty. This was the first thing measured and it contradicts the
//     assumption that a placeholder is always dim.
//
//  3. SUBMITTING WHILE BUSY INTERRUPTS. See hermesBusyComposer.
//
//  3b. A NEWLINE IS A SUBMIT UNLESS THE PASTE IS BRACKETED. This one was
//     measured the hard way. bp's inject() pastes with `paste-buffer -d`, no
//     -p, so tmux writes the buffer's bytes raw and the receiving TUI decides
//     what a 0x0A means. Claude and Codex detect a paste by timing and keep the
//     newlines as text; Hermes does not. A three-line message pasted that way
//     into an IDLE Hermes submitted line one, then — because the first line had
//     already started a turn — the pane printed "⚡ Sending after interrupt:
//     'ikinci satir…'" and cancelled its own turn to take line two, leaving line
//     three in the composer. One message became three, two of them interrupts.
//     The same message pasted with `-p` (tmux emits the bracketed-paste
//     markers, which Hermes honours) stayed in the composer as three rendered
//     rows and went out whole on ONE Enter. So the Hermes path pastes bracketed;
//     see hermesPaste and Client.inject. The flag is NOT turned on globally:
//     tmux only emits the markers when the application asked for them, but
//     Claude's chip thresholds and Codex's expand-then-submit mechanics were
//     measured under the current unbracketed paste and there is no reason to
//     re-open them here.
//
//  4. THERE IS NO PASTE CHIP. A multi-line paste is rendered as raw continuation
//     rows between the two rules, so the FULL box view (hermesComposerBox) is the
//     only way an injected multi-line message can be compared against what landed;
//     without it classifyComposer would judge a 12-row paste by its last row alone
//     and call the composer foreign. No chip also means C-u clears one RENDERED
//     line at a time with nothing hiding the rest, which is what the adaptive
//     budget in clearWithCtrlU is for.

// hermesBin is the program `bp open --hermes` launches. Plain, no flags: Hermes
// has no permission/sandbox switch of the kind claude and codex are opened with,
// and inventing one here would be a guess.
const hermesBin = "hermes"

// hermesCaduceus is U+2695 ⚕, the glyph Hermes prefixes its LIVE rows with (the
// busy composer and the status row). It is the cheapest thing that says "this is
// Hermes" and the anchor of every matcher below.
const hermesCaduceus = "⚕"

// hermesPlaceholderText is the idle composer's placeholder, measured verbatim.
// The trailing ellipsis (U+2026) is deliberately NOT part of the constant: it is
// the single most likely character to change between builds, and matching the
// sentence without it fails safe in the harmless direction (a placeholder we
// still recognise) rather than the harmful one (an idle pane read as typed-in).
const hermesPlaceholderText = "Ask anything, or type / for commands"

// hermesIdleComposer matches the idle composer row:
//
//	❯ Ask anything, or type / for commands…
//
// The prompt marker must START the row (Hermes draws it at column 0, inside the
// two rules), so a transcript line merely quoting the sentence does not match.
var hermesIdleComposer = regexp.MustCompile(`^[❯›]\s+` + regexp.QuoteMeta(hermesPlaceholderText))

// hermesBusyComposer matches the BUSY composer row. The signature is the
// CADUCEUS IN FRONT OF THE PROMPT MARKER, and getting to that took a correction
// worth recording, because the obvious reading of the busy screen is wrong.
//
// A busy Hermes with an EMPTY composer renders:
//
//	⚕ ❯ msg=interrupt · /queue · /bg · /steer · Ctrl+C cancel
//
// which reads like a fixed affordance row. It is not: "msg=interrupt · …" is the
// busy composer's PLACEHOLDER, and it disappears the moment anything is typed or
// pasted. Measured 2026-08-22 in blueprint-hermes-test, all four states:
//
//	idle,  empty   ->  "❯ Ask anything, or type / for commands…"
//	idle,  text    ->  "❯ ucuncu satir da var"
//	busy,  empty   ->  "⚕ ❯ msg=interrupt · /queue · /bg · /steer · Ctrl+C cancel"
//	busy,  text    ->  "⚕ ❯ ucuncu satir da var"
//
// A first version of this matcher required the literal "msg=interrupt" and would
// therefore have called the fourth state IDLE — a busy pane holding text, which
// is exactly the state a TOCTOU delivery lands in, and the one where a wrong
// "idle" costs the most (see hermesForceRefused: Enter there cancels the turn).
// The caduceus is the state, the placeholder is only what fills the empty case.
//
// The kaomoji spinner rows above it ("  (¬_¬) processing...", "  ◉_◉
// brainstorming...", "  ٩(๑❛ᴗ❛๑)۶ ruminating...  (↓ 42 tok)") are deliberately
// NOT matched: they are indented decoration whose emoticon and verb rotate every
// frame, the kind of thing this package has refused to enumerate since the 2026-
// 08-15 rewrite of Busy.
var hermesBusyComposer = regexp.MustCompile(`^` + hermesCaduceus + `\s*[❯›]`)

// hermesStatusRow matches the live status row Hermes draws just ABOVE its
// composer (note: above, where Claude's footer is below):
//
//	⚕ x-preview-f-free · 2% · 2m                ─ Say ve /srv dizin...
//
// Model name, context percentage and turn age. It is the third recognition
// marker and the only one present in BOTH states, which is what lets a pane be
// recognised as Hermes in the one frame where neither composer row is readable.
// The percentage and the two separators are required so the transcript's own box
// header ("╭─ ⚕ Hermes ───╮" — caduceus present, but never at the start of a row
// and never followed by "· N% ·") cannot match.
//
// THE CONTEXT FIELD IS NOT ALWAYS A PERCENTAGE. A pane that has not run a turn
// yet draws "--" there ("⚕ x-preview-f-free · -- · 3s"), and the first version of
// this pattern accepted only digits. That is the state EVERY agent is in for its
// first message: `bp open --hermes` returns, the operator sends the task, and the
// pane is unrecognisable — so the idle placeholder counts as somebody's typing and
// the message queues forever. It cost probot-egitim a hand-delivery and me the
// wrong root cause (2026-08-22: the queue record was made three hours AFTER the
// ghost-text fix that supposedly covered it, which is what proved this was a
// second, separate bug rather than a stale binary).
var hermesStatusRow = regexp.MustCompile(`^` + hermesCaduceus + `\s+\S.*·\s*(?:\d+%|--)\s*·`)

// hermesDialogComposer matches the composer row while Hermes is WAITING FOR A
// HUMAN DECISION. Measured 2026-08-23 on probot-outreach-ig-kuanta, whose agent
// tried to run a `curl | python3` heredoc and hit Hermes' own security scanner:
//
//	│ ❯ 1. Allow once                                                │
//	│   2. Allow for this session                                    │
//	│   4. Deny                                                      │
//	  ↑/↓ to select, Enter to confirm  (62s)
//	 ⚕ x-preview-f-free · 40% · 1.2d          ─ kuanta.md görev d...
//	────────────────────────────────────────────────────────────────
//	⚠ ❯
//	────────────────────────────────────────────────────────────────
//
// The state marker in front of the prompt is a WARNING SIGN (U+26A0) where the
// idle pane has nothing and the busy pane has the caduceus. The composer itself
// is EMPTY, which is exactly why this state was invisible: bp read the pane as
// idle and would have pasted into it — and the paste's Enter would have
// answered a security prompt whose selected line was "Allow once". bp deciding
// a permission dialog is the same class of harm as pressing Escape into a pane,
// and gets the same answer: refuse, wait for the human.
//
// "Idle" and "blocked" look identical from outside and mean opposite things
// (probot-outreach, who found this from the other side: three of their panes
// were reported working-but-idle for minutes while they sat on this screen).
var hermesDialogComposer = regexp.MustCompile(`^⚠\s*[❯›]`)

// hermesSelectAffordance is the second, independent signature of the same
// state: the key hint Hermes draws under a selection list. It is textual and
// therefore the more fragile of the two, which is why it is an OR rather than
// a requirement — either marker is enough to refuse.
var hermesSelectAffordance = regexp.MustCompile(`(?i)to select.*to confirm`)

// hermesDialog reports whether a Hermes pane is holding a modal the HUMAN must
// answer (a permission prompt, a selection list).
//
// The asymmetry is deliberate and matches every other gate in this package: a
// wrong "blocked" costs one dispatch pass, a wrong "idle" answers somebody's
// security prompt with a pasted message.
func hermesDialog(pane string) bool {
	if !HermesPane(pane) {
		return false
	}
	for _, line := range hermesRegion(pane) {
		if hermesDialogComposer.MatchString(line) || hermesSelectAffordance.MatchString(line) {
			return true
		}
	}
	return false
}

// hermesTailRows is how far up from the bottom of a capture the markers are
// looked for. Same discipline as Busy/AuthExpired: a LIVE marker sits in the
// narrow strip the TUI owns, a quotation of one usually does not. Measured, the
// three markers land within the last 6 rows of a capture (the status row, the top
// rule, the composer, the bottom rule, and one or two trailing blanks); 14 is
// generous room for a taller composer without reaching into the transcript.
const hermesTailRows = 14

// hermesRegion returns the tail rows a live Hermes marker may appear in, with
// ANSI colour removed but dim segments PRESERVED — the same rule as Busy(): the
// markers are colour- and italic-rendered, and StripDim would delete rows we
// depend on.
func hermesRegion(pane string) []string {
	lines := trimTrailingBlank(strings.Split(pane, "\n"))
	start := len(lines) - hermesTailRows
	if start < 0 {
		start = 0
	}
	out := make([]string, 0, len(lines)-start)
	for _, line := range lines[start:] {
		out = append(out, strings.TrimLeft(ansiSeq.ReplaceAllString(line, ""), " \t "))
	}
	return out
}

// trimTrailingBlank drops the empty rows at the END of a capture, so "the last N
// rows" means the last N rows the TUI actually DREW.
//
// Hermes does not paint to the bottom of a fresh window: a just-opened pane put
// its composer at row 19 of a 39-row capture and left 20 blank rows underneath
// (measured 2026-08-22, blueprint-hermes-fresh). Every marker was therefore
// outside the 14-row window and the pane read as "not Hermes" — the same visible
// failure as the "--" status field, from a completely different cause, which is
// why the first fix looked deployed and the bug kept happening. Claude fills its
// window, which is why no path in this package had needed this before.
func trimTrailingBlank(lines []string) []string {
	end := len(lines)
	for end > 0 && strings.TrimSpace(ansiSeq.ReplaceAllString(lines[end-1], "")) == "" {
		end--
	}
	return lines[:end]
}

// HermesPane reports whether the pane's live region shows the Hermes TUI. It is
// the SCREEN half of agent-ness for a python pane (IsAgentPane); on its own it
// answers only "this screen is Hermes".
func HermesPane(pane string) bool {
	for _, line := range hermesRegion(pane) {
		if hermesIdleComposer.MatchString(line) ||
			hermesBusyComposer.MatchString(line) ||
			hermesStatusRow.MatchString(line) {
			return true
		}
	}
	// A structural marker for the composer row itself (prompt marker followed by
	// an italic segment) was tried here and REMOVED the same hour: it matches a
	// Claude pane holding italic text just as well — Claude draws the same "❯"
	// prompt — and on a pane wrongly read as Hermes the italic strip would throw
	// away a human's real input and paste over it. The status row already covers
	// the case it was added for (a fresh pane showing "--"), so the marker bought
	// nothing and risked the one direction this package never trades away.
	return false
}

// HermesIdle reports the specific readiness state `bp open --hermes` waits for:
// the idle composer placeholder is on screen, so the TUI has finished booting and
// will accept a paste. A pane mid-turn is NOT idle here.
func HermesIdle(pane string) bool {
	for _, line := range hermesRegion(pane) {
		if hermesIdleComposer.MatchString(line) {
			return true
		}
	}
	return false
}

// hermesBusy reports the Hermes busy signature: the composer row that says Enter
// would interrupt. Busy() ORs it with the claude/codex signatures.
func hermesBusy(pane string) bool {
	for _, line := range hermesRegion(pane) {
		if hermesBusyComposer.MatchString(line) {
			return true
		}
	}
	return false
}

// hermesForceRefused reports whether the busy gate must hold even for a FORCED
// delivery (bp msg --force-busy / Client.SendForce).
//
// It is true for exactly one thing: a Hermes pane that is busy. Everywhere else
// force means what it has always meant — "I know the agent is working, put the
// message in the pane anyway" — and the cost of being wrong is a message merged
// into a redrawing composer. On Hermes the cost is categorically different:
// measured 2026-08-22 in blueprint-hermes-test, the busy composer reads
// "msg=interrupt", i.e. submitting text mid-turn CANCELS the running turn. A
// forced message would not merely arrive awkwardly, it would destroy the work the
// sender was trying to interrupt-but-not-cancel. That is the same class of harm
// as pressing Escape into an agent pane, which this package has been forbidden
// from doing since 2026-08-11, and it gets the same answer: refuse, and let the
// message queue normally until the turn ends.
//
// (Hermes DOES have a native queue — the same row advertises "/queue" — but
// reaching it means prefixing the operator's text with a slash command, i.e.
// rewriting the message bp was asked to deliver. bp does not edit messages; v1
// waits instead.)
func hermesForceRefused(pane string) bool {
	return hermesBusy(pane) && HermesPane(pane)
}

// hermesGhostSeg matches one italic-rendered segment in an ANSI capture:
// \x1b[3m up to the reset that ends it (\x1b[0m, or \x1b[23m = italic off).
// Inner colour codes (the measured wrapper nests \x1b[38;5;136m inside the
// italic) are consumed by the non-greedy body. It is the italic twin of
// tmux.go's dimSeg, and it is applied ONLY to rows of a screen-confirmed
// Hermes pane: Hermes marks everything that is not input — the idle
// placeholder and the rotating ghost suggestions — with this one attribute.
var hermesGhostSeg = regexp.MustCompile("\x1b\\[3m.*?\x1b\\[(?:0|23)m")

// hermesPlaceholderOnly reports whether a composer row (prompt marker already
// stripped, ANSI/dim already removed) is nothing but the Hermes placeholder.
//
// This is fact 2 at the top of the file made operational: the placeholder is
// italic, not dim, so it survives StripDim and would otherwise count as typed
// text. The comparison is whitespace-stripped, like every other comparison in
// this package, and prefix-based so the trailing ellipsis — or a future " (esc to
// clear)" tail — does not break it. Anything a user typed in FRONT of the
// sentence fails the prefix test and is correctly treated as content; the
// placeholder never coexists with real input anyway (it is replaced verbatim on
// the first keystroke, measured).
func hermesPlaceholderOnly(row string) bool {
	stripped := stripSpace(row)
	if stripped == "" {
		return false
	}
	return strings.HasPrefix(stripped, stripSpace(hermesPlaceholderText))
}

// IsHermesCommand reports whether cmd (a pane's #{pane_current_command}) COULD be
// a Hermes pane. It is not a whitelist and must never be used as one: it names
// the interpreters Hermes runs under, all of which are shared with every other
// python program on the machine. Only IsAgentPane — command AND screen — decides
// that a pane may receive keystrokes.
func IsHermesCommand(cmd string) bool {
	switch cmd {
	case "python", "python3", "hermes":
		return true
	default:
		return false
	}
}

// IsAgentPane is IsAgentCommand widened by one screen-confirmed case: a python
// pane whose screen shows the Hermes TUI. Callers that hold a capture should
// prefer it; callers that only have a command string keep IsAgentCommand and
// simply do not see Hermes (which fails safe — an unrecognised pane is never
// typed into).
//
// pane may be empty (a capture that failed): the answer then falls back to the
// command alone, so a read error can never turn a shell into a valid target.
func IsAgentPane(cmd, pane string) bool {
	if IsAgentCommand(cmd) {
		return true
	}
	if IsHermesCommand(cmd) && HermesPane(pane) {
		return true
	}
	// Codex without its sandbox reports "node" (2026-08-25). Same rule, same
	// reason: the command nominates, the screen confirms.
	if IsCodexCommand(cmd) && CodexPane(pane) {
		return true
	}
	// opencode, the fourth TUI (2026-08-26). Its command name is unique enough to
	// trust on its own; it goes through the screen anyway, because a rule applied
	// only when it is strictly necessary is a rule the next surprise finds
	// missing.
	return IsOpenCodeCommand(cmd) && OpenCodePane(pane)
}

// hermesComposerBox is the BOX view (see composer.go) for a Hermes pane.
//
// The live structure, measured 2026-08-22 in blueprint-hermes-test:
//
//	 ⚕ x-preview-f-free · 2% · 2m         ─ Say ve /srv dizin...   <- status, ABOVE
//	────────────────────────────────────────────────────────────   <- top rule
//	❯ Ask anything, or type / for commands…                        <- composer
//	────────────────────────────────────────────────────────────   <- bottom rule
//
// Why it cannot reuse composerBoxAt's Claude path: that one anchors on the
// status/footer line BELOW the box ("-- INSERT --", "bypass permissions") and
// walks upward. Hermes puts its status row ABOVE the box and prints none of those
// markers, so the Claude path returns ok=false for every Hermes pane — which is
// survivable for Codex (whose big pastes hide behind a chip anyway) but not here,
// where a multi-line paste is rendered as raw rows and the box is the ONLY way to
// compare it against what we injected.
//
// Both rules are PURE runs of ─ (no label inside, unlike Claude's top border), so
// isComposerBorder — the strict test — is the right one. The bottom rule is the
// LAST one in the capture; the top rule is the nearest one above it.
//
// ok is false, and every caller falls back to the single-row behavior, whenever:
//   - the pane is not recognisably Hermes (never guess at a foreign UI);
//   - fewer than two rules sit at the end of the capture;
//   - the first interior row carries no prompt marker — which is exactly the BUSY
//     state ("⚕ ❯ msg=interrupt · …" starts with the caduceus, so promptLine does
//     not match it). A busy Hermes composer is therefore never readable as a box,
//     and that is correct: nothing may be typed into it at all.
func hermesComposerBox(pane string) (string, int, bool) {
	if !HermesPane(pane) {
		return "", -1, false
	}
	lines := strings.Split(pane, "\n")
	bottom := -1
	for i, line := range lines {
		if isComposerBorder(stripSpace(StripDim(line))) {
			bottom = i // the LAST rule: the live composer's bottom edge
		}
	}
	if bottom <= 0 {
		return "", -1, false
	}
	top := -1
	for j := bottom - 1; j >= 0 && bottom-j <= composerBoxMaxRows; j-- {
		if isComposerBorder(stripSpace(StripDim(lines[j]))) {
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
		// Ghost/placeholder text is italic-marked in ANSI captures; strip it
		// before StripDim erases the attribute that identifies it (plain
		// captures carry no italic wrapper and pass through unchanged).
		clean := StripDim(hermesGhostSeg.ReplaceAllString(row, ""))
		if k == 0 {
			clean = promptLine.ReplaceAllString(clean, "")
			if hermesPlaceholderOnly(clean) {
				clean = "" // an idle composer, not somebody's text
			}
		} else if promptLine.MatchString(row) {
			// A second prompt marker means these rows are not one box.
			return "", -1, false
		}
		out = append(out, strings.TrimRight(clean, " \t "))
	}
	return strings.Join(out, "\n"), top, true
}

const (
	// hermesClearPressesPerRow and hermesClearMargin size the C-u budget
	// clearWithCtrlU is allowed on a Hermes pane.
	//
	// Measured 2026-08-22 in blueprint-hermes-test, on a 3-row pasted message,
	// pressing C-u once per capture: the presses went "row 3 emptied", "row 3
	// removed", "row 2 emptied", "row 2 removed", "row 1 emptied and the
	// placeholder came back" — FIVE presses for three rows, i.e. C-u kills the
	// content of a line and its newline SEPARATELY (2N-1). Hermes also has no
	// paste chip, so an N-row message really does occupy N rendered rows; the
	// fixed bound of 8 would have left the top of anything past four rows sitting
	// in the composer, blocking that agent's queue behind text bp itself put
	// there.
	hermesClearPressesPerRow = 2
	// hermesClearMargin covers the row the cursor sits on and a redraw landing
	// between a press and its capture.
	hermesClearMargin = 4
	// hermesClearAttemptsMax is the hard ceiling on that budget. It exists so a
	// misread box (a pane that is not really Hermes, a scrolled window) can never
	// turn into an unbounded stream of keypresses into somebody's session.
	hermesClearAttemptsMax = 48
)

// hermesClearBudget returns how many C-u presses this pane's composer may need.
// It returns 0 for anything that is not a readable Hermes composer, meaning "keep
// the ordinary bound".
func hermesClearBudget(pane string) int {
	box, _, ok := hermesComposerBox(pane)
	if !ok {
		return 0
	}
	budget := composerBoxRows(box)*hermesClearPressesPerRow + hermesClearMargin
	if budget > hermesClearAttemptsMax {
		return hermesClearAttemptsMax
	}
	return budget
}
