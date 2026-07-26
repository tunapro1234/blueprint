package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"blueprint/internal/tokens"
)

const tokensUsage = `usage:
  bp tokens [--day YYYY-MM-DD | --since Nd] [--agent NAME] [--hours | --prompts] [--top N] [--json]
  bp tokens collect [--json]
  bp tokens gc [--json]`

type tokenOptions struct {
	day     string
	since   int
	agent   string
	hours   bool
	prompts bool
	top     int
	json    bool
}

func (a *app) tokens(args []string) error {
	config := tokens.DefaultConfig(a.config.StateDir, a.config.Agentbooks)
	if len(args) > 0 && (args[0] == "collect" || args[0] == "gc") {
		action := args[0]
		jsonOutput := false
		for _, argument := range args[1:] {
			if argument != "--json" || jsonOutput {
				return fmt.Errorf("%s", tokensUsage)
			}
			jsonOutput = true
		}
		if action == "collect" {
			stats, err := tokens.Collect(config)
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeTokenJSON(a.out, stats)
			}
			fmt.Fprintf(a.out, "Collected %d new requests from %d files (%d duplicates, %d affected days).\n", stats.Added, stats.Files, stats.Deduped, stats.Days)
			return nil
		}
		stats, err := tokens.GC(config)
		if err != nil {
			return err
		}
		if jsonOutput {
			if err := writeTokenJSON(a.out, stats); err != nil {
				return err
			}
			return warnTokenFootprint(a, config)
		}
		fmt.Fprintf(a.out, "GC compressed %d files, deleted %d files, freed %s; store is %s.\n",
			stats.Compressed, stats.Deleted, tokens.Human(stats.BytesFreed), tokens.Human(stats.StoreBytes))
		return warnTokenFootprint(a, config)
	}

	options, err := parseTokenOptions(args)
	if err != nil {
		return err
	}
	now := time.Now().In(time.FixedZone("Europe/Istanbul", 3*60*60))
	period := tokens.DayRange(now)
	if options.day != "" {
		day, err := time.Parse("2006-01-02", options.day)
		if err != nil {
			return fmt.Errorf("invalid --day %q: expected YYYY-MM-DD", options.day)
		}
		period = tokens.DayRange(day)
	} else if options.since > 0 {
		period = tokens.LastDaysRange(now, options.since)
	}

	if options.hours {
		result, err := tokens.Hours(config.StoreDir, period, options.agent)
		if err != nil {
			return err
		}
		if options.json {
			return writeTokenJSON(a.out, result)
		}
		renderTokenHours(a, result)
		return nil
	}
	if options.prompts {
		result, err := tokens.Prompts(config.StoreDir, period, options.agent, options.top)
		if err != nil {
			return err
		}
		if options.json {
			return writeTokenJSON(a.out, result)
		}
		renderTokenPrompts(a, result)
		return nil
	}
	if options.agent != "" {
		summary, err := tokens.Summary(config.StoreDir, period, options.agent)
		if err != nil {
			return err
		}
		hours, err := tokens.Hours(config.StoreDir, period, options.agent)
		if err != nil {
			return err
		}
		prompts, err := tokens.Prompts(config.StoreDir, period, options.agent, options.top)
		if err != nil {
			return err
		}
		if options.json {
			return writeTokenJSON(a.out, struct {
				Day     string                `json:"day"`
				Agent   string                `json:"agent"`
				Models  []tokens.SummaryRow   `json:"models"`
				Hours   []tokens.HourlyRecord `json:"hours"`
				Prompts []tokens.PromptRecord `json:"prompts"`
			}{period.Label(), options.agent, summary.Rows, hours.Rows, prompts.Rows})
		}
		fmt.Fprintf(a.out, "=== %s — %s ===\n\n", period.Label(), options.agent)
		renderTokenModelSummary(a, summary)
		fmt.Fprintln(a.out)
		renderTokenHours(a, hours)
		fmt.Fprintln(a.out)
		renderTokenPrompts(a, prompts)
		return nil
	}
	result, err := tokens.Summary(config.StoreDir, period, "")
	if err != nil {
		return err
	}
	if options.json {
		if err := writeTokenJSON(a.out, result); err != nil {
			return err
		}
		return warnTokenFootprint(a, config)
	}
	renderTokenSummary(a, result)
	return warnTokenFootprint(a, config)
}

