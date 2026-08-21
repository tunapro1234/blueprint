package tmux

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
)

var promptLine = regexp.MustCompile("^(?:\x1b\\[[0-9;]*m|[\t ])*[❯›](?:\x1b\\[[0-9;]*m)?")

// Typing reports whether the final rendered composer line contains real text.
// Older prompt lines are deliberately ignored.
func Typing(pane string) bool {
	return composerContent(pane) != ""
}

// composerContent returns the whitespace-stripped text of the final rendered
// composer line, after the prompt marker, with dim placeholder/ghost text and
// ANSI colour removed. NBSP counts as whitespace (unicode.IsSpace covers it),
// so an "empty" composer padded with NBSP returns "".
func composerContent(pane string) string {
	var composer string
	for _, line := range strings.Split(pane, "\n") {
		if promptLine.MatchString(line) {
			composer = line
		}
	}
	if composer == "" {
		return ""
	}
	composer = StripDim(composer) // dim placeholder/ghost metni gercek yazi DEGIL (2026-07-10)
	after := promptLine.ReplaceAllString(composer, "")
	return stripSpace(after)
}

var codexChip = regexp.MustCompile(`\[Pasted Content\s+\d+\s+chars\]`)

// codexPasteChip reports whether Codex's large-paste placeholder chip is present
// in the composer. Codex replaces a large (>~1024 char) bracketed paste with a
// "[Pasted Content N chars]" chip instead of rendering the literal text, so the
// space-collapsed composerContent can never equal the literal message and
// submit()'s retry guard would otherwise bail without pressing Enter again. This
// detector lets submit() recognize the composer still holds OUR own unsubmitted
// paste so it keeps pressing Enter until the composer clears.
//
// Live-observed Codex mechanics (v0.144.x), which drive the shape of this
// matcher:
//   - A fresh large paste renders as the COLLAPSED chip "› [Pasted Content 1024
//     chars]" on a single line (the count is capped at 1024 and is unreliable —
//     a 1216- and a 2000-char paste both report 1024 — so it is matched only as
//     \d+, never compared to len(message)).
//   - The FIRST Enter does NOT submit; it EXPANDS the chip, revealing the >1024
//     overflow tail after the label. The styled chip label then WRAPS across two
//     rendered rows (".. Content 1024" / "chars] <overflow…>"), so the label is
//     no longer a single line and the trailing prompt line is not the whole chip.
//   - A FURTHER Enter submits the expanded paste and clears the composer.
//
// To count both the collapsed and expanded/wrapped forms as "holds our paste",
// detection joins every rendered row from the final prompt line to the end of the
// pane (composer + overflow + status line, never the transcript above), strips
// dim/ANSI and the prompt marker, and matches the chip label anywhere with
// whitespace-tolerant spacing (\s+) so a wrap splitting "1024␤chars]" still
// matches. A message that merely contains the word "Pasted" lacks the bracketed
// "[Pasted Content N chars]" form and does not match.
func codexPasteChip(pane string) bool {
	tail, ok := composerTail(pane)
	return ok && codexChip.MatchString(tail)
}

// claudeChip matches Claude Code's own large/multiline paste placeholder
// ("[Pasted text #1 +2 lines]"). Like the Codex chip it stands IN PLACE of the
// literal message, so a composer showing it is holding OUR paste even though
// composerContent can never equal the message.
var claudeChip = regexp.MustCompile(`\[Pasted text[^\]]*\]`)

// claudePasteChip reports whether the composer shows Claude's paste placeholder.
// It is used only to CLASSIFY what the composer holds (classifyComposer), never
// to authorize another Enter: composerHoldsMessage deliberately stays limited to
// the literal text and the Codex chip, whose retry mechanics are known.
func claudePasteChip(pane string) bool {
	tail, ok := composerTail(pane)
	return ok && claudeChip.MatchString(tail)
}

// composerTail joins every rendered row from the final prompt line to the end of
// the pane (composer + overflow + status line, never the transcript above) with
// dim/ANSI and the prompt marker removed, so a chip label split by a wrap still
// matches as one string. ok is false when the pane has no prompt line at all.
func composerTail(pane string) (string, bool) {
	lines := strings.Split(pane, "\n")
	last := -1
	for i, line := range lines {
		if promptLine.MatchString(line) {
			last = i
		}
	}
	if last < 0 {
		return "", false
	}
	var b strings.Builder
	for _, line := range lines[last:] {
		b.WriteString(promptLine.ReplaceAllString(StripDim(line), ""))
		b.WriteByte(' ') // a wrap between rows is whitespace, not a join
	}
	return b.String(), true
}

// codexBusyQueuePhrase is the dim footer affordance a BUSY Codex renders under
// the composer: while a turn is running, Enter does NOT submit (it only expands
// the paste chip) and the message must instead be pushed into Codex's OWN native
// queue with Tab. The footer reads "tab to queue message".
const codexBusyQueuePhrase = "tab to queue message"

// codexBusyQueue reports whether the pane shows Codex's busy-composer queue
// affordance, meaning submit() must press Tab (native-queue) rather than Enter.
//
// The phrase is matched on the RAW pane (it is dim-rendered, and StripDim would
// delete it), then gated against false positives: the affordance is a dim footer
// line, so it disappears under StripDim. A user message that merely contains the
// literal words "tab to queue message" is NOT dim, survives StripDim, and is
// therefore rejected. (Codex hides large pastes behind a chip, so such text can
// only reach the composer as a small literal message — still guarded here.)
// Codex-only by construction: Claude never renders this phrase, so a Claude
// target can never be sent Tab.
func codexBusyQueue(pane string) bool {
	if !strings.Contains(pane, codexBusyQueuePhrase) {
		return false
	}
	// Dim footer -> removed by StripDim. If it survives, it is literal composer
	// content (a user message), not the affordance: reject.
	return !strings.Contains(StripDim(pane), codexBusyQueuePhrase)
}

// composerHoldsMessage reports whether the composer still holds exactly our
// unsubmitted message: the FULL box content, the literal text on the final row
// (space-collapsed match), or the Codex large-paste chip that stands in for it.
// When true, pressing Enter again is safe; when false the composer either
// cleared (submitted) or a user edited it, and no key may be sent.
//
// The box is consulted first and only ever WIDENS the answer, and only on proof:
// a message that WRAPPED across several rows has no single row equal to it, so
// the row-only test used to bail without pressing Enter again and leave the
// message hanging. The box compares the whole content, which is exactly the
// evidence needed to press Enter on our own multi-row paste.
func composerHoldsMessage(pane, want string) bool {
	if box, ok := composerBoxText(pane); ok && box == want {
		return true
	}
	return composerContent(pane) == want || codexPasteChip(pane)
}

// composerVerdict classifies what the composer holds right after we pasted a
// message into it.
type composerVerdict int

const (
	// composerCleared: nothing is typed. After the composer was seen holding our
	// message this is a submit; before that it proves nothing either way.
	composerCleared composerVerdict = iota
	// composerMine: our message, in one of the forms a TUI renders it in — the
	// literal text, a Codex/Claude paste chip standing in for it, the first
	// rendered row of a message that WRAPPED (a prefix), or our text with
	// something appended (a user typing after our injection).
	composerMine
	// composerOther: content that has nothing to do with our message. Our paste
	// never landed — this is proof of failure, not ambiguity. It is the state the
	// 2026-08-01 incident left behind: a 600-character brief was "sent" while the
	// composer held a 12-character fragment from an earlier interaction.
	composerOther
)

// classifyComposer compares the rendered composer against the whitespace-stripped
// message we injected. The prefix tests run in BOTH directions so neither a
// wrapped render (we see only the first row) nor a user's later keystrokes (our
// text plus theirs) is ever mistaken for a foreign composer: only content with no
// relation at all to ours is called composerOther, because that verdict makes the
// caller queue the message for another delivery attempt.
// When the composer BOX can be read it is preferred over the single row, because
// it is the complete content: a message that merely WRAPPED reads as an exact
// match instead of a prefix, and text that is genuinely unrelated is recognised
// as such even when its first row happens to start like ours.
func classifyComposer(pane, want string) composerVerdict {
	if codexPasteChip(pane) || claudePasteChip(pane) {
		return composerMine
	}
	got, boxed := composerJudgeText(pane)
	if !boxed {
		got = composerContent(pane)
	}
	if got == "" {
		return composerCleared
	}
	if got == want || strings.HasPrefix(want, got) || strings.HasPrefix(got, want) {
		return composerMine
	}
	return composerOther
}

// composerTrail inspects the rendered lines between the final composer line
// (last ❯/› prompt) and the bottom border of the composer box (a run of ─/━).
// It reports how many trailing EMPTY lines sit there — each one is a literal
// newline in the composer, the signature of a paste-detected Enter. A long
// single-line message that merely WRAPS renders non-empty continuation lines;
// any non-empty line in the gap sets foreign instead, because it may be user
// text or wrap and must never be backspaced or submitted. found is false when
// no border follows the prompt (unfamiliar UI); callers should then fall back
// to the plain behavior.
func composerTrail(pane string) (empty int, foreign, found bool) {
	lines := strings.Split(pane, "\n")
	last := -1
	for i, line := range lines {
		if promptLine.MatchString(line) {
			last = i
		}
	}
	if last < 0 {
		return 0, false, false
	}
	var gap []string
	for _, line := range lines[last+1:] {
		s := stripSpace(StripDim(line))
		if isComposerBorder(s) {
			found = true
			break
		}
		gap = append(gap, s)
	}
	if !found {
		return 0, false, false
	}
	for _, s := range gap {
		if s != "" {
			return 0, true, true
		}
	}
	return len(gap), false, true
}

