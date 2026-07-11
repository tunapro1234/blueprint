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
// composer actually cleared and, if not, presses Enter again (bounded). Under
// load Claude Code's paste detector can fold the injecting text and the Enter
// into a single PTY read and treat the trailing newline as pasted content
// instead of a submit, leaving the message stuck in the composer. A later,
// separately-read Enter submits it cleanly.
//
// The retries never re-inject the text — only the Enter keypress repeats — so
// the operation stays at-most-once. Enter is pressed only while the live
// composer STILL holds exactly our message and no attached client has been
// active in the settle window, so a user who started editing is never
// disturbed and their partial input is never submitted.
func (c *Client) submit(ctx context.Context, target, session, message string) {
	want := stripSpace(message)
	for attempt := 0; attempt <= submitRetries; attempt++ {
		pane, captureErr := c.CaptureAnsi(ctx, session)
		activity, activityErr := c.clientActivity(ctx, session)
		if captureErr != nil || activityErr != nil {
			return
		}
		if !activity.IsZero() && c.Now().Sub(activity) < clientIdleWindow {
			// A user touched the attached client; leave their composer alone.
			return
		}
		if attempt == 0 {
			// The injected message is sitting in the composer; an empty
			// composer means it already submitted (or a paste-chip replaced
			// it, handled by the Typing check).
			if !Typing(pane) {
				return
			}
		} else if composerContent(pane) != want {
			// Either it submitted (composer cleared) or the content no longer
			// matches ours (user edited it). Never press Enter on foreign text.
			return
		}
		_, _ = c.run(ctx, nil, "send-keys", "-t", target, "Enter")
		if attempt < submitRetries {
			c.Sleep(submitVerifyWindow)
		}
	}
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
