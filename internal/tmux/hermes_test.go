package tmux

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Every string in this file is QUOTED FROM A LIVE CAPTURE of the Hermes Agent
// TUI, taken 2026-08-22 in the tmux session `blueprint-hermes-test` (200x50,
// /usr/local/bin/hermes). Only the transcript prose is trimmed for width; the
// status row, the two composer rows, the rules and the kaomoji spinner are
// byte-for-byte what tmux handed back. That is the same discipline the Claude
// fixtures in composer_test.go were rebuilt under on 2026-08-11, and for the same
// reason: fixtures invented from a description agree with code invented from the
// same description, and both can be wrong together.
const (
	// hermesStatusLine sits ABOVE the composer (Claude's footer sits below), and
	// carries model · context% · turn age.
	hermesStatusLine = " ⚕ x-preview-f-free · 2% · 12m               ─ Say ve /srv dizin..."
	// hermesRule is the composer's border: a PURE run of ─, no label inside.
	hermesRule = "────────────────────────────────────────────────────────────────────"
	// hermesIdleRow is the idle composer: prompt marker at column 0, placeholder
	// behind it.
	hermesIdleRow = "❯ Ask anything, or type / for commands…"
	// hermesIdleRowAnsi is the SAME row as tmux capture-pane -e returns it. The
	// placeholder is ITALIC (\x1b[3m) and coloured — it is NOT dim, so StripDim
	// leaves it in place. This byte string is the whole reason
	// hermesPlaceholderOnly exists.
	hermesIdleRowAnsi = "\x1b[38;5;230m❯ \x1b[3m\x1b[38;5;136mAsk anything, or type / for commands…\x1b[0m"
	// hermesGhostRowAnsi is the placeholder's OTHER face, measured on
	// probot-outreach-ig-error (2026-08-22): a rotating suggestion in the same
	// italic wrapper. The first five real Hermes agents on this fleet all
	// queued forever behind "composer'da yabanci metin var" because the text
	// filter knew only the "Ask anything" sentence — the attribute, not the
	// words, is the signature.
	hermesGhostRowAnsi = "\x1b[38;5;230m❯ \x1b[3m\x1b[38;5;136mDraft a reply to the last email in my inbox\x1b[0m"
	// hermesBusyRow is the busy composer with nothing typed into it: the
	// "msg=interrupt · …" text is the BUSY PLACEHOLDER, not a fixed affordance.
	hermesBusyRow = "⚕ ❯ msg=interrupt · /queue · /bg · /steer · Ctrl+C cancel"
	// hermesBusyTypedRow is the state that corrected the first version of the busy
	// matcher: a busy pane whose composer holds text. The placeholder is gone, the
	// caduceus stays.
	hermesBusyTypedRow = "⚕ ❯ ucuncu satir da var"
	// hermesIdleTypedRow is the same text on an IDLE pane — no caduceus.
	hermesIdleTypedRow = "❯ ucuncu satir da var"
	// hermesKaomoji is one frame of the spinner. Two-space indent, rotating
	// emoticon and verb; deliberately not matched by anything.
	hermesKaomoji = "  (¬_¬) processing..."
)

// hermesTranscript is what sits above the composer: Hermes' answer box (whose
// header carries a caduceus that must NOT be mistaken for the status row) and the
// user-echo block, which is fenced by its own PURE rules — the reason
// hermesComposerBox must take the LAST rule in the capture rather than any rule.
func hermesTranscript() []string {
	return []string{
		"╭─ ⚕ Hermes ───────────────────────────────────────────────────────╮",
		"/srv dizini; blueprint, kavram, outpost gibi klasorler iceriyor.",
		"╰──────────────────────────────────────────────────────────────────╯",
		"────────────────────────────────────────",
		"● Lutfen terminal aracinla `sleep 45` calistir, sonra bitti de.",
		"────────────────────────────────────────",
		"",
	}
}

// hermesPane wraps composer rows in the live IDLE structure.
func hermesPane(rows ...string) string {
	lines := hermesTranscript()
	lines = append(lines, hermesStatusLine, hermesRule)
	lines = append(lines, rows...)
	lines = append(lines, hermesRule, "")
	return strings.Join(lines, "\n")
}

