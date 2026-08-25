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
var codexWorking = regexp.MustCompile(`^[•·]\s+Working\b`)

// codexFooter matches the status line under the composer: the model, its
// settings, then " · " and an ABSOLUTE PATH (the session's cwd). It is the only
// marker present in every state — composer empty or full, pane idle or busy —
// which is what keeps a Codex pane recognisable while a human's half-typed line
// sits in the box (without it such a pane reads "dead" in bp status).
//
// The path requirement is what keeps it from matching prose: a row that ends in
// "· /something" after three whitespace-separated fields is a status line, not
// a sentence.
var codexFooter = regexp.MustCompile(`^\S+\s+\S+\s+\S+\s+·\s+/\S`)

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
	for _, line := range hermesRegion(pane) {
		if codexPlaceholder.MatchString(line) ||
			codexWorking.MatchString(line) ||
			codexFooter.MatchString(line) ||
			codexChip.MatchString(line) {
			return true
		}
	}
	return false
}

// hermesRegion is shared rather than duplicated: both TUIs need the same tail
// window with the same treatment (ANSI colour removed, dim segments preserved,
// unpainted trailing rows dropped — the last of which was itself a measured
// bug, see trimTrailingBlank). The name stays as it is because renaming it
// would touch every Hermes call site for no behavioural gain; this comment is
// the pointer for the next reader.
var _ = strings.TrimSpace
