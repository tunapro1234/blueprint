package tmux

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTypingUsesOnlyLastPrompt(t *testing.T) {
	pane := "❯ old message\nresponse\n  ❯ \u00a0  \t\n"
	if Typing(pane) {
		t.Fatal("empty final composer was reported as typing")
	}
	pane += "output\n  ❯ new message\n"
	if !Typing(pane) {
		t.Fatal("non-empty final composer was not reported as typing")
	}
}

func TestTypingRecognizesCodexPrompt(t *testing.T) {
	if Typing("output\n  › \u00a0 \t\n") {
		t.Fatal("empty Codex composer was reported as typing")
	}
	if !Typing("output\n  › draft message\n") {
		t.Fatal("non-empty Codex composer was not reported as typing")
	}
}

func TestTypingNoPrompt(t *testing.T) {
	if Typing("normal output\n❯\u00a0\nmore output") {
		t.Fatal("NBSP-only composer must be empty")
	}
}

func TestBusyRequiresLiveIndicator(t *testing.T) {
	// Live indicators: spinner timer or the ⏵ footer.
	if !Busy("✻ Working… (23s · Esc to interrupt)") {
		t.Fatal("spinner line should be busy")
	}
	if !Busy("⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt") {
		t.Fatal("footer line should be busy")
	}
	// Quoted prose, bare phrase and background-shell footers are NOT busy.
	if Busy(`transcript quoting "esc to interrupt" in a rule message`) ||
		Busy("Esc to interrupt") ||
		Busy("⏵⏵ bypass permissions on · 2 shells · esc to interrupt") ||
		Busy("ready") {
		t.Fatal("false positive busy")
	}
}

// Real ANSI capture bytes (Codex v0.144.x, capture-pane -e). U+203A prompt in
// bold, chip label in colour 38;5;6, status line directly below (no composer
// border under Codex, so composerTrail reports found=false).
const (
	collapsedChip = "\x1b[1m›\x1b[0m \x1b[38;5;6m[Pasted Content 1024 chars]\x1b[39m\n  gpt-5.6-sol low · /tmp\n"
	// After the first Enter the chip expands and its label WRAPS across two rows,
	// with the >1024 overflow tail (here "AAA") following it.
	expandedChip = "\x1b[1m›\x1b[0m \x1b[38;5;6m[Pasted Content 1024\x1b[39m\n  \x1b[38;5;6mchars]\x1b[39mAAAAAAAAAAAAAAAAAAAA\n  gpt-5.6-sol low · /tmp\n"
	// Empty composer shows the dim ghost placeholder, stripped by StripDim.
	ghostComposer = "\x1b[1m›\x1b[0m \x1b[2mExplain this codebase\x1b[22m\n  gpt-5.6-sol low · /tmp\n"
	// A BUSY Codex holding our large paste: the "Working" spinner, the chip on
	// the prompt line, and the DIM "tab to queue message" footer affordance.
	// Real capture bytes (probot-builder-worker, Codex v0.144.x): the affordance
	// is dim-styled (\x1b[2m…\x1b[0m), so StripDim removes it.
	busyQueueChip = "\x1b[2mWorking (12s · esc to interrupt)\x1b[0m\n" +
		"\x1b[0;1m›\x1b[0m \x1b[38;5;6m[Pasted Content 1021 chars]\x1b[39mopup'in yildizi siparis akisi.\n" +
		"  \x1b[2mtab to queue message\x1b[0m                                      \x1b[2m30% context left\x1b[0m\n"
)

func TestCodexBusyQueueDetection(t *testing.T) {
	// True on the real busy affordance bytes.
	if !codexBusyQueue(busyQueueChip) {
		t.Fatal("busy Codex queue affordance not detected")
	}
	// False: idle Codex chip has no footer affordance.
	if codexBusyQueue(collapsedChip) || codexBusyQueue(expandedChip) {
		t.Fatal("idle Codex chip reported as busy-queue affordance")
	}
	// False: a normal idle composer.
	if codexBusyQueue(ghostComposer) || codexBusyQueue("output\n  ›   \t\n") {
		t.Fatal("idle composer reported as busy-queue affordance")
	}
	// False: a Claude busy pane (never renders the phrase).
	if codexBusyQueue("✻ Working… (23s · Esc to interrupt)\n❯ \n") {
		t.Fatal("Claude busy pane reported as busy-queue affordance")
	}
	// False-positive guard: a message whose inline (non-dim) text contains the
	// literal words survives StripDim and must NOT trigger.
	if codexBusyQueue("\x1b[1m›\x1b[0m please tab to queue message for me later\n") {
		t.Fatal("literal inline text reported as busy-queue affordance")
	}
}

