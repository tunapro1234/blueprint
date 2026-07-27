package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/cache"
	"blueprint/internal/pending"
	bptmux "blueprint/internal/tmux"
	"blueprint/internal/usagecli"
)

// bar renders one tmux status-right line for a single agent. It is called by
// every pane's status bar on every status-interval tick, so it must stay cheap:
// a short-lived cache file absorbs the repeats and the measurement itself only
// reads the tail of one session file.
//
// The blueprint palette: accent #7ea6ff (111), dim #7c8aa5 (103), ink (189).
// Colour carries the warning, not shouting — only the segment that is off
// changes hue, so a healthy bar stays grey-blue.
// Each segment is a filled block: readable at a glance from across the room,
// which fading foreground colour is not. Quiet segments keep the bar's own dark
// blue; only a segment that wants attention lights up.
// Every metric is a filled chip in its own colour, and the gaps between chips
// are grey so neighbouring chips never bleed into one another. The name plate
// is grey too: it carries the agent's accent as text, so it stays legible
// against the accent-coloured bar without competing with the metrics.
const (
	barGap = "colour236" // grey between chips, and behind the name plate

	barQuiet = "colour240" // resting: a chip that says nothing is wrong
	barCalm  = "colour31"  // steady blue: warm cache, good news
	barWarn  = "colour136" // amber: worth knowing
	barAlert = "colour131" // red: act before the next long task

	barCacheTTL = 45 * time.Second

	// barDefaultAccent is Claude Code's own default plate colour, so an agent
	// that never ran /color is left unrecorded rather than pinned to the default.
	barDefaultAccent = "37"
)

func (a *app) bar(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: bp bar <agent>")
	}
	agent := args[0]
	if line, ok := a.barCached(agent); ok {
		fmt.Fprintln(a.out, line)
		return nil
	}
	line := a.barLine(agent)
	a.barStore(agent, line)
	fmt.Fprintln(a.out, line)
	return nil
}

// barName renders the agent's name plate, and sets the whole bar to that
// agent's Claude Code accent colour so the tmux bar and the pane above it read
// as one surface. The metrics panel keeps its own grey slab on top of it.
func (a *app) barName(agent string) string {
	accent := a.barAccent(agent)
	a.barApplyStyle(agent, accent)
	return "#[bg=" + barGap + ",fg=colour" + accent + ",bold] " + agent + " #[default]"
}

// barApplyStyle keeps the session's status-style in step with the accent. It
// writes only on a real change: tmux redraws the bar when an option is set, and
// a redraw re-runs this command.
func (a *app) barApplyStyle(agent, accent string) {
	want := "bg=colour" + accent + ",fg=" + readableOn(accent)
	if current, err := a.tmux.Option(a.ctx, agent, "status-style"); err == nil && current == want {
		return
	}
	_ = a.tmux.SetOption(a.ctx, agent, "status-style", want)
}

// agentChip matches the name plate Claude Code paints at the bottom of its
// composer, e.g. "\x1b[48;5;178m server-main ". The background index is the
// accent colour the agent was given with /color, which is not persisted
// anywhere on disk — the rendered pane is the only place it can be read.
var agentChip = regexp.MustCompile(`48;5;(\d+)m ([A-Za-z0-9._-]+) `)

// barAccent returns the agent's own Claude Code accent colour. The rendered
// pane is the live source of truth — /color changes it at any moment and writes
// it nowhere on disk — so an open agent is always read from its pane and the
// answer is mirrored into the agentbook. The book is what a CLOSED agent falls
// back to, since there is no pane left to read.
func (a *app) barAccent(agent string) string {
	stored := ""
	if fleet, err := book.LoadFleet(book.Paths(a.config.Agentbooks)); err == nil {
		if entry, ok := fleet.Agents[agent]; ok {
			stored = entry.Color
		}
	}
	live, ok := a.paneAccent(agent)
	if !ok {
		if stored != "" {
			return stored
		}
		return barDefaultAccent
	}
	// Only record a real change, and never create an entry just to say an agent
	// still has the colour it was born with.
	if live != stored && !(stored == "" && live == barDefaultAccent) {
		_ = book.SetColor(a.config.Agentbooks, agent, live)
	}
	return live
}

func (a *app) paneAccent(agent string) (string, bool) {
	pane, err := a.tmux.CaptureAnsi(a.ctx, agent)
	if err != nil {
		return "", false
	}
	for _, match := range agentChip.FindAllStringSubmatch(pane, -1) {
		if match[2] == agent {
			return match[1], true
		}
	}
	return "", false
}