func isComposerBorder(s string) bool {
	n := 0
	for _, r := range s {
		if r != '─' && r != '━' {
			return false
		}
		n++
	}
	return n >= 3
}

func stripSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

var busyIndicator = regexp.MustCompile(`\(\s*\d+\s*[a-z]?\s*s?\s*[·•]|⏵`)

// busySpinner is the CURRENT generation of the "this pane is working" signature:
// the animated status row Claude Code draws above its composer while a turn runs.
// All live shapes are one pattern, because the counter grows into the row rather
// than being part of it. Measured on 2026-08-15 by driving a throwaway 2.1.233
// session and polling twice a second across whole turns:
//
//	✽ Unravelling…                                   <- the first seconds
//	· Misting…
//	✻ Marinating… (1s · thinking with medium effort) <- timer, no token count yet
//	· Marinating… (5s · ↓ 256 tokens · thought for 2s)
//	✻ Baking… (2m 32s · ↓ 6.1k tokens · thought for 6s)
//	✽ Baking… (30s · ↓ 943 tokens)
//	· Symbioting… (2m 13s · ↓ 422 tokens)
//
// Neither the counter nor the token segment may therefore be required: a whole
// three-second turn was captured without the timer ever appearing, and the first
// seconds of every longer turn carry a timer with no token count. A pattern
// demanding either would call a live working pane idle — the same silent-false
// shape this rewrite exists to remove.
//
// What is left carries the weight, and each piece is a discriminator against
// QUOTED prose, the historical enemy of this function (2026-07-10, third
// false-positive class — one agent discussing the busy rule made every other agent
// look busy):
//
//   - column 0. The live row starts at the very left edge; a quotation sits inside
//     a paragraph, which on these panes is always indented.
//   - one SYMBOL glyph (never a letter or a digit) plus a space. The frame rune
//     rotates (✻ ✽ ✳ · ✢ * +) and future builds will rotate others, so the set is
//     deliberately NOT enumerated — only "a symbol, alone, first".
//   - a SINGLE word, and the ELLIPSIS that ends it. This is what separates running
//     from finished, and it was measured rather than guessed: the same row reads
//     "✻ Baked for 3s" when the turn is over, "✻ Baked for 6m 19s · 1 shell still
//     running" while a background shell outlives it, and "✻ Waiting for 1
//     background agent to finish" in a pane whose composer is free — none of them
//     one word, none of them with an ellipsis, all of them correctly not busy.
//     The verb itself (Baking/Symbioting/Unravelling/Misting/…) changes with every
//     release and is therefore never matched.
//   - the counter, WHEN present, must be the whole rest of the row: an opening
//     parenthesis, a timer ("30s", "2m 32s"), anything else, and then the row ENDS
//     with the closing parenthesis. A quotation carries the rest of its sentence
//     behind it ("… tokens). Yani …") and fails here.
//
// What this cannot tell apart is a transcript that reproduces the row EXACTLY, at
// column 0, alone on its line — for that there is the region test (busyRegion),
// and beyond it the daemon's busy-sanity watchdog.
//
// One measured GAP, recorded here so the next reader does not rediscover it as a
// bug: while a long assistant message is STREAMING, 2.1.233 draws no indicator at
// all — the spinner is replaced by the growing text, and the pane is
// indistinguishable from an idle one in a single frame. It returns for every
// thinking and tool phase, which is where agent turns spend nearly all of their
// time (in a driven measurement Busy was true for every frame of the working
// phase, 11 of 11). The gap is also the mildest one: a composer typed into during
// streaming QUEUES the text in the TUI instead of interrupting a tool call. Closing
// it would take two frames and a clock, and Busy must stay a pure function of one.
var busySpinner = regexp.MustCompile(`^[^\p{L}\p{N}\s] +[^\s()]*(?:…|\.\.\.) *(?:\((?:\d+h +)?(?:\d+m +)?\d+(?:\.\d+)?s[^()]*\))?$`)

// busySpinnerLookback is how far ABOVE the composer box's top border the live
// spinner row is looked for, and busyTailRows the same window for a pane whose box
// cannot be read (a Codex pane, a modal picker, an unfamiliar build). The live
// spinner always sits in that narrow strip; a transcript quote of one usually does
// not, and the region test is what makes the difference cheap.
const (
	busySpinnerLookback = 8
	busyTailRows        = 12
)

// busyRegion returns the rows in which a LIVE spinner may appear: the strip just
// above the composer box, or — when the box is not readable — the tail of the
// capture. Everything else is transcript, where the same text is only ever a
// quotation.
func busyRegion(pane string) []string {
	lines := strings.Split(pane, "\n")
	if _, top, ok := composerBoxAt(pane); ok {
		start := top - busySpinnerLookback
		if start < 0 {
			start = 0
		}
		return lines[start:top]
	}
	start := len(lines) - busyTailRows
	if start < 0 {
		start = 0
	}
	return lines[start:]
}

// Busy reports whether the pane is mid-turn. It reads ONE frame and decides from
// its structure alone: no timing, no history, no side effects — every caller
// (msgq's delivery gate, `bp status`, compact, the send path) depends on that.
//
// TWO GENERATIONS of signature are accepted, because the fleet runs both.
//
// The new one (busySpinner) is the spinner row Claude Code 2.1.233 draws. The old
// one is the "esc to interrupt" affordance, which Codex panes and older Claude
// builds still print; it is kept as an OR branch and its own rules are unchanged —
// the phrase alone was never enough (transcripts quote it, and a "2 shells · esc to
// interrupt" footer only reports background shells), so a live-indicator signature
// is still required next to it on the same row.
//
// Why the rewrite (2026-08-15). Claude Code 2.1.233 stopped printing "esc to
// interrupt" in a working pane entirely — 45+ live samples, zero occurrences — and
// because the old code required that phrase BEFORE looking at anything else, Busy
// returned false for every working Claude pane on the machine. Nothing crashed and
// nothing was logged: `bp status` simply showed an idle fleet, and every guard built
// on top of it (compact, the queue's "do not type into a working pane", the send
// path's pre-flight check) silently became a no-op. That is the failure mode this
// function must be read against — a detector that goes quiet is worse than one that
// is wrong, so the daemon's "busy-sanity" loop now measures THIS function against
// the transcript gate (book.TurnOpen): panes the transcript proves mid-turn should
// also read busy here at least occasionally, and a dozen proven-busy samples with
// zero screen agreement means the signature has drifted again. (The first version
// watched "panes changing for a day while Busy never fires" and false-alarmed on
// a quiet night of point-sampling, 2026-08-18 — activity between sweeps is not
// evidence about the instant a sweep looks.)
//
// What this function CANNOT see, and is not asked to: while a long assistant
// message is being streamed, the TUI draws no spinner and no affordance at all —
// measured on 2026-08-15, a 147-second answer during which every screen sample
// read idle. There is nothing on the frame to match, so the blind window is
// closed one level up, by the second gate in book.TurnOpen, which asks the
// agent's own transcript instead of its screen. Busy stays a pure single-frame
// function; callers OR the two gates together.
//
// The tool-run box a working pane also shows ("⎿ $ cmd (27s · 28 lines)" plus its
// "ctrl+b ctrl+b to run in background" hint) is deliberately NOT a second signal:
// the spinner is present for the WHOLE turn while that box only appears around a
// shell command, so it would add false-positive surface (its shape survives being
// quoted much better) for no coverage.
func Busy(pane string) bool {
	for _, line := range busyRegion(pane) {
		// Only ANSI colour is stripped, never dim segments: the spinner may well be
		// dim-rendered, and StripDim would delete the very row being tested.
		clean := strings.TrimRight(ansiSeq.ReplaceAllString(line, ""), " \t ")
		if busySpinner.MatchString(clean) {
			return true
		}
	}
	for _, line := range strings.Split(strings.ToLower(pane), "\n") {
		if !strings.Contains(line, "esc to interrupt") || strings.Contains(line, "shell") {
			continue
		}
		if busyIndicator.MatchString(line) {
			return true
		}
	}
	return false
}

type Client struct {
	Bin   string
	Sleep func(time.Duration)
	Now   func() time.Time
	exec  func(context.Context, []byte, ...string) ([]byte, error)
}

// Location records both the current pane directory and the directory in which
// its tmux session was created.
type Location struct {
	Session    string
	CurrentDir string
	StartDir   string
}

// PaneProcess identifies the active process in a session's target pane.
type PaneProcess struct {
	Command string
	PID     int
}

func New() *Client {
	return &Client{Bin: "tmux", Sleep: time.Sleep, Now: time.Now}
}

func (c *Client) run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if c.exec != nil {
		return c.exec(ctx, stdin, args...)
	}
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

var ansiSeq = regexp.MustCompile("\x1b\\[[0-9;]*m")
var dimSeg = regexp.MustCompile("\x1b\\[2m.*?\x1b\\[(?:0|22)m")

// StripDim removes dim-rendered segments (placeholders / ghost suggestions render dim in
// both Codex and Claude Code composers), then strips remaining ANSI colour codes.
func StripDim(s string) string {
	return ansiSeq.ReplaceAllString(dimSeg.ReplaceAllString(s, ""), "")
}

// CaptureAnsi returns the pane content with escape sequences preserved (-e), which lets
// Typing distinguish real typed text from dim placeholder/ghost text.
func (c *Client) CaptureAnsi(ctx context.Context, session string) (string, error) {
	out, err := c.run(ctx, nil, "capture-pane", "-t", "="+session+":", "-e", "-p")
	return string(out), err
}

