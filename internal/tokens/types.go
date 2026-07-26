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
}

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
	Day        string `json:"day"`
	Agent      string `json:"agent"`
	Src        string `json:"src"`
	Model      string `json:"model"`
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
