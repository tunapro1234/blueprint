package tmux

import (
	"regexp"
	"strings"
)

// The fourth TUI, and the third one to arrive by surprise: opencode 1.18.23
// (compec-mail-ox, /srv/compec/mail-ox, stealth/ox-alpha over OpenRouter). Its
// pane reports "opencode", which no other program on this machine is called, so
// unlike "python" and "node" the name would be safe on its own — it still goes
// through the screen test, because the rule is what makes the next TUI cheap,
// not the name.
//
// Everything here was measured on a disposable pane (blueprint-ox-test, 100x30,
// 2026-08-26), not read from documentation:
//
//	  ┃
//	  ┃  Ask anything... "What is the tech stack of this project?"
//	  ┃
//	  ┃  Build · Ox Alpha (stealth) OpenRouter
//	  ╹▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
//	  tab agents  ctrl+p commands
//	  /srv/compec/mail-ox:master                        1.18.23
//
// Three findings from that pane drive the code below, and each of them is a
// class bp has already been hurt by once:
//
//  1. An UNBRACKETED multi-line paste SUBMITS. "satir bir\nsatir iki\nsatir uc"
//     went out as one message with the newlines swallowed and the turn started
//     immediately — the Hermes failure exactly (2026-08-22), where three lines
//     became three messages and interrupted each other. opencode therefore
//     joins Hermes on the bracketed-paste side of inject().
//  2. A bracketed multi-line paste renders as a CHIP, "[Pasted ~3 lines]" — a
//     third chip spelling, after Claude's and Codex's. A chip means the
//     composer's content cannot be read, which every verdict in this package
//     has to know before it decides whose text is in the box.
//  3. A single-line paste renders LITERALLY and wraps inside the ┃ rail, and
//     the transcript above uses the SAME rail character. So the composer reader
//     anchors on the bottom edge ("╹▀▀▀"), never on the rail alone — the Codex
//     lesson (anchor on what only the live composer draws) applied before it
//     had to be relearned.

// openCodeIdleText is the composer placeholder. Only the stable opening is
// matched: the sentence continues with a rotating example question.
const openCodeIdleText = "Ask anything..."

var (
	// openCodeRail is the left edge opencode draws down its composer.
	openCodeRail = regexp.MustCompile(`^┃`)
	// openCodeBottom is the composer's bottom edge: the corner glyph followed by
	// a run of upper-half blocks. This is the anchor, because the transcript
	// above the composer reuses the rail but never draws this.
	openCodeBottom = regexp.MustCompile(`^╹▀+`)
	// openCodeMode is the row inside the composer naming the mode, model and
	// provider ("Build · Ox Alpha (stealth) OpenRouter"). It sits between the
	// text and the bottom edge in every state, which makes it both a signature
	// and the end of the composer's content.
	openCodeMode = regexp.MustCompile(`^┃\s+\S+\s+·\s+\S`)
	// openCodeHints is the affordance row under the composer.
	openCodeHints = regexp.MustCompile(`tab agents\s+ctrl\+p commands`)
	// openCodeFooter is the bottom status row: the session's cwd and git branch,
	// then the version. The cwd wraps to a second row on a narrow pane, so the
	// version is not required to be on the same row as the path.
	openCodeFooter = regexp.MustCompile(`^/\S*:\S+`)
	// openCodeChip stands in for a multi-line paste whose text is not rendered.
	// The count is approximate in opencode's own wording ("~3 lines"), so it is
	// matched as a shape and never compared to the message.
	openCodeChip = regexp.MustCompile(`\[Pasted\s+~?\d+\s+lines?\]`)
	// openCodeBusyRow is the working signature: the progress dots and the
	// interrupt affordance opencode draws while a turn runs. "esc interrupt" —
	// note NOT Claude's "esc to interrupt", which is why the existing busy
	// branches all read this pane as idle.
	openCodeBusyRow = regexp.MustCompile(`esc\s+interrupt`)
)

// IsOpenCodeCommand reports whether cmd could be an opencode pane.
func IsOpenCodeCommand(cmd string) bool { return cmd == "opencode" }

// OpenCodePane reports whether the pane's live region shows the opencode TUI.
func OpenCodePane(pane string) bool {
	for _, raw := range hermesRegion(pane) {
		line := stripSpace1(raw)
		if openCodeBottom.MatchString(line) ||
			openCodeMode.MatchString(line) ||
			openCodeHints.MatchString(line) ||
			openCodeFooter.MatchString(line) {
			return true
		}
	}
	return false
}

// openCodeBusy reports whether an opencode pane is mid-turn.
func openCodeBusy(pane string) bool {
	if !OpenCodePane(pane) {
		return false
	}
	for _, raw := range hermesRegion(pane) {
		if openCodeBusyRow.MatchString(stripSpace1(raw)) {
			return true
		}
	}
	return false
}

// openCodeComposerBox reads the composer: the rail rows between the LAST bottom
// edge and the mode row above it. Blank rail rows are opencode's own padding and
// the placeholder is not somebody's text, so both read as empty.
func openCodeComposerBox(pane string) (string, int, bool) {
	if !OpenCodePane(pane) {
		return "", -1, false
	}
	lines := strings.Split(pane, "\n")
	bottom := -1
	for i, line := range lines {
		if openCodeBottom.MatchString(stripSpace1(StripDim(line))) {
			bottom = i // the LAST edge: the live composer, never a transcript quote
		}
	}
	if bottom <= 0 {
		return "", -1, false
	}
	// The mode row closes the content. Without it there is no composer to read:
	// the box is drawn in one piece and a missing mode row means an unfamiliar
	// rendering, where this package's rule is to refuse rather than guess.
	mode := -1
	for j := bottom - 1; j >= 0 && bottom-j <= composerBoxMaxRows; j-- {
		if openCodeMode.MatchString(stripSpace1(StripDim(lines[j]))) {
			mode = j
			break
		}
	}
	if mode < 0 {
		return "", -1, false
	}
	top := -1
	for j := mode - 1; j >= 0 && mode-j <= composerBoxMaxRows; j-- {
		if !openCodeRail.MatchString(stripSpace1(StripDim(lines[j]))) {
			top = j
			break
		}
	}
	if top < 0 {
		return "", -1, false
	}
	out := make([]string, 0, mode-top)
	for _, row := range lines[top+1 : mode] {
		clean := strings.TrimSpace(openCodeRail.ReplaceAllString(strings.TrimSpace(StripDim(row)), ""))
		if openCodePlaceholderOnly(clean) {
			clean = ""
		}
		out = append(out, clean)
	}
	return strings.TrimSpace(strings.Join(trimTrailingBlank(out), "\n")), top, true
}

// openCodePlaceholderOnly recognises the idle composer's own sentence. An idle
// composer that reads as OCCUPIED blocks its agent's queue behind text nobody
// typed — measured on Hermes with five agents at once (2026-08-22), and not a
// lesson worth paying for twice.
func openCodePlaceholderOnly(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), openCodeIdleText)
}