func TestCodexPasteChipDetection(t *testing.T) {
	// Both live render regimes count as "composer still holds our paste".
	if !codexPasteChip(collapsedChip) {
		t.Fatal("collapsed Codex paste chip not detected")
	}
	if !codexPasteChip(expandedChip) {
		t.Fatal("expanded/wrapped Codex paste chip not detected")
	}
	// Varying digit counts (the reported count is capped/unreliable, so we only
	// match \\d+, never len(message)).
	if !codexPasteChip("› [Pasted Content 2000 chars]\n") {
		t.Fatal("chip with different digit count not detected")
	}
	// Negatives.
	if codexPasteChip(ghostComposer) {
		t.Fatal("empty (ghost) composer reported as chip")
	}
	if codexPasteChip("output\n  ›   \t\n") {
		t.Fatal("blank composer reported as chip")
	}
	if codexPasteChip("❯ real user text\n") {
		t.Fatal("normal composer text reported as chip")
	}
	if codexPasteChip("❯ I just Pasted Content 5 chars into the box\n") {
		t.Fatal("literal message merely containing 'Pasted' reported as chip")
	}
	if codexPasteChip("  gpt-5.6-sol low · /tmp\n") {
		t.Fatal("Codex status line (no prompt) reported as chip")
	}
}

func TestParseClientActivityUsesLatestMatchingClient(t *testing.T) {
	got := parseClientActivity("other\t100\ntarget\t120\ntarget\t125\nbad\tnope\n", "target")
	if got.Unix() != 125 {
		t.Fatalf("activity=%v", got)
	}
	if got := parseClientActivity("other\t100\n", "target"); !got.Equal(time.Time{}) {
		t.Fatalf("unattached session activity=%v", got)
	}
}

type sendHarness struct {
	captures   []string
	activities []string
	mutations  []string
	// command is what display-message reports for #{pane_current_command}. It
	// defaults to "claude" so existing agent-path tests need not set it; set it
	// to a shell name (e.g. "zsh") to exercise the non-agent guard.
	command string
}

func (h *sendHarness) run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	switch args[0] {
	case "display-message":
		cmd := h.command
		if cmd == "" {
			cmd = "claude"
		}
		return []byte(cmd + "\n"), nil
	case "capture-pane":
		value := h.captures[0]
		h.captures = h.captures[1:]
		return []byte(value), nil
	case "list-clients":
		value := h.activities[0]
		h.activities = h.activities[1:]
		return []byte(value), nil
	case "load-buffer", "paste-buffer", "send-keys", "delete-buffer":
		h.mutations = append(h.mutations, strings.Join(args, " "))
		return nil, nil
	default:
		return nil, errors.New("unexpected command: " + strings.Join(args, " "))
	}
}

func testClient(h *sendHarness) *Client {
	return &Client{
		Sleep: func(time.Duration) {},
		Now:   func() time.Time { return time.Unix(1000, 0) },
		exec:  h.run,
	}
}

func TestIsAgentCommand(t *testing.T) {
	for _, cmd := range []string{"claude", "codex", "bwrap"} {
		if !IsAgentCommand(cmd) {
			t.Errorf("IsAgentCommand(%q) = false, want true", cmd)
		}
	}
	for _, cmd := range []string{"zsh", "bash", "sh", "dash", "fish", "tmux", "node", ""} {
		if IsAgentCommand(cmd) {
			t.Errorf("IsAgentCommand(%q) = true, want false", cmd)
		}
	}
}

