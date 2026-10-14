package tokens

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultBudget    = int64(2 << 30)
	defaultHardLimit = int64(10 << 30)
)

var istanbul = time.FixedZone("Europe/Istanbul", 3*60*60)

type Config struct {
	StoreDir       string
	ClaudeRoot     string
	CodexRoot      string
	Agentbooks     []string
	Now            func() time.Time
	BudgetBytes    int64
	HardLimitBytes int64
	Log            io.Writer
}

func DefaultConfig(stateDir string, agentbooks []string) Config {
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return Config{
		StoreDir:       filepath.Join(stateDir, "tokens"),
		ClaudeRoot:     filepath.Join(home, ".claude", "projects"),
		CodexRoot:      filepath.Join(home, ".codex", "sessions"),
		Agentbooks:     append([]string(nil), agentbooks...),
		Now:            time.Now,
		BudgetBytes:    defaultBudget,
		HardLimitBytes: defaultHardLimit,
		Log:            os.Stderr,
	}
}

func (c Config) normalized() Config {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.BudgetBytes <= 0 {
		c.BudgetBytes = defaultBudget
	}
	if c.HardLimitBytes <= 0 {
		c.HardLimitBytes = defaultHardLimit
	}
	if c.Log == nil {
		c.Log = io.Discard
	}
	return c
}

type RawRecord struct {
	Key        string `json:"key"`
	TS         string `json:"ts"`
	Day        string `json:"day"`
	Hour       int    `json:"hour"`
	Src        string `json:"src"`
	Agent      string `json:"agent"`
	Owner      string `json:"owner,omitempty"`
	Model      string `json:"model"`
	Requests   int64  `json:"reqs"`
	In         int64  `json:"in"`
	Out        int64  `json:"out"`
	CacheWrite int64  `json:"cache_write"`
	CacheRead  int64  `json:"cache_read"`
	PromptID   string `json:"prompt_id"`
	Prompt     string `json:"prompt"`
	Sidechain  bool   `json:"sidechain,omitempty"`
	// Kind names where this usage came from: "" for the agent's own main
	// transcript, "subagent" for a spawned subagent and "workflow" for a
	// workflow agent. Subagent and workflow usage is attributed to the main
	// agent that owns the session (Owner), so it rolls up under that agent
	// while staying separable by Kind.
	Kind string `json:"kind,omitempty"`
	// WF is the workflow id (wf_...) for Kind=="workflow", empty otherwise.
	WF string `json:"wf,omitempty"`
}

// isSub reports whether the record is subagent or workflow usage rather than
// the agent's own main-transcript usage.
func (r RawRecord) isSub() bool { return r.Kind == "subagent" || r.Kind == "workflow" }

func (r RawRecord) rollupAgent() string {
	if strings.Contains(r.Agent, "@") {
		return r.Agent
	}
	if r.Owner != "" {
		return r.Owner
	}
	return r.Agent
}

type PromptRecord struct {
	Day        string `json:"day"`
	PromptID   string `json:"prompt_id"`
	FirstTS    string `json:"first_ts"`
	Agent      string `json:"agent"`
	Src        string `json:"src"`
	Model      string `json:"model"`
	Requests   int64  `json:"reqs"`
	In         int64  `json:"in"`
	Out        int64  `json:"out"`
	CacheWrite int64  `json:"cache_write"`
	CacheRead  int64  `json:"cache_read"`
	Preview    string `json:"preview"`
}

type HourlyRecord struct {
	Day        string `json:"day"`
	Hour       int    `json:"hour"`
	Agent      string `json:"agent"`
	Src        string `json:"src"`
	Model      string `json:"model"`
	Requests   int64  `json:"reqs"`
	In         int64  `json:"in"`
	Out        int64  `json:"out"`
	CacheWrite int64  `json:"cache_write"`
	CacheRead  int64  `json:"cache_read"`
}

type DailyRecord struct {
	Day   string `json:"day"`
	Agent string `json:"agent"`
	Src   string `json:"src"`
	Model string `json:"model"`
	// Kind separates main ("") from "subagent"/"workflow" usage within an
	// agent's day, so the daily rollup — which outlives the 14-day raw files —
	// still carries the split that bp usage/tokens reports.
	Kind       string `json:"kind,omitempty"`
	Requests   int64  `json:"reqs"`
	In         int64  `json:"in"`
	Out        int64  `json:"out"`
	CacheWrite int64  `json:"cache_write"`
	CacheRead  int64  `json:"cache_read"`
}

type CollectStats struct {
	Files   int `json:"files"`
	Added   int `json:"added"`
	Deduped int `json:"deduped"`
	Days    int `json:"days"`
}

type GCStats struct {
	Compressed int   `json:"compressed"`
	Deleted    int   `json:"deleted"`
	BytesFreed int64 `json:"bytes_freed"`
	StoreBytes int64 `json:"store_bytes"`
}

type FootprintStats struct {
	CheckedAt string `json:"checked_at"`
	Claude    int64  `json:"claude"`
	Codex     int64  `json:"codex"`
	Store     int64  `json:"store"`
	Total     int64  `json:"total"`
}