// hermesBusyPane is the live BUSY structure: a kaomoji frame above the status
// row, and the caduceus-prefixed composer between the rules.
func hermesBusyPane(composer string) string {
	lines := hermesTranscript()
	lines = append(lines, hermesKaomoji, "", hermesStatusLine, hermesRule, composer, hermesRule, "")
	return strings.Join(lines, "\n")
}

func TestHermesPaneRecognisesTheMeasuredScreens(t *testing.T) {
	tests := []struct {
		name string
		pane string
		want bool
	}{
		{"idle composer", hermesPane(hermesIdleRow), true},
		{"idle composer with ansi", hermesPane(hermesIdleRowAnsi), true},
		{"idle composer holding text", hermesPane(hermesIdleTypedRow), true},
		{"busy composer", hermesBusyPane(hermesBusyRow), true},
		{"busy composer holding text", hermesBusyPane(hermesBusyTypedRow), true},
		// The status row alone is enough: it is the only marker present in both
		// states, and it is what recognises a pane mid-redraw.
		{"status row only", strings.Join([]string{hermesStatusLine, hermesRule, "", hermesRule}, "\n"), true},
		{"claude pane", claudePane(emptyRow), false},
		{"empty capture", "", false},
		// A pane merely QUOTING the placeholder is not Hermes: the prompt marker
		// must start the row.
		{"transcript quoting the placeholder", claudePane("❯ hermes'in composer'i '❯ Ask anything, or type / for commands' yaziyor"), false},
		// Hermes' own answer-box header carries the caduceus, but not at the start
		// of a row and never with "· N% ·" behind it.
		{"answer box header only", strings.Join([]string{"╭─ ⚕ Hermes ──╮", "cevap", "╰──╯"}, "\n"), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := HermesPane(test.pane); got != test.want {
				t.Fatalf("HermesPane = %v, want %v for:\n%s", got, test.want, test.pane)
			}
		})
	}
}

// The status row is the only marker present in BOTH states, so it must identify
// the pane on its own — that is what recognises a Hermes caught mid-redraw, with
// neither composer row readable.
func TestHermesStatusRowAloneIdentifiesThePane(t *testing.T) {
	if !HermesPane(hermesStatusLine + "\n" + hermesRule + "\n") {
		t.Fatal("the live status row did not identify a Hermes pane")
	}
}

func TestHermesIdlePlaceholderIsNotTypedText(t *testing.T) {
	// The measured bug: Hermes draws its placeholder ITALIC, not dim, so it
	// survives StripDim. Without hermesPlaceholderOnly every idle Hermes pane
	// reads "somebody is typing" and every message to that agent queues forever.
	pane := hermesPane(hermesIdleRowAnsi)
	if Typing(pane) {
		t.Fatalf("idle Hermes placeholder counted as typed text: %q", composerContent(pane))
	}
	if composerFilled(pane) {
		t.Fatal("idle Hermes composer read as filled")
	}
	if reason := ComposerBlockReason(pane, nil); reason != "" {
		t.Fatalf("idle Hermes pane blocked with %q", reason)
	}
	// Real text on the same pane must still be seen.
	if !Typing(hermesPane(hermesIdleTypedRow)) {
		t.Fatal("real composer text on a Hermes pane was not seen")
	}
	// The rotating ghost suggestion is placeholder too — recognised by its
	// italic wrapper, whatever sentence it happens to show.
	ghost := hermesPane(hermesGhostRowAnsi)
	if Typing(ghost) {
		t.Fatalf("Hermes ghost suggestion counted as typed text: %q", composerContent(ghost))
	}
	if reason := ComposerBlockReason(ghost, nil); reason != "" {
		t.Fatalf("Hermes ghost suggestion blocked delivery with %q", reason)
	}
	// The same sentence in a NON-Hermes pane must still count as somebody's
	// text: italic is only Hermes' not-input marker, nowhere else's.
	if !Typing(claudeStyle("❯ \x1b[3mDraft a reply to the last email in my inbox\x1b[0m")) {
		t.Fatal("italic text in a non-Hermes pane was wrongly discarded")
	}
}

// claudeStyle builds a minimal non-Hermes pane holding one composer row.
func claudeStyle(row string) string {
	return "some transcript above\n" + row + "\n"
}

