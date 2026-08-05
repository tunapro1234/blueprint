package cache

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"time"

	bptmux "blueprint/internal/tmux"
)

const (
	// tailSize is the window bp normally reads from the end of a session file:
	// enough for the recent records, cheap enough to run for every agent on
	// every status refresh.
	tailSize = 500 * 1024
	// maxTailSize bounds the retry for the case tailSize cannot cover: ONE
	// record longer than the window. Claude transcript lines reach 3.5 MB and
	// codex rollout lines 7.1 MB in this fleet, both well past tailSize, and a
	// window that lands inside such a record yields no complete record at all.
	// Reporting "unknown" from that window would be a silent partial read
	// dressed up as a finished one, so the tail is re-read once at this bound
	// before giving up. Beyond 16 MiB a single record is genuinely unreadable
	// here and the state stays unknown.
	maxTailSize = 16 * 1024 * 1024
)

// readTail returns the complete records at the end of a session file: the
// tailSize window with its leading partial record removed, widened once to
// maxTailSize when that window turned out to hold no complete record. false
// means the file could not be read at all.
func readTail(file *os.File, size int64) ([]byte, bool) {
	for _, window := range [...]int64{tailSize, maxTailSize} {
		start := size - window
		if start < 0 {
			start = 0
		}
		data := make([]byte, size-start)
		n, err := file.ReadAt(data, start)
		if err != nil && n == 0 {
			return nil, false
		}
		data = data[:n]
		if start == 0 {
			return data, true
		}
		if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
			data = data[newline+1:]
		} else {
			data = nil
		}
		if len(bytes.TrimSpace(data)) > 0 {
			return data, true
		}
	}
	return nil, false
}

var digestPrefix = regexp.MustCompile(`^\[\d+ birikmis duyuru`)

type State struct {
	Age          time.Duration
	CtxTokens    int
	LastHumanAge time.Duration
	Known        bool
	// Model is what the session actually ran last, read from the assistant
	// records. Settings files only hold the configured default: /model changes
	// a live session without touching them, so they cannot be trusted for this.
	Model string
	// Window is the model's context window when the session reports one
	// (codex does, claude does not). Zero means unknown: absolute thresholds
	// apply instead of percentages.
	Window int
}

func Read(projectsRoot, folder, agent string) State {
	path, ok := bptmux.ResumeSessionPath(projectsRoot, folder, agent)
	if !ok {
		return State{LastHumanAge: -1}
	}
	file, err := os.Open(path)
	if err != nil {
		return State{LastHumanAge: -1}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return State{LastHumanAge: -1}
	}
	data, ok := readTail(file, info.Size())
	if !ok {
		return State{LastHumanAge: -1}
	}

	var usageTime, humanTime time.Time
	ctxTokens := 0
	model := ""
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record struct {
			Type      string          `json:"type"`
			Timestamp string          `json:"timestamp"`
			Message   json.RawMessage `json:"message"`
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
		}
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		var message struct {
			Role    string          `json:"role"`
			Model   string          `json:"model"`
			Content json.RawMessage `json:"content"`
			Usage   *struct {
				CacheRead     int `json:"cache_read_input_tokens"`
				CacheCreation int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		}
		_ = json.Unmarshal(record.Message, &message)
		// "<synthetic>" marks interrupt/error placeholders, not a real turn.
		if message.Model != "" && message.Model != "<synthetic>" {
			model = message.Model
		}
		if message.Usage != nil {
			if timestamp, ok := parseTime(record.Timestamp); ok {
				usageTime = timestamp
				ctxTokens = message.Usage.CacheRead + message.Usage.CacheCreation
			}
		}
		if record.Type != "user" && record.Role != "user" && message.Role != "user" {
			continue
		}
		content := message.Content
		if len(content) == 0 {
			content = record.Content
		}
		text := contentText(content)
		if automatic(text) {
			continue
		}
		if timestamp, ok := parseTime(record.Timestamp); ok {
			humanTime = timestamp
		}
	}

	result := State{CtxTokens: ctxTokens, LastHumanAge: -1, Model: model}
	now := time.Now()
	if !usageTime.IsZero() {
		result.Known = true
		result.Age = age(now, usageTime)
	}
	if !humanTime.IsZero() {
		result.LastHumanAge = age(now, humanTime)
	}
	return result
}

func Fleet(projectsRoot string, folders map[string]string) map[string]State {
	states := make(map[string]State, len(folders))
	for agent, folder := range folders {
		states[agent] = Read(projectsRoot, folder, agent)
	}
	return states
}

func contentText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var values []string
	for _, part := range parts {
		values = append(values, part.Text)
	}
	return strings.Join(values, " ")
}

func automatic(text string) bool {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "[ANNOUNCE") ||
		strings.HasPrefix(text, "health-watch:") ||
		strings.HasPrefix(text, "[usage-policy]") ||
		digestPrefix.MatchString(text) {
		return true
	}
	first := text
	if newline := strings.IndexByte(first, '\n'); newline >= 0 {
		first = first[:newline]
	}
	return strings.HasPrefix(text, "[ ") && strings.Contains(first, " DUYURU (")
}

func parseTime(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil
}

func age(now, then time.Time) time.Duration {
	value := now.Sub(then)
	if value < 0 {
		return 0
	}
	return value
}