func (c *Client) Capture(ctx context.Context, session string) (string, error) {
	out, err := c.run(ctx, nil, "capture-pane", "-t", "="+session+":", "-p")
	return string(out), err
}

// PaneCommand returns the foreground command (#{pane_current_command}) of the
// pane that keystrokes would be delivered to. It targets "=<session>:" — exactly
// the pane Send types into — so the agent check matches the actual send target,
// not merely the session's first pane (as Commands' list-panes does).
func (c *Client) PaneCommand(ctx context.Context, session string) (string, error) {
	out, err := c.run(ctx, nil, "display-message", "-p", "-t", "="+session+":", "#{pane_current_command}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// PaneProcess returns the command and PID of the active pane in a session.
func (c *Client) PaneProcess(ctx context.Context, session string) (PaneProcess, error) {
	out, err := c.run(ctx, nil, "list-panes", "-t", "="+session+":", "-F", "#{pane_active}\t#{pane_current_command}\t#{pane_pid}")
	if err != nil {
		return PaneProcess{}, err
	}
	var fallback PaneProcess
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(fields[2]))
		if err != nil {
			continue
		}
		process := PaneProcess{Command: strings.TrimSpace(fields[1]), PID: pid}
		if fallback.PID == 0 {
			fallback = process
		}
		if strings.TrimSpace(fields[0]) == "1" {
			return process, nil
		}
	}
	if fallback.PID != 0 {
		return fallback, nil
	}
	return PaneProcess{}, fmt.Errorf("tmux pane process not found for %s", session)
}

// Option reads one session option, empty when tmux has no value set for it.
func (c *Client) Option(ctx context.Context, session, name string) (string, error) {
	out, err := c.run(ctx, nil, "show-options", "-t", session, "-v", name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// SetOption sets one session option.
func (c *Client) SetOption(ctx context.Context, session, name, value string) error {
	_, err := c.run(ctx, nil, "set-option", "-t", session, name, value)
	return err
}

func (c *Client) HasSession(ctx context.Context, session string) bool {
	_, err := c.run(ctx, nil, "has-session", "-t", "="+session)
	return err == nil
}

func (c *Client) Sessions(ctx context.Context) ([]string, error) {
	out, err := c.run(ctx, nil, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no server running") ||
			strings.Contains(strings.ToLower(err.Error()), "no sessions") {
			return nil, nil
		}
		return nil, err
	}
	var sessions []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			sessions = append(sessions, line)
		}
	}
	return sessions, nil
}

func (c *Client) Locations(ctx context.Context) ([]Location, error) {
	out, err := c.run(ctx, nil, "list-panes", "-a", "-F", "#{session_name}\t#{pane_current_path}\t#{session_path}")
	if err != nil {
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "no server running") || strings.Contains(lower, "no sessions") {
			return nil, nil
		}
		return nil, err
	}
	var locations []Location
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 || strings.TrimSpace(fields[0]) == "" {
			continue
		}
		locations = append(locations, Location{Session: fields[0], CurrentDir: fields[1], StartDir: fields[2]})
	}
	return locations, nil
}

func (c *Client) IsTyping(ctx context.Context, session string) (bool, error) {
	pane, err := c.CaptureAnsi(ctx, session)
	return Typing(pane), err
}

func (c *Client) IsBusy(ctx context.Context, session string) (bool, error) {
	pane, err := c.Capture(ctx, session)
	return Busy(pane), err
}

var ErrTyping = errors.New("composer is not empty")

// ErrBusy is returned when a pane is mid-turn ("esc to interrupt"). Nothing may
// be typed into a working agent: Escape would cancel its turn and any other key
// would land in the middle of someone's work.
var ErrBusy = errors.New("pane is working (esc to interrupt)")

// ErrNotAgent is returned by Send when the target pane is not running an agent
// CLI (e.g. it dropped to a root shell). It signals the caller to skip delivery
// rather than inject the message text into a shell prompt.
var ErrNotAgent = errors.New("target pane is not an agent CLI")

// ErrNotReady reports a PROVABLE non-delivery: either the pane could not accept
// the message at all (an expired login, where a Claude session renders a /login
// banner and silently eats input) or the composer was observed holding content
// unrelated to what we pasted. Wrapped with the concrete reason at the return
// site. The message did not land, so callers MUST queue it: it is retried and
// leaves a record, exactly as ErrTyping already does.
var ErrNotReady = errors.New("message was not delivered")

// ErrUnverified reports the third outcome: the message was injected and Enter
// was pressed, but the delivery could not be confirmed either way (a capture
// failed, a client started typing, or the composer was never observed holding
// our text). It may well have landed, so it must NOT be re-injected or queued —
// a retry would duplicate it. The CLI reports it instead of calling it sent.
var ErrUnverified = errors.New("delivery could not be verified")

// authExpiredMarkers are the phrases a Claude Code session renders in its status
// footer when its credentials expired ("● Login expired · Please run /login").
// Such a pane looks exactly like a healthy idle one to Typing/readyToSend — an
// empty, stable composer — but it does not consume input, which is how a
// 600-character brief was reported "sent" and never arrived.
var authExpiredMarkers = []string{"login expired", "run /login"}

// authStatusBullet is the marker Claude Code prefixes its status banners with.
// Requiring it keeps prose that merely mentions the words from matching.
const authStatusBullet = "●"

// AuthExpired reports whether the pane's LIVE STATUS FOOTER shows the
// expired-credentials state.
//
// The region matters more than the phrase. A first version of this scanned the
// whole pane and immediately fired on healthy agents: every agent discussing
// this very incident carries "Login expired" somewhere in its scrollback, which
// turned working delivery into a permanently queued message — worse than the bug
// it was meant to catch. So the match follows the same discipline as Busy(),
// codexBusyQueue() and composerTrail(): look only where the live UI is, and
// require a signature rather than bare words. (Busy() arrived at the region half
// of that discipline late — its 2026-08-15 rewrite — and its legacy "esc to
// interrupt" branch still scans the whole pane, leaning on a two-part signature
// on one row instead. The rule here is unchanged either way: this function reads
// the footer region only.)
//
//   - Scan starts BELOW the last prompt line, so transcript text (which is
//     always above the composer) can never match, however often an agent quotes
//     the banner.
//   - When the composer box has a bottom border, the scan starts below THAT, so
//     a message holding the phrase — typed, pasted or wrapped inside the
//     composer — cannot match either.
//   - The matching line must also carry the status bullet ●, the way the real
//     banner renders.
//
// Only ANSI colour is stripped, never dim segments: the footer may itself be
// dim-rendered and StripDim would delete the very line we look for.
//
// The residual direction is now a MISSED detection (a build that renders the
// banner somewhere else), and that costs little: such a pane swallows the paste,
// the composer is never seen holding our message, and Send already reports the
// delivery as unverified instead of "sent".
func AuthExpired(pane string) bool {
	for _, line := range statusFooter(pane) {
		clean := ansiSeq.ReplaceAllString(line, "")
		if !strings.Contains(clean, authStatusBullet) {
			continue
		}
		clean = strings.ToLower(clean)
		for _, marker := range authExpiredMarkers {
			if strings.Contains(clean, marker) {
				return true
			}
		}
	}
	return false
}

// statusFooter returns the rendered rows BELOW the live composer — where a TUI
// draws its status/footer lines. Everything above (the transcript) and the
// composer's own content are excluded. A pane with no prompt line at all has no
// live composer to speak of and yields nothing.
func statusFooter(pane string) []string {
	lines := strings.Split(pane, "\n")
	last := -1
	for i, line := range lines {
		if promptLine.MatchString(line) {
			last = i
		}
	}
	if last < 0 {
		return nil
	}
	rest := lines[last+1:]
	// Claude draws a border under the composer; anything above it is still
	// composer content (a wrapped message), so the footer starts below it.
	// Codex has no border: there the rows under the prompt line ARE the footer.
	for i, line := range rest {
		if isComposerBorder(stripSpace(StripDim(line))) {
			return rest[i+1:]
		}
	}
	return rest
}

// IsAgentCommand reports whether cmd (a tmux pane's #{pane_current_command}) is
// one of the agent runtimes we may safely inject keystrokes into: "claude",
// "codex", or "bwrap" (a sandboxed Codex runs inside bubblewrap and reports
// "bwrap"; a --yolo/no-sandbox Codex reports "codex" — both are accepted).
// Everything else — an interactive shell ("zsh"/"bash"/"sh"/"dash"/"fish"),
// "tmux", a bare "node", or the empty string — is NOT an agent and must never
// receive a delivered message. The whitelist is deliberately a small, documented
// set: only known agent runtimes are allowed.
func IsAgentCommand(cmd string) bool {
	switch cmd {
	case "claude", "codex", "bwrap":
		return true
	default:
		return false
	}
}