// The state every Hermes agent is in for its FIRST message, and the one that
// cost probot-egitim a hand-delivery (2026-08-22): a pane that has not run a
// turn draws "--" where the context percentage goes, and its placeholder is a
// rotating suggestion no text matcher knows. Recognition has to survive both at
// once, or the very first bp msg to a brand-new agent queues forever.
func TestFreshHermesPaneIsRecognisedBeforeItsFirstTurn(t *testing.T) {
	fresh := "\x1b[38;5;250m\x1b[48;5;234m ⚕ \x1b[1m\x1b[38;5;220mx-preview-f-free\x1b[0m\x1b[38;5;101m\x1b[48;5;234m · -- · 3s\x1b[38;5;250m \x1b[39m\x1b[49m\n" +
		"────────────────────────────────────────\n" +
		"\x1b[38;5;230m❯ \x1b[3m\x1b[38;5;136mResearch this topic and write me a brief\x1b[0m\n" +
		"────────────────────────────────────────\n"
	if !HermesPane(fresh) {
		t.Fatal("a fresh Hermes pane was not recognised as Hermes")
	}
	if Typing(fresh) {
		t.Fatalf("fresh-pane placeholder counted as typed text: %q", composerContent(fresh))
	}
	if reason := ComposerBlockReason(fresh, nil); reason != "" {
		t.Fatalf("fresh Hermes pane blocked delivery with %q", reason)
	}
	// A FRESH window is not painted to the bottom: the measured capture put the
	// composer at row 19 and left 20 blank rows under it, which pushed every
	// marker out of the tail window and made the pane read "not Hermes" even
	// after the "--" fix. Same symptom, second cause.
	padded := fresh + strings.Repeat("\n", 20)
	if !HermesPane(padded) {
		t.Fatal("a fresh pane with unpainted rows below the composer was not recognised")
	}
	if reason := ComposerBlockReason(padded, nil); reason != "" {
		t.Fatalf("unpainted-tail pane blocked delivery with %q", reason)
	}

	// The status row alone must carry recognition too: the composer may be
	// mid-redraw in the frame we captured.
	statusOnly := "\x1b[38;5;250m ⚕ x-preview-f-free · -- · 3s\x1b[0m\n"
	if !HermesPane(statusOnly) {
		t.Fatal("the pre-first-turn status row did not identify the pane")
	}
	// And a human typing on that same fresh pane is still seen: measured, typed
	// text carries no italic ("❯ \x1b[39minsan yazisi testi").
	typed := "\x1b[38;5;250m ⚕ x-preview-f-free · -- · 9s\x1b[0m\n" +
		"────────────────────────────────────────\n" +
		"\x1b[38;5;230m❯ \x1b[39minsan yazisi testi\n" +
		"────────────────────────────────────────\n"
	if !Typing(typed) {
		t.Fatal("a human's text on a fresh Hermes pane was discarded as ghost")
	}
}

// The permission prompt measured 2026-08-23 on probot-outreach-ig-kuanta: an
// agent's `curl | python3` heredoc tripped Hermes' security scanner. Every
// ordinary signal here says IDLE — the composer is empty, there is no spinner —
// and that is the trap: bp would have pasted onto the selection list and its
// Enter would have answered "Allow once" on a security dialog.
func TestHermesPermissionPromptIsNeverIdle(t *testing.T) {
	dialog := strings.Join([]string{
		"│ ❯ 1. Allow once                                                │",
		"│   2. Allow for this session                                    │",
		"│   3. Add to permanent allowlist                                │",
		"│   4. Deny                                                      │",
		"╰────────────────────────────────────────────────────────────────╯",
		"  💻 sleep 30 + 27 commands  (04m22s · ↓ 960 tok)",
		"  ↑/↓ to select, Enter to confirm  (62s)",
		" ⚕ x-preview-f-free · 40% · 1.2d             ─ kuanta.md görev d...",
		hermesRule,
		"⚠ ❯",
		hermesRule,
		"",
	}, "\n")
	if !HermesPane(dialog) {
		t.Fatal("the dialog screen was not recognised as Hermes")
	}
	if !hermesDialog(dialog) {
		t.Fatal("permission prompt not detected")
	}
	if got := ComposerBlockReason(dialog, nil); got != BlockedByDialog {
		t.Fatalf("reason = %q, want %q", got, BlockedByDialog)
	}
	// Forced delivery must be refused too: force overrides "the agent is
	// working", never "a human is being asked something".
	if got := ComposerContentBlockReason(dialog, nil); got != BlockedByDialog {
		t.Fatalf("forced reason = %q, want %q", got, BlockedByDialog)
	}
	// Either marker alone is enough — the warning composer without the hint...
	onlyMarker := strings.Join([]string{hermesStatusLine, hermesRule, "⚠ ❯", hermesRule, ""}, "\n")
	if !hermesDialog(onlyMarker) {
		t.Fatal("warning composer alone did not signal a dialog")
	}
	// ...and an ordinary idle pane still delivers.
	if hermesDialog(hermesPane(hermesIdleRow)) {
		t.Fatal("an idle Hermes pane was called a dialog")
	}
	// A non-Hermes pane is never judged by these markers.
	if hermesDialog(claudePane("⚠ ❯")) {
		t.Fatal("a Claude pane was judged by Hermes dialog markers")
	}
}

