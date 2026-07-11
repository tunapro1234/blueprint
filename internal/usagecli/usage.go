package usagecli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

const HistoryPath = "/srv/server-main/usage/history.jsonl"

// fallbackWindow bounds how far back we search for a row with usable values,
// mirroring the dashboard generator's 48h contract.
const fallbackWindow = 48 * time.Hour

type Resets struct {
	Five string `json:"5h"`
	Week string `json:"7d"`
}

type Sample struct {
	TS           string `json:"ts"`
	Claude5      any    `json:"claude_5h"`
	Claude7      any    `json:"claude_7d"`
	ClaudeFable7 any    `json:"claude_fable_7d"`
	ClaudeResets Resets `json:"claude_resets"`
	Codex5       any    `json:"codex_5h"`
	Codex7       any    `json:"codex_7d"`
	CodexResets  Resets `json:"codex_resets"`
}

// Resolved carries the newest row plus, per provider, the most recent row
// (within fallbackWindow) whose core values are non-null. During collector
// outages the newest rows hold nulls; the per-provider rows keep values and
// resets mutually consistent by always coming from a single source row.
type Resolved struct {
	TS     string
	Claude Sample
	Codex  Sample
}

func hasClaude(s Sample) bool { return s.Claude5 != nil && s.Claude7 != nil }
func hasCodex(s Sample) bool  { return s.Codex5 != nil && s.Codex7 != nil }

func readSamples(path string) ([]Sample, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	samples := make([]Sample, 0, 256)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var sample Sample
		if err := json.Unmarshal(line, &sample); err != nil {
			return nil, err
		}
		samples = append(samples, sample)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return nil, fmt.Errorf("history is empty")
	}
	return samples, nil
}

// Latest resolves the newest row with per-provider fallback to the most
// recent non-null row within fallbackWindow of the newest timestamp.
func Latest(path string) (Resolved, error) {
	samples, err := readSamples(path)
	if err != nil {
		return Resolved{}, err
	}
	return resolve(samples), nil
}

func resolve(samples []Sample) Resolved {
	newest := samples[len(samples)-1]
	resolved := Resolved{TS: newest.TS, Claude: newest, Codex: newest}
	var horizon time.Time
	if ts, err := time.Parse(time.RFC3339, newest.TS); err == nil {
		horizon = ts.Add(-fallbackWindow)
	}
	inWindow := func(s Sample) bool {
		if horizon.IsZero() {
			return true
		}
		ts, err := time.Parse(time.RFC3339, s.TS)
		return err == nil && !ts.Before(horizon)
	}
	for i := len(samples) - 1; i >= 0 && (!hasClaude(resolved.Claude) || !hasCodex(resolved.Codex)); i-- {
		s := samples[i]
		if !inWindow(s) {
			break
		}
		if !hasClaude(resolved.Claude) && hasClaude(s) {
			resolved.Claude = s
		}
		if !hasCodex(resolved.Codex) && hasCodex(s) {
			resolved.Codex = s
		}
	}
	return resolved
}

func Lines(r Resolved) []string {
	claudeNote := ""
	if r.Claude.TS != r.TS {
		claudeNote = fmt.Sprintf(" (as of %s)", r.Claude.TS)
	}
	codexNote := ""
	if r.Codex.TS != r.TS {
		codexNote = fmt.Sprintf(" (as of %s)", r.Codex.TS)
	}
	return []string{
		"ts: " + r.TS,
		fmt.Sprintf("Claude: 5h %%%v (reset %s), 7d %%%v (reset %s), Fable 7d %%%v%s", r.Claude.Claude5, r.Claude.ClaudeResets.Five, r.Claude.Claude7, r.Claude.ClaudeResets.Week, r.Claude.ClaudeFable7, claudeNote),
		fmt.Sprintf("Codex:  5h %%%v (reset %s), 7d %%%v (reset %s)%s", r.Codex.Codex5, r.Codex.CodexResets.Five, r.Codex.Codex7, r.Codex.CodexResets.Week, codexNote),
	}
}