// isShellCommand reports whether cmd is an interactive shell — the pane state
// left behind when an agent exits or crashes. Only such a pane may be reused
// to relaunch an agent; anything else (vim, htop, ...) is someone's live work.
func isShellCommand(cmd string) bool {
	switch cmd {
	case "zsh", "bash", "sh", "dash", "fish":
		return true
	default:
		return false
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// RemoteControlMenu reports whether the pane shows the Remote Control modal
// (Continue / Disconnect / QR, cursor on Continue). It appears at startup when
// a resumed session reconnects remote control, and after /remote-control when
// RC is already active. While it is up the composer is blocked: delivered text
// falls into the menu instead of the prompt, so it must be dismissed (Enter =
// Continue) before anything is sent.
func RemoteControlMenu(pane string) bool {
	if !strings.Contains(pane, "Enter to select") {
		return false
	}
	return strings.Contains(pane, "Disconnect this session") || strings.Contains(pane, "Remote Control")
}

func parseClientActivity(output, session string) time.Time {
	var latest time.Time
	for _, line := range strings.Split(output, "\n") {
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 || fields[0] != session {
			continue
		}
		seconds, err := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64)
		if err == nil && seconds > latest.Unix() {
			latest = time.Unix(seconds, 0)
		}
	}
	return latest
}

func (c *Client) clientActivity(ctx context.Context, session string) (time.Time, error) {
	out, err := c.run(ctx, nil, "list-clients", "-F", "#{session_name}\t#{client_activity}")
	if err != nil {
		return time.Time{}, err
	}
	return parseClientActivity(string(out), session), nil
}

const (
	composerQuietWindow  = 300 * time.Millisecond
	composerSettleWindow = 400 * time.Millisecond
	clientIdleWindow     = 2 * time.Second
	// submitVerifyWindow is the pause between submitting and re-checking whether
	// the composer actually cleared before pressing Enter again.
	submitVerifyWindow = 300 * time.Millisecond
	// submitRetries bounds the extra Enter keypresses after the first one.
	submitRetries = 2
	// submitBackspaceDelay separates the BSpace presses of a multiline recovery.
	submitBackspaceDelay = 150 * time.Millisecond
	// submitBackspaceMax caps how many trailing newlines a recovery will delete;
	// more than that is not a state our own keypresses could have created.
	submitBackspaceMax = 5
)

// readyToSend requires a stable empty composer and a short period without
// keyboard activity in any client attached to the target session. The second
// composer check closes the old check-then-paste window in which a user could
// begin typing just as a queued message was injected.
//
// firstPane is the capture the stuck-paste gate (resolveStuckPaste) already took;
// it becomes this function's first pass, so the pane is not captured twice in a
// row. An empty string means "look yourself", which is what the gate passes after
// it pressed any key, since its own capture is then stale.
//
// It also refuses a pane whose credentials expired. That state passes every
// other check — the composer is empty and perfectly stable — yet the session
// consumes nothing, so pasting into it loses the message silently. Both captures
// are tested, so a login that expires inside the quiet window is caught too. The
// refusal is an ErrNotReady error rather than a plain not-ready result: it is
// PROOF the message cannot land, and the caller must queue it.
func (c *Client) readyToSend(ctx context.Context, session, firstPane string) (bool, error) {
	if firstPane == "" {
		captured, err := c.CaptureAnsi(ctx, session)
		if err != nil {
			return false, err
		}
		firstPane = captured
	}
	if Typing(firstPane) {
		return false, nil
	}
	if AuthExpired(firstPane) {
		return false, fmt.Errorf("%w: %s", ErrNotReady, authExpiredReason)
	}
	firstActivity, err := c.clientActivity(ctx, session)
	if err != nil {
		return false, err
	}
	c.Sleep(composerQuietWindow)
	secondPane, err := c.CaptureAnsi(ctx, session)
	if err != nil || Typing(secondPane) {
		return false, err
	}
	if AuthExpired(secondPane) {
		return false, fmt.Errorf("%w: %s", ErrNotReady, authExpiredReason)
	}
	secondActivity, err := c.clientActivity(ctx, session)
	if err != nil {
		return false, err
	}
	if secondActivity.After(firstActivity) {
		return false, nil
	}
	now := c.Now()
	if !secondActivity.IsZero() && now.Sub(secondActivity) < clientIdleWindow {
		return false, nil
	}
	return true, nil
}

const (
	// composerClearWindow is the pause between a clearing keypress and the capture
	// that checks what it did.
	composerClearWindow = 250 * time.Millisecond
	// composerClearAttempts bounds the C-u presses of one clearing pass. The
	// operator measured a single C-u clearing only ONE line: multi-line content
	// needs it repeated 6-8 times. The loop stops as soon as the composer reads
	// empty, so the bound is only reached when the presses are not working — and
	// then the caller is told (ErrTyping) rather than left believing the composer
	// is clean.
	composerClearAttempts = 8
)

// ClearComposer prepares a pane for a SLASH COMMAND bp is about to type into it
// (/rename, /compact). It is deliberately NOT part of Send: Send also delivers
// ordinary messages, and clearing on every delivery would eventually wipe a
// half-typed line out of someone's composer.
//
// Why it exists: a Claude composer sitting in vim INSERT mode can hold state
// that renders as an empty line yet still makes the pre-send emptiness check
// (Typing) report "composer is not empty", so the slash command is never
// delivered — the false positive that left `bp rename` half-done, with the tmux
// session and the agentbook renamed but the agent's own transcript title still
// on the old name.
//
// ESCAPE IS RETRACTED (operator's instruction, 2026-08-11). This function used
// to press Escape first and this comment used to call that safe because "it
// never destroys typed text". That claim is withdrawn: Escape CANCELS a running
// turn, the Busy check that was supposed to keep it away from working panes has
// been wrong in both directions before, and a cancelled turn destroys work that
// cannot be recovered. Nothing in bp may press Escape into an agent pane again.
// The vim-INSERT state Escape used to settle is now simply refused instead
// (ErrTyping), which costs a `bp rename` retry and risks nothing.
//
// The steps, in order, and the reason each one is where it is:
//
//  1. Refuse a working pane (Busy) before pressing anything.
//  2. Read the composer. Real visible text — the full box when it can be read,
//     otherwise the final row — is left EXACTLY as it is and reported with
//     ErrTyping: destroying a human's half-written line is a worse outcome than
//     any command bp wanted to send. Dim ghost text is not input (StripDim).
//  3. C-u, repeated while anything remains visible and bounded by
//     composerClearAttempts, each press followed by a fresh capture. A single
//     C-u clears one line only; the operator measured 6-8 presses for multi-line
//     content.
//  4. The last capture is the caller's guarantee: it hands off to Send with a
//     composer that has been OBSERVED empty rather than assumed empty.
func (c *Client) ClearComposer(ctx context.Context, session string) error {
	// Same chokepoint discipline as Send: C-u at a shell prompt would erase
	// whatever a human left typed there.
	cmd, err := c.PaneCommand(ctx, session)
	if err != nil {
		return err
	}
	if !IsAgentCommand(cmd) {
		return ErrNotAgent
	}
	pane, err := c.CaptureAnsi(ctx, session)
	if err != nil {
		return err
	}
	if Busy(pane) {
		return ErrBusy
	}
	if composerFilled(pane) {
		// Someone's text (or a paste we cannot read): never erase it. nil `mine`
		// below would refuse it anyway; refusing here also keeps the promise that
		// not a single key is pressed in this case.
		return ErrTyping
	}
	return c.clearWithCtrlU(ctx, session, nil)
}

// ClearDelivered erases a composer that provably holds text which has ALREADY
// been delivered, and only then.
//
// It exists because proof arriving late is still proof. When the queue's
// transcript witness finds a message in the agent's own session file, and the
// same text is still sitting in the composer, both facts together are decisive:
// the text is OURS (it matches a record, by the same comparison and the same
// 32-character thresholds as every other judgment here) and it has already
// reached the agent. Leaving it there would be the deadlock all over again from
// the other end — the record is gone, so nothing can recognise the text as ours
// any more, and every later message queues behind it forever.
//
// texts are the messages known to be delivered. Without a match in that set
// nothing is touched: no proof, no keys. Enter is never pressed here — the
// message has landed already, and pressing it would deliver a second copy.
//
// Reports whether it actually cleared anything, so the caller can say so out
// loud instead of clearing a human's composer in silence.
func (c *Client) ClearDelivered(ctx context.Context, session string, texts []string) (bool, error) {
	cmd, err := c.PaneCommand(ctx, session)
	if err != nil {
		return false, err
	}
	if !IsAgentCommand(cmd) {
		return false, ErrNotAgent
	}
	pane, err := c.CaptureAnsi(ctx, session)
	if err != nil {
		return false, err
	}
	if Busy(pane) {
		return false, ErrBusy
	}
	if !composerFilled(pane) {
		return false, nil // nothing hanging: the ordinary case
	}
	if _, ours := StuckPaste(pane, texts); !ours {
		return false, nil // not provably ours: leave it exactly as it is
	}
	if err := c.clearWithCtrlU(ctx, session, texts); err != nil {
		return false, err
	}
	return true, nil
}

// composerFilled reports whether a composer holds anything visible, using the
// full box when it can be read and the final rendered row otherwise. A paste
// chip counts as content: it stands in for text we cannot read.
func composerFilled(pane string) bool {
	if box, ok := composerBoxText(pane); ok {
		return box != "" || codexPasteChip(pane) || claudePasteChip(pane)
	}
	return Typing(pane) || codexPasteChip(pane) || claudePasteChip(pane)
}

// clearWithCtrlU empties a composer with repeated C-u presses — never Escape,
// which would cancel a running turn.
//
// mine lists the texts C-u is allowed to erase: our own message and any queue
// record pending for this target. The moment what remains in the composer is NOT
// one of those (a human started typing, or a paste chip made the content
// unreadable) the loop stops at once with ErrTyping and leaves the rest alone.
// A nil/empty mine therefore means "erase nothing that is visible", which is what
// ClearComposer wants: it presses C-u only to settle an INVISIBLE state.
//
// At least one press always happens, because that invisible state is the whole
// reason ClearComposer exists — a composer that reads empty can still refuse the
// next paste.
func (c *Client) clearWithCtrlU(ctx context.Context, session string, mine []string) error {
	target := "=" + session + ":"
	for attempt := 0; attempt < composerClearAttempts; attempt++ {
		if _, err := c.run(ctx, nil, "send-keys", "-t", target, "C-u"); err != nil {
			return err
		}
		c.Sleep(composerClearWindow)
		pane, err := c.CaptureAnsi(ctx, session)
		if err != nil {
			return err
		}
		if Busy(pane) {
			// The pane started working under us: stop pressing keys immediately.
			return ErrBusy
		}
		if !composerFilled(pane) {
			return nil
		}
		if _, ok := StuckPaste(pane, mine); !ok {
			// What is left is not ours to erase.
			return ErrTyping
		}
	}
	return ErrTyping
}

var bufferSequence uint64

const (
	authExpiredReason    = "pane oturumu dusmus (Login expired / run /login)"
	composerOtherReason  = "composer'da baska metin var: paste hic girmemis"
	composerBrokenReason = "paste composer'a bozuk girdi (yeniden yazildi, hala eslesmiyor)"
)

// Send resolves anything of ours already hanging in the composer, then injects
// the message, lets the TUI turn a large paste into its internal "Pasted text"
// object, verifies what landed, and submits it. The return value tells the caller
// exactly which of three things happened:
//
//	nil            verified: the composer was observed holding our message and
//	               then observed to clear. (Also the outcome when the message was
//	               ALREADY sitting in the composer from an earlier attempt and one
//	               Enter finished it.)
//	ErrNotReady    proven failure: the pane could not receive it (expired login),
//	               the composer held unrelated text after the paste, or our paste
//	               landed damaged and a second attempt did not fix it. Nothing was
//	               delivered; the caller must queue the message. A verdict read
//	               off a pane that is still MOVING is not proof of anything and
//	               becomes ErrUnverified instead (see provenFailure).
//	ErrUnverified  unknown: injected and Enter pressed, but nothing confirmed it
//	               either way. The caller must NOT retry (it may have landed) and
//	               must not report it as sent either.
//
// Any other error is a plumbing failure (tmux itself, a non-agent pane, a busy
// composer) with the same meaning as before.
//
// DELIVERY is at-most-once: Enter is pressed only on content verified to be
// exactly this message, so no caller can be told "sent" twice for one message.
// The TEXT may be pasted twice — once by the damage repair — but only after the
// composer was observed empty and with no Enter in between, so a repair cannot
// produce a second delivery.
//
// If a user touches the attached client during the settle window, their mixed
// composer is left alone and Enter is deliberately not sent.
func (c *Client) Send(ctx context.Context, session, message string) error {
	_, err := c.SendWithPending(ctx, session, message, nil)
	return err
}

// SendWithPending is Send with the queue's own records in hand.
//
// pending lists the message texts already QUEUED for this target. They are used
// for one purpose: recognising bp's OWN unsubmitted paste in a non-empty
// composer. Without them a hanging paste of an earlier queued message reads as
// "a human is typing", every later delivery queues behind it, and the queue
// blocks itself — measured at four days for compec-main and 25 hours for another
// target, with `bp qstat` cheerfully reporting "still busy" at three idle agents.
//
// finished names the pending texts this call SUBMITTED out of the composer (it
// pressed Enter on text that was already sitting there, whole). The caller owns
// those queue records and must close them; leaving them open is what let a
// hand-delivered message be pasted a second time.
func (c *Client) SendWithPending(ctx context.Context, session, message string, pending []string) (finished []string, err error) {
	return c.send(ctx, session, message, pending, false)
}

// SendForce delivers a message into a pane that is MID-TURN, and it is the only
// entry point that may.
//
// Exactly one refusal is dropped: the pre-send gate's "this pane is working, do
// not touch it". Everything else stands — a target that is not an agent CLI, an
// expired login, a composer holding somebody's text, a keyboard that was in use
// a moment ago, our own hanging paste and its repair, and every verification
// step after the paste. What is being overridden is the agent's concentration,
// never anybody's input.
//
// The caller must expect ErrUnverified here, and must treat it as the ORDINARY
// outcome rather than a fault: a redrawing pane hands back torn frames, so the
// screen cannot confirm a paste it is in the middle of repainting (that is why
// provenFailure downgrades every verdict read off a busy pane). In force mode the
// screen is therefore not the decider — the transcript witness is. The queue
// record stays open, never pasted again, until the witness settles it. A "sent"
// that the screen cannot back up is precisely the claim this package refuses to
// make.
//
// It exists because the alternative was measurably worse. The WhatsApp bridge
// carried Tuna's own messages into busy panes by typing into them directly, with
// no pane lock, no duplicate guard, no witness and no cooldown; two writers in
// one composer is how two messages became one on 2026-08-17. Doing the same
// delivery HERE puts it back under all of that.
func (c *Client) SendForce(ctx context.Context, session, message string) error {
	_, err := c.send(ctx, session, message, nil, true)
	return err
}

// send is the body of every delivery. force drops the busy refusal and nothing
// else; see SendForce.
func (c *Client) send(ctx context.Context, session, message string, pending []string, force bool) (finished []string, err error) {
	// Guard first: never inject keystrokes into a pane that is not running an
	// agent CLI. A session that dropped to a root shell (zsh) would otherwise
	// receive the message text at its shell prompt. Send is the single delivery
	// chokepoint, so checking here covers every path (deliver->Send,
	// msgq.Dispatch->Send). The check targets the exact pane keystrokes go to.
	cmd, err := c.PaneCommand(ctx, session)
	if err != nil {
		return nil, err
	}
	if !IsAgentCommand(cmd) {
		return nil, ErrNotAgent
	}
	target := "=" + session + ":"
	finished, firstPane, outcome, err := c.resolveStuckPaste(ctx, target, session, message, pending, force)
	if err != nil {
		return finished, err
	}
	switch outcome {
	case stuckDelivered:
		// The composer was already holding THIS message, whole, from an earlier
		// paste whose Enter never registered. It has now been submitted: a normal
		// delivery, and emphatically not something to queue behind.
		return finished, nil
	case stuckUnresolved:
		// Ours, but it could not be finished or cleaned up. Behave exactly as a
		// busy composer does: the message is queued and nothing was mangled.
		return finished, ErrTyping
	}
	ready, err := c.readyToSend(ctx, session, firstPane)
	if err != nil {
		return finished, err
	}
	if !ready {
		return finished, ErrTyping
	}
	if err := c.inject(ctx, target, message); err != nil {
		return finished, err
	}

	// From this point onward the text is in the pane, so an error must never
	// mean "paste it again" unless we PROVED it did not land: ErrNotReady is
	// returned only for a composer holding foreign content or a paste that
	// arrived broken, and the ambiguous case gets ErrUnverified, which no caller
	// retries.
	c.Sleep(composerSettleWindow)
	pane, stable := c.composerStable(ctx, session)
	if !stable {
		// A client is typing or the pane could not be read: no key may be pressed
		// and nothing confirms the delivery either way, which is exactly what
		// submit would have reported after the same check.
		return finished, ErrUnverified
	}
	pane, ok := c.checkPaste(ctx, target, session, message, pane)
	if !ok {
		return finished, c.provenFailure(ctx, session, pane, composerBrokenReason)
	}
	switch c.submit(ctx, target, session, message, &pane) {
	case sendVerified:
		return finished, nil
	case sendMismatch:
		return finished, c.provenFailure(ctx, session, pane, composerOtherReason)
	default:
		return finished, ErrUnverified
	}
}

// provenFailure decides whether a non-delivery verdict may be reported as PROOF
// (ErrNotReady, which the caller retries) or only as doubt (ErrUnverified, which
// nobody retries).
//
// The verdict itself was read off ONE capture. That is sound on a still pane and
// worthless on a moving one: a pane redrawing mid-turn hands back torn frames,
// and on q163159804 (2026-08-15) those frames produced alternating
// "composer'da baska metin var" / "paste composer'a bozuk girdi" verdicts for a
// message the agent had ALREADY queued internally. Each verdict said "provably
// not delivered", the record stayed pending, and the daemon pasted the same
// message again every 30 seconds.
//
// So the screen is asked a second time before the word "proof" is used. The
// verdict stands only when a fresh capture shows the very same still composer:
//
//   - the capture fails       -> we cannot see anything, so we know nothing
//   - the pane reads BUSY     -> the frame the verdict came from was a moving one
//   - the composer changed    -> likewise; the verdict described a frame, not a state
//
// Every one of those downgrades to ErrUnverified. Nothing is re-injected on
// either path, so this can never produce a second delivery — it only decides
// what the caller is allowed to believe.
func (c *Client) provenFailure(ctx context.Context, session, verdictPane, reason string) error {
	fresh, err := c.CaptureAnsi(ctx, session)
	if err != nil || Busy(fresh) {
		return ErrUnverified
	}
	if composerSnapshot(fresh) != composerSnapshot(verdictPane) {
		return ErrUnverified
	}
	return fmt.Errorf("%w: %s", ErrNotReady, reason)
}

// composerSnapshot reduces a capture to the composer state a verdict rests on,
// read the same way every verdict in this package reads it: the full box while
// it is a complete view, the final rendered row otherwise, and a bare marker for
// a paste chip (whose content cannot be read at all, so two chips are as equal
// as they can ever be known to be). The prefix keeps the three readings from
// comparing equal to one another by accident.
func composerSnapshot(pane string) string {
	if codexPasteChip(pane) || claudePasteChip(pane) {
		return "chip"
	}
	if box, ok := composerJudgeText(pane); ok {
		return "box:" + box
	}
	return "row:" + composerContent(pane)
}

// inject puts message into the composer with a bracketed paste, never
// send-keys -l. Every agent on this fleet runs Claude Code with editorMode
// "vim": literal keystrokes into a NORMAL-mode composer are interpreted as vim
// commands and silently eaten until an i/a/s happens to appear in the text —
// exactly how single-line federation messages arrived mangled. A paste is
// inserted as text in any mode, and it is also what keeps external message
// content data rather than keys.
func (c *Client) inject(ctx context.Context, target, message string) error {
	buffer := fmt.Sprintf("bp-agentmsg-%d-%d", os.Getpid(), atomic.AddUint64(&bufferSequence, 1))
	if _, err := c.run(ctx, []byte(message), "load-buffer", "-b", buffer, "-"); err != nil {
		return err
	}
	if _, err := c.run(ctx, nil, "paste-buffer", "-b", buffer, "-d", "-t", target); err != nil {
		// A failed paste may leave the uniquely named buffer behind.
		_, _ = c.run(ctx, nil, "delete-buffer", "-b", buffer)
		return err
	}
	return nil
}

// stuckOutcome is what the pre-send gate made of a non-empty composer.
type stuckOutcome int

const (
	// stuckAbsent: nothing of ours to resolve. Either the composer is empty, or
	// it holds text we may not touch — readyToSend then makes the same call it
	// always did (empty -> proceed, anything else -> ErrTyping -> queue).
	stuckAbsent stuckOutcome = iota
	// stuckDelivered: the composer held the message we were about to send, and
	// pressing Enter submitted it. Nothing left to paste.
	stuckDelivered
	// stuckUnresolved: ours, but not resolvable right now (Enter did not clear
	// it, or the clearing was refused). The caller queues, as for a busy pane.
	stuckUnresolved
)

// resolveStuckPaste is the fix for the deadlock, and the one place bp is allowed
// to touch a composer it did not just paste into.
//
// It answers a question the old code never asked: is this text in the composer
// SOMEONE ELSE'S, or is it OUR OWN paste whose Enter never registered? The full
// box content (never the single visible row — a multi-line paste could never be
// compared against the message that produced it) is matched against the message
// about to be sent and every queue record pending for this target:
//
//	exact    -> our unsubmitted paste, intact. Finish it with the same submit
//	            machinery a fresh paste uses, verify the composer clears, and
//	            report it as the normal delivery it is. Never queue behind it.
//	damaged  -> our paste, mutilated (truncated, or sharing a long affix). Enter
//	            is NEVER pressed: that is precisely how the operator's message
//	            went out with its leading ~200 characters missing. The composer is
//	            cleared with C-u and the caller re-pastes the whole message.
//	foreign  -> somebody's half-written line, an unreadable paste chip, or a pane
//	            whose structure we do not recognise. Untouched, queued, exactly as
//	            before. Every doubt resolves into this branch.
//
// Two refusals come before any of that: a BUSY pane is never touched at all, and
// neither is a composer whose attached client showed keyboard activity inside
// clientIdleWindow — if a human is at the keyboard, whatever is in the box is
// theirs by default.
//
// The returned pane is the capture this gate took, handed on to readyToSend as
// its first pass so an untouched pane is never captured twice; it is empty
// whenever a key was pressed and the capture is therefore stale.
func (c *Client) resolveStuckPaste(ctx context.Context, target, session, message string, pending []string, force bool) ([]string, string, stuckOutcome, error) {
	pane, err := c.CaptureAnsi(ctx, session)
	if err != nil {
		return nil, "", stuckAbsent, err
	}
	if Busy(pane) && !force {
		// A pane mid-turn is refused HERE, before anything is pasted. It used to
		// fall through as stuckAbsent into readyToSend, which asks about typing,
		// credentials and client activity but never about BUSY — so a message was
		// pasted into a running turn, the redrawing screen was then read for
		// verification, and the torn frames it produced became "proof" of
		// non-delivery. That is the loop measured on q163159804 (2026-08-15): one
		// message delivered three times. The caller queues it, exactly as it does
		// for a busy composer.
		//
		// force is the single exception, and it is narrow on purpose: it says the
		// sender already knows the agent is working and wants the message in the
		// pane anyway (SendForce). The verification below still runs and will still
		// read torn frames off the redrawing screen — which is why a forced
		// delivery normally comes back ErrUnverified and is settled by the
		// transcript rather than by the screen. The gates BELOW this one are not
		// touched by force: a filled composer, a keyboard in use and an expired
		// login refuse a forced message exactly as they refuse any other.
		return nil, "", stuckAbsent, ErrBusy
	}
	// An empty composer is the overwhelmingly common case and needs nothing from
	// this gate — not even the client-activity round trip readyToSend will make.
	if !composerFilled(pane) {
		return nil, pane, stuckAbsent, nil
	}
	activity, err := c.clientActivity(ctx, session)
	if err != nil {
		return nil, "", stuckAbsent, err
	}
	if !activity.IsZero() && c.Now().Sub(activity) < clientIdleWindow {
		// Someone is at the keyboard: the composer is theirs until proven
		// otherwise, and readyToSend will queue the message.
		return nil, pane, stuckAbsent, nil
	}
	texts := append([]string{message}, pending...)
	verdict, text := classifyPaste(pane, texts)
	switch verdict {
	case pasteExact:
		if c.submit(ctx, target, session, text, &pane) != sendVerified {
			// Enter did not clear it. Nothing was re-injected and nothing was
			// destroyed; the message goes to the queue like any busy pane.
			return nil, "", stuckUnresolved, nil
		}
		if text == message {
			return nil, "", stuckDelivered, nil
		}
		// A DIFFERENT queued message was sitting there and has now been
		// delivered. Its record must be closed by the caller (or the queue would
		// paste it again), and our own message still has to go in.
		return []string{text}, "", stuckAbsent, nil
	case pasteDamaged:
		if err := c.clearWithCtrlU(ctx, session, texts); err != nil {
			return nil, "", stuckUnresolved, nil
		}
		return nil, "", stuckAbsent, nil
	default:
		return nil, pane, stuckAbsent, nil
	}
}

// checkPaste verifies, BEFORE any Enter, that what landed in the composer is
// what we pasted, and returns the pane capture the submit step should judge on.
//
// pane is the capture taken after the settle window; it has already passed the
// client-activity check. The verdicts:
//
//   - the box reads exactly our message (a wrap included, since whitespace is
//     stripped): submit.
//   - the box cannot be read (no status footer, a collapsed empty composer, a
//     Codex pane, a paste chip standing in for the text): unchanged behavior —
//     submit decides on the single rendered row, as it always has.
//   - the box is a PREFIX of our message, or the box is tall enough to be
//     scrolled (composerBoxScrollRows): the render is a window, not the content.
//     Treated as unreadable rather than damaged, deliberately: a scrolled box
//     that read as "damaged" every time would clear, re-paste and queue the same
//     message forever — the very failure mode this whole change is about.
//   - the box is related to our message but shorter/altered: our paste arrived
//     broken. Clear it and paste it ONCE more. If the second attempt still does
//     not match, ok=false and the caller returns ErrNotReady so the message is
//     queued rather than delivered mangled.
//   - the box holds something unrelated: our paste never landed. ok=false, and
//     no key is pressed on someone else's text.
//
// What this cannot catch: anything the pane does not RENDER. Content scrolled out
// of the visible box, a truncation hidden behind a paste chip, or bytes the TUI
// accepted but drew differently are all invisible here — the box is evidence
// about the screen, not about the composer's internal buffer. The final witness
// for a delivery is still the agent's own transcript (see msgq reconciliation).
func (c *Client) checkPaste(ctx context.Context, target, session, message, pane string) (string, bool) {
	switch c.pasteIntegrity(pane, message) {
	case pasteIntact:
		return pane, true
	case pasteMangled:
		// Repair exactly once: clear our own broken text, paste it again, and
		// re-verify. Re-pasting is not a second DELIVERY — no Enter has been
		// pressed, and the composer was observed empty in between.
		if err := c.clearWithCtrlU(ctx, session, []string{message}); err != nil {
			return pane, false
		}
		if err := c.inject(ctx, target, message); err != nil {
			return pane, false
		}
		c.Sleep(composerSettleWindow)
		next, ok := c.composerStable(ctx, session)
		if !ok {
			return pane, false
		}
		if verdict := c.pasteIntegrity(next, message); verdict == pasteBroken || verdict == pasteMangled {
			return next, false
		}
		return next, true
	case pasteBroken:
		return pane, false
	default:
		return pane, true
	}
}

// pasteVerification is checkPaste's reading of a pane.
type pasteVerification int

const (
	// pasteUnreadable: no usable box view; judge as before, on the single row.
	pasteUnreadable pasteVerification = iota
	// pasteIntact: the box holds exactly our message.
	pasteIntact
	// pasteMangled: our message, damaged — repairable by clear + re-paste.
	pasteMangled
	// pasteBroken: unrelated content where our paste should be. Proof of failure.
	pasteBroken
)

func (c *Client) pasteIntegrity(pane, message string) pasteVerification {
	if codexPasteChip(pane) || claudePasteChip(pane) {
		return pasteUnreadable
	}
	box, top, ok := composerBoxAt(pane)
	if !ok {
		return pasteUnreadable
	}
	got, want := stripSpace(box), stripSpace(message)
	switch {
	case got == want:
		return pasteIntact
	case got == "":
		// Empty right after the paste: it may have auto-submitted, or the pane may
		// have swallowed it. submit() already reports that honestly as unverified.
		return pasteUnreadable
	case strings.HasPrefix(want, got), composerBoxScrolled(box, top):
		// A partial render or a scrolled window, not proof of a partial paste.
		return pasteUnreadable
	case relatedPaste(got, want):
		return pasteMangled
	default:
		return pasteBroken
	}
}

// sendResult is submit's verdict, mapped to Send's error contract above.
type sendResult int

const (
	// sendUnverified is the default: nothing observed contradicts delivery, and
	// nothing confirms it.
	sendUnverified sendResult = iota
	// sendVerified: the composer was seen holding our message and later seen
	// empty (or, on a busy Codex, moved into its native queue).
	sendVerified
	// sendMismatch: the composer holds content unrelated to our message, which
	// proves the paste never landed.
	sendMismatch
)

// submit presses Enter to send the freshly injected message, then verifies the
// composer actually cleared. Under load Claude Code's paste detector can fold
// the injecting text and the Enter into a single PTY read and treat the
// trailing newline as pasted content instead of a submit, leaving the message
// stuck in a now-MULTILINE composer ("/compact" plus a literal trailing
// newline). On current builds a later, separately-read Enter submits that
// state; on older long-running builds every further Enter only appends another
// newline. submit therefore distinguishes the two verified states:
//
//   - composer still holds exactly our message on a single line: press Enter
//     again (bounded by submitRetries).
//   - composer holds our message plus trailing EMPTY lines before the box
//     border: do NOT press Enter; delete one newline per empty line with
//     BSpace, re-verify the composer is single-line and still exactly our
//     message, and only then press Enter once. One recovery attempt total; if
//     the BSpaces were ignored (old-build vim insert boundary) give up
//     silently — ambiguity resolves to delivered, as before.
//
// The retries never re-inject the text — only keypresses repeat — so the
// operation stays at-most-once. No key is ever sent while the composer content
// differs from our message or an attached client showed recent activity, so a
// user who started editing is never disturbed and their input never mangled.
//
// Alongside the keypresses submit REPORTS what it observed. A delivery counts as
// verified only when the composer was seen holding our message (held) and later
// seen empty; an empty composer that never held it is not evidence of anything,
// because that is exactly what a pane that swallowed the paste looks like. A
// composer holding unrelated content is the opposite: proof the paste is gone.
// first, when non-nil, is a capture already taken (and already checked for
// client activity) that the first attempt judges on instead of capturing again:
// the integrity check in Send/resolveStuckPaste has just looked at the composer,
// and a second capture would only widen the window between looking and pressing.
func (c *Client) submit(ctx context.Context, target, session, message string, first *string) sendResult {
	want := stripSpace(message)
	held := false
	for attempt := 0; attempt <= submitRetries; attempt++ {
		var pane string
		var ok bool
		if attempt == 0 && first != nil {
			pane, ok = *first, true
		} else {
			pane, ok = c.composerStable(ctx, session)
		}
		if !ok {
			return sendUnverified
		}
		if codexBusyQueue(pane) {
			// TOCTOU: the target went BUSY after Dispatch's idle check and our
			// paste. On a busy Codex, Enter never submits (it only expands the
			// chip); the message must be pushed into Codex's own native queue
			// with Tab. This branch takes precedence over the Enter/BSpace paths
			// so Enter is never sent while the affordance is present.
			_, _ = c.run(ctx, nil, "send-keys", "-t", target, "Tab")
			held = true // the affordance renders under OUR paste chip
			c.Sleep(submitVerifyWindow)
			next, ok := c.composerStable(ctx, session)
			if !ok {
				return sendUnverified
			}
			if !Typing(next) && !codexPasteChip(next) {
				// Composer cleared: the message moved into Codex's queue.
				// Tab-queuing is a real delivery, and it was observed both
				// holding and clearing, so it counts as verified.
				return sendVerified
			}
			// Still holding our paste: retry Tab, bounded by the loop. If it
			// never clears we give up silently at the bound WITHOUT ever
			// falling through to Enter.
			continue
		}
		switch classifyComposer(pane, want) {
		case composerCleared:
			// Empty composer. After we watched it hold our message this is the
			// submit we were waiting for; before that (attempt 0, straight after
			// the paste) it proves nothing — the paste may never have registered.
			if held {
				return sendVerified
			}
			return sendUnverified
		case composerOther:
			// The pane holds someone else's text where our paste should be: the
			// message is provably gone. Report it so the caller queues it.
			return sendMismatch
		}
		held = true
		if attempt > 0 {
			if !composerHoldsMessage(pane, want) {
				// Ours, but not in a form another Enter may be pressed on: a
				// partial/wrapped render, or our text with a user's keystrokes
				// appended. Stop pressing keys; the outcome is unknown.
				return sendUnverified
			}
			empty, foreign, found := composerTrail(pane)
			if foreign {
				return sendUnverified
			}
			if found && empty > 0 {
				// Multiline-stuck: Enter would only append newlines here.
				// One recovery attempt total, and never for a newline count
				// our own keypresses could not have produced.
				if empty > submitBackspaceMax {
					return sendUnverified
				}
				if c.recoverTrailingNewlines(ctx, target, session, want, empty) {
					// Verified single-line composer holding exactly our
					// message: one final Enter, then stop either way. Nothing
					// re-checks the composer afterwards, so the outcome of that
					// last Enter is honestly reported as unverified.
					_, _ = c.run(ctx, nil, "send-keys", "-t", target, "Enter")
				}
				return sendUnverified
			}
		}
		_, _ = c.run(ctx, nil, "send-keys", "-t", target, "Enter")
		if attempt < submitRetries {
			c.Sleep(submitVerifyWindow)
		}
	}
	// The bound was reached with the composer still holding our message: every
	// Enter was ignored, and we cannot tell whether the first one landed.
	return sendUnverified
}

// composerStable captures the pane and reports false on any error or when an
// attached client showed keyboard activity inside the idle window — the signal
// to abort all further keypresses and leave the composer to its user.
func (c *Client) composerStable(ctx context.Context, session string) (string, bool) {
	pane, captureErr := c.CaptureAnsi(ctx, session)
	activity, activityErr := c.clientActivity(ctx, session)
	if captureErr != nil || activityErr != nil {
		return "", false
	}
	if !activity.IsZero() && c.Now().Sub(activity) < clientIdleWindow {
		return "", false
	}
	return pane, true
}

// recoverTrailingNewlines deletes count literal newlines from the end of the
// composer with individual BSpace presses, then verifies the composer is a
// single line again and still holds exactly want. It returns true only in that
// fully verified state; any other outcome (BSpace ignored by an old build,
// content changed, client activity, capture error) returns false so the caller
// gives up without sending Enter.
func (c *Client) recoverTrailingNewlines(ctx context.Context, target, session, want string, count int) bool {
	for i := 0; i < count; i++ {
		_, _ = c.run(ctx, nil, "send-keys", "-t", target, "BSpace")
		c.Sleep(submitBackspaceDelay)
	}
	pane, ok := c.composerStable(ctx, session)
	if !ok {
		return false
	}
	if composerContent(pane) != want {
		return false
	}
	empty, foreign, found := composerTrail(pane)
	return found && !foreign && empty == 0
}

type OpenOptions struct {
	Resume   bool
	Codex    bool
	NoPrompt bool
	Legacy   bool
}

const (
	legacyOnboarding   = "Selam, sen %s agentisin (ismine gore calisirsin; proje detayini kullanici sonra verebilir). Bu COK-SERVISLI bir sunucu (nginx 80/443 public + Cloudflare, Docker+systemd: gitea, mail, probot, kitap...). Orchestrator=server-main, evi /srv/server-main. ONCE OKU: /srv/server-main/AGENT-ONBOARDING.md (server + PORT kurallari) ve /srv/server-main/agentbook.json (agentlar + iletisim). DIGER AGENTLARLA KONUSMA: bp msg <ad> <mesaj> — cevabi okumak icin bp peek <ad>, filo icin bp status. Elle tmux send-keys KULLANMA (bp mesgul-kontrolu ve kuyrugu atlanir); sadece bp calismazsa bilincli fallback. bp'nin tum komutlari ve kurallari: /srv/blueprint/README.md (ya da bp help). Model secimi + codex + subagent kurallari global CLAUDE.md inde (otomatik yuklu) - uygula. UYARI1 ghost-text: soluk oneri gercek degil. UYARI2 vim modu: submit icin cogu zaman fazladan Enter (bp msg bunu kendi halleder). Okuyunca kisa hazirim de."
	portableOnboarding = "Sen '%s' agentisin. Diger agentlarla iletisim: bp msg <ad> <mesaj>."
)

// mungeProjectPath replicates Claude Code's cwd -> project-dir encoding: every
// byte that is not an ASCII letter or digit becomes '-' (so "/srv/probot-business"
// -> "-srv-probot-business", and "/srv/kitap/.worktrees/x" -> "-srv-kitap--worktrees-x").
func mungeProjectPath(dir string) string {
	b := make([]byte, len(dir))
	for i := 0; i < len(dir); i++ {
		c := dir[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b[i] = c
		default:
			b[i] = '-'
		}
	}
	return string(b)
}

// claudeProjectsRoot is the directory where Claude Code stores per-cwd session logs.
func ClaudeProjectsRoot() string {
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".claude", "projects")
}