func (a *app) barLine(agent string) string {
	var segments []string

	folder := a.barFolder(agent)
	state := cache.Read(bptmux.ClaudeProjectsRoot(), folder, agent)
	if state.Known {
		ctx := humanTokens(state.CtxTokens)
		colour := barQuiet
		switch {
		case state.CtxTokens > 300_000:
			colour = barAlert
			ctx += " !"
		case state.CtxTokens > 200_000:
			colour = barWarn
		}
		segments = append(segments, style(colour, ctx))

		temperature, colour := "cold", barQuiet
		if state.Age < time.Hour {
			temperature, colour = "warm", barCalm
		}
		segments = append(segments, style(colour, temperature+" "+formatAge(state.Age)))

		if state.LastHumanAge >= 0 {
			colour := barQuiet
			if state.LastHumanAge > 24*time.Hour {
				colour = barWarn
			}
			segments = append(segments, style(colour, "talk "+formatAge(state.LastHumanAge)))
		}
	}

	if items, _, err := pending.Load(filepath.Join(a.config.StateDir, "pending"), agent); err == nil && len(items) > 0 {
		segments = append(segments, style(barCalm, "queue "+strconv.Itoa(len(items))))
	}

	if quota := a.barQuota(); quota != "" {
		segments = append(segments, quota)
	}

	segments = append(segments, style(barQuiet, time.Now().Format("15:04")))
	gap := "#[bg=" + barGap + "] "
	return gap + strings.Join(segments, gap) + gap + "#[default]"
}

// barQuota renders the fleet-wide budget: the shared Claude 7-day window is
// what actually forces model and effort decisions, so it belongs on every
// agent's bar, not just the orchestrator's.
func (a *app) barQuota() string {
	if a.config.UsageHistory == "" {
		return ""
	}
	sample, err := usagecli.Latest(a.config.UsageHistory)
	if err != nil {
		return ""
	}
	week, ok := percent(sample.Claude.Claude7)
	if !ok {
		return ""
	}
	colour := barQuiet
	switch {
	case week >= 80:
		colour = barAlert
	case week >= 60:
		colour = barWarn
	}
	text := "7d " + strconv.Itoa(int(week)) + "%"
	if hours, ok := percent(sample.Claude.Claude5); ok && hours >= 80 {
		text += " (5h " + strconv.Itoa(int(hours)) + "%)"
	}
	return style(colour, text)
}

func percent(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case int:
		return float64(number), true
	}
	return 0, false
}

// barFolder prefers the agentbook folder over the pane's cwd: a renamed folder
// leaves the running process on the old inode, and the session log lives under
// the path the agent was opened with.
func (a *app) barFolder(agent string) string {
	fleet, err := book.LoadFleet(book.Paths(a.config.Agentbooks))
	if err == nil {
		if entry, ok := fleet.Agents[agent]; ok && entry.Folder != "" {
			return firstPath(entry.Folder)
		}
	}
	locations, err := a.tmux.Locations(a.ctx)
	if err != nil {
		return ""
	}
	for _, location := range locations {
		if location.Session == agent {
			return location.CurrentDir
		}
	}
	return ""
}

// firstPath strips the annotation an agentbook folder may carry, e.g.
// "/srv (home: /srv/server-main)".
func firstPath(folder string) string {
	folder = strings.TrimSpace(folder)
	if !strings.HasPrefix(folder, "/") {
		return ""
	}
	if index := strings.IndexAny(folder, " \t\r\n"); index >= 0 {
		folder = folder[:index]
	}
	return folder
}

// style fills one metric chip with its own colour, picking text that stays
// readable on it.
func style(colour, text string) string {
	return "#[bg=" + colour + ",fg=" + readableOn(strings.TrimPrefix(colour, "colour")) + "] " + text + " "
}

// readableOn picks black or white text for a 256-colour background, so an agent
// whose accent is pale (white, cream, light yellow) does not end up with white
// text on it. Indices decode by their three ranges: system, 6x6x6 cube, greys.
func readableOn(index string) string {
	value, err := strconv.Atoi(index)
	if err != nil {
		return "colour231"
	}
	var r, g, b int
	switch {
	case value < 8:
		r, g, b = 0, 0, 0 // dark system colours
		if value == 7 {
			r, g, b = 192, 192, 192
		}
	case value < 16:
		r, g, b = 255, 255, 255 // bright system colours
	case value < 232:
		steps := []int{0, 95, 135, 175, 215, 255}
		value -= 16
		r, g, b = steps[value/36], steps[(value/6)%6], steps[value%6]
	default:
		level := 8 + (value-232)*10
		r, g, b = level, level, level
	}
	// Rec. 601 luma: bright backgrounds take black text, dark ones white.
	if (299*r+587*g+114*b)/1000 > 140 {
		return "colour16"
	}
	return "colour231"
}

func (a *app) barCachePath(agent string) string {
	return filepath.Join(a.config.StateDir, "bar", agent+".txt")
}

func (a *app) barCached(agent string) (string, bool) {
	path := a.barCachePath(agent)
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) > barCacheTTL {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimRight(string(data), "\n"), true
}

func (a *app) barStore(agent, line string) {
	path := a.barCachePath(agent)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(line+"\n"), 0o644)
}