func parseTokenOptions(args []string) (tokenOptions, error) {
	options := tokenOptions{top: 20}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		value := func() (string, error) {
			if index+1 >= len(args) {
				return "", fmt.Errorf("%s", tokensUsage)
			}
			index++
			return args[index], nil
		}
		switch {
		case arg == "--day":
			next, err := value()
			if err != nil {
				return options, err
			}
			options.day = next
		case strings.HasPrefix(arg, "--day="):
			options.day = strings.TrimPrefix(arg, "--day=")
		case arg == "--since":
			next, err := value()
			if err != nil {
				return options, err
			}
			days, err := parseTokenDays(next)
			if err != nil {
				return options, err
			}
			options.since = days
		case strings.HasPrefix(arg, "--since="):
			days, err := parseTokenDays(strings.TrimPrefix(arg, "--since="))
			if err != nil {
				return options, err
			}
			options.since = days
		case arg == "--agent":
			next, err := value()
			if err != nil {
				return options, err
			}
			options.agent = next
		case strings.HasPrefix(arg, "--agent="):
			options.agent = strings.TrimPrefix(arg, "--agent=")
		case arg == "--top":
			next, err := value()
			if err != nil {
				return options, err
			}
			top, err := strconv.Atoi(next)
			if err != nil || top < 1 {
				return options, fmt.Errorf("--top must be a positive integer")
			}
			options.top = top
		case strings.HasPrefix(arg, "--top="):
			top, err := strconv.Atoi(strings.TrimPrefix(arg, "--top="))
			if err != nil || top < 1 {
				return options, fmt.Errorf("--top must be a positive integer")
			}
			options.top = top
		case arg == "--hours":
			options.hours = true
		case arg == "--prompts":
			options.prompts = true
		case arg == "--json":
			options.json = true
		default:
			return options, fmt.Errorf("%s", tokensUsage)
		}
	}
	if options.day != "" && options.since > 0 {
		return options, fmt.Errorf("--day and --since are mutually exclusive")
	}
	if options.hours && options.prompts {
		return options, fmt.Errorf("--hours and --prompts are mutually exclusive")
	}
	return options, nil
}

func parseTokenDays(value string) (int, error) {
	if !strings.HasSuffix(value, "d") {
		return 0, fmt.Errorf("--since must use Nd, for example 7d")
	}
	days, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
	if err != nil || days < 1 {
		return 0, fmt.Errorf("--since must use a positive number of days")
	}
	return days, nil
}

// renderTokenSummary prints one ranked table per source. Claude and Codex
// totals are not comparable (Codex reports huge cached-input counts), so they
// are never interleaved in a single ranking.
func renderTokenSummary(a *app, result tokens.SummaryResult) {
	var requests int64
	for _, row := range result.Rows {
		requests += row.Requests
	}
	fmt.Fprintf(a.out, "=== %s (Europe/Istanbul) — %d requests ===\n", result.Day, requests)
	if len(result.Rows) == 0 {
		fmt.Fprintln(a.out, "\n(no token records)")
		return
	}
	for _, src := range tokenSummarySources(result.Rows) {
		var rows []tokens.SummaryRow
		var out, reqs int64
		for _, row := range result.Rows {
			if row.Src != src {
				continue
			}
			rows = append(rows, row)
			out += row.Out
			reqs += row.Requests
		}
		fmt.Fprintf(a.out, "\n--- %s: %s out, %d requests, %d agents ---\n",
			strings.ToUpper(src), tokens.Human(out), reqs, len(rows))
		fmt.Fprintf(a.out, "%3s %-32s %6s %8s %8s %8s %9s\n", "#", "AGENT", "REQS", "OUT", "IN", "CACHE-W", "CACHE-R")
		for index, row := range rows {
			fmt.Fprintf(a.out, "%3d %-32s %6d %8s %8s %8s %9s\n",
				index+1, truncateTokenText(row.Agent, 32), row.Requests, tokens.Human(row.Out),
				tokens.Human(row.In), tokens.Human(row.CacheWrite), tokens.Human(row.CacheRead))
		}
	}
}

