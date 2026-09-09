package tmux

import (
	"regexp"
	"strings"
)

// Codex panes report their command in three different ways, and the third one
// arrived on 2026-08-25: Tuna turned the sandbox off for probot-out-codex
// (CODEX_BWRAPPED=1), so the pane no longer runs under bubblewrap and
// #{pane_current_command} reads "node" — the runtime Codex is written in.
// bp then read that agent as DEAD and refused every inbound delivery
// (server-main).
//
// "node" cannot simply join IsAgentCommand's whitelist. It is the single most
// common long-running command on this machine — every build watcher, every
// bridge, every script — and that whitelist decides where bp is allowed to
// press keys. So Codex follows the rule Hermes established for "python": the
// COMMAND may only nominate a pane, the SCREEN has to confirm it.
//
// Everything below was measured on the live probot-out-codex pane (78x18,
// read-only capture, 2026-08-25):
//
//   - Working (0s • esc to interrupt)          <- busy row, dim-rendered
//     › Ask Codex to do anything                 <- idle composer placeholder
//     gpt-5.6-sol medium fast · /srv/probot/out-codex   <- footer: model · cwd
const (
	// codexIdleText is the composer placeholder. "Ask Codex" is the two-word
	// core; the rest of the sentence is left out because a placeholder's tail is
	// the part builds like to reword.
	codexIdleText = "Ask Codex"
)

// codexPlaceholder matches the idle composer: a prompt marker, then the
// placeholder. Anchored at the row start so a transcript QUOTING the sentence
// cannot match.
var codexPlaceholder = regexp.MustCompile(`^[›❯]\s+` + regexp.QuoteMeta(codexIdleText))

// codexWorking matches the busy row Codex draws above its composer. The bullet
// is part of the signature: "Working" on its own is a word any program may
// print, while "• Working (…)" at a row start is this TUI's own affordance.
var codexWorking = regexp.MustCompile(`^[•·◦]\s+Working\b`)

// codexFooter matches the status line under the composer: the model, its
// settings, then " · " and an ABSOLUTE PATH (the session's cwd). It is the only
// marker present in every state — composer empty or full, pane idle or busy —
// which is what keeps a Codex pane recognisable while a human's half-typed line
// sits in the box (without it such a pane reads "dead" in bp status).
//
// The model slug and absolute cwd distinguish it from ordinary prose.
var codexFooter = regexp.MustCompile(`^(?:gpt-|o[1-9])\S*(?:\s+[^·]+)?\s+·\s+/\S`)

// IsCodexCommand reports whether cmd COULD be a Codex pane. Like
// IsHermesCommand it is not a whitelist: "node" is shared with half the
// machine, and only CodexPane's screen evidence turns it into an agent.
//
// "codex" and "bwrap" stay in IsAgentCommand as they always were — those names
// are Codex-specific and have been trusted since before this file existed.
// This function exists for the third spelling.
func IsCodexCommand(cmd string) bool {
	switch cmd {
	case "node", "codex", "bwrap":
		return true
	default:
		return false
	}
}

// CodexPane reports whether the pane's live region shows the Codex TUI.
func CodexPane(pane string) bool {
	for _, raw := range hermesRegion(pane) {
		// Rows are normalised before matching: Codex indents its footer and may
		// indent the composer, and every marker below is anchored at the row
		// start. Matching raw rows made CodexPane depend on which marker
		// happened to sit flush left (measured 2026-08-25: the footer never
		// matched, so a pane whose composer held text was recognised only by
		// luck).
		line := stripSpace1(raw)
		if codexPlaceholder.MatchString(line) ||
			codexWorking.MatchString(line) ||
			codexFooter.MatchString(line) ||
			codexChip.MatchString(line) {
			return true
		}
	}
	return false
}

