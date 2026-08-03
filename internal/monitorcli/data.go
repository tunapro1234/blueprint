package monitorcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultDataPath  = "/srv/monitor/site/data.json"
	DefaultRadarPath = "/srv/monitor/watch/radar.json"
	maxFeedSize      = 32 << 20
)

type Number struct {
	Value float64
	Valid bool
}

func (n *Number) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "" || text == "null" {
		return nil
	}
	var value float64
	if text[0] == '"' {
		var encoded string
		if json.Unmarshal(data, &encoded) != nil {
			return nil
		}
		parsed, err := strconv.ParseFloat(strings.TrimSpace(encoded), 64)
		if err != nil {
			return nil
		}
		value = parsed
	} else if json.Unmarshal(data, &value) != nil {
		return nil
	}
	n.Value, n.Valid = value, true
	return nil
}

type Document struct {
	GeneratedAt   string
	Usage         *Usage
	Agents        *Agents
	AgentTimeline *AgentTimeline
	Tokens        *Tokens
	Cost          *Cost
	Services      *Services
	Radar         *Radar
	Notes         map[string]string
}

type Usage struct {
	Current UsageSample   `json:"current"`
	History []UsageSample `json:"history"`
}

type UsageSample struct {
	TS            string `json:"ts"`
	Claude5H      Number `json:"claude_5h"`
	Claude7D      Number `json:"claude_7d"`
	ClaudeFable7D Number `json:"claude_fable_7d"`
	// `codex_5h` holds the API's primary quota window regardless of its real
	// length (weekly on the current prolite plan) and `codex_7d` holds the
	// secondary window, which plans like prolite do not have. The keys are the
	// collector's contract; only the display drops the window label.
	Codex5H Number            `json:"codex_5h"`
	Codex7D Number            `json:"codex_7d"`
	Resets  map[string]string `json:"resets"`
}

type Agents struct {
	TS   string      `json:"ts"`
	Tree []AgentNode `json:"tree"`
}

type AgentNode struct {
	Name     string      `json:"name"`
	Nickname string      `json:"nickname"`
	Role     string      `json:"role"`
	Status   string      `json:"status"`
	State    string      `json:"state"`
	Children []AgentNode `json:"children"`
}

type AgentTimeline struct {
	From    string          `json:"from"`
	To      string          `json:"to"`
	StepMin Number          `json:"step_min"`
	Agents  []TimelineAgent `json:"agents"`
}

type TimelineAgent struct {
	Name    string    `json:"name"`
	BusyMin Number    `json:"busy_min"`
	Segs    []Segment `json:"segs"`
}

type Segment struct {
	Start string `json:"a"`
	End   string `json:"b"`
	State string `json:"s"`
}

type Tokens struct {
	Series []TokenSample `json:"series"`
}

type TokenSample struct {
	TS       string                  `json:"ts"`
	Projects map[string]ProjectToken `json:"projects"`
}

type ProjectToken struct {
	Input      Number `json:"in"`
	Output     Number `json:"out"`
	CacheRead  Number `json:"cache_r"`
	CacheWrite Number `json:"cache_w"`
}

type Cost struct {
	Month           string         `json:"month"`
	SubscriptionUSD Number         `json:"subscription_usd"`
	TotalUSD        Number         `json:"total_usd"`
	ValueRatio      Number         `json:"value_ratio"`
	AllTimeUSD      Number         `json:"all_time_usd"`
	ValueRatioTotal Number         `json:"value_ratio_total"`
	Models          []ModelCost    `json:"models"`
	Daily           []DailyCost    `json:"daily"`
	Months          []MonthCost    `json:"months"`
	Subscriptions   *Subscriptions `json:"subscriptions"`
	Cache           *CacheCost     `json:"cache"`
	ByProject       []ProjectCost  `json:"by_project"`
	ByAgent         []AgentCost    `json:"by_agent"`
	ProjectGroups   []ProjectGroup `json:"project_groups"`
	ByProjectDaily  []ProjectDaily `json:"by_project_daily"`
}

type ModelCost struct {
	Model      string `json:"model"`
	Input      Number `json:"input"`
	Output     Number `json:"output"`
	CacheRead  Number `json:"cache_read"`
	CacheWrite Number `json:"cache_write"`
	USD        Number `json:"usd"`
}

type DailyCost struct {
	Date    string            `json:"date"`
	USD     Number            `json:"usd"`
	ByModel map[string]Number `json:"by_model"`
}

type MonthCost struct {
	Month  string `json:"month"`
	USD    Number `json:"usd"`
	Output Number `json:"out"`
}

type Subscriptions struct {
	Claude Number `json:"claude"`
	Codex  Number `json:"codex"`
	Total  Number `json:"total"`
}

type CacheCost struct {
	ReadUSD         Number `json:"read_usd"`
	WriteUSD        Number `json:"write_usd"`
	FreshInputUSD   Number `json:"fresh_input_usd"`
	OutputUSD       Number `json:"output_usd"`
	WithoutCacheUSD Number `json:"without_cache_usd"`
}

type ProjectCost struct {
	Project      string `json:"project"`
	USD          Number `json:"usd"`
	OutputTokens Number `json:"output_tokens"`
}

type AgentCost struct {
	Agent    string   `json:"agent"`
	USD      Number   `json:"usd"`
	Output   Number   `json:"out"`
	Projects []string `json:"projects"`
}

