package usagecli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

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
	// The codex keys are the collector's contract, not a statement about
	// window length: usage-pulse writes the API's primary window as `5h` and
	// the secondary as `7d`, but the primary window is plan-dependent. Since
	// the account moved from plus to prolite (2026-07-13) the primary window
	// is 7 days (window_minutes 10080) and the secondary is absent, so
	// `codex_5h` carries the WEEKLY figure and `codex_7d` is always null.
	// Do not rename these keys and do not label the value with a window we
	// cannot derive from the data; render it unlabelled instead.
	Codex5      any    `json:"codex_5h"`
	Codex7      any    `json:"codex_7d"`
	CodexResets Resets `json:"codex_resets"`
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

// hasCodex deliberately ignores Codex7: the secondary window only exists on
// plans that have one, so requiring it would make the fallback below dead code
// on the current plan and leave outage rows rendering as nulls.
func hasCodex(s Sample) bool { return s.Codex5 != nil }

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
	// One figure, no window label: see the Sample comment. A secondary window
	// is only meaningful when the API reports one; when it does, the primary
	// is the short window and both labels are true again.
	codexQuota := fmt.Sprintf("%%%v (reset %s)", r.Codex.Codex5, r.Codex.CodexResets.Five)
	if r.Codex.Codex7 != nil {
		codexQuota = fmt.Sprintf("5h %s, 7d %%%v (reset %s)", codexQuota, r.Codex.Codex7, r.Codex.CodexResets.Week)
	}
	return []string{
		"ts: " + r.TS,
		fmt.Sprintf("Claude: 5h %%%v (reset %s), 7d %%%v (reset %s), Fable 7d %%%v%s", r.Claude.Claude5, r.Claude.ClaudeResets.Five, r.Claude.Claude7, r.Claude.ClaudeResets.Week, r.Claude.ClaudeFable7, claudeNote),
		fmt.Sprintf("Codex:  %s%s", codexQuota, codexNote),
	}
}
