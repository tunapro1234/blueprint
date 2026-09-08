package cache

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
	Activity  *Activity
	UsageAt   time.Time
	TurnAt    time.Time
	TurnKnown bool
	Age       time.Duration
	// CacheTTL is known only from the latest request's explicit write breakdown.
	// CacheAge starts at its first observed message chunk, not its final chunk.
	// Neither proves that a future request will reuse the same prefix.
	CacheTTL     time.Duration
	CacheAge     time.Duration
	CtxTokens    int
	LastHumanAge time.Duration
	Known        bool
	// Model is what the session actually ran last, read from the assistant
	// records. Settings files only hold the configured default: /model changes
	// a live session without touching them, so they cannot be trusted for this.
	Model        string
	Effort       string
	ServiceTier  string
	ThreadID     string
	Path         string
	Busy         bool
	Runtime      string
	RuntimeError string
	// Window is the model's context window when the session reports one
	// (codex does, claude does not). Zero means unknown: absolute thresholds
	// apply instead of percentages.
	Window int
}

// CacheHint labels estimates explicitly. A usage timestamp alone is not a TTL.
func (s State) CacheHint() (string, time.Duration) {
	if !s.Known || s.CacheTTL <= 0 {
		return "age", s.Age
	}
	if s.CacheAge < s.CacheTTL {
		return "warm~", s.CacheAge
	}
	return "cold~", s.CacheAge
}

// FolderPath is the one place a caller's folder string becomes a path. An
// agentbook folder may carry an annotation — server-main's reads
// "/srv (home: /srv/server-main)" — and munging that whole string yields a
// project directory that cannot exist, so the agent silently reports no state
// at all (measured 2026-08-25: `bp status` showed server-main with no context
// and no last-talk while `bp bar server-main`, which already normalised, showed
// 244.8k). Normalising here rather than at each call site means a new caller
// cannot reintroduce the bug by forgetting.
//
// The rule is book.FirstPath's, deliberately duplicated: package book imports
// tmux and this package sits beside it, so importing book here to share four
// lines would tie the low-level reader to the fleet model.
func FolderPath(folder string) string {
	fields := strings.Fields(folder)
	if len(fields) > 1 && strings.HasPrefix(fields[0], "/") {
		return fields[0]
	}
	return folder
}

func Read(projectsRoot, folder, agent string) State {
	path, ok := bptmux.ResumeSessionPath(projectsRoot, FolderPath(folder), agent)
	if !ok {
		return State{LastHumanAge: -1}
	}
	return ReadClaudePath(path)
}

// ReadClaudePath reads metrics only from the caller's resolved session.
func ReadClaudePath(path string) State {
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

	var usageTime, humanTime, compactTime time.Time
	var cacheTime time.Time
	var cacheTTL time.Duration
	messageStarts := make(map[string]time.Time)
	ctxTokens := 0
	model, effort := "", ""
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record struct {
			Type             string `json:"type"`
			Subtype          string `json:"subtype"`
			IsCompactSummary bool   `json:"isCompactSummary"`
			IsSidechain      bool   `json:"isSidechain"`
			CompactMetadata  *struct {
				PostTokens *int `json:"postTokens"`
			} `json:"compactMetadata"`
			Timestamp string          `json:"timestamp"`
			Message   json.RawMessage `json:"message"`
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			Effort    json.RawMessage `json:"effort"`
		}
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		if record.IsSidechain {
			continue
		}
		if record.Type == "system" && record.Subtype == "compact_boundary" {
			compactTime, _ = parseTime(record.Timestamp)
			usageTime, ctxTokens = time.Time{}, 0
			cacheTime, cacheTTL = time.Time{}, 0
			if record.CompactMetadata != nil && record.CompactMetadata.PostTokens != nil && *record.CompactMetadata.PostTokens >= 0 {
				usageTime, ctxTokens = compactTime, *record.CompactMetadata.PostTokens
			}
			continue
		}
		var message struct {
			ID      string          `json:"id"`
			Role    string          `json:"role"`
			Model   string          `json:"model"`
			Content json.RawMessage `json:"content"`
			Usage   *struct {
				Input         int `json:"input_tokens"`
				CacheRead     int `json:"cache_read_input_tokens"`
				CacheCreation int `json:"cache_creation_input_tokens"`
				Creation      struct {
					Hour int `json:"ephemeral_1h_input_tokens"`
					Five int `json:"ephemeral_5m_input_tokens"`
				} `json:"cache_creation"`
			} `json:"usage"`
		}
		_ = json.Unmarshal(record.Message, &message)
		if stamp, ok := parseTime(record.Timestamp); ok && message.ID != "" {
			if previous, exists := messageStarts[message.ID]; !exists || stamp.Before(previous) {
				messageStarts[message.ID] = stamp
			}
		}
		// "<synthetic>" marks interrupt/error placeholders, not a real turn.
		if message.Model != "" && message.Model != "<synthetic>" {
			model = message.Model
			// Claude stores effort on the outer assistant record. Keep it paired
			// with this model/turn; an older turn or another agent's global
			// default cannot fill an absent value. Unknown shapes don't discard
			// otherwise valid token usage in the record.
			effort = ""
			if record.Type == "assistant" || message.Role == "assistant" {
				_ = json.Unmarshal(record.Effort, &effort)
			}
		}
		if message.Usage != nil {
			if timestamp, ok := parseTime(record.Timestamp); ok && (compactTime.IsZero() || timestamp.After(compactTime)) {
				usageTime = timestamp
				ctxTokens = message.Usage.Input + message.Usage.CacheRead + message.Usage.CacheCreation
				cacheTTL, cacheTime = 0, timestamp
				if start, ok := messageStarts[message.ID]; ok {
					cacheTime = start
				}
				// Mixed/absent TTLs and read-only hits do not establish the
				// lifetime of the current prefix. Do not infer it from auth/model.
				c := message.Usage.Creation
				if c.Hour > 0 && c.Five == 0 && c.Hour == message.Usage.CacheCreation {
					cacheTTL = time.Hour
				} else if c.Five > 0 && c.Hour == 0 && c.Five == message.Usage.CacheCreation {
					cacheTTL = 5 * time.Minute
				}
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
		if record.IsCompactSummary || strings.TrimSpace(text) == "" || automatic(text) {
			continue
		}
		if timestamp, ok := parseTime(record.Timestamp); ok {
			humanTime = timestamp
		}
	}

	result := State{CtxTokens: ctxTokens, LastHumanAge: -1, Model: model, Effort: effort, Path: path, ThreadID: strings.TrimSuffix(filepath.Base(path), ".jsonl"), UsageAt: usageTime}
	now := time.Now()
	if cacheTTL > 0 {
		result.CacheTTL, result.CacheAge = cacheTTL, age(now, cacheTime)
	}
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