func TestBusyReadsTheHermesComposerRow(t *testing.T) {
	tests := []struct {
		name string
		pane string
		want bool
	}{
		{"busy, empty composer", hermesBusyPane(hermesBusyRow), true},
		// The correction: a busy pane whose composer holds text has lost the
		// "msg=interrupt" placeholder but kept the caduceus. Reading this as idle
		// is the expensive mistake — Enter here cancels the turn.
		{"busy, composer holding text", hermesBusyPane(hermesBusyTypedRow), true},
		{"idle, placeholder", hermesPane(hermesIdleRow), false},
		{"idle, holding text", hermesPane(hermesIdleTypedRow), false},
		// The kaomoji frame on its own is decoration, not a signature.
		{"kaomoji only", strings.Join([]string{hermesKaomoji, "", hermesRule}, "\n"), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Busy(test.pane); got != test.want {
				t.Fatalf("Busy = %v, want %v for:\n%s", got, test.want, test.pane)
			}
		})
	}
}

func TestHermesIdleIsReadinessNotMereRecognition(t *testing.T) {
	// `bp open --hermes` waits for HermesIdle, which must be false for a booting
	// or working pane even though HermesPane is already true for the latter.
	if !HermesIdle(hermesPane(hermesIdleRow)) {
		t.Fatal("idle Hermes not reported ready")
	}
	if HermesIdle(hermesBusyPane(hermesBusyRow)) {
		t.Fatal("a working Hermes reported ready")
	}
	if HermesIdle(claudePane(emptyRow)) {
		t.Fatal("a Claude pane reported Hermes-ready")
	}
}

func TestHermesComposerBoxReadsAMultiRowPaste(t *testing.T) {
	// Measured: a bracketed multi-line paste renders as raw continuation rows
	// between the rules — there is no paste chip. The box is therefore the only
	// view that can compare what landed against what we injected.
	pane := hermesPane(
		"❯ birinci satir bir mesajin ilk parcasi",
		"ikinci satir devam ediyor burada",
		"ucuncu satir da var",
	)
	box, ok := composerBox(pane)
	if !ok {
		t.Fatalf("no box read from a live-shaped Hermes pane:\n%s", pane)
	}
	if rows := strings.Split(box, "\n"); len(rows) != 3 {
		t.Fatalf("box rows=%d, want 3: %q", len(rows), box)
	}
	if strings.Contains(box, "❯") {
		t.Fatalf("prompt marker not stripped: %q", box)
	}
	if strings.Contains(box, "Hermes") || strings.Contains(box, "x-preview") {
		t.Fatalf("box leaked rows from outside the rules: %q", box)
	}
	// An idle placeholder is an EMPTY box, not a one-row one.
	if box, ok := composerBoxText(hermesPane(hermesIdleRowAnsi)); !ok || box != "" {
		t.Fatalf("idle placeholder box = %q (ok=%v), want empty", box, ok)
	}
	// A BUSY composer has no readable box at all: its row carries the caduceus
	// before the prompt marker, so nothing may be judged — or typed — there.
	if _, ok := composerBox(hermesBusyPane(hermesBusyTypedRow)); ok {
		t.Fatal("a busy Hermes composer was read as a box")
	}
}

