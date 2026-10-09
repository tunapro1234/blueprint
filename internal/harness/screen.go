package harness

import "regexp"

// The screen signatures below are what bp reads off a terminal to tell a TUI's
// states apart. Each one was measured on a live pane; the matching logic and
// the history of why each anchor was chosen stay next to the code that uses
// them (internal/tmux). When a TUI build changes what it draws, the fix is an
// edit here plus a new fixture in internal/harness/conformance.

// ClaudeScreen holds Claude Code's screen signatures (measured on 2.1.233 -
// 2.1.295).
var ClaudeScreen = struct {
	// BusySpinner is the animated status row above the composer while a turn
	// runs: one symbol glyph at column 0, one word ending in an ellipsis, and
	// optionally a "(<timer> ...)" counter that ends the row.
	BusySpinner *regexp.Regexp
	// LegacyBusyCounter is the pre-2.1.233 timer/glyph that shared a row with
	// LegacyBusyHint.
	LegacyBusyCounter *regexp.Regexp
	LegacyBusyHint    string
	// PasteChip stands in for a large or multi-line paste ("[Pasted text #1 +2 lines]").
	PasteChip *regexp.Regexp
	// ComposerFooter markers sit on the status row directly below the composer
	// box (vim mode, permission mode).
	ComposerFooter []string
	// ComposerBorder are the runes the composer box is drawn with.
	ComposerBorder string
	// DialogFooter markers replace the composer footer while a modal is open.
	DialogFooter []string
	// AuthExpired phrases appear on a footer row prefixed by StatusBullet.
	AuthExpired  []string
	StatusBullet string
	UsageLimit   *regexp.Regexp
	UsageReset   *regexp.Regexp
	// RemoteControl* are the /remote-control menu and status lines.
	RemoteControlMenu         string
	RemoteControlMenuItems    []string
	RemoteControlURL          *regexp.Regexp
	RemoteControlActive       *regexp.Regexp
	RemoteControlDisconnected *regexp.Regexp
}{
	BusySpinner:               regexp.MustCompile(`^[^\p{L}\p{N}\s] +[^\s()]*(?:…|\.\.\.) *(?:\((?:\d+h +)?(?:\d+m +)?\d+(?:\.\d+)?s[^()]*\))?$`),
	LegacyBusyCounter:         regexp.MustCompile(`\(\s*(?:\d+h\s+)?(?:\d+m\s+)?\d+(?:\.\d+)?s?\s*[·•]|⏵`),
	LegacyBusyHint:            "esc to interrupt",
	PasteChip:                 regexp.MustCompile(`\[Pasted text[^\]]*\]`),
	ComposerFooter:            []string{"-- INSERT --", "-- NORMAL --", "bypass permissions"},
	ComposerBorder:            "─━",
	DialogFooter:              []string{"Enter to select", "Esc to cancel"},
	AuthExpired:               []string{"login expired", "run /login"},
	StatusBullet:              "●",
	UsageLimit:                regexp.MustCompile(`(?i)^⚠(?:️)?\s+usage\s+limit\s+reached(?:\b|$)`),
	UsageReset:                regexp.MustCompile(`(?i)\blimit\s+resets?\s+([^·…]+)`),
	RemoteControlMenu:         "Enter to select",
	RemoteControlMenuItems:    []string{"Disconnect this session", "Remote Control"},
	RemoteControlURL:          regexp.MustCompile(`https://claude\.ai/code/\S+`),
	RemoteControlActive:       regexp.MustCompile(`^\s*(?:⎿\s*)?/remote-control is active\b`),
	RemoteControlDisconnected: regexp.MustCompile(`^\s*●\s*Remote Control disconnected\b`),
}

// CodexIdleText is the two-word core of the Codex composer placeholder; the
// rest of the sentence is what builds like to reword.
const CodexIdleText = "Ask Codex"

