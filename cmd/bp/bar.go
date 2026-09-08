package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/cache"
	bpconfig "blueprint/internal/config"
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

	barCacheTTL = 2 * time.Second

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
	return "#[bg=" + barGap + ",fg=colour" + accent + ",bold] " + a.liveName(agent) + " #[default]"
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
			if entry.ColorOverride != "" {
				return entry.ColorOverride
			}
			stored = entry.Color
		}
	}
	if a.config.Bar.DefaultColor != "" {
		if index, err := bpconfig.ColorIndex(a.config.Bar.DefaultColor); err == nil {
			return index
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
	matches := agentChip.FindAllStringSubmatch(pane, -1)
	if len(matches) == 0 {
		return "", false
	}
	label := a.liveName(agent)
	for _, match := range matches {
		if match[2] == agent || match[2] == label {
			return match[1], true
		}
	}
	return "", false
}

func (a *app) barLine(agent string) string {
	var segments []string

	var folder string
	folderRead := false
	readFolder := func() string {
		if !folderRead {
			folder = a.barFolder(agent)
			folderRead = true
		}
		return folder
	}
	var process bptmux.PaneProcess
	processRead := false
	readProcess := func() bptmux.PaneProcess {
		if !processRead {
			if a.tmux != nil {
				if value, err := a.tmux.PaneProcess(a.ctx, agent); err == nil {
					process = value
				}
			}
			processRead = true
		}
		return process
	}
	var state cache.State
	stateRead := false
	readState := func() cache.State {
		if !stateRead {
			state = a.readCache(map[string]string{agent: readFolder()})[agent]
			stateRead = true
		}
		return state
	}
	if a.tmux != nil || a.loadCache != nil {
		if activity := readState().Activity; activity != nil {
			symbol, colour := "?", barWarn
			switch activity.State {
			case "idle":
				symbol, colour = "·", barQuiet
			case "working":
				frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
				symbol, colour = string(frames[(time.Now().Unix()/2)%int64(len(frames))]), barCalm
			case "blocked":
				symbol = "!"
			case "dead":
				symbol, colour = "×", barAlert
			}
			segments = append(segments, style(colour, symbol))
		}
	}
	for _, widget := range a.config.Bar.Widgets {
		switch widget {
		case "ctx":
			state := readState()
			if !state.Known {
				if state.Runtime != "" {
					segments = append(segments, style(barQuiet, "ctx —"))
				}
				continue
			}
			text := humanTokens(state.CtxTokens)
			if a.config.Bar.Context == "remaining" && state.Window <= 0 {
				text += "/?"
			}
			if a.config.Bar.Context == "remaining" && state.Window > 0 {
				remaining := state.Window - state.CtxTokens
				if remaining < 0 {
					remaining = 0
				}
				text = humanTokens(remaining) + " boş"
			}
			colour := barQuiet
			// Claude never reports its window, so those keep the absolute
			// thresholds; a session that does report one is judged by how
			// full it actually is.
			limitWarn, limitAlert := 200_000, 300_000
			if state.Window > 0 {
				limitWarn, limitAlert = state.Window*3/4, state.Window*9/10
			}
			switch {
			case state.CtxTokens > limitAlert:
				colour = barAlert
				text += " !"
			case state.CtxTokens > limitWarn:
				colour = barWarn
			}
			segments = append(segments, style(colour, text))
		case "temp":
			state := readState()
			if !state.Known {
				continue
			}
			temperature, elapsed := state.CacheHint()
			colour := barQuiet
			if temperature == "warm~" {
				colour = barCalm
			}
			segments = append(segments, style(colour, temperature+" "+formatAge(elapsed)))
		case "talk":
			state := readState()
			if state.LastHumanAge < 0 {
				continue
			}
			colour := barQuiet
			if state.LastHumanAge > 24*time.Hour {
				colour = barWarn
			}
			segments = append(segments, style(colour, "talk "+formatAge(state.LastHumanAge)))
		case "queue":
			// Stat, not Load: the bar refreshes on every tick and shows no drop
			// count, so loading here would prune the spool behind the operator's
			// back. StateDir is the root — pending itself appends "pending/".
			if items, _, err := pending.Stat(a.config.StateDir, agent); err == nil && items > 0 {
				segments = append(segments, style(barCalm, "queue "+strconv.Itoa(items)))
			}
		case "model":
			if model := a.barModel(agent, readProcess(), readFolder(), readState); model != "" {
				segments = append(segments, style(barQuiet, model))
			}
		case "quota":
			runtime := readState().Runtime
			if runtime == "claude" || runtime == "codex" || runtime == "codex-remote" {
				if quota := a.barQuotaFor(strings.HasPrefix(runtime, "codex")); quota != "" {
					segments = append(segments, quota)
				}
			}
		case "clock":
			segments = append(segments, style(barQuiet, time.Now().Format("15:04")))
		}
	}

	gap := "#[bg=" + barGap + "] "
	return gap + strings.Join(segments, gap) + gap + "#[default]"
}

func (a *app) barModel(agent string, process bptmux.PaneProcess, folder string, state func() cache.State) string {
	if live := state(); live.Activity != nil {
		if live.Model == "" {
			return ""
		}
		return modelLabel(live.Model, live.Effort)
	}
	home, _ := os.UserHomeDir()
	var model, effort string
	if a.codexPane(agent, process) {
		live := state()
		if live.Model != "" {
			return modelLabel(live.Model, live.Effort)
		}
		if live.Runtime == "codex-remote" {
			return "" // the client's config cannot describe an unknown server model
		}
		root := codexHome(process.PID)
		if root == "" {
			return ""
		}
		model, effort = readCodexModel(filepath.Join(root, "config.toml"))
		if pinModel, pinEffort := readCodexModel(filepath.Join(folder, ".codex", "config.toml")); pinModel != "" || pinEffort != "" {
			if pinModel != "" {
				model = pinModel
			}
			if pinEffort != "" {
				effort = pinEffort
			}
		}
		// The launch line beats the config file, for the same reason the session
		// record beats a Claude pin: `codex -c model_reasoning_effort=medium`
		// changes what this pane actually runs and touches no file. Measured
		// 2026-08-25 — both codex agents were launched with -c overrides while
		// config.toml still said xhigh, so the bar advertised an effort the fleet
		// has banned on a pane that was not using it.
		if liveModel, liveEffort := codexLaunchModel(process.PID); liveModel != "" || liveEffort != "" {
			if liveModel != "" {
				model = liveModel
			}
			if liveEffort != "" {
				effort = liveEffort
			}
		}
		return modelLabel(model, effort)
	}
	switch process.Command {
	case "claude":
		model, effort = readClaudeModel(folder, home)
		// The session record beats every settings file: /model switches a live
		// agent without touching its pin, and writes the GLOBAL default, so the
		// settings answer can be wrong in both directions at once.
		if live := state().Model; live != "" {
			model = live
		}
	default:
		return ""
	}
	return modelLabel(model, effort)
}

// codexPane reports whether this pane is running Codex, by the rule the
// delivery path already follows: the COMMAND may only nominate a pane, the
// SCREEN has to confirm it.
//
// "codex" and "bwrap" are Codex-specific names and answer on their own. The
// third spelling does not: since Tuna turned the sandbox off (2026-08-25) a
// Codex pane reports "node", which is also every build watcher and bridge on
// this machine, so that one costs a read-only capture. Nothing here presses a
// key, so a capture is the whole price of being right.
func (a *app) codexPane(agent string, process bptmux.PaneProcess) bool {
	if !bptmux.IsCodexCommand(process.Command) {
		return false
	}
	if process.Command != "node" {
		return true
	}
	if a.tmux == nil {
		return false
	}
	pane, err := a.tmux.Capture(a.ctx, agent)
	if err != nil {
		return false
	}
	return bptmux.CodexPane(pane)
}

// codexHome resolves the CODEX_HOME the pane's process actually runs with,
// falling back to the default ~/.codex.
func codexHome(pid int) string {
	if home := processEnv(pid, "CODEX_HOME"); home != "" {
		return home
	}
	if user, _ := os.UserHomeDir(); user != "" {
		return filepath.Join(user, ".codex")
	}
	return ""
}

func readClaudeModel(folder, home string) (string, string) {
	if folder != "" {
		local := filepath.Join(folder, ".claude", "settings.local.json")
		data, err := os.ReadFile(local)
		if err == nil {
			// A settings.local.json often carries only a permissions allowlist,
			// no model pin - that agent runs the global default, so fall through.
			if model, effort := parseClaudeModel(data); model != "" || effort != "" {
				return model, effort
			}
		} else if !os.IsNotExist(err) {
			return "", ""
		}
	}
	if home == "" {
		return "", ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		return "", ""
	}
	return parseClaudeModel(data)
}

func parseClaudeModel(data []byte) (string, string) {
	var settings struct {
		Model       string `json:"model"`
		EffortLevel string `json:"effortLevel"`
	}
	if json.Unmarshal(data, &settings) != nil {
		return "", ""
	}
	return settings.Model, settings.EffortLevel
}

func readCodexModel(path string) (string, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	var model, effort string
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			break
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			continue
		}
		switch strings.TrimSpace(key) {
		case "model":
			model = unquoted
		case "model_reasoning_effort":
			effort = unquoted
		}
	}
	return model, effort
}

