package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode"
)

var promptLine = regexp.MustCompile(`^[\t ]*[❯›]`)

// Typing reports whether the final rendered composer line contains real text.
// Older prompt lines are deliberately ignored.
func Typing(pane string) bool {
	var composer string
	for _, line := range strings.Split(pane, "\n") {
		if promptLine.MatchString(line) {
			composer = line
		}
	}
	if composer == "" {
		return false
	}
	after := promptLine.ReplaceAllString(composer, "")
	after = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, after)
	return after != ""
}

func Busy(pane string) bool {
	return strings.Contains(strings.ToLower(pane), "esc to interrupt")
}

type Client struct {
	Bin   string
	Sleep func(time.Duration)
}

// Location records both the current pane directory and the directory in which
// its tmux session was created.
type Location struct {
	Session    string
	CurrentDir string
	StartDir   string
}

func New() *Client {
	return &Client{Bin: "tmux", Sleep: time.Sleep}
}

func (c *Client) run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
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

func (c *Client) Capture(ctx context.Context, session string) (string, error) {
	out, err := c.run(ctx, nil, "capture-pane", "-t", session, "-p")
	return string(out), err
}

func (c *Client) HasSession(ctx context.Context, session string) bool {
	_, err := c.run(ctx, nil, "has-session", "-t", session)
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
	pane, err := c.Capture(ctx, session)
	return Typing(pane), err
}

func (c *Client) IsBusy(ctx context.Context, session string) (bool, error) {
	pane, err := c.Capture(ctx, session)
	return Busy(pane), err
}

var ErrTyping = errors.New("composer is not empty")

// Send preserves the timing and submit verification of bin/agent send_msg.
func (c *Client) Send(ctx context.Context, session, message string) error {
	pane, err := c.Capture(ctx, session)
	if err != nil {
		return err
	}
	if Typing(pane) {
		return ErrTyping
	}
	if strings.Contains(message, "\n") {
		if _, err = c.run(ctx, []byte(message), "load-buffer", "-b", "agentmsg", "-"); err != nil {
			return err
		}
		if _, err = c.run(ctx, nil, "paste-buffer", "-b", "agentmsg", "-d", "-t", session); err != nil {
			return err
		}
	} else if _, err = c.run(ctx, nil, "send-keys", "-t", session, "-l", message); err != nil {
		return err
	}
	c.Sleep(400 * time.Millisecond)
	if _, err = c.run(ctx, nil, "send-keys", "-t", session, "Enter"); err != nil {
		return err
	}
	c.Sleep(1200 * time.Millisecond)
	pane, err = c.Capture(ctx, session)
	if err != nil {
		return err
	}
	if !Busy(pane) && strings.Contains(pane, tailBytes(message, 40)) {
		_, err = c.run(ctx, nil, "send-keys", "-t", session, "Enter")
	}
	return err
}

func tailBytes(value string, count int) string {
	if len(value) <= count {
		return value
	}
	return value[len(value)-count:]
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
	if _, err := c.run(ctx, nil, "send-keys", "-t", session, command, "Enter"); err != nil {
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
					_, _ = c.run(ctx, nil, "send-keys", "-t", session, "Enter")
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
					_, _ = c.run(ctx, nil, "send-keys", "-t", session, "Down")
					c.Sleep(500 * time.Millisecond)
					_, _ = c.run(ctx, nil, "send-keys", "-t", session, "Enter")
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
	_, err := c.run(ctx, nil, "kill-session", "-t", session)
	return err
}

func (c *Client) DisplaySession(ctx context.Context) (string, error) {
	out, err := c.run(ctx, nil, "display-message", "-p", "#S")
	return strings.TrimSpace(string(out)), err
}