// CodexScreen holds the Codex TUI's screen signatures (measured on 0.144 -
// 0.162, GPT-5.6 and GPT-6 footers).
var CodexScreen = struct {
	IdleText    string
	Placeholder *regexp.Regexp
	// Working is the busy row above the composer ("• Working (0s • esc to interrupt)").
	Working *regexp.Regexp
	// Footer is "model settings · /abs/cwd" under the composer, present in every state.
	Footer *regexp.Regexp
	// BusyQueuePhrase is the dim footer affordance of a busy composer: Enter
	// does not submit, Tab queues into Codex's own queue.
	BusyQueuePhrase string
	// PasteChip stands in for a >~1024 character paste; the first Enter
	// expands it, the second submits.
	PasteChip *regexp.Regexp
	// NavigationMenu items identify the start-up navigation menu.
	NavigationMenu []string
	// SearchShortcuts is the footer hint that is NOT an active search prompt.
	SearchShortcuts string
}{
	IdleText:        CodexIdleText,
	Placeholder:     regexp.MustCompile(`^[›❯]\s+` + regexp.QuoteMeta(CodexIdleText)),
	Working:         regexp.MustCompile(`^[•·◦]\s+Working\b`),
	Footer:          regexp.MustCompile(`^(?i:gpt-|o[1-9])\S*(?:\s+[^·]+)?\s+·\s+/\S`),
	BusyQueuePhrase: "tab to queue message",
	PasteChip:       regexp.MustCompile(`\[Pasted Content\s+\d+\s+chars\]`),
	NavigationMenu:  []string{"1. new chat", "2. agent command center", "3. resume another chat"},
	SearchShortcuts: "? for shortcuts",
}

const (
	hermesCaduceus        = "⚕"
	hermesPlaceholderText = "Ask anything, or type / for commands"
)

// HermesScreen holds Hermes Agent's screen signatures (measured on v0.20.x,
// 2026-08-22).
var HermesScreen = struct {
	// Caduceus prefixes Hermes' live rows (the busy composer and the status row).
	Caduceus        string
	PlaceholderText string
	IdleComposer    *regexp.Regexp
	// BusyComposer is the caduceus IN FRONT of the prompt marker: the state,
	// whether the composer is empty ("msg=interrupt · ...") or holds text.
	BusyComposer *regexp.Regexp
	// DialogComposer and SelectAffordance mark a modal the human must answer.
	DialogComposer   *regexp.Regexp
	SelectAffordance *regexp.Regexp
	// TailRows is how far from the bottom live markers are looked for.
	TailRows int
	// Binary is what `bp open --hermes` launches.
	Binary string
}{
	Caduceus:         hermesCaduceus,
	PlaceholderText:  hermesPlaceholderText,
	IdleComposer:     regexp.MustCompile(`^[❯›]\s+` + regexp.QuoteMeta(hermesPlaceholderText)),
	BusyComposer:     regexp.MustCompile(`^` + hermesCaduceus + `\s*[❯›]`),
	DialogComposer:   regexp.MustCompile(`^⚠\s*[❯›]`),
	SelectAffordance: regexp.MustCompile(`(?i)to select.*to confirm`),
	TailRows:         14,
	Binary:           "hermes",
}

// OpenCodeScreen holds OpenCode's screen signatures (measured on 1.x, 2026-08).
var OpenCodeScreen = struct {
	IdleText string
	// Rail is the left edge drawn down the composer; Bottom ("╹▀▀▀") is the
	// anchor, because the transcript reuses the rail but never draws it.
	Rail   *regexp.Regexp
	Bottom *regexp.Regexp
	// Mode is the "Build · <model> <provider>" row inside the composer.
	Mode   *regexp.Regexp
	Hints  *regexp.Regexp
	Footer *regexp.Regexp
	// PasteChip stands in for a multi-line paste ("[Pasted ~3 lines]").
	PasteChip *regexp.Regexp
	// BusyRow is the working signature ("esc interrupt", not Claude's "esc to interrupt").
	BusyRow *regexp.Regexp
}{
	IdleText:  "Ask anything...",
	Rail:      regexp.MustCompile(`^┃`),
	Bottom:    regexp.MustCompile(`^╹▀+`),
	Mode:      regexp.MustCompile(`^┃\s+\S+\s+·\s+\S`),
	Hints:     regexp.MustCompile(`tab agents\s+ctrl\+p commands`),
	Footer:    regexp.MustCompile(`^/\S*:\S+`),
	PasteChip: regexp.MustCompile(`\[Pasted\s+~?\d+\s+lines?\]`),
	BusyRow:   regexp.MustCompile(`esc\s+interrupt`),
}

// PromptMarkers are the composer prompt glyphs Claude, Codex and Hermes draw.
const PromptMarkers = "❯›"