func processEnv(pid int, key string) string {
	if pid <= 0 {
		return ""
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return ""
	}
	prefix := key + "="
	for _, entry := range strings.Split(string(data), "\x00") {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

func modelLabel(model, effort string) string {
	lower := strings.ToLower(model)
	for _, name := range []string{"opus", "fable", "sonnet", "haiku", "astra", "sol", "terra", "luna"} {
		if strings.Contains(lower, name) {
			model = name
			break
		}
	}
	runes := []rune(model)
	if len(runes) > 10 {
		model = string(runes[:10])
	}
	switch strings.ToLower(effort) {
	case "medium":
		effort = "med"
	case "low", "high", "xhigh", "max":
		effort = strings.ToLower(effort)
	default:
		effort = ""
	}
	return strings.TrimSpace(model + " " + effort)
}

// barQuota is the legacy Claude budget helper; live bars select their provider.
func (a *app) barQuota() string { return a.barQuotaFor(false) }

func (a *app) barQuotaFor(codex bool) string {
	if a.config.UsageHistory == "" {
		return ""
	}
	sample, err := usagecli.Latest(a.config.UsageHistory)
	if err != nil {
		return ""
	}
	weekly, short := sample.Claude.Claude7, sample.Claude.Claude5
	if codex {
		weekly, short = sample.Codex.Codex7, sample.Codex.Codex5
		if weekly == nil {
			weekly = short
			short = nil
		}
	}
	week, ok := percent(weekly)
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
	provider := "cc "
	if codex {
		provider = "gpt "
	}
	text := provider + strconv.Itoa(int(week)) + "%"
	if hours, ok := percent(short); ok {
		if codex {
			text += "/" + strconv.Itoa(int(hours)) + "%"
		} else {
			text = provider + strconv.Itoa(int(hours)) + "%/" + strconv.Itoa(int(week)) + "%"
		}
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
			return book.FirstPath(entry.Folder)
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

// codexLaunchModel reads the model and reasoning effort a codex pane was
// actually started with. The pane's own process is checked first, then its
// direct children, because the pane pid is sometimes the shell that exec'd
// codex rather than codex itself.
func codexLaunchModel(pid int) (string, string) {
	if pid <= 0 {
		return "", ""
	}
	candidates := append([]int{pid}, childPIDs(pid)...)
	for _, candidate := range candidates {
		if model, effort := codexArgsModel(procArgs(candidate)); model != "" || effort != "" {
			return model, effort
		}
	}
	return "", ""
}

// codexArgsModel understands the two ways a launch line states them: the
// dedicated -m/--model flag, and -c key=value config overrides.
func codexArgsModel(argv []string) (string, string) {
	var model, effort string
	for i := 0; i < len(argv); i++ {
		switch argv[i] {
		case "-m", "--model":
			if i+1 < len(argv) {
				model = argv[i+1]
				i++
			}
		case "-c", "--config":
			if i+1 >= len(argv) {
				continue
			}
			key, value, ok := strings.Cut(argv[i+1], "=")
			i++
			if !ok {
				continue
			}
			value = strings.Trim(strings.TrimSpace(value), `"'`)
			switch strings.TrimSpace(key) {
			case "model":
				model = value
			case "model_reasoning_effort":
				effort = value
			}
		}
	}
	return model, effort
}

func procArgs(pid int) []string {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil
	}
	var argv []string
	for _, field := range strings.Split(string(data), "\x00") {
		if field != "" {
			argv = append(argv, field)
		}
	}
	return argv
}

// childPIDs lists the direct children of pid, read from /proc rather than
// spawned as pgrep: the bar runs on every status tick.
func childPIDs(pid int) []int {
	entries, err := filepath.Glob(filepath.Join("/proc", strconv.Itoa(pid), "task", "*", "children"))
	if err != nil {
		return nil
	}
	var children []int
	for _, entry := range entries {
		data, err := os.ReadFile(entry)
		if err != nil {
			continue
		}
		for _, field := range strings.Fields(string(data)) {
			if child, err := strconv.Atoi(field); err == nil {
				children = append(children, child)
			}
		}
	}
	return children
}
