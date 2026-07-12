package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	lines := strings.Split(pane, "\n")
	last := -1
	for i, line := range lines {
		if promptLine.MatchString(line) {
			last = i
		}
	}
	if last < 0 {
		return false
	}
	var b strings.Builder
	for _, line := range lines[last:] {
		b.WriteString(promptLine.ReplaceAllString(StripDim(line), ""))
		b.WriteByte(' ') // a wrap between rows is whitespace, not a join
	}
	return codexChip.MatchString(b.String())
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
// unsubmitted message: either the literal text (space-collapsed match) or the
// Codex large-paste chip that stands in for it. When true, pressing Enter again
// is safe; when false the composer either cleared (submitted) or a user edited
// it, and no key may be sent.
func composerHoldsMessage(pane, want string) bool {
	return composerContent(pane) == want || codexPasteChip(pane)
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

func Busy(pane string) bool {
	// "esc to interrupt" alone is NOT enough: transcripts often QUOTE the phrase (rule
	// announcements), and a "2 shells · esc to interrupt" footer only means background shells.
	// Count busy only when the line carries a live indicator signature: the ⏵ footer or a
	// spinner timer like "(23s ·" (2026-07-10, third false-positive class).
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
func (c *Client) readyToSend(ctx context.Context, session string) (bool, error) {
	firstPane, err := c.CaptureAnsi(ctx, session)
	if err != nil || Typing(firstPane) {
		return false, err
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

var bufferSequence uint64

// Send first injects the message, then lets the TUI turn a large paste into its
// internal "Pasted text" object before submitting it. Once injection succeeds,
// the operation is at-most-once: later ambiguity is treated as delivered so the
// dispatcher can never paste the same message again. If a user touches the
// attached client during the settle window, their mixed composer is left alone
// and Enter is deliberately not sent.
func (c *Client) Send(ctx context.Context, session, message string) error {
	ready, err := c.readyToSend(ctx, session)
	if err != nil {
		return err
	}
	if !ready {
		return ErrTyping
	}
	target := "=" + session + ":"
	if strings.Contains(message, "\n") {
		buffer := fmt.Sprintf("bp-agentmsg-%d-%d", os.Getpid(), atomic.AddUint64(&bufferSequence, 1))
		if _, err = c.run(ctx, []byte(message), "load-buffer", "-b", buffer, "-"); err != nil {
			return err
		}
		_, err = c.run(ctx, nil, "paste-buffer", "-b", buffer, "-d", "-t", target)
		if err != nil {
			// A failed paste may leave the uniquely named buffer behind.
			_, _ = c.run(ctx, nil, "delete-buffer", "-b", buffer)
			return err
		}
	} else {
		_, err = c.run(ctx, nil, "send-keys", "-t", target, "-l", message)
		if err != nil {
			return err
		}
	}

	// From this point onward, returning an error would leave the queue record
	// pending and cause a duplicate paste on the next dispatch.
	c.Sleep(composerSettleWindow)
	c.submit(ctx, target, session, message)
	return nil
}

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
func (c *Client) submit(ctx context.Context, target, session, message string) {
	want := stripSpace(message)
	for attempt := 0; attempt <= submitRetries; attempt++ {
		pane, ok := c.composerStable(ctx, session)
		if !ok {
			return
		}
		if codexBusyQueue(pane) {
			// TOCTOU: the target went BUSY after Dispatch's idle check and our
			// paste. On a busy Codex, Enter never submits (it only expands the
			// chip); the message must be pushed into Codex's own native queue
			// with Tab. This branch takes precedence over the Enter/BSpace paths
			// so Enter is never sent while the affordance is present.
			_, _ = c.run(ctx, nil, "send-keys", "-t", target, "Tab")
			c.Sleep(submitVerifyWindow)
			next, ok := c.composerStable(ctx, session)
			if !ok {
				return
			}
			if !Typing(next) && !codexPasteChip(next) {
				// Composer cleared: the message moved into Codex's queue.
				// Tab-queuing is a real delivery, so returning here (Send -> nil)
				// keeps the msgq "delivered" mark correct.
				return
			}
			// Still holding our paste: retry Tab, bounded by the loop. If it
			// never clears we give up silently at the bound WITHOUT ever
			// falling through to Enter (ambiguity resolves to delivered).
			continue
		}
		if attempt == 0 {
			// The injected message is sitting in the composer; an empty
			// composer means it already submitted (or a paste-chip replaced
			// it, handled by the Typing check).
			if !Typing(pane) {
				return
			}
		} else {
			if !composerHoldsMessage(pane, want) {
				// Either it submitted (composer cleared) or the content no
				// longer matches ours (user edited it). Never press Enter on
				// foreign text. A Codex large-paste chip standing in for our
				// literal message still counts as holding our message.
				return
			}
			empty, foreign, found := composerTrail(pane)
			if foreign {
				return
			}
			if found && empty > 0 {
				// Multiline-stuck: Enter would only append newlines here.
				// One recovery attempt total, and never for a newline count
				// our own keypresses could not have produced.
				if empty > submitBackspaceMax {
					return
				}
				if c.recoverTrailingNewlines(ctx, target, session, want, empty) {
					// Verified single-line composer holding exactly our
					// message: one final Enter, then stop either way.
					_, _ = c.run(ctx, nil, "send-keys", "-t", target, "Enter")
				}
				return
			}
		}
		_, _ = c.run(ctx, nil, "send-keys", "-t", target, "Enter")
		if attempt < submitRetries {
			c.Sleep(submitVerifyWindow)
		}
	}
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
}

const onboarding = "Selam, sen '%s' agentisin (ismine gore calisirsin; proje detayini kullanici sonra verebilir). Bu COK-SERVISLI bir sunucu (nginx 80/443 public + Cloudflare, Docker+systemd: gitea, mail, probot, kitap...). Orchestrator=server-main, evi /srv/server-main. ONCE OKU: /srv/server-main/AGENT-ONBOARDING.md (server + PORT kurallari) ve /srv/server-main/agentbook.json (agentlar + iletisim). DIGER AGENTLARLA KONUSMA: tmux send-keys -t <hedef> -l '<mesaj>' + AYRI Enter. Model secimi + codex + subagent kurallari global CLAUDE.md'inde (otomatik yuklu) - uygula. UYARI1 ghost-text: soluk oneri gercek degil. UYARI2 vim modu: submit icin cogu zaman fazladan Enter. Okuyunca kisa 'hazirim' de."

func (c *Client) Open(ctx context.Context, session, dir string, opts OpenOptions, warn func(string)) error {
	if c.HasSession(ctx, session) {
		return nil
	}
	if _, err := c.run(ctx, nil, "new-session", "-d", "-s", session, "-c", dir); err != nil {
		return err
	}
	command := "claude --dangerously-skip-permissions"
	if opts.Resume {
		command += " -c"
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
		c.Sleep(time.Second)
	}
	if !opts.NoPrompt {
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

func (c *Client) DisplaySession(ctx context.Context) (string, error) {
	out, err := c.run(ctx, nil, "display-message", "-p", "#S")
	return strings.TrimSpace(string(out)), err
}