func TestPaneProcessSelectsActivePane(t *testing.T) {
	client := New()
	client.exec = func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
		if got := strings.Join(args, " "); !strings.Contains(got, "list-panes -t =agent: -F") {
			t.Fatalf("tmux args=%q", got)
		}
		return []byte("0\tzsh\t100\n1\tbwrap\t234\n"), nil
	}
	got, err := client.PaneProcess(context.Background(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	if got.Command != "bwrap" || got.PID != 234 {
		t.Fatalf("pane process=%+v", got)
	}
}

func TestSendRejectsNonAgentPane(t *testing.T) {
	// A pane that dropped to a shell must be rejected with ErrNotAgent BEFORE any
	// paste/keystroke: no capture, no activity check, no mutation ever happens.
	for _, cmd := range []string{"zsh", "bash", "sh", "dash", "fish", "tmux", "node"} {
		h := &sendHarness{command: cmd}
		err := testClient(h).Send(context.Background(), "target", "hello")
		if !errors.Is(err, ErrNotAgent) {
			t.Fatalf("command %q: err=%v, want ErrNotAgent", cmd, err)
		}
		if len(h.mutations) != 0 {
			t.Fatalf("command %q: injected into non-agent pane: %v", cmd, h.mutations)
		}
	}
}

func TestSendProceedsForCodexAgentPane(t *testing.T) {
	// A sandboxed Codex reports pane_current_command "bwrap"; the guard must let
	// delivery proceed exactly as for "claude" — inject once, then Enter to submit.
	h := &sendHarness{
		command:    "bwrap",
		captures:   []string{"› \n", "› \n", "› queued\n", "› \n"},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	}
	if err := testClient(h).Send(context.Background(), "target", "queued"); err != nil {
		t.Fatal(err)
	}
	if countInjections(h.mutations) != 1 {
		t.Fatalf("expected exactly 1 injection, got: %v", h.mutations)
	}
	if got := countEnter(h.mutations); got != 1 {
		t.Fatalf("expected 1 Enter press, got %d: %v", got, h.mutations)
	}
}

func TestSendWaitsForStableEmptyComposer(t *testing.T) {
	h := &sendHarness{
		captures:   []string{"❯ \n", "❯ user started typing\n"},
		activities: []string{"target\t900\n"},
	}
	err := testClient(h).Send(context.Background(), "target", "queued")
	if !errors.Is(err, ErrTyping) {
		t.Fatalf("err=%v", err)
	}
	if len(h.mutations) != 0 {
		t.Fatalf("message was injected during typing: %v", h.mutations)
	}
}

func TestSendSettlesLargePasteBeforeSubmitting(t *testing.T) {
	// The trailing "❯ \n" capture/activity pair is the post-Enter verification
	// pass added by the submit-retry logic: composer cleared, so no retry.
	h := &sendHarness{
		captures: []string{
			"❯ \n",
			"❯ \n",
			"❯ [Pasted text #1 +2 lines]\n",
			"❯ \n",
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	}
	if err := testClient(h).Send(context.Background(), "target", "line one\nline two"); err != nil {
		t.Fatal(err)
	}
	if len(h.mutations) != 3 || !strings.HasPrefix(h.mutations[0], "load-buffer ") ||
		!strings.HasPrefix(h.mutations[1], "paste-buffer ") || h.mutations[2] != "send-keys -t =target: Enter" {
		t.Fatalf("mutations=%v", h.mutations)
	}
}

// countInjections counts text injections. Injection is always a bracketed
// paste (load-buffer + paste-buffer); only paste-buffer is counted so a
// load/paste pair is one injection.
func countInjections(mutations []string) int {
	n := 0
	for _, m := range mutations {
		if strings.HasPrefix(m, "paste-buffer ") {
			n++
		}
	}
	return n
}

func countEnter(mutations []string) int {
	n := 0
	for _, m := range mutations {
		if m == "send-keys -t =target: Enter" {
			n++
		}
	}
	return n
}

func TestSendRetriesEnterWhenComposerStillHoldsMessage(t *testing.T) {
	// Paste detection ate the first Enter: "/compact" is still in the composer.
	// A second Enter submits it; the third verification sees an empty composer.
	h := &sendHarness{
		captures: []string{
			"❯ \n",         // readyToSend pass 1
			"❯ \n",         // readyToSend pass 2
			"❯ /compact\n", // post-inject: message present -> Enter #1
			"❯ /compact\n", // still stuck -> Enter #2 (retry)
			"❯ \n",         // cleared -> stop
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	}
	if err := testClient(h).Send(context.Background(), "target", "/compact"); err != nil {
		t.Fatal(err)
	}
	if got := countEnter(h.mutations); got != 2 {
		t.Fatalf("expected 2 Enter presses, got %d: %v", got, h.mutations)
	}
	// Only one text injection ever — retries never re-inject.
	if countInjections(h.mutations) != 1 {
		t.Fatalf("message was re-injected: %v", h.mutations)
	}
}

func TestSendRetriesEnterWhenCodexPasteChipHoldsMessage(t *testing.T) {
	// A large paste renders as a chip, not literal text, so composerContent never
	// equals the message. Live Codex mechanics: the first Enter EXPANDS the chip
	// (label wraps, overflow tail appears) instead of submitting; a further Enter
	// submits. The retry must recognize BOTH the collapsed and expanded forms as
	// OUR unsubmitted paste and press Enter AGAIN rather than bailing.
	h := &sendHarness{
		captures: []string{
			"› \n", "› \n", // readyToSend
			collapsedChip, // post-inject: collapsed chip -> Enter #1 (expands)
			expandedChip,  // expanded/wrapped chip -> Enter #2 (submits)
			ghostComposer, // cleared -> stop
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	}
	if err := testClient(h).Send(context.Background(), "target", "a very large pasted message"); err != nil {
		t.Fatal(err)
	}
	if got := countEnter(h.mutations); got != 2 {
		t.Fatalf("expected 2 Enter presses (chip retry), got %d: %v", got, h.mutations)
	}
	// At-most-once: the text is injected exactly once, never re-pasted.
	if countInjections(h.mutations) != 1 {
		t.Fatalf("message was re-injected: %v", h.mutations)
	}
}

func TestSendStopsAfterCodexChipSubmits(t *testing.T) {
	// Once an Enter submits the chip (composer returns to the empty ghost
	// placeholder), no further Enter may be pressed.
	h := &sendHarness{
		captures: []string{
			"› \n", "› \n", // readyToSend
			collapsedChip, // post-inject: chip present -> Enter #1
			ghostComposer, // submitted, composer cleared -> stop, no Enter #2
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	}
	if err := testClient(h).Send(context.Background(), "target", "a very large pasted message"); err != nil {
		t.Fatal(err)
	}
	if got := countEnter(h.mutations); got != 1 {
		t.Fatalf("expected exactly 1 Enter press, got %d: %v", got, h.mutations)
	}
}

func TestSendTabQueuesOnBusyCodex(t *testing.T) {
	// TOCTOU: the Codex went BUSY after readyToSend and the paste. On a busy
	// Codex, Enter never submits; submit() must press Tab exactly once to move
	// the paste into Codex's native queue, and stop once the composer clears.
	// No Enter may ever be sent while the affordance is present.
	h := &sendHarness{
		captures: []string{
			"› \n", "› \n", // readyToSend
			busyQueueChip, // post-inject: busy affordance -> Tab (native-queue)
			ghostComposer, // queued, composer cleared -> stop
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	}
	if err := testClient(h).Send(context.Background(), "target", "a very large pasted message"); err != nil {
		t.Fatal(err)
	}
	if got := countKey(h.mutations, "Tab"); got != 1 {
		t.Fatalf("expected exactly 1 Tab press, got %d: %v", got, h.mutations)
	}
	if got := countEnter(h.mutations); got != 0 {
		t.Fatalf("expected NO Enter while busy affordance present, got %d: %v", got, h.mutations)
	}
	// At-most-once: the text is injected exactly once, never re-pasted.
	if countInjections(h.mutations) != 1 {
		t.Fatalf("message was re-injected: %v", h.mutations)
	}
}

func TestSendTabGivesUpWhenAffordancePersists(t *testing.T) {
	// The affordance never clears (Tab somehow ineffective): submit() presses Tab
	// bounded by the retry count, then gives up silently — never falling through
	// to Enter while the busy affordance is present. Each attempt re-captures at
	// the top and again after Tab, so 3 attempts consume 6 busy captures.
	busy := busyQueueChip
	h := &sendHarness{
		captures: []string{
			"› \n", "› \n", // readyToSend
			busy, busy, busy, busy, busy, busy, // 3 attempts (1+2 bound), 2 captures each
		},
		activities: []string{
			"target\t900\n", "target\t900\n",
			"target\t900\n", "target\t900\n", "target\t900\n",
			"target\t900\n", "target\t900\n", "target\t900\n",
		},
	}
	// Nothing observed the composer clear, so the outcome is UNVERIFIED: keys
	// stop, the message is never re-injected, and the caller is told so.
	if err := testClient(h).Send(context.Background(), "target", "a very large pasted message"); !errors.Is(err, ErrUnverified) {
		t.Fatalf("err=%v, want ErrUnverified", err)
	}
	if got := countEnter(h.mutations); got != 0 {
		t.Fatalf("expected NO Enter ever, got %d: %v", got, h.mutations)
	}
	if got := countKey(h.mutations, "Tab"); got != 3 {
		t.Fatalf("expected 3 Tab presses (1+2 bound), got %d: %v", got, h.mutations)
	}
}

func TestSendRetryStopsAtBound(t *testing.T) {
	// Composer never clears; retries are bounded to 1 initial + 2 retries.
	h := &sendHarness{
		captures: []string{
			"❯ \n", "❯ \n",
			"❯ /compact\n", "❯ /compact\n", "❯ /compact\n",
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	}
	// Nothing observed the composer clear, so the outcome is UNVERIFIED: keys
	// stop, the message is never re-injected, and the caller is told so.
	if err := testClient(h).Send(context.Background(), "target", "/compact"); !errors.Is(err, ErrUnverified) {
		t.Fatalf("err=%v, want ErrUnverified", err)
	}
	if got := countEnter(h.mutations); got != 3 {
		t.Fatalf("expected 3 Enter presses (1+2 bound), got %d: %v", got, h.mutations)
	}
}

func TestSendRetryStopsWhenUserEditsAfterInjection(t *testing.T) {
	// After the first Enter the composer content changed to something that is
	// not our message: the user is editing, so no further Enter is pressed.
	h := &sendHarness{
		captures: []string{
			"❯ \n", "❯ \n",
			"❯ /compact\n",               // Enter #1
			"❯ /compact and user text\n", // differs from ours -> stop, no Enter #2
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	}
	// Nothing observed the composer clear, so the outcome is UNVERIFIED: keys
	// stop, the message is never re-injected, and the caller is told so.
	if err := testClient(h).Send(context.Background(), "target", "/compact"); !errors.Is(err, ErrUnverified) {
		t.Fatalf("err=%v, want ErrUnverified", err)
	}
	if got := countEnter(h.mutations); got != 1 {
		t.Fatalf("expected 1 Enter press, got %d: %v", got, h.mutations)
	}
}

func TestSendRetryStopsWhenClientActiveAfterInjection(t *testing.T) {
	// A recent attached-client activity between the first and second Enter must
	// abort the retry even though our message is still present.
	h := &sendHarness{
		captures: []string{
			"❯ \n", "❯ \n",
			"❯ /compact\n", // Enter #1 (activity old)
			"❯ /compact\n", // still present but activity now recent -> stop
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t1000\n"},
	}
	// Nothing observed the composer clear, so the outcome is UNVERIFIED: keys
	// stop, the message is never re-injected, and the caller is told so.
	if err := testClient(h).Send(context.Background(), "target", "/compact"); !errors.Is(err, ErrUnverified) {
		t.Fatalf("err=%v, want ErrUnverified", err)
	}
	if got := countEnter(h.mutations); got != 1 {
		t.Fatalf("expected 1 Enter press, got %d: %v", got, h.mutations)
	}
}

const composerBorder = "──────────────────────────────\n"

func TestComposerTrailDetection(t *testing.T) {
	// Multiline-stuck: message plus one literal trailing newline before the border.
	empty, foreign, found := composerTrail("❯ /compact\n\n" + composerBorder + "  -- INSERT --\n")
	if !found || foreign || empty != 1 {
		t.Fatalf("stuck state: empty=%d foreign=%v found=%v", empty, foreign, found)
	}
	// Two trailing newlines (an extra blind Enter already appended one).
	empty, foreign, found = composerTrail("❯ /compact\n\n\n" + composerBorder)
	if !found || foreign || empty != 2 {
		t.Fatalf("double stuck state: empty=%d foreign=%v found=%v", empty, foreign, found)
	}
	// Clean single-line composer: nothing between prompt and border.
	empty, foreign, found = composerTrail("❯ /compact\n" + composerBorder)
	if !found || foreign || empty != 0 {
		t.Fatalf("clean state: empty=%d foreign=%v found=%v", empty, foreign, found)
	}
	// WRAPPED long message: continuation line is non-empty -> foreign, never
	// counted as trailing newlines (false-positive guard).
	empty, foreign, found = composerTrail("❯ a very long single line message that\n  wraps onto a second rendered line\n" + composerBorder)
	if !found || !foreign || empty != 0 {
		t.Fatalf("wrapped state: empty=%d foreign=%v found=%v", empty, foreign, found)
	}
	// Wrap followed by a trailing newline still counts as foreign (conservative).
	if _, foreign, _ = composerTrail("❯ long message that\n  wraps here\n\n" + composerBorder); !foreign {
		t.Fatal("wrap+newline must be foreign")
	}
	// No border after the prompt (unfamiliar UI) -> not found.
	if _, _, found = composerTrail("❯ /compact\n"); found {
		t.Fatal("border-less pane must report found=false")
	}
	// ANSI-coloured border (real CaptureAnsi output renders it 38;5;244).
	pane := "\x1b[39m❯  \x1b[38;5;153m/compact\x1b[39m\n\n\x1b[38;5;244m──────────\x1b[39m\n"
	empty, foreign, found = composerTrail(pane)
	if !found || foreign || empty != 1 {
		t.Fatalf("ansi stuck state: empty=%d foreign=%v found=%v", empty, foreign, found)
	}
}

func countKey(mutations []string, key string) int {
	n := 0
	for _, m := range mutations {
		if m == "send-keys -t =target: "+key {
			n++
		}
	}
	return n
}

func TestSendRecoversMultilineStuckComposerWithBackspace(t *testing.T) {
	// Old-build paste detection left "/compact\n" in the composer: last ❯ line
	// holds the message and one EMPTY line sits before the border. Enter must
	// NOT be pressed blindly; one BSpace removes the newline, the re-capture
	// verifies a single-line composer with exactly our message, then one Enter.
	h := &sendHarness{
		captures: []string{
			"❯ \n", "❯ \n", // readyToSend
			"❯ /compact\n" + composerBorder,   // post-inject -> Enter #1
			"❯ /compact\n\n" + composerBorder, // multiline-stuck -> recovery
			"❯ /compact\n" + composerBorder,   // post-BSpace verify -> Enter #2
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	}
	// Nothing observed the composer clear, so the outcome is UNVERIFIED: keys
	// stop, the message is never re-injected, and the caller is told so.
	if err := testClient(h).Send(context.Background(), "target", "/compact"); !errors.Is(err, ErrUnverified) {
		t.Fatalf("err=%v, want ErrUnverified", err)
	}
	if got := countKey(h.mutations, "BSpace"); got != 1 {
		t.Fatalf("expected 1 BSpace, got %d: %v", got, h.mutations)
	}
	if got := countEnter(h.mutations); got != 2 {
		t.Fatalf("expected 2 Enter presses, got %d: %v", got, h.mutations)
	}
	if countInjections(h.mutations) != 1 {
		t.Fatalf("message was re-injected: %v", h.mutations)
	}
	// Order: inject (load+paste), Enter, BSpace, Enter.
	keys := h.mutations[2:]
	want := []string{
		"send-keys -t =target: Enter",
		"send-keys -t =target: BSpace",
		"send-keys -t =target: Enter",
	}
	if strings.Join(keys, "|") != strings.Join(want, "|") {
		t.Fatalf("mutations=%v", h.mutations)
	}
}

func TestSendGivesUpWhenBackspaceIneffective(t *testing.T) {
	// Old-build vim insert boundary: BSpace cannot cross the newline, the empty
	// line persists. Give up silently — no further Enter, no more BSpace.
	h := &sendHarness{
		captures: []string{
			"❯ \n", "❯ \n", // readyToSend
			"❯ /compact\n" + composerBorder,   // post-inject -> Enter #1
			"❯ /compact\n\n" + composerBorder, // multiline-stuck -> recovery
			"❯ /compact\n\n" + composerBorder, // BSpace ignored -> give up
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	}
	// Nothing observed the composer clear, so the outcome is UNVERIFIED: keys
	// stop, the message is never re-injected, and the caller is told so.
	if err := testClient(h).Send(context.Background(), "target", "/compact"); !errors.Is(err, ErrUnverified) {
		t.Fatalf("err=%v, want ErrUnverified", err)
	}
	if got := countEnter(h.mutations); got != 1 {
		t.Fatalf("expected exactly 1 Enter press, got %d: %v", got, h.mutations)
	}
	if got := countKey(h.mutations, "BSpace"); got != 1 {
		t.Fatalf("expected 1 BSpace, got %d: %v", got, h.mutations)
	}
}

func TestSendRecoveryStopsWhenContentChangesAfterBackspace(t *testing.T) {
	// If after the BSpaces the composer no longer holds exactly our message
	// (user snuck an edit in), no Enter may follow.
	h := &sendHarness{
		captures: []string{
			"❯ \n", "❯ \n",
			"❯ /compact\n" + composerBorder,
			"❯ /compact\n\n" + composerBorder,
			"❯ /compac\n" + composerBorder, // BSpace ate message text instead -> stop
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	}
	// Nothing observed the composer clear, so the outcome is UNVERIFIED: keys
	// stop, the message is never re-injected, and the caller is told so.
	if err := testClient(h).Send(context.Background(), "target", "/compact"); !errors.Is(err, ErrUnverified) {
		t.Fatalf("err=%v, want ErrUnverified", err)
	}
	if got := countEnter(h.mutations); got != 1 {
		t.Fatalf("expected exactly 1 Enter press, got %d: %v", got, h.mutations)
	}
}

func TestSendDoesNotSubmitIfUserTypesAfterInjection(t *testing.T) {
	h := &sendHarness{
		captures:   []string{"❯ \n", "❯ \n", "❯ queued plus user text\n"},
		activities: []string{"target\t900\n", "target\t900\n", "target\t1000\n"},
	}
	// Nothing observed the composer clear, so the outcome is UNVERIFIED: keys
	// stop, the message is never re-injected, and the caller is told so.
	if err := testClient(h).Send(context.Background(), "target", "queued"); !errors.Is(err, ErrUnverified) {
		t.Fatalf("err=%v, want ErrUnverified", err)
	}
	if len(h.mutations) != 2 || !strings.HasPrefix(h.mutations[0], "load-buffer ") ||
		!strings.HasPrefix(h.mutations[1], "paste-buffer ") {
		t.Fatalf("user text was submitted or message was retried: %v", h.mutations)
	}
}

func TestAuthExpiredDetection(t *testing.T) {
	// (a) The real state: an empty composer with the banner in the STATUS FOOTER
	// under the composer border, exactly where Claude Code draws it.
	expired := "  \x1b[38;5;153m> summarize the repo\x1b[39m\n" +
		"\x1b[39m❯  \x1b[39m\n" +
		"\x1b[38;5;244m" + strings.TrimSuffix(composerBorder, "\n") + "\x1b[39m\n" +
		"  \x1b[2m⏵⏵ bypass permissions on\x1b[0m   \x1b[31m●\x1b[39m Login expired · Please run /login\n"
	if !AuthExpired(expired) {
		t.Fatal("expired-login footer not detected")
	}
	// A dim-rendered footer must still be found: only ANSI colour is stripped.
	if !AuthExpired("❯  \n" + composerBorder + "\x1b[2m● Login expired · Please run /login\x1b[0m\n") {
		t.Fatal("dim footer banner not detected")
	}

	// (b) A healthy pane whose TRANSCRIPT carries the phrase — every agent that
	// discussed the 2026-08-01 incident does. This must never block delivery.
	transcript := "  ● Login expired · Please run /login is the banner we are fixing\n" +
		"  I grepped for \"login expired\" across the fleet\n" +
		"❯  \n" + composerBorder +
		"  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"
	if AuthExpired(transcript) {
		t.Fatal("quoted banner in the transcript reported as expired login")
	}

	// (c) The phrase as the operator's own composer text, both on the prompt line
	// and wrapped onto a continuation row inside the box.
	typed := "❯ why does ● Login expired · Please run /login show up?\n" + composerBorder
	wrapped := "❯ why does ● Login expired ·\n  Please run /login show up?\n" + composerBorder
	if AuthExpired(typed) || AuthExpired(wrapped) {
		t.Fatal("composer text reported as expired login")
	}

	// Guards: a healthy footer, a busy pane, and a pane with no composer at all.
	if AuthExpired("❯  \n"+composerBorder+"  ⏵⏵ bypass permissions on\n") ||
		AuthExpired("✻ Working… (23s · Esc to interrupt)\n") ||
		AuthExpired("● Login expired · Please run /login\n") {
		t.Fatal("false positive expired login")
	}
	// The bullet is required: a bare footer line of prose does not match.
	if AuthExpired("❯  \n" + composerBorder + "  login expired means the session died\n") {
		t.Fatal("footer prose without the status bullet reported as expired login")
	}
}

func TestClassifyComposer(t *testing.T) {
	want := stripSpace("deploy the new bar chips")
	cases := []struct {
		name    string
		pane    string
		verdict composerVerdict
	}{
		{"empty", "❯  \n" + composerBorder, composerCleared},
		{"exact", "❯ deploy the new bar chips\n" + composerBorder, composerMine},
		{"wrapped first row only", "❯ deploy the new\n" + composerBorder, composerMine},
		{"user appended", "❯ deploy the new bar chips please\n" + composerBorder, composerMine},
		{"claude paste chip", "❯ [Pasted text #1 +12 lines]\n" + composerBorder, composerMine},
		{"codex paste chip", collapsedChip, composerMine},
		{"foreign fragment", "❯ ls -la /srv\n" + composerBorder, composerOther},
	}
	for _, tc := range cases {
		if got := classifyComposer(tc.pane, want); got != tc.verdict {
			t.Errorf("%s: verdict=%d, want %d", tc.name, got, tc.verdict)
		}
	}
}

func TestSendRefusesExpiredLoginBeforePasting(t *testing.T) {
	// The 2026-08-01 incident state: the composer is empty and stable, but the
	// session's auth expired and it consumes nothing. Nothing may be injected,
	// and the caller must learn the message did NOT land.
	h := &sendHarness{
		captures: []string{"  earlier transcript output\n❯  \n" + composerBorder +
			"  ⏵⏵ bypass permissions on   ● Login expired · Please run /login\n"},
		activities: []string{},
	}
	err := testClient(h).Send(context.Background(), "target", "a 600 character brief")
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("err=%v, want ErrNotReady", err)
	}
	if len(h.mutations) != 0 {
		t.Fatalf("pasted into an expired-login pane: %v", h.mutations)
	}
	if !strings.Contains(err.Error(), "Login expired") {
		t.Fatalf("error does not name the reason: %v", err)
	}
}

func TestSendReportsFailureWhenComposerHoldsForeignText(t *testing.T) {
	// The incident's second half: the paste never landed and the composer holds
	// a short fragment left from an earlier interaction. That is proof of
	// failure, not ambiguity — no Enter is pressed on the foreign text and the
	// caller is told to queue the message.
	h := &sendHarness{
		captures: []string{
			"❯ \n", "❯ \n", // readyToSend
			"❯ /rename wor\n" + composerBorder, // 12 unrelated chars where our brief should be
			"❯ /rename wor\n" + composerBorder, // still-frame check: the screen is not moving
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n"},
	}
	err := testClient(h).Send(context.Background(), "target", strings.Repeat("brief ", 100))
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("err=%v, want ErrNotReady", err)
	}
	if got := countEnter(h.mutations); got != 0 {
		t.Fatalf("pressed Enter on foreign composer text: %v", h.mutations)
	}
	if countInjections(h.mutations) != 1 {
		t.Fatalf("message was re-injected: %v", h.mutations)
	}
}

func TestSendVerifiesHoldThenClear(t *testing.T) {
	// The only outcome that may be called "sent": the composer was observed
	// holding exactly our message and then observed empty.
	h := &sendHarness{
		captures: []string{
			"❯ \n", "❯ \n", // readyToSend
			"❯ [ada] hello\n" + composerBorder, // holds ours -> Enter
			"❯  \n" + composerBorder,           // cleared -> verified
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	}
	if err := testClient(h).Send(context.Background(), "target", "[ada] hello"); err != nil {
		t.Fatalf("verified send returned %v", err)
	}
	if got := countEnter(h.mutations); got != 1 {
		t.Fatalf("expected 1 Enter, got %d: %v", got, h.mutations)
	}
}

func TestSendUnverifiedWhenComposerNeverHeldMessage(t *testing.T) {
	// Straight after the paste the composer is empty. It may have auto-submitted,
	// or the pane may have swallowed the paste; nothing distinguishes the two.
	// No key is pressed, nothing is re-injected, and the outcome is UNVERIFIED
	// rather than a silent "sent".
	h := &sendHarness{
		captures: []string{
			"❯ \n", "❯ \n", // readyToSend
			"❯  \n" + composerBorder, // never seen holding our text
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n"},
	}
	err := testClient(h).Send(context.Background(), "target", "a 600 character brief")
	if !errors.Is(err, ErrUnverified) {
		t.Fatalf("err=%v, want ErrUnverified", err)
	}
	if got := countEnter(h.mutations); got != 0 {
		t.Fatalf("expected no Enter, got %d: %v", got, h.mutations)
	}
	if countInjections(h.mutations) != 1 {
		t.Fatalf("message was re-injected: %v", h.mutations)
	}
}

func TestSendDeliversToPaneQuotingTheBanner(t *testing.T) {
	// Live regression (2026-08-04): a perfectly healthy agent that had been
	// DISCUSSING the expired-login incident carried "Login expired" in its
	// scrollback, and a whole-pane match turned its delivery into a permanent
	// queue. The transcript must not gate delivery — this pane gets its message.
	quoting := "  we are fixing the ● Login expired · Please run /login banner\n" +
		"  grep -ci \"login expired\" -> 1\n"
	healthy := composerBorder + "  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"
	h := &sendHarness{
		captures: []string{
			quoting + "❯  \n" + healthy,                          // readyToSend pass 1
			quoting + "❯  \n" + healthy,                          // readyToSend pass 2
			quoting + "❯ [ada] yanlis-pozitif testi\n" + healthy, // holds ours -> Enter
			quoting + "❯  \n" + healthy,                          // cleared -> verified
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	}
	if err := testClient(h).Send(context.Background(), "target", "[ada] yanlis-pozitif testi"); err != nil {
		t.Fatalf("healthy pane quoting the banner was refused: %v", err)
	}
	if countInjections(h.mutations) != 1 || countEnter(h.mutations) != 1 {
		t.Fatalf("mutations=%v", h.mutations)
	}
}