// readCustomTitle returns the customTitle set for a Claude session file (via the
// `{"type":"custom-title",...}` records bp writes with /rename).
//
// A session can be renamed more than once, and each /rename APPENDS a fresh
// record rather than rewriting the first one, so the newest record is the
// session's real name. Reading whichever record comes first would report the
// name the agent used to have — which is exactly what made a renamed agent show
// "-" in bp status and made `bp open --resume` miss its own conversation.
//
// So: never stop at the first hit. Small files are read whole; large ones get a
// bounded head scan (the common case: titled on line 1 and never renamed) plus a
// bounded tail scan, and the tail wins whenever it holds a record, because a
// later rename can only have landed at the end.
func ReadCustomTitle(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", false
	}

	if info.Size() <= titleTailBytes {
		return lastCustomTitle(f, -1)
	}

	head, headOK := lastCustomTitle(f, 200)
	if _, err := f.Seek(info.Size()-titleTailBytes, io.SeekStart); err != nil {
		return head, headOK
	}
	tail := bufio.NewScanner(f)
	tail.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	tail.Scan() // discard the partial line the offset landed in
	if title, ok := scanCustomTitles(tail, -1); ok {
		return title, true
	}
	return head, headOK
}

// lastCustomTitle reads up to limit lines from the reader's current position
// (limit < 0 means to the end) and returns the last custom-title record seen.
func lastCustomTitle(r io.Reader, limit int) (string, bool) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	return scanCustomTitles(scanner, limit)
}

