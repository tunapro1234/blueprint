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

// tailSize is the window bp normally reads from the end of a session file:
// enough for the recent records, cheap enough to run for every agent on
// every status refresh. A variable only so tests can shrink it.
var tailSize int64 = 500 * 1024

const (
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

// Persisted transcripts from older bp versions use the Turkish digest prefix;
// accept it alongside the current English form when classifying automatic input.
var digestPrefix = regexp.MustCompile(`^\[\d+ (?:accumulated announcements|birikmis duyuru)`)

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
	metrics, ok := cachedClaudeTail(file, info)
	if !ok {
		return State{LastHumanAge: -1}
	}
	return metrics.state(path, time.Now())
}

// claudeRow is one transcript record reduced to what the metrics use. Decoding
// is the costly part of a read, so the cache keeps rows instead of bytes.
type claudeRow struct {
	// ignored rows (unparsable or sidechain) still count as content.
	ignored  bool
	boundary bool
	// postTokens is the compact boundary's reported size, -1 when absent.
	postTokens int
	stamp      time.Time
	stamped    bool
	id         string
	model      string
	effort     string
	usage      bool
	ctx        int
	ttl        time.Duration
	human      bool
}

func decodeClaudeRow(line []byte) claudeRow {
	row := claudeRow{postTokens: -1}
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
	if json.Unmarshal(line, &record) != nil || record.IsSidechain {
		row.ignored = true
		return row
	}
	row.stamp, row.stamped = parseTime(record.Timestamp)
	if record.Type == "system" && record.Subtype == "compact_boundary" {
		row.boundary = true
		if record.CompactMetadata != nil && record.CompactMetadata.PostTokens != nil && *record.CompactMetadata.PostTokens >= 0 {
			row.postTokens = *record.CompactMetadata.PostTokens
		}
		return row
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
	row.id = message.ID
	// "<synthetic>" marks interrupt/error placeholders, not a real turn.
	if message.Model != "" && message.Model != "<synthetic>" {
		row.model = message.Model
		// Claude stores effort on the outer assistant record. Keep it paired
		// with this model/turn; an older turn or another agent's global
		// default cannot fill an absent value. Unknown shapes don't discard
		// otherwise valid token usage in the record.
		if record.Type == "assistant" || message.Role == "assistant" {
			_ = json.Unmarshal(record.Effort, &row.effort)
		}
	}
	if message.Usage != nil {
		row.usage = true
		row.ctx = message.Usage.Input + message.Usage.CacheRead + message.Usage.CacheCreation
		// Mixed/absent TTLs and read-only hits do not establish the
		// lifetime of the current prefix. Do not infer it from auth/model.
		c := message.Usage.Creation
		if c.Hour > 0 && c.Five == 0 && c.Hour == message.Usage.CacheCreation {
			row.ttl = time.Hour
		} else if c.Five > 0 && c.Hour == 0 && c.Five == message.Usage.CacheCreation {
			row.ttl = 5 * time.Minute
		}
	}
	if record.Type != "user" && record.Role != "user" && message.Role != "user" {
		return row
	}
	content := message.Content
	if len(content) == 0 {
		content = record.Content
	}
	text := contentText(content)
	row.human = !record.IsCompactSummary && strings.TrimSpace(text) != "" && !automatic(text) && row.stamped
	return row
}

// claudeMetrics is a folded window. Ages are left to the caller, so a cached
// fold stays valid as time passes.
type claudeMetrics struct {
	usageTime, humanTime, cacheTime time.Time
	cacheTTL                        time.Duration
	ctxTokens                       int
	model, effort                   string
}

type claudeFold struct {
	claudeMetrics
	compactTime   time.Time
	messageStarts map[string]time.Time
}

func newClaudeFold() *claudeFold {
	return &claudeFold{messageStarts: make(map[string]time.Time)}
}

func (f *claudeFold) add(row claudeRow) {
	if row.ignored {
		return
	}
	if row.boundary {
		f.compactTime = row.stamp
		f.usageTime, f.ctxTokens = time.Time{}, 0
		f.cacheTime, f.cacheTTL = time.Time{}, 0
		if row.postTokens >= 0 {
			f.usageTime, f.ctxTokens = f.compactTime, row.postTokens
		}
		return
	}
	if row.stamped && row.id != "" {
		if previous, exists := f.messageStarts[row.id]; !exists || row.stamp.Before(previous) {
			f.messageStarts[row.id] = row.stamp
		}
	}
	if row.model != "" {
		f.model, f.effort = row.model, row.effort
	}
	if row.usage && row.stamped && (f.compactTime.IsZero() || row.stamp.After(f.compactTime)) {
		f.usageTime = row.stamp
		f.ctxTokens = row.ctx
		f.cacheTTL, f.cacheTime = row.ttl, row.stamp
		if start, ok := f.messageStarts[row.id]; ok {
			f.cacheTime = start
		}
	}
	if row.human {
		f.humanTime = row.stamp
	}
}

func (f *claudeFold) metrics() claudeMetrics { return f.claudeMetrics }

func (m claudeMetrics) state(path string, now time.Time) State {
	result := State{CtxTokens: m.ctxTokens, LastHumanAge: -1, Model: m.model, Effort: m.effort, Path: path, ThreadID: strings.TrimSuffix(filepath.Base(path), ".jsonl"), UsageAt: m.usageTime}
	if m.cacheTTL > 0 {
		result.CacheTTL, result.CacheAge = m.cacheTTL, age(now, m.cacheTime)
	}
	if !m.usageTime.IsZero() {
		result.Known = true
		result.Age = age(now, m.usageTime)
	}
	if !m.humanTime.IsZero() {
		result.LastHumanAge = age(now, m.humanTime)
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
	// Older announcement producers used DUYURU; keep reading it alongside the English marker.
	return strings.HasPrefix(text, "[ ") &&
		(strings.Contains(first, " ANNOUNCEMENT (") || strings.Contains(first, " DUYURU ("))
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
