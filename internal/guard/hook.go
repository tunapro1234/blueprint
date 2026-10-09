package guard

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// The tool-call tripwire behind `bp guard hook claude` (docs/security/
// guard-hooks-module.md). A harness hands every tool call to the hook before
// it runs; the hook alerts when an agent that recently received outside text
// touches a secret, or when anything touches a canary. It never denies and
// never blocks an untainted agent: a broken guard must not break the agent.

// HookMode is what the hook does on a tainted secret access.
type HookMode string

const (
	// HookObserve writes the alert and lets the call run.
	HookObserve HookMode = "observe"
	// HookAsk also asks the human to confirm the call.
	HookAsk HookMode = "ask"
)

// DefaultTaintWindow is how long outside text keeps an agent tainted.
const DefaultTaintWindow = 30 * time.Minute

// taintTailBytes bounds how much of messages.jsonl one hook call reads.
const taintTailBytes = 1 << 20

// HookInput is the part of a harness's pre-tool payload the hook reads.
type HookInput struct {
	SessionID string          `json:"session_id"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	Cwd       string          `json:"cwd"`
}

// ParseHook reads one pre-tool payload.
func ParseHook(r io.Reader) (HookInput, error) {
	var in HookInput
	data, err := io.ReadAll(io.LimitReader(r, 4<<20))
	if err != nil {
		return in, err
	}
	if err := json.Unmarshal(data, &in); err != nil {
		return in, err
	}
	return in, nil
}

// Flatten joins every string value in a tool input, in key order, one per
// line, so path and command patterns can match wherever the harness put them.
func Flatten(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			out = append(out, t)
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(t[k])
			}
		}
	}
	walk(v)
	return strings.Join(out, "\n")
}

// MatchCanary reports the first canary pattern found in input. A pattern is
// a plain substring (a path or a file name); a leading "~/" is expanded with
// home so both spellings match.
func MatchCanary(patterns []string, home, input string) (string, bool) {
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.Contains(input, p) {
			return p, true
		}
		if home != "" && strings.HasPrefix(p, "~/") && strings.Contains(input, home+p[1:]) {
			return p, true
		}
	}
	return "", false
}

// Taint is the most recent outside message delivered to an agent.
type Taint struct {
	// Peer names the source for people: alias and peer ID, or the sender.
	Peer string
	// Alias is the peer alias alone, as Watch events name peers.
	Alias string
	ID    string
	At    time.Time
}

// logLine is the subset of msgq.LogEntry the hook reads; guard does not
// import msgq.
type logLine struct {
	ID       string  `json:"id"`
	To       string  `json:"to"`
	From     string  `json:"from"`
	TS       float64 `json:"ts"`
	Finished float64 `json:"finished"`
	Status   string  `json:"status"`
	Peer     string  `json:"peer"`
	PeerID   string  `json:"peerId"`
	Remote   bool    `json:"remote"`
}

// TaintFrom reads the tail of a messages.jsonl and returns the newest
// outside message to agent that reached it at or after since. A message is
// outside when it carries an Origin (remote) or an "external:" sender label.
// A missing log means no taint.
func TaintFrom(logPath, agent string, since time.Time) (Taint, bool, error) {
	f, err := os.Open(logPath)
	if os.IsNotExist(err) {
		return Taint{}, false, nil
	}
	if err != nil {
		return Taint{}, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Taint{}, false, err
	}
	offset := info.Size() - taintTailBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return Taint{}, false, err
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), taintTailBytes)
	first := offset > 0
	var best Taint
	found := false
	for scanner.Scan() {
		line := scanner.Bytes()
		if first {
			// The first line of a tail read is usually cut; skip it.
			first = false
			continue
		}
		if !bytes.Contains(line, []byte(agent)) {
			continue
		}
		var e logLine
		if json.Unmarshal(line, &e) != nil || e.To != agent || !reached(e.Status) {
			continue
		}
		if !e.Remote && !strings.HasPrefix(e.From, "external:") {
			continue
		}
		stamp := e.Finished
		if stamp == 0 {
			stamp = e.TS
		}
		at := time.Unix(0, int64(stamp*1e9))
		if at.Before(since) || (found && !at.After(best.At)) {
			continue
		}
		peer := e.Peer
		if e.PeerID != "" {
			peer = strings.TrimSpace(peer + " " + e.PeerID)
		}
		if peer == "" {
			peer = e.From
		}
		alias := e.Peer
		if alias == "" {
			alias = e.From
		}
		best, found = Taint{Peer: peer, Alias: alias, ID: e.ID, At: at}, true
	}
	return best, found, scanner.Err()
}

// reached reports whether a final status means the text may have reached the
// agent. Unknown statuses count: a missed taint is worse than a spare alert.
func reached(status string) bool {
	s := strings.ToLower(status)
	return !strings.HasPrefix(s, "canceled") && !strings.HasPrefix(s, "not delivered") && !strings.HasPrefix(s, "failed")
}

// HookConfig is the hook's behaviour, from its command line.
type HookConfig struct {
	Mode     HookMode
	Canaries []string
	Home     string
	Window   time.Duration
}

// HookResult is what one tool call produced.
type HookResult struct {
	// Alert is set when the call must be reported.
	Alert *Alert
	// Output is the hook's stdout (empty means "no opinion").
	Output []byte
}

// Match is the fast path: whether a tool call touches a secret or a canary.
// It does no I/O. canary is true for a canary hit.
func (c HookConfig) Match(in HookInput) (what string, canary, ok bool) {
	input := Flatten(in.ToolInput)
	if p, hit := MatchCanary(c.Canaries, c.Home, input); hit {
		return in.ToolName + ": " + p, true, true
	}
	what, ok = Sensitive(in.ToolName, input)
	return what, false, ok
}

// Evaluate decides a matched tool call. taint is the agent's newest outside
// message, if any, within the window. An untainted access to an ordinary
// secret is normal work and yields nothing; a canary always alerts.
func (c HookConfig) Evaluate(in HookInput, agent, what string, canary bool, taint *Taint, now time.Time) HookResult {
	if taint == nil && !canary {
		return HookResult{}
	}
	alert := &Alert{Time: now, Rule: "tainted-secret-access", Severity: High, Agent: agent}
	switch {
	case taint != nil && canary:
		alert.Rule = "canary-access"
		alert.Peer, alert.Channel = taint.Peer, taint.ID
		alert.Summary = fmt.Sprintf("%s touched canary %s %s after outside text from %s", agentOr(agent), what, ago(now, taint.At), taint.Peer)
	case canary:
		alert.Rule = "canary-access"
		alert.Summary = fmt.Sprintf("%s touched canary %s", agentOr(agent), what)
	default:
		alert.Peer, alert.Channel = taint.Peer, taint.ID
		alert.Summary = fmt.Sprintf("%s accessed %s %s after outside text from %s", agentOr(agent), what, ago(now, taint.At), taint.Peer)
	}
	result := HookResult{Alert: alert}
	if c.Mode == HookAsk && taint != nil {
		reason := fmt.Sprintf("bp guard: this agent received outside text from %s %s; confirm access to %s", taint.Peer, ago(now, taint.At), what)
		out, err := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "ask",
			"permissionDecisionReason": reason,
		}})
		if err == nil {
			result.Output = out
		}
	}
	return result
}

func agentOr(agent string) string {
	if agent == "" {
		return "an unknown agent"
	}
	return agent
}

func ago(now, at time.Time) string {
	d := now.Sub(at).Round(time.Minute)
	if d < time.Minute {
		return "under a minute ago"
	}
	return fmt.Sprintf("%d min ago", int(d.Minutes()))
}