// codexComposerBox reads every row between the last prompt and model footer.
// Current Codex has no top border; older versions may draw one above the prompt.
func codexComposerBox(pane string) (string, int, bool) {
	if !CodexPane(pane) {
		return "", -1, false
	}
	lines := strings.Split(pane, "\n")
	footer := -1
	for i, line := range lines {
		// Dim styling is structural here, not a placeholder: dropping it can
		// remove the separator between model and cwd.
		if codexFooter.MatchString(stripSpace1(ansiSeq.ReplaceAllString(line, ""))) {
			footer = i // the LAST footer: the live one, never a transcript quote
		}
	}
	if footer <= 0 {
		return "", -1, false
	}
	bottom := footer - 1
	for bottom >= 0 && stripSpace(StripDim(lines[bottom])) == "" {
		bottom--
	}
	if bottom < 0 {
		return "", -1, false
	}
	// Current Codex has no rule above the composer. The last prompt before
	// the footer is the anchor; everything above it may be streamed transcript.
	start := -1
	for j := bottom; j >= 0 && bottom-j <= composerBoxMaxRows; j-- {
		if promptLine.MatchString(lines[j]) {
			start = j
			break
		}
	}
	if start < 0 {
		return "", -1, false
	}
	rows := lines[start : bottom+1]
	if len(rows) == 0 || !promptLine.MatchString(rows[0]) {
		return "", -1, false
	}
	out := make([]string, 0, len(rows))
	for k, row := range rows {
		clean := StripDim(row)
		if k == 0 {
			clean = promptLine.ReplaceAllString(clean, "")
			if codexPlaceholderOnly(clean) {
				clean = "" // the idle placeholder is not somebody's text
			}
		} else if promptLine.MatchString(row) {
			// A second prompt marker means these rows are not one composer.
			return "", -1, false
		}
		out = append(out, strings.TrimRight(clean, " \t "))
	}
	return strings.Join(out, "\n"), start, true
}

// codexPlaceholderOnly recognises the idle composer's own sentence, so an empty
// Codex composer reads as empty rather than as a human typing.
func codexPlaceholderOnly(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), codexIdleText)
}

// stripSpace1 collapses runs of whitespace to a single space, which is what the
// footer pattern expects (it counts fields). It is NOT stripSpace: that one
// removes whitespace entirely and would glue the footer's fields together.
func stripSpace1(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// hermesRegion is shared rather than duplicated: both TUIs need the same tail
// window with the same treatment (ANSI colour removed, dim segments preserved,
// unpainted trailing rows dropped — the last of which was itself a measured
// bug, see trimTrailingBlank). The name stays as it is because renaming it
// would touch every Hermes call site for no behavioural gain; this comment is
// the pointer for the next reader.
var _ = strings.TrimSpace

// Modal confirmation belongs to the user, even if an old composer is visible.
func paneDialog(pane string) bool {
	if hermesDialog(pane) || codexSearchActive(pane) || codexNavigationMenu(pane) {
		return true
	}
	for _, line := range hermesRegion(pane) {
		text := strings.ToLower(line)
		if (strings.Contains(text, "esc to cancel") || strings.Contains(text, "esc to go back")) && (strings.Contains(text, "enter to") || strings.Contains(text, "confirm")) {
			return true
		}
	}
	return false
}

// Dialog exposes the same input protection used by the delivery path.
func Dialog(pane string) bool { return paneDialog(pane) }

// This navigation menu has no "enter to confirm / esc to cancel" footer.
// Enter selects a destination, so a disappearing draft here is not delivery.
func codexNavigationMenu(pane string) bool {
	choices := []string{"1. new chat", "2. agent command center", "3. resume another chat"}
	next := 0
	for _, line := range hermesRegion(pane) {
		text := strings.ToLower(strings.Trim(strings.TrimSpace(line), "›❯> "))
		if text == choices[next] {
			next++
			if next == len(choices) {
				return true
			}
		}
	}
	return false
}

// Vim / and ? searches replace Codex's footer and receive paste events instead
// of the draft (Codex 0.153.4, textarea/vim_search.rs). Leave that input alone,
// including an empty search. Ordinary shortcut hints are not search editors.
func codexSearchActive(pane string) bool {
	lines := strings.Split(strings.TrimSpace(ansiSeq.ReplaceAllString(pane, "")), "\n")
	// Search replaces the model footer, so a populated draft may have lost
	// CodexPane's other signatures. Its own prompt still anchors the editor.
	prompt := false
	for _, line := range lines[:len(lines)-1] {
		if strings.HasPrefix(strings.TrimSpace(line), "›") {
			prompt = true
		}
	}
	if !prompt && !CodexPane(pane) {
		return false
	}
	last := strings.TrimSpace(lines[len(lines)-1])
	return strings.HasPrefix(last, "/") || strings.HasPrefix(last, "?") && last != "? for shortcuts"
}