func TestHermesClearBudgetGrowsWithTheRenderedRows(t *testing.T) {
	// Measured on a 3-row paste: five C-u presses (content and newline die
	// separately), so the budget must exceed 2N-1 and the fixed bound of 8 is not
	// enough past four rows.
	rows := []string{"❯ satir bir"}
	for i := 0; i < 9; i++ {
		rows = append(rows, "devam satiri")
	}
	got := hermesClearBudget(hermesPane(rows...))
	if got < 2*len(rows)-1 {
		t.Fatalf("budget=%d, want at least %d for %d rows", got, 2*len(rows)-1, len(rows))
	}
	if got > hermesClearAttemptsMax {
		t.Fatalf("budget=%d exceeds the hard ceiling %d", got, hermesClearAttemptsMax)
	}
	if n := hermesClearBudget(claudePane(emptyRow)); n != 0 {
		t.Fatalf("a Claude pane got a Hermes budget: %d", n)
	}
}

func TestIsAgentPaneNeedsBothTheCommandAndTheScreen(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		pane string
		want bool
	}{
		{"claude needs no screen", "claude", "", true},
		{"codex needs no screen", "codex", "", true},
		{"hermes python with hermes screen", "python", hermesPane(hermesIdleRow), true},
		{"hermes python while working", "python", hermesBusyPane(hermesBusyRow), true},
		{"python3 with hermes screen", "python3", hermesPane(hermesIdleRow), true},
		// The whole point: a python pane that is NOT Hermes stays untouchable.
		{"a plain python script", "python", "traceback... loop 44/100\n", false},
		{"python with an unreadable screen", "python", "", false},
		{"a python REPL", "python3", ">>> for x in range(3):\n...     print(x)\n", false},
		{"a shell showing a hermes transcript", "zsh", hermesPane(hermesIdleRow), false},
		{"vim", "vim", hermesPane(hermesIdleRow), false},
		{"nothing at all", "", "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsAgentPane(test.cmd, test.pane); got != test.want {
				t.Fatalf("IsAgentPane(%q, screen)=%v, want %v", test.cmd, got, test.want)
			}
		})
	}
}

func TestIsHermesCommandIsNotAWhitelist(t *testing.T) {
	for _, cmd := range []string{"python", "python3", "hermes"} {
		if !IsHermesCommand(cmd) {
			t.Errorf("IsHermesCommand(%q) = false, want true", cmd)
		}
		// It must NOT have leaked into the command-only whitelist: that would make
		// every python process on the machine a legal keystroke target.
		if IsAgentCommand(cmd) {
			t.Errorf("IsAgentCommand(%q) = true — python must never be whitelisted on the command alone", cmd)
		}
	}
	for _, cmd := range []string{"claude", "codex", "bwrap", "zsh", "node", ""} {
		if IsHermesCommand(cmd) {
			t.Errorf("IsHermesCommand(%q) = true, want false", cmd)
		}
	}
}

// --- delivery -------------------------------------------------------------

// hermesSendHarness is sendHarness with the pane command a live Hermes reports.
// The guard (requireAgentPane) spends one extra plain capture confirming the
// screen, which is why every Hermes send fixture carries one more capture than
// its Claude equivalent.
func hermesSendHarness(captures []string, activities []string) *sendHarness {
	return &sendHarness{command: "python", captures: captures, activities: activities}
}

func TestSendRefusesToForceIntoAWorkingHermes(t *testing.T) {
	// The absolute gate. On Claude/Codex --force-busy delivers into a working
	// pane; on Hermes the same keystrokes CANCEL the turn ("msg=interrupt"), so
	// force is refused and the message queues like any other.
	busy := hermesBusyPane(hermesBusyRow)
	h := hermesSendHarness([]string{busy, busy}, nil)
	if err := testClient(h).SendForce(context.Background(), "target", stuckMessage); !errors.Is(err, ErrBusy) {
		t.Fatalf("err=%v, want ErrBusy: a forced message must never interrupt a Hermes turn", err)
	}
	if len(h.mutations) != 0 {
		t.Fatalf("keys were sent into a working Hermes: %v", h.mutations)
	}
	// And the same refusal for a busy pane whose composer already holds text.
	typed := hermesBusyPane(hermesBusyTypedRow)
	h = hermesSendHarness([]string{typed, typed}, nil)
	if err := testClient(h).SendForce(context.Background(), "target", stuckMessage); !errors.Is(err, ErrBusy) {
		t.Fatalf("err=%v, want ErrBusy", err)
	}
	if len(h.mutations) != 0 {
		t.Fatalf("keys were sent into a working Hermes: %v", h.mutations)
	}
}