type ProjectGroup struct {
	Group    string   `json:"group"`
	USD      Number   `json:"usd"`
	Output   Number   `json:"out"`
	Projects []string `json:"projects"`
}

type ProjectDaily struct {
	Date     string            `json:"date"`
	Projects map[string]Number `json:"projects"`
}

type Services struct {
	TS    string        `json:"ts"`
	Error string        `json:"error"`
	Units []ServiceUnit `json:"units"`
}

type ServiceUnit struct {
	Name    string       `json:"name"`
	Service *ServiceInfo `json:"service"`
	Timer   *TimerInfo   `json:"timer"`
}

type ServiceInfo struct {
	Active  string `json:"active"`
	Sub     string `json:"sub"`
	Result  string `json:"result"`
	Desc    string `json:"desc"`
	LastRun string `json:"last_run"`
	Since   string `json:"since"`
}

type TimerInfo struct {
	Active  string `json:"active"`
	NextRun string `json:"next_run"`
}

type Radar struct {
	GeneratedAt string            `json:"generated_at"`
	Events      []RadarEvent      `json:"events"`
	Resets      []RadarReset      `json:"resets"`
	LastCheck   map[string]string `json:"last_check"`
}

type RadarEvent struct {
	ID              string          `json:"id"`
	TS              string          `json:"ts"`
	Provider        string          `json:"provider"`
	Models          []string        `json:"models"`
	Notified        bool            `json:"notified"`
	ReactionsDue    string          `json:"reactions_due"`
	ReactionsStatus string          `json:"reactions_status"`
	Reactions       []RadarReaction `json:"reactions"`
	ParamRumors     string          `json:"param_rumors"`
}

type RadarReaction struct {
	Channel   string `json:"channel"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Published string `json:"published"`
	Summary   string `json:"summary"`
}

type RadarReset struct {
	TS       string `json:"ts"`
	Window   string `json:"window"`
	Peak     Number `json:"peak"`
	Notified bool   `json:"notified"`
}

type SourceOptions struct {
	LocalDataPath  string
	LocalRadarPath string
	BaseURL        string
	Client         *http.Client
}

func Load(ctx context.Context, options SourceOptions) (*Document, string, error) {
	if options.LocalDataPath == "" {
		options.LocalDataPath = DefaultDataPath
	}
	data, err := os.ReadFile(options.LocalDataPath)
	source := options.LocalDataPath
	if err != nil {
		dataURL, urlErr := feedURL(options.BaseURL, "data.json")
		if urlErr != nil {
			return nil, "", urlErr
		}
		data, err = fetch(ctx, options.Client, dataURL)
		source = dataURL
	}
	if err != nil {
		return nil, "", fmt.Errorf("load monitor data: %w", err)
	}
	doc, err := Decode(data)
	if err != nil {
		return nil, source, fmt.Errorf("decode monitor data from %s: %w", source, err)
	}
	return doc, source, nil
}

func LoadRadar(ctx context.Context, doc *Document, options SourceOptions) (*Radar, string, error) {
	if options.LocalRadarPath == "" {
		options.LocalRadarPath = DefaultRadarPath
	}
	if data, err := os.ReadFile(options.LocalRadarPath); err == nil {
		var radar Radar
		if err := json.Unmarshal(data, &radar); err != nil {
			return nil, options.LocalRadarPath, err
		}
		return &radar, options.LocalRadarPath, nil
	}
	if doc != nil && doc.Radar != nil {
		return doc.Radar, "data.json", nil
	}
	radarURL, err := feedURL(options.BaseURL, "radar.json")
	if err != nil {
		return nil, "", err
	}
	data, err := fetch(ctx, options.Client, radarURL)
	if err != nil {
		return nil, radarURL, err
	}
	var radar Radar
	if err := json.Unmarshal(data, &radar); err != nil {
		return nil, radarURL, err
	}
	return &radar, radarURL, nil
}

func Decode(data []byte) (*Document, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	doc := &Document{Notes: make(map[string]string)}
	decodeString(root["generated_at"], &doc.GeneratedAt)
	decodeSection(root, "usage", &doc.Usage, doc.Notes)
	decodeSection(root, "agents", &doc.Agents, doc.Notes)
	decodeSection(root, "agent_timeline", &doc.AgentTimeline, doc.Notes)
	decodeSection(root, "tokens", &doc.Tokens, doc.Notes)
	decodeSection(root, "cost", &doc.Cost, doc.Notes)
	decodeSection(root, "services", &doc.Services, doc.Notes)
	decodeSection(root, "radar", &doc.Radar, doc.Notes)
	return doc, nil
}

func decodeSection[T any](root map[string]json.RawMessage, name string, target **T, notes map[string]string) {
	raw, ok := root[name]
	if !ok || string(raw) == "null" {
		return
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		notes[name] = "section could not be decoded: " + err.Error()
		return
	}
	*target = &value
}

func decodeString(raw json.RawMessage, target *string) {
	_ = json.Unmarshal(raw, target)
}

func feedURL(base, filename string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", fmt.Errorf("DASH_URL is empty")
	}
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("invalid DASH_URL: %s", base)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + filename
	parsed.RawQuery, parsed.Fragment = "", ""
	return parsed.String(), nil
}

func fetch(ctx context.Context, client *http.Client, address string) ([]byte, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: %s", address, response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxFeedSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFeedSize {
		return nil, fmt.Errorf("GET %s: response exceeds %d bytes", address, maxFeedSize)
	}
	return data, nil
}