func scanCustomTitles(scanner *bufio.Scanner, limit int) (string, bool) {
	title, found := "", false
	for i := 0; (limit < 0 || i < limit) && scanner.Scan(); i++ {
		if value, ok := customTitleLine(scanner.Bytes()); ok {
			title, found = value, true
		}
	}
	return title, found
}

// titleTailBytes bounds the fallback scan so `bp status` stays cheap on the
// large session files (100 MB+) this runs against.
const titleTailBytes = 512 * 1024

func customTitleLine(line []byte) (string, bool) {
	if !bytes.Contains(line, []byte(`"custom-title"`)) {
		return "", false
	}
	var rec struct {
		Type        string `json:"type"`
		CustomTitle string `json:"customTitle"`
	}
	if json.Unmarshal(line, &rec) == nil && rec.Type == "custom-title" && rec.CustomTitle != "" {
		return rec.CustomTitle, true
	}
	return "", false
}

// resumeSessionID finds the most recently modified Claude session under
// projectsRoot for `dir` whose customTitle equals `agent`, returning that
// session's id (the file's base name). This disambiguates agents that share a
// working directory — which `claude -c` (continue-most-recent-in-cwd) cannot,
// causing it to resume a co-located agent's conversation instead of ours.
func ResumeSessionPath(projectsRoot, dir, agent string) (string, bool) {
	entries, err := os.ReadDir(filepath.Join(projectsRoot, mungeProjectPath(dir)))
	if err != nil {
		return "", false
	}
	var bestPath string
	var bestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		title, ok := ReadCustomTitle(filepath.Join(projectsRoot, mungeProjectPath(dir), e.Name()))
		if !ok || title != agent {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if bestPath == "" || info.ModTime().After(bestMod) {
			bestPath = filepath.Join(projectsRoot, mungeProjectPath(dir), e.Name())
			bestMod = info.ModTime()
		}
	}
	return bestPath, bestPath != ""
}

