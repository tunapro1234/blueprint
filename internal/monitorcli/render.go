package monitorcli

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

const DefaultJobsPath = "/srv/blueprint/state/jobs.json"

var meterSpecs = []struct {
	Key   string
	Label string
	Value func(UsageSample) Number
	Reset string
}{
	{"claude_5h", "Claude 5h", func(s UsageSample) Number { return s.Claude5H }, "claude_5h"},
	{"claude_7d", "Claude 7d", func(s UsageSample) Number { return s.Claude7D }, "claude_7d"},
	{"claude_fable_7d", "Claude Fable 7d", func(s UsageSample) Number { return s.ClaudeFable7D }, "claude_7d"},
	// The codex meter carries no window label: the collector maps the API's
	// primary window to `codex_5h` whatever its length, and on the current
	// plan (prolite since 2026-07-13) that primary window is 7 days while the
	// secondary is absent. Rows with an invalid value are skipped, so the
	// secondary meter renders only on plans that actually report one.
	{"codex_5h", "Codex", func(s UsageSample) Number { return s.Codex5H }, "codex_5h"},
	{"codex_7d", "Codex 7d", func(s UsageSample) Number { return s.Codex7D }, "codex_7d"},
}

type JobState struct {
	LastRun  string `json:"last_run"`
	Status   string `json:"status"`
	NextRun  string `json:"next_run"`
	Duration string `json:"duration"`
	Error    string `json:"error"`
}

type RenderOptions struct {
	Now      time.Time
	Jobs     map[string]JobState
	JobsNote string
	Radar    *Radar
}