// tokenSummarySources lists the sources present, claude first, then codex,
// then anything else in first-seen order.
func tokenSummarySources(rows []tokens.SummaryRow) []string {
	seen := map[string]bool{}
	var rest []string
	for _, row := range rows {
		if seen[row.Src] {
			continue
		}
		seen[row.Src] = true
		if row.Src != "claude" && row.Src != "codex" {
			rest = append(rest, row.Src)
		}
	}
	var ordered []string
	for _, src := range []string{"claude", "codex"} {
		if seen[src] {
			ordered = append(ordered, src)
		}
	}
	return append(ordered, rest...)
}

func renderTokenModelSummary(a *app, result tokens.SummaryResult) {
	fmt.Fprintln(a.out, "=== MODEL BREAKDOWN ===")
	fmt.Fprintf(a.out, "%-7s %-24s %6s %8s %7s %8s %9s\n", "SRC", "MODEL", "REQS", "OUT", "IN", "CACHE-W", "CACHE-R")
	for _, row := range result.Rows {
		fmt.Fprintf(a.out, "%-7s %-24s %6d %8s %7s %8s %9s\n",
			row.Src, truncateTokenText(row.Model, 24), row.Requests, tokens.Human(row.Out), tokens.Human(row.In),
			tokens.Human(row.CacheWrite), tokens.Human(row.CacheRead))
	}
	if len(result.Rows) == 0 {
		fmt.Fprintln(a.out, "(no token records)")
	}
}

func renderTokenHours(a *app, result tokens.HourResult) {
	fmt.Fprintf(a.out, "=== HOURLY %s (output tokens) ===\n", result.Day)
	if len(result.Rows) == 0 {
		fmt.Fprintln(a.out, "(no hourly records)")
		return
	}
	type key struct {
		day, agent string
		hour       int
	}
	values := map[key]int64{}
	var peak int64
	for _, row := range result.Rows {
		k := key{row.Day, row.Agent, row.Hour}
		values[k] += row.Out
		if values[k] > peak {
			peak = values[k]
		}
	}
	keys := make([]key, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].day != keys[j].day {
			return keys[i].day < keys[j].day
		}
		if keys[i].hour != keys[j].hour {
			return keys[i].hour < keys[j].hour
		}
		return keys[i].agent < keys[j].agent
	})
	for _, k := range keys {
		width := int(28 * values[k] / peak)
		if width < 1 {
			width = 1
		}
		label := fmt.Sprintf("%s %02d:00", k.day, k.hour)
		fmt.Fprintf(a.out, "%s %-24s %-28s %8s\n", label, truncateTokenText(k.agent, 24), strings.Repeat("#", width), tokens.Human(values[k]))
	}
}

func renderTokenPrompts(a *app, result tokens.PromptResult) {
	fmt.Fprintf(a.out, "=== MOST EXPENSIVE PROMPTS %s (output tokens) ===\n", result.Day)
	if len(result.Rows) == 0 {
		fmt.Fprintln(a.out, "(no prompt records)")
		return
	}
	fmt.Fprintf(a.out, "%-16s %-24s %8s %6s  %s\n", "FIRST", "AGENT", "OUT", "REQS", "PROMPT")
	for _, row := range result.Rows {
		first := row.FirstTS
		if ts, err := time.Parse(time.RFC3339Nano, row.FirstTS); err == nil {
			first = ts.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(a.out, "%-16s %-24s %8s %6d  %s\n", first, truncateTokenText(row.Agent, 24),
			tokens.Human(row.Out), row.Requests, truncateTokenText(row.Preview, 80))
	}
}

func truncateTokenText(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

func writeTokenJSON(out interface{ Write([]byte) (int, error) }, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func warnTokenFootprint(a *app, config tokens.Config) error {
	footprint, err := tokens.Footprint(config)
	if err != nil {
		return fmt.Errorf("measure token log footprint: %w", err)
	}
	if footprint.Total > 8<<30 {
		fmt.Fprintf(a.err, "warning: log footprint %s (claude %s, codex %s, store %s) - approaching the 10G ceiling\n",
			tokens.HumanBytes(footprint.Total), tokens.HumanBytes(footprint.Claude), tokens.HumanBytes(footprint.Codex), tokens.HumanBytes(footprint.Store))
	}
	return nil
}