func ResumeSessionID(projectsRoot, dir, agent string) (string, bool) {
	path, ok := ResumeSessionPath(projectsRoot, dir, agent)
	if !ok {
		return "", false
	}
	return strings.TrimSuffix(filepath.Base(path), ".jsonl"), true
}

func (c *Client) Open(ctx context.Context, session, dir string, opts OpenOptions, warn func(string)) error {
	if c.HasSession(ctx, session) {
		process, err := c.PaneProcess(ctx, session)
		if err != nil {
			return nil // unreadable pane: assume open rather than double-launch
		}
		if IsAgentCommand(process.Command) {
			return nil
		}
		if !isShellCommand(process.Command) {
			return fmt.Errorf("session %s exists but its pane runs %q (not an agent, not a shell); bp close %s first", session, process.Command, session)
		}
		// The agent exited and left a bare shell: relaunch in place. Clear
		// anything half-typed at the prompt, and cd because the shell may have
		// wandered since the session was created.
		if warn != nil {
			warn(session + ": dead shell in existing session; relaunching agent in place")
		}
		if _, err := c.run(ctx, nil, "send-keys", "-t", "="+session+":", "C-u"); err != nil {
			return err
		}
		if _, err := c.run(ctx, nil, "send-keys", "-t", "="+session+":", "cd "+shellQuote(dir), "Enter"); err != nil {
			return err
		}
	} else if _, err := c.run(ctx, nil, "new-session", "-d", "-s", session, "-c", dir); err != nil {
		return err
	}
	// RC oturum adi tmux adiyla eslessin diye prefix ver (claude.ai/code listesinde
	// hostname yerine agent adi gorunur).
	command := "CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=" + session + " claude --dangerously-skip-permissions"
	if opts.Resume && !opts.Codex {
		// Resume THIS agent's own conversation by id, not `claude -c` (which
		// continues whichever conversation in the cwd is most recent and so
		// grabs a co-located agent's session in a shared directory).
		if id, ok := ResumeSessionID(ClaudeProjectsRoot(), dir, session); ok {
			command += " --resume " + id
		} else if warn != nil {
			warn("no prior '" + session + "' conversation found under " + dir + "; opening a fresh session")
		}
	}
	if opts.Codex {
		command = `codex -c model_reasoning_effort="high"`
	}
	if _, err := c.run(ctx, nil, "send-keys", "-t", "="+session+":", command, "Enter"); err != nil {
		return err
	}

	ready, picked, trusted := false, false, false
	for i := 0; i < 45; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		pane, err := c.Capture(ctx, session)
		if err == nil {
			lower := strings.ToLower(pane)
			if opts.Codex {
				if !trusted && strings.Contains(lower, "trust") && strings.Contains(lower, "directory") {
					_, _ = c.run(ctx, nil, "send-keys", "-t", "="+session+":", "Enter")
					trusted = true
					c.Sleep(2 * time.Second)
					continue
				}
				if strings.Contains(pane, "›") {
					ready = true
					break
				}
			} else {
				if RemoteControlMenu(pane) {
					_, _ = c.run(ctx, nil, "send-keys", "-t", "="+session+":", "Enter")
					c.Sleep(2 * time.Second)
					continue
				}
				if !picked && (strings.Contains(lower, "resume from summary") || strings.Contains(lower, "resume full session")) {
					_, _ = c.run(ctx, nil, "send-keys", "-t", "="+session+":", "Down")
					c.Sleep(500 * time.Millisecond)
					_, _ = c.run(ctx, nil, "send-keys", "-t", "="+session+":", "Enter")
					picked = true
					c.Sleep(2 * time.Second)
					continue
				}
				if strings.Contains(lower, "bypass permissions") || strings.Contains(pane, "-- INSERT --") {
					ready = true
					break
				}
			}
		}
		c.Sleep(2 * time.Second)
	}
	if !ready && warn != nil {
		warn("WARNING: agent did not appear ready; continuing anyway")
	}
	c.Sleep(time.Second)
	if !opts.Codex {
		_ = c.Send(ctx, session, "/rename "+session)
		c.Sleep(time.Second)
		_ = c.Send(ctx, session, "/remote-control")
		// When RC is already active (a resumed session reconnects on its own)
		// the command opens the Continue/Disconnect menu instead of just
		// printing the URL, and the menu can render seconds late. Sweep until
		// two consecutive clean captures so the onboarding prompt below lands
		// in the composer, not the menu.
		clean := 0
		for tries := 0; tries < 10 && clean < 2; tries++ {
			c.Sleep(time.Second)
			if pane, err := c.Capture(ctx, session); err == nil && RemoteControlMenu(pane) {
				_, _ = c.run(ctx, nil, "send-keys", "-t", "="+session+":", "Enter")
				clean = 0
				continue
			}
			clean++
		}
	}
	if !opts.NoPrompt {
		onboarding := portableOnboarding
		if opts.Legacy {
			onboarding = legacyOnboarding
		}
		if err := c.Send(ctx, session, fmt.Sprintf(onboarding, session)); err != nil {
			if warn != nil {
				warn("WARNING: could not send onboarding prompt: " + err.Error())
			}
		}
	}
	return nil
}