func LoadJobs(path string) (map[string]JobState, error) {
	if path == "" {
		path = DefaultJobsPath
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state struct {
		Jobs map[string]JobState `json:"jobs"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return state.Jobs, nil
}

func Render(out io.Writer, doc *Document, view string, options RenderOptions) error {
	if doc == nil {
		return fmt.Errorf("monitor data is nil")
	}
	if options.Now.IsZero() {
		options.Now = time.Now()
	}
	switch view {
	case "", "overview":
		renderOverview(out, doc, options.Now)
	case "usage":
		renderUsage(out, doc)
	case "cost":
		renderCost(out, doc)
	case "agents":
		renderAgents(out, doc, options.Now)
	case "projects":
		renderProjects(out, doc)
	case "services":
		renderServices(out, doc, options)
	case "radar":
		renderRadar(out, options.Radar)
	default:
		return fmt.Errorf("usage: bp monitor [usage|cost|agents|projects|services|radar]")
	}
	return nil
}

func renderOverview(out io.Writer, doc *Document, now time.Time) {
	fmt.Fprintln(out, "MONITOR OVERVIEW")
	stamp(out, "Generated", doc.GeneratedAt)
	if doc.Usage == nil {
		note(out, doc, "usage", "usage data unavailable")
		return
	}
	if doc.Usage.Current.TS != "" {
		stamp(out, "Measured", doc.Usage.Current.TS)
	}
	table(out, []string{"METER", "USAGE", "STATE", "RESET IN", "RESET AT"}, func(tw *tabwriter.Writer) {
		for _, spec := range meterSpecs {
			value := spec.Value(doc.Usage.Current)
			if !value.Valid {
				continue
			}
			reset := doc.Usage.Current.Resets[spec.Reset]
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", spec.Label, percent(value), usageState(value.Value), countdown(reset, now), displayTime(reset))
		}
	})
	if !hasAnyMeter(doc.Usage.Current) {
		fmt.Fprintln(out, "Note: no usage meters are present.")
	}
}

func renderUsage(out io.Writer, doc *Document) {
	fmt.Fprintln(out, "USAGE HISTORY")
	if doc.Usage == nil || len(doc.Usage.History) == 0 {
		note(out, doc, "usage", "usage history unavailable")
		return
	}
	rows := sortedUsage(doc.Usage.History)
	end, ok := parseTime(rows[len(rows)-1].TS)
	if !ok {
		fmt.Fprintln(out, "Note: usage history has no valid timestamps.")
		return
	}
	table(out, []string{"RANGE", "METER", "MIN", "MAX", "NOW", "SAMPLES"}, func(tw *tabwriter.Writer) {
		for _, window := range []struct {
			label string
			dur   time.Duration
		}{{"24h", 24 * time.Hour}, {"7d", 7 * 24 * time.Hour}} {
			from := end.Add(-window.dur)
			for _, spec := range meterSpecs {
				min, max, last, count := meterSummary(rows, spec.Value, from, end)
				if count == 0 {
					continue
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\n", window.label, spec.Label, percent(min), percent(max), percent(last), count)
			}
		}
	})
	fmt.Fprintf(out, "Data coverage: %s to %s (%d samples)\n", displayTime(rows[0].TS), displayTime(rows[len(rows)-1].TS), len(rows))
}

func renderCost(out io.Writer, doc *Document) {
	fmt.Fprintln(out, "API-EQUIVALENT COST")
	if doc.Cost == nil {
		note(out, doc, "cost", "cost data unavailable")
		return
	}
	c := doc.Cost
	table(out, []string{"PERIOD", "API VALUE", "SUBSCRIPTION", "VALUE MULTIPLE"}, func(tw *tabwriter.Writer) {
		sub := c.SubscriptionUSD
		ratio := c.ValueRatio
		if c.Subscriptions != nil && c.Subscriptions.Total.Valid {
			sub = c.Subscriptions.Total
		}
		if c.ValueRatioTotal.Valid {
			ratio = c.ValueRatioTotal
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", fallback(c.Month, "current month"), money(c.TotalUSD), money(sub), multiple(ratio))
		if c.AllTimeUSD.Valid {
			fmt.Fprintf(tw, "All time\t%s\t-\t-\n", money(c.AllTimeUSD))
		}
	})
	if c.Subscriptions != nil {
		fmt.Fprintln(out, "\nSUBSCRIPTIONS")
		table(out, []string{"PLAN", "MONTHLY"}, func(tw *tabwriter.Writer) {
			if c.Subscriptions.Claude.Valid {
				fmt.Fprintf(tw, "Claude\t%s\n", money(c.Subscriptions.Claude))
			}
			if c.Subscriptions.Codex.Valid {
				fmt.Fprintf(tw, "Codex\t%s\n", money(c.Subscriptions.Codex))
			}
			if c.Subscriptions.Total.Valid {
				fmt.Fprintf(tw, "Total\t%s\n", money(c.Subscriptions.Total))
			}
		})
	}
	if len(c.Models) == 0 {
		fmt.Fprintln(out, "Note: per-model cost data unavailable.")
	} else {
		fmt.Fprintln(out, "\nMODEL BREAKDOWN")
		table(out, []string{"MODEL", "INPUT", "OUTPUT", "CACHE READ", "CACHE WRITE", "API VALUE"}, func(tw *tabwriter.Writer) {
			for _, model := range c.Models {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", model.Model, integer(model.Input), integer(model.Output), integer(model.CacheRead), integer(model.CacheWrite), money(model.USD))
			}
		})
	}
	if c.Cache == nil {
		fmt.Fprintln(out, "Note: cache economics data unavailable.")
	} else {
		cache := c.Cache
		actual := sumNumbers(cache.ReadUSD, cache.WriteUSD, cache.FreshInputUSD, cache.OutputUSD)
		saved := Number{}
		if cache.WithoutCacheUSD.Valid && actual.Valid {
			saved = Number{Value: math.Max(0, cache.WithoutCacheUSD.Value-actual.Value), Valid: true}
		}
		fmt.Fprintln(out, "\nCACHE ECONOMICS")
		table(out, []string{"WITHOUT CACHE", "EST. SAVINGS", "READ", "WRITE", "FRESH INPUT", "OUTPUT"}, func(tw *tabwriter.Writer) {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", money(cache.WithoutCacheUSD), money(saved), money(cache.ReadUSD), money(cache.WriteUSD), money(cache.FreshInputUSD), money(cache.OutputUSD))
		})
	}
	if len(c.Daily) > 0 {
		fmt.Fprintln(out, "\nDAILY API VALUE")
		table(out, []string{"DATE", "VALUE", "BY MODEL"}, func(tw *tabwriter.Writer) {
			for _, day := range c.Daily {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", day.Date, money(day.USD), numberMap(day.ByModel, money))
			}
		})
	}
	if len(c.Months) > 0 {
		fmt.Fprintln(out, "\nMONTH HISTORY")
		table(out, []string{"MONTH", "API VALUE", "OUTPUT"}, func(tw *tabwriter.Writer) {
			for _, month := range c.Months {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", month.Month, money(month.USD), integer(month.Output))
			}
		})
	}
	if len(c.ProjectGroups) > 0 {
		fmt.Fprintln(out, "\nPROJECT GROUPS")
		table(out, []string{"GROUP", "API VALUE", "OUTPUT", "PROJECTS"}, func(tw *tabwriter.Writer) {
			for _, group := range c.ProjectGroups {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", group.Group, money(group.USD), integer(group.Output), len(group.Projects))
			}
		})
	}
}

type flatAgent struct {
	Node  AgentNode
	Depth int
}

func renderAgents(out io.Writer, doc *Document, now time.Time) {
	fmt.Fprintln(out, "AGENTS")
	if doc.Agents == nil || len(doc.Agents.Tree) == 0 {
		note(out, doc, "agents", "agent tree unavailable")
		return
	}
	stamp(out, "Snapshot", doc.Agents.TS)
	activity := activityByAgent(doc.AgentTimeline, now)
	tokens := make(map[string]Number)
	if doc.Cost != nil {
		for _, agent := range doc.Cost.ByAgent {
			tokens[agent.Agent] = agent.Output
		}
	}
	var agents []flatAgent
	flattenAgents(doc.Agents.Tree, 0, &agents)
	table(out, []string{"TREE", "NICKNAME", "STATUS", "STATE", "LAST ACTIVITY", "MONTH OUTPUT"}, func(tw *tabwriter.Writer) {
		for _, agent := range agents {
			prefix := strings.Repeat("  ", agent.Depth)
			if agent.Depth > 0 {
				prefix += "|- "
			}
			fmt.Fprintf(tw, "%s%s\t%s\t%s\t%s\t%s\t%s\n", prefix, agent.Node.Name, dash(agent.Node.Nickname), dash(agent.Node.Status), dash(agent.Node.State), dash(activity[agent.Node.Name]), integer(tokens[agent.Node.Name]))
		}
	})
	roles := 0
	for _, agent := range agents {
		if strings.TrimSpace(agent.Node.Role) != "" {
			if roles == 0 {
				fmt.Fprintln(out, "\nROLES")
			}
			fmt.Fprintf(out, "%s: %s\n", agent.Node.Name, oneLine(agent.Node.Role))
			roles++
		}
	}
	if doc.AgentTimeline == nil {
		fmt.Fprintln(out, "Note: agent timeline unavailable; last activity omitted.")
	}
	if doc.Cost == nil || len(doc.Cost.ByAgent) == 0 {
		fmt.Fprintln(out, "Note: per-agent token totals unavailable.")
	}
}

func renderProjects(out io.Writer, doc *Document) {
	fmt.Fprintln(out, "PROJECT OUTPUT TOKENS")
	if doc.Tokens == nil || len(doc.Tokens.Series) == 0 {
		note(out, doc, "tokens", "project token series unavailable")
		return
	}
	series := sortedTokens(doc.Tokens.Series)
	end, ok := parseTime(series[len(series)-1].TS)
	if !ok {
		fmt.Fprintln(out, "Note: project token series has no valid timestamps.")
		return
	}
	ranges := []struct {
		label string
		dur   time.Duration
	}{{"24H", 24 * time.Hour}, {"3D", 72 * time.Hour}, {"7D", 168 * time.Hour}}
	values := make(map[string][]float64)
	for index, window := range ranges {
		from := end.Add(-window.dur)
		for _, sample := range series {
			ts, valid := parseTime(sample.TS)
			if !valid || ts.Before(from) || ts.After(end) {
				continue
			}
			for project, counts := range sample.Projects {
				if !counts.Output.Valid {
					continue
				}
				if values[project] == nil {
					values[project] = make([]float64, len(ranges))
				}
				values[project][index] += counts.Output.Value
			}
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if values[names[i]][0] == values[names[j]][0] {
			return names[i] < names[j]
		}
		return values[names[i]][0] > values[names[j]][0]
	})
	table(out, []string{"PROJECT", "24H", "3D", "7D"}, func(tw *tabwriter.Writer) {
		for _, name := range names {
			v := values[name]
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", projectLabel(name), formatInteger(v[0]), formatInteger(v[1]), formatInteger(v[2]))
		}
	})
	fmt.Fprintf(out, "Data coverage: %s to %s (%d samples); each column is clipped to available data.\n", displayTime(series[0].TS), displayTime(series[len(series)-1].TS), len(series))
}

func renderServices(out io.Writer, doc *Document, options RenderOptions) {
	fmt.Fprintln(out, "SERVICES")
	units := make(map[string]ServiceUnit)
	if doc.Services != nil {
		stamp(out, "Snapshot", doc.Services.TS)
		for _, unit := range doc.Services.Units {
			units[unit.Name] = unit
		}
	}
	names := make(map[string]bool)
	for name := range units {
		names[name] = true
	}
	for name := range options.Jobs {
		names[name] = true
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	if len(ordered) == 0 {
		note(out, doc, "services", "service data unavailable")
		if options.JobsNote != "" {
			fmt.Fprintln(out, "Note:", options.JobsNote)
		}
		return
	}
	table(out, []string{"NAME", "HEALTH", "STATUS", "LAST", "NEXT", "DURATION", "ERROR", "DESCRIPTION"}, func(tw *tabwriter.Writer) {
		for _, name := range ordered {
			unit := units[name]
			job, hasJob := options.Jobs[name]
			health, status, last, next, duration, problem := dashboardService(unit, doc.Services)
			if hasJob {
				status = job.Status
				health = jobHealth(job.Status)
				last, next, duration, problem = job.LastRun, job.NextRun, job.Duration, job.Error
			}
			desc := ""
			if unit.Service != nil {
				desc = unit.Service.Desc
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", name, health, dash(status), displayTime(last), displayTime(next), dash(duration), oneLine(problem), oneLine(desc))
		}
	})
	if options.JobsNote != "" {
		fmt.Fprintln(out, "Note:", options.JobsNote)
	}
}

func renderRadar(out io.Writer, radar *Radar) {
	fmt.Fprintln(out, "MODEL RADAR")
	if radar == nil {
		fmt.Fprintln(out, "Note: radar data unavailable.")
		return
	}
	stamp(out, "Generated", radar.GeneratedAt)
	if len(radar.Events) == 0 {
		fmt.Fprintln(out, "No model findings.")
	} else {
		table(out, []string{"TIME", "PROVIDER", "MODELS", "REACTIONS", "NOTIFIED"}, func(tw *tabwriter.Writer) {
			for _, event := range radar.Events {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", displayTime(event.TS), dash(event.Provider), strings.Join(event.Models, ", "), dash(event.ReactionsStatus), yesNo(event.Notified))
			}
		})
		for _, event := range radar.Events {
			if event.ParamRumors != "" {
				fmt.Fprintf(out, "Rumors [%s]: %s\n", fallback(event.ID, strings.Join(event.Models, ", ")), oneLine(event.ParamRumors))
			}
			for _, reaction := range event.Reactions {
				fmt.Fprintf(out, "Reaction [%s / %s]: %s (%s)\n", reaction.Channel, reaction.Title, oneLine(reaction.Summary), reaction.URL)
			}
		}
	}
	if len(radar.Resets) > 0 {
		fmt.Fprintln(out, "\nRECENT RESETS")
		table(out, []string{"TIME", "WINDOW", "PEAK", "NOTIFIED"}, func(tw *tabwriter.Writer) {
			for _, reset := range radar.Resets {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", displayTime(reset.TS), reset.Window, percent(reset.Peak), yesNo(reset.Notified))
			}
		})
	}
	if len(radar.LastCheck) > 0 {
		fmt.Fprintln(out, "\nLAST CHECKS")
		keys := sortedKeys(radar.LastCheck)
		table(out, []string{"WATCHER", "TIME"}, func(tw *tabwriter.Writer) {
			for _, key := range keys {
				fmt.Fprintf(tw, "%s\t%s\n", key, displayTime(radar.LastCheck[key]))
			}
		})
	}
}

func table(out io.Writer, headers []string, rows func(*tabwriter.Writer)) {
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	rows(tw)
	_ = tw.Flush()
}

func note(out io.Writer, doc *Document, section, fallbackText string) {
	message := fallbackText
	if doc != nil && doc.Notes[section] != "" {
		message += ": " + doc.Notes[section]
	}
	fmt.Fprintln(out, "Note:", message+".")
}

func stamp(out io.Writer, label, value string) {
	if value != "" {
		fmt.Fprintf(out, "%s: %s\n", label, displayTime(value))
	}
}

func usageState(value float64) string {
	switch {
	case value >= 90:
		return "critical"
	case value >= 70:
		return "warning"
	default:
		return "normal"
	}
}

func countdown(value string, now time.Time) string {
	target, ok := parseTime(value)
	if !ok {
		return "-"
	}
	duration := target.Sub(now)
	if duration <= 0 {
		return "due"
	}
	return formatDuration(duration)
}

func formatDuration(duration time.Duration) string {
	duration = duration.Round(time.Minute)
	days := int(duration / (24 * time.Hour))
	duration -= time.Duration(days) * 24 * time.Hour
	hours := int(duration / time.Hour)
	duration -= time.Duration(hours) * time.Hour
	minutes := int(duration / time.Minute)
	parts := make([]string, 0, 3)
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	return strings.Join(parts, " ")
}

func parseTime(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil
}

func displayTime(value string) string {
	if value == "" {
		return "-"
	}
	if parsed, ok := parseTime(value); ok {
		return parsed.UTC().Format("2006-01-02 15:04 MST")
	}
	return oneLine(value)
}

func hasAnyMeter(sample UsageSample) bool {
	for _, spec := range meterSpecs {
		if spec.Value(sample).Valid {
			return true
		}
	}
	return false
}

func sortedUsage(rows []UsageSample) []UsageSample {
	copyRows := append([]UsageSample(nil), rows...)
	sort.SliceStable(copyRows, func(i, j int) bool { return timeBefore(copyRows[i].TS, copyRows[j].TS) })
	return copyRows
}

func sortedTokens(rows []TokenSample) []TokenSample {
	copyRows := append([]TokenSample(nil), rows...)
	sort.SliceStable(copyRows, func(i, j int) bool { return timeBefore(copyRows[i].TS, copyRows[j].TS) })
	return copyRows
}

func timeBefore(left, right string) bool {
	leftTime, leftOK := parseTime(left)
	rightTime, rightOK := parseTime(right)
	if leftOK && rightOK {
		return leftTime.Before(rightTime)
	}
	if leftOK != rightOK {
		return leftOK
	}
	return left < right
}

func meterSummary(rows []UsageSample, value func(UsageSample) Number, from, to time.Time) (Number, Number, Number, int) {
	var min, max, last Number
	count := 0
	for _, row := range rows {
		ts, ok := parseTime(row.TS)
		v := value(row)
		if !ok || ts.Before(from) || ts.After(to) || !v.Valid {
			continue
		}
		if count == 0 || v.Value < min.Value {
			min = v
		}
		if count == 0 || v.Value > max.Value {
			max = v
		}
		last = v
		count++
	}
	return min, max, last, count
}

func flattenAgents(nodes []AgentNode, depth int, result *[]flatAgent) {
	for _, node := range nodes {
		*result = append(*result, flatAgent{Node: node, Depth: depth})
		flattenAgents(node.Children, depth+1, result)
	}
}

func activityByAgent(timeline *AgentTimeline, now time.Time) map[string]string {
	result := make(map[string]string)
	if timeline == nil {
		return result
	}
	to, _ := parseTime(timeline.To)
	for _, agent := range timeline.Agents {
		if len(agent.Segs) == 0 {
			continue
		}
		latest := agent.Segs[0]
		for _, segment := range agent.Segs[1:] {
			if timeBefore(latest.End, segment.End) {
				latest = segment
			}
		}
		end, validEnd := parseTime(latest.End)
		if validEnd && !to.IsZero() && !end.Before(to.Add(-time.Minute)) && !end.Before(now.Add(-20*time.Minute)) {
			result[agent.Name] = latest.State + " since " + displayTime(latest.Start)
		} else {
			result[agent.Name] = latest.State + " until " + displayTime(latest.End)
		}
	}
	return result
}

func dashboardService(unit ServiceUnit, services *Services) (health, status, last, next, duration, problem string) {
	if unit.Service == nil {
		return "unknown", "-", "", "", "", ""
	}
	s := unit.Service
	status = strings.Trim(strings.TrimSpace(strings.Join([]string{s.Active, s.Sub}, "/")), "/")
	last = s.LastRun
	if unit.Timer != nil {
		next = unit.Timer.NextRun
	}
	switch {
	case s.Active == "failed" || (s.Result != "" && s.Result != "success"):
		health = "error"
		problem = s.Result
	case s.Active == "active":
		health = "running"
	case unit.Timer != nil && unit.Timer.Active == "active":
		health = "scheduled"
		if services != nil {
			ref, refOK := parseTime(services.TS)
			nextTime, nextOK := parseTime(next)
			if refOK && nextOK && nextTime.Before(ref.Add(-15*time.Minute)) {
				health = "late"
			}
		}
	default:
		health = "inactive"
	}
	return
}

func jobHealth(status string) string {
	switch strings.ToLower(status) {
	case "ok":
		return "healthy"
	case "running":
		return "running"
	case "failed", "error":
		return "error"
	case "stopped":
		return "stopped"
	default:
		return "unknown"
	}
}

func percent(value Number) string {
	if !value.Valid {
		return "-"
	}
	return formatFloat(value.Value) + "%"
}

func money(value Number) string {
	if !value.Valid {
		return "-"
	}
	return "$" + strconv.FormatFloat(value.Value, 'f', 2, 64)
}

func multiple(value Number) string {
	if !value.Valid {
		return "-"
	}
	return strconv.FormatFloat(value.Value, 'f', 1, 64) + "x"
}

func integer(value Number) string {
	if !value.Valid {
		return "-"
	}
	return formatInteger(value.Value)
}

func formatInteger(value float64) string {
	negative := value < 0
	if negative {
		value = -value
	}
	digits := strconv.FormatInt(int64(math.Round(value)), 10)
	for index := len(digits) - 3; index > 0; index -= 3 {
		digits = digits[:index] + "," + digits[index:]
	}
	if negative {
		digits = "-" + digits
	}
	return digits
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func sumNumbers(values ...Number) Number {
	result := Number{Valid: true}
	for _, value := range values {
		if !value.Valid {
			return Number{}
		}
		result.Value += value.Value
	}
	return result
}

func numberMap(values map[string]Number, format func(Number) string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+" "+format(values[key]))
	}
	return strings.Join(parts, ", ")
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func projectLabel(name string) string {
	label := strings.TrimPrefix(name, "-srv-")
	label = strings.TrimPrefix(label, "-")
	label = strings.ReplaceAll(label, "--worktrees-", "/")
	if label == "srv" || name == "-srv" {
		return "server-main"
	}
	return label
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func dash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return oneLine(value)
}

func fallback(value, fallbackValue string) string {
	if value == "" {
		return fallbackValue
	}
	return value
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
