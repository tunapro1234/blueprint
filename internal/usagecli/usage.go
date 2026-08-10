package usagecli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"blueprint/internal/codexauth"
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
	// CodexLastTS is the timestamp of the newest row that carried a codex value
	// at all, with no window limit. It is evidence about the silence, never a
	// value to render: when the outage is longer than fallbackWindow, Codex
	// holds nulls and this is the only way to say how old the last real reading
	// was instead of printing nothing (or a nil).
	CodexLastTS string
}

// Options carries the render-time context that the history file cannot supply:
// the clock, and whether codex access works at all. Both are injected so
// rendering stays a pure function of its inputs.
type Options struct {
	// Now is the reference clock for age arithmetic. When zero, the newest
	// sample's timestamp is used instead, which keeps output deterministic in
	// tests and honest in production (ages are then relative to the last
	// collector run rather than to a wall clock that is not available).
	Now time.Time
	// Auth is the local codex session verdict. The zero value (Unknown) makes
	// the renderer claim nothing about access.
	Auth codexauth.State
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
	// The last codex reading is searched over the whole history, deliberately
	// ignoring fallbackWindow: it is used only to date the silence.
	for i := len(samples) - 1; i >= 0; i-- {
		if hasCodex(samples[i]) {
			resolved.CodexLastTS = samples[i].TS
			break
		}
	}
	return resolved
}

func Lines(r Resolved, opts Options) []string {
	claudeNote := ""
	if r.Claude.TS != r.TS {
		claudeNote = fmt.Sprintf(" (as of %s)", r.Claude.TS)
	}
	// Claude keeps its per-figure shape: hasClaude() only guarantees the two
	// windows, so the Fable figure can be null on its own and each figure has to
	// be able to say "absent" by itself.
	claude := fmt.Sprintf("Claude: %s, %s, %s%s",
		figure("5h", r.Claude.Claude5, r.Claude.ClaudeResets.Five),
		figure("7d", r.Claude.Claude7, r.Claude.ClaudeResets.Week),
		figure("Fable 7d", r.Claude.ClaudeFable7, ""),
		claudeNote)
	return []string{
		"ts: " + r.TS,
		claude,
		"Codex:  " + codexText(r, opts),
	}
}

// figure renders one quota reading. A missing value is stated, never formatted:
// "%v" on a nil any yields "<nil>", which is how `%<nil> (reset )` reached the
// screen for three days. An empty reset is likewise dropped rather than printed
// as an empty parenthesis.
func figure(label string, value any, reset string) string {
	if label != "" {
		label += " "
	}
	if value == nil {
		return label + "veri yok"
	}
	text := fmt.Sprintf("%s%%%v", label, value)
	if reset != "" {
		text += fmt.Sprintf(" (reset %s)", reset)
	}
	return text
}

// codexText renders the codex figure, or - when there is none - says so
// instead of formatting a nil into a value. Three forms, exactly:
//
//	%52 (reset 2026-08-10T13:15:18Z)      a real reading
//	ERISIM YOK (codex auth: ...)          access is provably broken
//	veri yok (son deger 2d 2h once)       nothing arrived, access looks plausible
func codexText(r Resolved, opts Options) string {
	if !hasCodex(r.Codex) {
		// Broken access explains the silence, so it outranks the age of the
		// last reading; anything less than proof stays a data-gap report.
		if opts.Auth.Broken() {
			return "ERISIM YOK (codex auth: " + opts.Auth.Reason + ")"
		}
		if age, ok := codexSilence(r, opts); ok {
			return "veri yok (son deger " + age + " once)"
		}
		return "veri yok (kayitli deger yok)"
	}
	note := ""
	if r.Codex.TS != r.TS {
		note = fmt.Sprintf(" (as of %s)", r.Codex.TS)
	}
	// One figure, no window label: see the Sample comment. A secondary window
	// is only meaningful when the API reports one; when it does, the primary
	// is the short window and both labels are true again.
	quota := figure("", r.Codex.Codex5, r.Codex.CodexResets.Five)
	if r.Codex.Codex7 != nil {
		quota = figure("5h", r.Codex.Codex5, r.Codex.CodexResets.Five) + ", " + figure("7d", r.Codex.Codex7, r.Codex.CodexResets.Week)
	}
	return quota + note
}

// codexSilence measures how long ago the last codex reading arrived. It fails
// when the history holds no reading at all, or when the timestamps needed for
// the arithmetic are unusable - in both cases the caller must not invent an age.
func codexSilence(r Resolved, opts Options) (string, bool) {
	if r.CodexLastTS == "" {
		return "", false
	}
	last, err := time.Parse(time.RFC3339, r.CodexLastTS)
	if err != nil {
		return "", false
	}
	reference := opts.Now
	if reference.IsZero() {
		reference, err = time.Parse(time.RFC3339, r.TS)
		if err != nil {
			return "", false
		}
	}
	age := reference.Sub(last)
	if age < 0 {
		return "", false
	}
	return shortAge(age), true
}

// shortAge formats a duration coarsely, at most two units. Kept local rather
// than shared with codexauth's identical formatter so that package's API stays
// limited to the auth verdict.
func shortAge(d time.Duration) string {
	d = d.Round(time.Minute)
	days := int(d / (24 * time.Hour))
	hours := int(d % (24 * time.Hour) / time.Hour)
	minutes := int(d % time.Hour / time.Minute)
	switch {
	case days > 0 && hours > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case days > 0:
		return fmt.Sprintf("%dd", days)
	case hours > 0 && minutes > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}