func (c *Client) Close(ctx context.Context, session string) error {
	_, err := c.run(ctx, nil, "kill-session", "-t", "="+session)
	return err
}

// RenameSession renames a live tmux session. The `=` prefix forces an exact
// match so a rename cannot land on a session that merely shares a prefix.
func (c *Client) RenameSession(ctx context.Context, session, name string) error {
	_, err := c.run(ctx, nil, "rename-session", "-t", "="+session, name)
	return err
}

func (c *Client) DisplaySession(ctx context.Context) (string, error) {
	out, err := c.run(ctx, nil, "display-message", "-p", "#S")
	return strings.TrimSpace(string(out)), err
}

// Commands maps each session to the foreground command of its first pane
// (e.g. "claude", "codex", "bwrap", "zsh"). Used to tell Claude sessions
// apart from Codex ones before sending Claude-only slash commands.
func (c *Client) Commands(ctx context.Context) (map[string]string, error) {
	out, err := c.run(ctx, nil, "list-panes", "-a", "-F", "#{session_name}\t#{pane_active}\t#{pane_current_command}")
	if err != nil {
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "no server running") || strings.Contains(lower, "no sessions") {
			return nil, nil
		}
		return nil, err
	}
	commands := map[string]string{}
	active := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 || strings.TrimSpace(fields[0]) == "" {
			continue
		}
		session, isActive := fields[0], fields[1] == "1"
		if _, seen := commands[session]; !seen || (isActive && !active[session]) {
			commands[session] = strings.TrimSpace(fields[2])
			active[session] = isActive
		}
	}
	return commands, nil
}

// PressEnter sends a bare Enter key to the session. Used to dismiss
// harmless modal menus (e.g. the /remote-control "Continue" dialog).
func (c *Client) PressEnter(ctx context.Context, session string) error {
	_, err := c.run(ctx, nil, "send-keys", "-t", "="+session+":", "Enter")
	return err
}