func TestSendPastesBracketedIntoAHermesPane(t *testing.T) {
	// Unbracketed, Hermes reads each newline as a submit: a three-line message
	// went out as three, the last two INTERRUPTING the turn the first started.
	// With -p it stays in the composer and leaves on one Enter.
	message := "birinci satir bir mesajin ilk parcasi\nikinci satir devam ediyor burada\nucuncu satir da var"
	idle := hermesPane(hermesIdleRowAnsi)
	pasted := hermesPane(
		"❯ birinci satir bir mesajin ilk parcasi",
		"ikinci satir devam ediyor burada",
		"ucuncu satir da var",
	)
	h := hermesSendHarness(
		[]string{idle, idle, idle, pasted, idle},
		[]string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	)
	if _, err := testClient(h).SendWithPending(context.Background(), "target", message, nil); err != nil {
		t.Fatalf("err=%v, want a verified delivery", err)
	}
	var pastes []string
	for _, m := range h.mutations {
		if strings.HasPrefix(m, "paste-buffer") {
			pastes = append(pastes, m)
		}
	}
	if len(pastes) != 1 {
		t.Fatalf("expected exactly one paste, got %v", pastes)
	}
	if !strings.Contains(pastes[0], " -p ") {
		t.Fatalf("Hermes paste was not bracketed: %q", pastes[0])
	}
	if got := countEnter(h.mutations); got != 1 {
		t.Fatalf("expected 1 Enter, got %d: %v", got, h.mutations)
	}
	if len(h.payloads) != 1 || string(h.payloads[0]) != message {
		t.Fatalf("payload was split or altered: %q", h.payloads)
	}
	assertNoEscape(t, h.mutations)
}

func TestSendKeepsClaudePastesUnbracketed(t *testing.T) {
	// The -p flag is scoped to Hermes: Claude's chip thresholds and Codex's
	// expand-then-submit mechanics were measured under the unbracketed paste this
	// package has always used, and nothing here re-opens them.
	h := &sendHarness{
		captures: []string{
			claudePane(emptyRow), claudePane(emptyRow),
			claudePane("❯ " + stuckMessage), claudePane(emptyRow),
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	}
	if _, err := testClient(h).SendWithPending(context.Background(), "target", stuckMessage, nil); err != nil {
		t.Fatalf("err=%v", err)
	}
	for _, m := range h.mutations {
		if strings.HasPrefix(m, "paste-buffer") && strings.Contains(m, " -p ") {
			t.Fatalf("a Claude paste was bracketed: %q", m)
		}
	}
}

func TestSendRefusesAPythonPaneThatIsNotHermes(t *testing.T) {
	// A python pane whose screen shows anything else is not an agent: the guard
	// must refuse it exactly as it refuses a shell, and before any keystroke.
	h := hermesSendHarness([]string{"loop 44/100\ntrain acc 0.91\n"}, nil)
	if _, err := testClient(h).SendWithPending(context.Background(), "target", stuckMessage, nil); !errors.Is(err, ErrNotAgent) {
		t.Fatalf("err=%v, want ErrNotAgent", err)
	}
	if len(h.mutations) != 0 {
		t.Fatalf("keys were sent into a plain python pane: %v", h.mutations)
	}
}

func TestClearComposerAcceptsAHermesPane(t *testing.T) {
	// ClearComposer's own guard must see through the "python" command too, and it
	// must press C-u — never Escape — on the way.
	idle := hermesPane(hermesIdleRowAnsi)
	h := hermesSendHarness([]string{idle, idle, idle}, nil)
	if err := testClient(h).ClearComposer(context.Background(), "target"); err != nil {
		t.Fatalf("err=%v", err)
	}
	if got := countKey(h.mutations, "C-u"); got != 1 {
		t.Fatalf("expected 1 C-u, got %d: %v", got, h.mutations)
	}
	assertNoEscape(t, h.mutations)
}
