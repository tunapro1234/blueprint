package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"blueprint/internal/book"
	bpcache "blueprint/internal/cache"
	"blueprint/internal/codexrpc"
	bpconfig "blueprint/internal/config"
	"blueprint/internal/daemon"
	"blueprint/internal/dashboard"
	"blueprint/internal/fed"
	"blueprint/internal/monitorcli"
	"blueprint/internal/msgq"
	"blueprint/internal/ntfy"
	"blueprint/internal/pending"
	bptmux "blueprint/internal/tmux"
	"blueprint/internal/usagecli"
	"blueprint/internal/wa"
	"blueprint/internal/worktree"
)

const usage = `blueprint (bp) — agent infrastructure CLI

bp status [--json] | bp tree
bp open <name> <directory> [--worktree <topic>] [--parent <name>] [--role <text>] [--resume] [--codex] [--no-prompt]
bp worktree add <repo-directory> <topic>
bp worktree list <repo-directory>
bp worktree rm <repo-directory> <topic> [--force]
bp close <name>
bp rename <old-name> <new-name> [--dry-run]
bp msg <name> <message...>   # bp stamps a [sender] envelope; never write your own
                             # a /slash command goes bare, and only down the hierarchy
bp announce <message...> [--dry-run]
bp compact [--idle-hours N] [--min-ctx N] [--apply]   # policy: idle+full claude agents
bp compact --all [--min-age <minutes>] [--exclude <name,...>] [--apply]
                             # lists by default; nothing is sent without --apply
bp remote [<name>...]        # print or open /remote-control (default: every live claude agent)
bp q | bp qstat <channel-id> | bp qcancel <channel-id>
bp peek <name> [n]
bp wa send [--to <target>] [--reply <msgId>] <message...>
bp wa read <target> [n] | bp wa chats
bp usage
bp tokens [--day YYYY-MM-DD | --since 7d] [--hours | --prompts] [--agent <name>] [--json]
bp tokens collect | bp tokens gc
bp monitor [usage|cost|agents|projects|services|radar]
bp policy status|override <hours>
bp service
bp con [agent-name]
bp img [recv]
bp dash [--port N]
bp fed status|ping|token|log [n]
bp daemon`

type app struct {
	ctx    context.Context
	config bpconfig.Config
	tmux   *bptmux.Client
	queue  *msgq.Queue
	out    *os.File
	err    *os.File

	loadFleet      func() (book.Fleet, map[string]book.State, error)
	loadCodex      func() []codexrpc.Thread
	deliverMessage func(string, string, string) (bool, string, error)
	sessionExists  func(string) bool
	loadCache      func(map[string]string) map[string]bpcache.State
	loadCommands   func() (map[string]string, error)
	capturePane    func(string) (string, error)
	clearPane      func(string) error
}

func main() {
	ctx := context.Background()
	config, err := bpconfig.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
	a := &app{ctx: ctx, config: config, tmux: bptmux.New(), queue: msgq.New(config.MsgqRoot), out: os.Stdout, err: os.Stderr}
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"status"}
	}
	if err := a.run(args); err != nil {
		if !errors.Is(err, errReported) {
			fmt.Fprintln(os.Stderr, "ERROR:", err)
		}
		os.Exit(1)
	}
}

// errReported marks a failure whose explanation the command already printed
// itself: bp still exits non-zero, but main adds no second, duplicate line.
var errReported = errors.New("reported above")

// deliveryReason unwraps a delivery sentinel so a user-visible line names the
// cause ("pane oturumu dusmus …") instead of repeating the plumbing prefix.
func deliveryReason(err error, sentinel error) string {
	return strings.TrimPrefix(err.Error(), sentinel.Error()+": ")
}

// helpRequested reports whether -h/--help appears among a subcommand's own
// flags. Commands that carry free-form text (msg, announce, wa) only honour it
// before the first non-flag argument, so "bp msg agent --help" still delivers
// the literal word instead of printing usage at the sender.
func helpRequested(args []string) bool {
	rest := args[1:]
	switch args[0] {
	case "msg", "announce", "wa":
		for index, arg := range rest {
			if !strings.HasPrefix(arg, "-") {
				rest = rest[:index]
				break
			}
		}
	}
	for _, arg := range rest {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

// rejectFlag guards commands whose first positional argument is an agent name.
// Without it a typo'd flag is taken for a session name — "bp status --help"
// once registered "--help" into the agentbook.
func rejectFlag(command, arg string) error {
	if strings.HasPrefix(arg, "-") {
		return fmt.Errorf("unknown %s option: %s", command, arg)
	}
	return nil
}

func (a *app) run(args []string) error {
	if len(args) == 0 {
		args = []string{"status"}
	}
	// Help is answered before any command logic so no subcommand can mistake
	// -h/--help for one of its own arguments.
	if helpRequested(args) {
		fmt.Fprintln(a.out, usage)
		return nil
	}
	if args[0] == "fed" && a.config.InvalidConfig != "" {
		return fmt.Errorf("federation disabled because config.json is invalid: %s", a.config.InvalidConfig)
	}
	switch args[0] {
	case "status":
		return a.status(args[1:])
	case "tree":
		return a.tree(args[1:])
	case "open":
		return a.open(args[1:])
	case "worktree":
		return a.worktree(args[1:])
	case "close":
		return a.close(args[1:])
	case "rename":
		return a.rename(args[1:])
	case "msg":
		return a.message(args[1:])
	case "announce":
		return a.announce(args[1:])
	case "compact":
		return a.compact(args[1:])
	case "remote":
		return a.remote(args[1:])
	case "q":
		return a.queueList(args[1:])
	case "qstat":
		return a.queueStatus(args[1:])
	case "qcancel":
		if len(args) < 2 {
			return fmt.Errorf("usage: bp qcancel <channel-id>")
		}
		if err := a.queue.Cancel(args[1]); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "canceled: %s\n", args[1])
		return nil
	case "peek":
		return a.peek(args[1:])
	case "wa":
		return a.whatsapp(args[1:])
	case "usage":
		return a.usage()
	case "tokens":
		return a.tokens(args[1:])
	case "monitor":
		return a.monitor(args[1:])
	case "policy":
		return a.policy(args[1:])
	case "service":
		return a.service()
	case "con":
		return a.connect(args[1:])
	case "img":
		return a.image(args[1:])
	case "bar":
		return a.bar(args[1:])
	case "name":
		if len(args) != 2 {
			return fmt.Errorf("usage: bp name <agent>")
		}
		fmt.Fprintln(a.out, a.barName(args[1]))
		return nil
	case "dash":
		return a.dashboard(args[1:])
	case "fed":
		return a.federation(args[1:])
	case "daemon":
		return a.daemon(args[1:])
	case "help", "-h", "--help":
		fmt.Fprintln(a.out, usage)
		return nil
	default:
		fmt.Fprintln(a.err, usage)
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

type connectConfig struct {
	Remote string
	Method string
}

type commandSpec struct {
	Path string
	Args []string
}

var safeSessionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func connectConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".config", "bp", "config"), nil
}

func loadConnectConfig(path string) (connectConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return connectConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	config := connectConfig{Method: "mosh"}
	for lineNumber, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return connectConfig{}, fmt.Errorf("%s:%d: expected KEY=VALUE", path, lineNumber+1)
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch key {
		case "REMOTE":
			config.Remote = value
		case "REMOTE_METHOD":
			config.Method = strings.ToLower(value)
		}
	}
	if config.Remote == "" {
		return connectConfig{}, fmt.Errorf("%s: REMOTE is required", path)
	}
	if strings.HasPrefix(config.Remote, "-") || strings.ContainsAny(config.Remote, " \t\r\n") {
		return connectConfig{}, fmt.Errorf("%s: REMOTE must be a single user@host or SSH host", path)
	}
	if config.Method == "" {
		config.Method = "mosh"
	}
	if config.Method != "mosh" && config.Method != "ssh" {
		return connectConfig{}, fmt.Errorf("%s: REMOTE_METHOD must be mosh or ssh", path)
	}
	return config, nil
}

func loadDashboardURL(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return dashboard.DefaultURL, nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	for lineNumber, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return "", fmt.Errorf("%s:%d: expected KEY=VALUE", path, lineNumber+1)
		}
		if strings.TrimSpace(key) == "DASH_URL" {
			value = strings.Trim(strings.TrimSpace(value), `"'`)
			if value == "" {
				return "", fmt.Errorf("%s:%d: DASH_URL cannot be empty", path, lineNumber+1)
			}
			return value, nil
		}
	}
	return dashboard.DefaultURL, nil
}

func parseDashboardPort(args []string) (int, error) {
	port := dashboard.DefaultPort
	seen := false
	for index := 0; index < len(args); index++ {
		argument := args[index]
		value := ""
		switch {
		case argument == "--port":
			if index+1 >= len(args) {
				return 0, fmt.Errorf("usage: bp dash [--port N]")
			}
			value = args[index+1]
			index++
		case strings.HasPrefix(argument, "--port="):
			value = strings.TrimPrefix(argument, "--port=")
		default:
			return 0, fmt.Errorf("usage: bp dash [--port N]")
		}
		if seen {
			return 0, fmt.Errorf("--port may only be specified once")
		}
		seen = true
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 65535 {
			return 0, fmt.Errorf("invalid dashboard port: %s", value)
		}
		port = parsed
	}
	return port, nil
}

func (a *app) dashboard(args []string) error {
	port, err := parseDashboardPort(args)
	if err != nil {
		return err
	}
	path, err := connectConfigPath()
	if err != nil {
		return err
	}
	dashboardURL, err := loadDashboardURL(path)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(a.ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return dashboard.Serve(ctx, dashboard.Options{Port: port, URL: dashboardURL, UsageBin: a.config.UsageBin, Out: a.out})
}

func findCommand(bin string, args ...string) (commandSpec, error) {
	path, err := exec.LookPath(bin)
	if err != nil {
		return commandSpec{}, fmt.Errorf("%s is not installed", bin)
	}
	return commandSpec{Path: path, Args: append([]string{bin}, args...)}, nil
}

func remoteAttachCommand(config connectConfig, name string) (commandSpec, error) {
	if config.Method == "mosh" {
		if spec, err := findCommand("mosh", config.Remote, "--", "tmux", "attach", "-t", name); err == nil {
			return spec, nil
		}
	}
	return findCommand("ssh", "-t", config.Remote, "tmux", "attach", "-t", name)
}

func replaceWith(spec commandSpec) error {
	return syscall.Exec(spec.Path, spec.Args, os.Environ())
}

func (a *app) connect(args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: bp con [agent-name]")
	}
	if len(args) == 0 {
		return a.listConnections()
	}

	name := args[0]
	if !safeSessionName.MatchString(name) {
		return fmt.Errorf("invalid agent name: %s", name)
	}
	if a.tmux.HasSession(a.ctx, name) {
		operation := "attach"
		if os.Getenv("TMUX") != "" {
			operation = "switch-client"
		}
		spec, err := findCommand("tmux", operation, "-t", name)
		if err != nil {
			return err
		}
		return replaceWith(spec)
	}

	path, err := connectConfigPath()
	if err != nil {
		return err
	}
	config, err := loadConnectConfig(path)
	if err != nil {
		return fmt.Errorf("no local session named %s and remote is not configured: %w", name, err)
	}
	spec, err := remoteAttachCommand(config, name)
	if err != nil {
		return err
	}
	return replaceWith(spec)
}

func (a *app) listConnections() error {
	sessions, err := a.tmux.Sessions(a.ctx)
	if err == nil && len(sessions) > 0 {
		for _, name := range sessions {
			fmt.Fprintln(a.out, name)
		}
		return nil
	}

	path, pathErr := connectConfigPath()
	if pathErr != nil {
		return pathErr
	}
	config, configErr := loadConnectConfig(path)
	if configErr != nil {
		if err != nil {
			return errors.Join(err, configErr)
		}
		return configErr
	}
	spec, specErr := findCommand("ssh", config.Remote, "tmux", "ls")
	if specErr != nil {
		return specErr
	}
	cmd := exec.CommandContext(a.ctx, spec.Path, spec.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, a.out, a.err
	return cmd.Run()
}

func (a *app) fleet() (book.Fleet, map[string]book.State, error) {
	if a.loadFleet != nil {
		return a.loadFleet()
	}
	fleet, err := book.LoadFleet(book.Paths(a.config.Agentbooks))
	if err != nil {
		return book.Fleet{}, nil, err
	}
	states, err := book.LiveStates(a.ctx, a.tmux, &fleet)
	return fleet, states, err
}

// tmuxStateLabel names what tmux shows for an agent: the same four words the
// human table and the JSON output both report.
func tmuxStateLabel(state book.State, alive bool) string {
	switch {
	case !alive:
		return "closed"
	case state.Dead:
		return "dead"
	case state.Busy:
		return "working"
	default:
		return "idle"
	}
}

func (a *app) status(args []string) error {
	asJSON := false
	for _, arg := range args {
		if arg != "--json" {
			return fmt.Errorf("unknown status option: %s", arg)
		}
		if asJSON {
			return fmt.Errorf("--json may only be specified once")
		}
		asJSON = true
	}
	fleet, states, err := a.fleet()
	if err != nil {
		return err
	}
	cacheStates := a.cacheStates(fleet)
	if asJSON {
		return a.statusJSON(fleet, states, cacheStates)
	}
	fmt.Fprintf(a.out, "%-24s %-10s %-20s %-10s %s\n", "AGENT", "TMUX", "CACHE", "LAST-TALK", "AGENTBOOK")
	for _, name := range fleet.SortedNames() {
		state, alive := states[name]
		tmuxState := tmuxStateLabel(state, alive)
		bookState := fleet.Agents[name].Status
		if bookState == "" {
			bookState = "?"
		}
		flag := ""
		if !alive && bookState == "open" {
			flag = "  <-- book:open but tmux is missing"
		}
		if alive && bookState == "closed" {
			flag = "  <-- tmux is open but book:closed"
		}
		cacheText, talkText := "-", "-"
		if state, ok := cacheStates[name]; ok {
			if state.Known {
				temperature := "cold"
				if state.Age < time.Hour {
					temperature = "warm"
				}
				cacheText = fmt.Sprintf("%s %s %s", temperature, shortAge(state.Age), humanTokens(state.CtxTokens))
			}
			if state.LastHumanAge >= 0 {
				talkText = shortAge(state.LastHumanAge)
			}
		}
		fmt.Fprintf(a.out, "%-24s %-10s %-20s %-10s %-10s%s\n", name, tmuxState, cacheText, talkText, bookState, flag)
	}
	a.renderCodexStatus(a.codexThreads())
	return nil
}

// statusReport is the machine-readable shape of bp status. Numbers that are
// merely unknown are omitted rather than sent as zeros: a zero token count or a
// zero age would read as a measured fact.
type statusReport struct {
	Agents        []statusAgent  `json:"agents"`
	Codex         []statusThread `json:"codex,omitempty"`
	CodexUnloaded int            `json:"codex_unloaded,omitempty"`
}

type statusAgent struct {
	Name                string `json:"name"`
	Tmux                string `json:"tmux"`
	Status              string `json:"status,omitempty"`
	Folder              string `json:"folder,omitempty"`
	Parent              string `json:"parent,omitempty"`
	CtxTokens           *int   `json:"ctx_tokens,omitempty"`
	CacheAgeSeconds     *int64 `json:"cache_age_seconds,omitempty"`
	LastHumanAgeSeconds *int64 `json:"last_human_age_seconds,omitempty"`
	Model               string `json:"model,omitempty"`
}

type statusThread struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	CWD       string `json:"cwd,omitempty"`
	CtxTokens *int64 `json:"ctx_tokens,omitempty"`
	Window    *int64 `json:"context_window,omitempty"`
}

func (a *app) statusJSON(fleet book.Fleet, states map[string]book.State, cacheStates map[string]bpcache.State) error {
	report := statusReport{Agents: make([]statusAgent, 0, len(fleet.Agents))}
	for _, name := range fleet.SortedNames() {
		state, alive := states[name]
		agent := fleet.Agents[name]
		row := statusAgent{
			Name:   name,
			Tmux:   tmuxStateLabel(state, alive),
			Status: agent.Status,
			Folder: agent.Folder,
			Parent: fleet.Parents[name],
		}
		if cacheState, ok := cacheStates[name]; ok {
			if cacheState.Known {
				tokens := cacheState.CtxTokens
				age := int64(cacheState.Age / time.Second)
				row.CtxTokens, row.CacheAgeSeconds = &tokens, &age
			}
			if cacheState.LastHumanAge >= 0 {
				lastHuman := int64(cacheState.LastHumanAge / time.Second)
				row.LastHumanAgeSeconds = &lastHuman
			}
			row.Model = cacheState.Model
		}
		report.Agents = append(report.Agents, row)
	}
	live, unloaded := liveCodexThreads(a.codexThreads())
	report.CodexUnloaded = unloaded
	for _, thread := range live {
		row := statusThread{Name: codexName(thread), State: codexState(thread.Status), CWD: thread.CWD}
		if usage := thread.TokenUsage; usage != nil {
			used := usage.Last.TotalTokens
			if used == 0 {
				used = usage.Total.TotalTokens
			}
			row.CtxTokens = &used
			if usage.ModelContextWindow != nil && *usage.ModelContextWindow > 0 {
				window := *usage.ModelContextWindow
				row.Window = &window
			}
		}
		report.Codex = append(report.Codex, row)
	}
	encoder := json.NewEncoder(a.out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func (a *app) cacheStates(fleet book.Fleet) map[string]bpcache.State {
	folders := make(map[string]string, len(fleet.Agents))
	for name, agent := range fleet.Agents {
		folders[name] = agent.Folder
	}
	done := make(chan map[string]bpcache.State, 1)
	go func() {
		if a.loadCache != nil {
			done <- a.loadCache(folders)
			return
		}
		done <- bpcache.Fleet(bptmux.ClaudeProjectsRoot(), folders)
	}()
	select {
	case states := <-done:
		return states
	case <-time.After(time.Second):
		return nil
	}
}

func (a *app) tree(args []string) error {
	if len(args) > 0 {
		if err := rejectFlag("tree", args[0]); err != nil {
			return err
		}
		return fmt.Errorf("usage: bp tree")
	}
	fleet, states, err := a.fleet()
	if err != nil {
		return err
	}
	worktreeLabels, err := a.worktreeLabels()
	if err != nil {
		return err
	}
	children := map[string][]string{}
	for _, name := range fleet.Order {
		if name == fleet.Root {
			continue
		}
		parent := fleet.Parents[name]
		if _, ok := fleet.Agents[parent]; !ok || parent == name {
			parent = fleet.Root
		}
		children[parent] = append(children[parent], name)
	}
	seen := map[string]bool{}
	var walk func(string, string, bool)
	walk = func(name, prefix string, last bool) {
		if seen[name] {
			return
		}
		seen[name] = true
		branch := ""
		if prefix != "" {
			if last {
				branch = "└── "
			} else {
				branch = "├── "
			}
		}
		agent := fleet.Agents[name]
		live := "closed"
		if state, ok := states[name]; ok {
			switch {
			case state.Dead:
				live = "dead"
			case state.Busy:
				live = "working"
			default:
				live = "idle"
			}
		}
		label := name
		if agent.Nickname != "" {
			label += " (" + agent.Nickname + ")"
		}
		if worktreeLabel := worktreeLabels[name]; worktreeLabel != "" {
			label += " " + worktreeLabel
		}
		status := agent.Status
		if status == "" {
			status = "unregistered"
		}
		fmt.Fprintf(a.out, "%s%s%s [%s/%s]\n", prefix, branch, label, live, status)
		nextPrefix := prefix
		if prefix != "" {
			if last {
				nextPrefix += "    "
			} else {
				nextPrefix += "│   "
			}
		} else {
			nextPrefix = " "
		}
		rows := children[name]
		for i, child := range rows {
			walk(child, nextPrefix, i == len(rows)-1)
		}
	}
	walk(fleet.Root, "", true)
	// Preserve visibility if a malformed book contains a detached cycle.
	for _, name := range fleet.Order {
		if !seen[name] {
			walk(name, " ", true)
		}
	}
	a.renderCodexTree(a.codexThreads())
	return nil
}

func (a *app) codexThreads() []codexrpc.Thread {
	if a.config.Codex == nil || len(a.config.Codex.Sockets) == 0 {
		return nil
	}
	if a.loadCodex != nil {
		return a.loadCodex()
	}
	base := a.ctx
	if base == nil {
		base = context.Background()
	}
	var threads []codexrpc.Thread
	seen := map[string]bool{}
	for _, socket := range a.config.Codex.Sockets {
		if socket == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(base, 2*time.Second)
		client, err := codexrpc.DialUnix(ctx, socket)
		if err == nil {
			listed, listErr := client.ThreadList(ctx)
			_ = client.Close()
			if listErr == nil {
				for _, thread := range listed {
					if thread.ID != "" && seen[thread.ID] {
						continue
					}
					seen[thread.ID] = true
					threads = append(threads, thread)
				}
			}
		}
		cancel()
	}
	return threads
}

// liveCodexThreads splits threads into the ones worth listing (loaded into the
// daemon) and a count of unloaded history, which would otherwise drown the
// fleet view: a long-lived app-server accumulates every past session.
func liveCodexThreads(threads []codexrpc.Thread) ([]codexrpc.Thread, int) {
	var live []codexrpc.Thread
	for _, thread := range threads {
		if thread.Status.Type != "notLoaded" {
			live = append(live, thread)
		}
	}
	return live, len(threads) - len(live)
}

func (a *app) renderCodexStatus(threads []codexrpc.Thread) {
	live, unloaded := liveCodexThreads(threads)
	if len(live) == 0 && unloaded == 0 {
		return
	}
	fmt.Fprintln(a.out, "\nCODEX THREADS")
	if len(live) > 0 {
		fmt.Fprintf(a.out, "%-28s %-10s %-16s %s\n", "THREAD", "STATE", "CONTEXT", "CWD")
	}
	for _, thread := range live {
		fmt.Fprintf(a.out, "%-28s %-10s %-16s %s\n", codexName(thread), codexState(thread.Status), codexContext(thread.TokenUsage), thread.CWD)
	}
	if unloaded > 0 {
		fmt.Fprintf(a.out, "+ %d unloaded thread(s)\n", unloaded)
	}
}

func (a *app) renderCodexTree(threads []codexrpc.Thread) {
	threads, unloaded := liveCodexThreads(threads)
	if len(threads) == 0 {
		if unloaded > 0 {
			fmt.Fprintf(a.out, "\nCodex: %d unloaded thread(s)\n", unloaded)
		}
		return
	}
	fmt.Fprintln(a.out, "\nCodex")
	for i, thread := range threads {
		branch := "├── "
		if i == len(threads)-1 {
			branch = "└── "
		}
		contextText := ""
		if usage := codexContext(thread.TokenUsage); usage != "-" {
			contextText = " (ctx " + usage + ")"
		}
		fmt.Fprintf(a.out, " %s%s [%s] %s%s\n", branch, codexName(thread), codexState(thread.Status), thread.CWD, contextText)
	}
	if unloaded > 0 {
		fmt.Fprintf(a.out, " + %d unloaded thread(s)\n", unloaded)
	}
}

func codexName(thread codexrpc.Thread) string {
	if thread.Name != "" {
		return thread.Name
	}
	if thread.AgentNickname != "" {
		return thread.AgentNickname
	}
	if thread.ID != "" {
		return thread.ID
	}
	return "unnamed"
}

func codexState(status codexrpc.ThreadStatus) string {
	switch status.Type {
	case "active":
		return "working"
	case "idle":
		return "idle"
	case "notLoaded":
		return "unloaded"
	case "systemError":
		return "error"
	case "":
		return "unknown"
	default:
		return status.Type
	}
}

func codexContext(usage *codexrpc.ThreadTokenUsage) string {
	if usage == nil {
		return "-"
	}
	used := usage.Last.TotalTokens
	if used == 0 {
		used = usage.Total.TotalTokens
	}
	if usage.ModelContextWindow != nil && *usage.ModelContextWindow > 0 {
		return humanTokens(int(used)) + "/" + humanTokens(int(*usage.ModelContextWindow))
	}
	return humanTokens(int(used))
}

func (a *app) open(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: bp open <name> <directory> [--worktree <topic>] [--parent <name>] [--role <text>] [--resume] [--codex] [--no-prompt]")
	}
	name, dir := args[0], args[1]
	for _, positional := range []string{name, dir} {
		if err := rejectFlag("open", positional); err != nil {
			return err
		}
	}
	opts := bptmux.OpenOptions{Legacy: a.config.Legacy}
	worktreeTopic := ""
	reg := book.Registration{}
	for index := 2; index < len(args); index++ {
		arg := args[index]
		switch arg {
		case "--resume":
			opts.Resume = true
		case "--codex":
			opts.Codex = true
		case "--no-prompt":
			opts.NoPrompt = true
		case "--worktree":
			if index+1 >= len(args) {
				return fmt.Errorf("--worktree requires a topic")
			}
			if worktreeTopic != "" {
				return fmt.Errorf("--worktree may only be specified once")
			}
			worktreeTopic = args[index+1]
			index++
		case "--parent":
			if index+1 >= len(args) {
				return fmt.Errorf("--parent requires an agent name")
			}
			reg.Parent = args[index+1]
			index++
		case "--role":
			if index+1 >= len(args) {
				return fmt.Errorf("--role requires a role text")
			}
			reg.Role = args[index+1]
			index++
		default:
			return fmt.Errorf("unknown open option: %s", arg)
		}
	}
	// The fleet answers two questions below: is --parent a real agent, and does
	// this folder sit under the parent's. Only the first is worth failing over
	// — a typo'd parent would register the agent under a name nobody reads, so
	// it is checked before any session is started or any book written.
	fleet, _, fleetErr := a.fleet()
	if reg.Parent != "" {
		if fleetErr != nil {
			return fleetErr
		}
		if _, ok := fleet.Agents[reg.Parent]; !ok {
			return fmt.Errorf("unknown parent: %s", reg.Parent)
		}
	}
	if a.tmux.HasSession(a.ctx, name) {
		// "Session exists" is not "agent running": a crashed CLI leaves the
		// tmux session up as a bare shell. Only a live agent pane counts as
		// already open; a dead shell falls through to Open, which relaunches
		// the agent in place (or errors if the pane runs something else).
		process, perr := a.tmux.PaneProcess(a.ctx, name)
		if perr != nil || bptmux.IsAgentCommand(process.Command) {
			// Nothing to launch — but explicit --parent/--role is a correction
			// of the agentbook entry, so it still applies to a running agent.
			if reg.Parent != "" || reg.Role != "" {
				reg.Sender = a.sender()
				if err := book.SetStatus(a.config.Agentbooks, name, "open", dir, reg); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "%s is already open (agentbook updated)\n", name)
				return nil
			}
			fmt.Fprintf(a.out, "%s is already open\n", name)
			return nil
		}
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("directory does not exist: %s", dir)
	}
	if worktreeTopic != "" {
		entry, err := worktree.New().Ensure(a.ctx, dir, worktreeTopic)
		if err != nil {
			return err
		}
		dir = entry.Path
	}
	// The owner wants the hierarchy visible on disk. This is a nudge, not a
	// gate: it prints at most one line and never changes what happens next.
	if fleetErr == nil {
		parent := reg.Parent
		if parent == "" {
			parent = fleet.Parents[name]
		}
		if hint := book.FolderHint(name, dir, parent, fleet.Agents[parent].Folder, fleet.Root); hint != "" {
			fmt.Fprintln(a.out, hint)
		}
	}
	if err := a.tmux.Open(a.ctx, name, dir, opts, func(text string) { fmt.Fprintln(a.out, text) }); err != nil {
		return err
	}
	reg.Sender = a.sender()
	if err := book.SetStatus(a.config.Agentbooks, name, "open", dir, reg); err != nil {
		return err
	}
	rc := ""
	if pane, err := a.tmux.Capture(a.ctx, name); err == nil {
		matches := regexp.MustCompile(`claude\.ai/code/session_[A-Za-z0-9]+`).FindAllString(pane, -1)
		if len(matches) > 0 {
			rc = "  rc:https://" + matches[len(matches)-1]
		}
	}
	if err := a.flushPending(name); err != nil {
		if errors.Is(err, bptmux.ErrUnverified) {
			fmt.Fprintf(a.err, "WARNING: pending digest for %s was sent but DOGRULANAMADI; check with bp peek %s\n", name, name)
		} else {
			fmt.Fprintf(a.err, "WARNING: pending messages for %s were not delivered: %v\n", name, err)
		}
	}
	fmt.Fprintf(a.out, "%s opened%s\n", name, rc)
	return nil
}

func (a *app) worktree(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: bp worktree add|list|rm ...")
	}
	manager := worktree.New()
	switch args[0] {
	case "add":
		if len(args) != 3 {
			return fmt.Errorf("usage: bp worktree add <repo-directory> <topic>")
		}
		entry, err := manager.Ensure(a.ctx, args[1], args[2])
		if err != nil {
			return err
		}
		fmt.Fprintln(a.out, entry.Path)
		return nil
	case "list":
		if len(args) != 2 {
			return fmt.Errorf("usage: bp worktree list <repo-directory>")
		}
		entries, err := manager.List(a.ctx, args[1])
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Fprintln(a.out, "(no worktrees)")
			return nil
		}
		locations, err := a.tmux.Locations(a.ctx)
		if err != nil {
			return err
		}
		agents := agentsByWorktree(entries, locations)
		fmt.Fprintln(a.out, "PATH\tBRANCH\tAGENT")
		for _, entry := range entries {
			names := agents[entry.Path]
			agent := "-"
			if len(names) > 0 {
				agent = strings.Join(names, ",")
			}
			fmt.Fprintf(a.out, "%s\t%s\t%s\n", entry.Path, entry.Branch, agent)
		}
		return nil
	case "rm":
		force := false
		positional := make([]string, 0, len(args)-1)
		for _, arg := range args[1:] {
			if arg == "--force" {
				force = true
				continue
			}
			if strings.HasPrefix(arg, "-") {
				return fmt.Errorf("unknown worktree rm option: %s", arg)
			}
			positional = append(positional, arg)
		}
		if len(positional) != 2 {
			return fmt.Errorf("usage: bp worktree rm <repo-directory> <topic> [--force]")
		}
		repo, err := manager.ResolveRepo(a.ctx, positional[0])
		if err != nil {
			return err
		}
		derived, err := worktree.Derive(repo, positional[1])
		if err != nil {
			return err
		}
		if err := manager.Remove(a.ctx, repo, positional[1], force); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s removed\n", derived.Path)
		return nil
	default:
		return fmt.Errorf("unknown worktree command: %s", args[0])
	}
}

func agentsByWorktree(entries []worktree.Info, locations []bptmux.Location) map[string][]string {
	sets := make(map[string]map[string]bool, len(entries))
	for _, entry := range entries {
		sets[entry.Path] = map[string]bool{}
	}
	for _, location := range locations {
		for _, dir := range []string{location.CurrentDir, location.StartDir} {
			if dir == "" {
				continue
			}
			for _, entry := range entries {
				if worktree.ContainsPath(entry.Path, dir) {
					sets[entry.Path][location.Session] = true
				}
			}
		}
	}
	agents := make(map[string][]string, len(entries))
	for _, entry := range entries {
		for name := range sets[entry.Path] {
			agents[entry.Path] = append(agents[entry.Path], name)
		}
		sort.Strings(agents[entry.Path])
	}
	return agents
}

func (a *app) worktreeLabels() (map[string]string, error) {
	locations, err := a.tmux.Locations(a.ctx)
	if err != nil {
		return nil, err
	}
	manager := worktree.New()
	labels := map[string]string{}
	inspected := map[string]worktree.Info{}
	failed := map[string]bool{}
	for _, location := range locations {
		if labels[location.Session] != "" {
			continue
		}
		for _, dir := range []string{location.CurrentDir, location.StartDir} {
			if dir == "" || failed[dir] {
				continue
			}
			entry, ok := inspected[dir]
			if !ok {
				entry, err = manager.Inspect(a.ctx, dir)
				if err != nil {
					failed[dir] = true
					continue
				}
				inspected[dir] = entry
			}
			if entry.Main {
				continue
			}
			labels[location.Session] = filepath.Base(entry.Repo) + "@" + entry.Branch
			break
		}
	}
	return labels, nil
}

func (a *app) close(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: bp close <name>")
	}
	name := args[0]
	if err := rejectFlag("close", name); err != nil {
		return err
	}
	if a.tmux.HasSession(a.ctx, name) {
		if err := a.tmux.Close(a.ctx, name); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s closed (history remains in JSONL)\n", name)
	} else {
		fmt.Fprintf(a.out, "%s is already closed\n", name)
	}
	return book.SetStatus(a.config.Agentbooks, name, "closed", "", book.Registration{Sender: a.sender()})
}

func (a *app) sender() string {
	// Inside tmux the pane's own session is the caller's identity, and it is
	// authoritative: it comes from the tmux server, not from anything the
	// caller can export. AGENT is honored only outside tmux (daemon, systemd,
	// plain shells) — checking it first would let any agent sign its messages
	// as someone else with a one-line export.
	if os.Getenv("TMUX") != "" {
		if value, err := a.tmux.DisplaySession(a.ctx); err == nil && value != "" {
			return value
		}
	}
	if value := os.Getenv("AGENT"); value != "" {
		return value
	}
	// Outside tmux with no AGENT the caller is usually a human on the box:
	// root login is disabled, so people SSH in as themselves and reach bp
	// through sudo. SUDO_USER/USER name that human and neither is spoofable
	// any more cheaply than AGENT already is. They are checked before the
	// "server-main" default on purpose: falling straight through would stamp
	// a person's message with the orchestrator's name, so agents would read
	// it as an order from the fleet's coordinator instead of from a human.
	// server-main stays the last resort for the callers that really are the
	// server itself — the daemon, cron and root shells, which have no
	// SUDO_USER and a USER of root.
	for _, key := range []string{"SUDO_USER", "USER", "LOGNAME"} {
		value := os.Getenv(key)
		if value == "" || value == "root" {
			continue
		}
		if !validAgentName(value) {
			continue
		}
		return value
	}
	return "server-main"
}

func (a *app) hasSession(name string) bool {
	if a.sessionExists != nil {
		return a.sessionExists(name)
	}
	return a.tmux.HasSession(a.ctx, name)
}

func (a *app) readCache(folders map[string]string) map[string]bpcache.State {
	if a.loadCache != nil {
		return a.loadCache(folders)
	}
	return bpcache.Fleet(bptmux.ClaudeProjectsRoot(), folders)
}

func (a *app) flushPending(name string) error {
	entries, dropped, err := pending.Load(a.config.StateDir, name)
	if err != nil || len(entries) == 0 {
		return err
	}
	if err := a.tmux.Send(a.ctx, name, formatDigest(entries, dropped)); err != nil {
		if !errors.Is(err, bptmux.ErrUnverified) {
			return err
		}
		// Unconfirmed, but the digest may well be in the pane: keeping the
		// entries would repeat every announcement on the next flush. Clear them
		// and let the caller report the doubt.
		if clearErr := pending.Clear(a.config.StateDir, name); clearErr != nil {
			return clearErr
		}
		return err
	}
	return pending.Clear(a.config.StateDir, name)
}

var istanbul = time.FixedZone("Europe/Istanbul", 3*60*60)

var turkishMonths = [...]string{"", "Oca", "Sub", "Mar", "Nis", "May", "Haz", "Tem", "Agu", "Eyl", "Eki", "Kas", "Ara"}

func formatDigest(entries []pending.Entry, dropped int) string {
	first := time.Unix(entries[0].TS, 0).In(istanbul)
	last := time.Unix(entries[len(entries)-1].TS, 0).In(istanbul)
	rangeText := fmt.Sprintf("%d %s", first.Day(), turkishMonths[first.Month()])
	if first.YearDay() != last.YearDay() || first.Year() != last.Year() {
		if first.Month() == last.Month() && first.Year() == last.Year() {
			rangeText = fmt.Sprintf("%d-%d %s", first.Day(), last.Day(), turkishMonths[first.Month()])
		} else {
			rangeText = fmt.Sprintf("%d %s-%d %s", first.Day(), turkishMonths[first.Month()], last.Day(), turkishMonths[last.Month()])
		}
	}
	var digest strings.Builder
	fmt.Fprintf(&digest, "[%d birikmis duyuru — %s]", len(entries), rangeText)
	for index, entry := range entries {
		when := time.Unix(entry.TS, 0).In(istanbul)
		fmt.Fprintf(&digest, "\n%d) (%d %s %s", index+1, when.Day(), turkishMonths[when.Month()], when.Format("15:04"))
		if entry.Kind == "msg" {
			fmt.Fprintf(&digest, ", %s", entry.From)
		}
		fmt.Fprintf(&digest, ") %s", entry.Text)
	}
	if dropped > 0 {
		fmt.Fprintf(&digest, "\n(+%d eski duyuru dusuldu)", dropped)
	}
	return digest.String()
}

func humanTokens(value int) string {
	switch {
	case value >= 1_000_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(value)/1_000_000), ".0") + "M"
	case value >= 1_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(value)/1_000), ".0") + "k"
	default:
		return strconv.Itoa(value)
	}
}

func shortAge(value time.Duration) string {
	if value < 0 {
		value = 0
	}
	switch {
	case value < 90*time.Minute:
		return fmt.Sprintf("%dm", int(value.Minutes()))
	case value < 48*time.Hour:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", value.Hours()), ".0") + "h"
	default:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", value.Hours()/24), ".0") + "d"
	}
}

func (a *app) message(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: bp msg <name> <message...>")
	}
	name, message := args[0], strings.TrimSpace(strings.Join(args[1:], " "))
	if err := rejectFlag("msg", name); err != nil {
		return err
	}
	if message == "" {
		return fmt.Errorf("empty message")
	}
	if strings.Contains(name, "@") {
		target, peer, _, err := fed.ParseAddress(name)
		if err != nil {
			return err
		}
		return a.federatedMessage(target, peer, message)
	}
	sender := a.sender()
	if strings.HasPrefix(message, "/") {
		// A bare slash command executes in the target CLI with no envelope and
		// no visible origin (a prefix would break the command). The only
		// authority for that is the hierarchy: root and ancestors may drive
		// their own agents' CLIs — nobody else, and never sideways. The same
		// gate will guard bp goal (roadmap §7).
		fleet, _, err := a.fleet()
		if err != nil {
			return err
		}
		root := fleet.Root
		if root == "" {
			root = "server-main"
		}
		if sender != root && !fleet.IsDescendant(name, sender) {
			return fmt.Errorf("slash command refused: %s is not above %s in the hierarchy", sender, name)
		}
		if rest, ok := strings.CutPrefix(message, "/goal"); ok {
			// The identity rides inside the payload so the target records who
			// set the goal. A bare /goal queries or clears it UI-side, so it is
			// left alone: an envelope there would become the goal text.
			if payload := strings.TrimSpace(rest); payload != "" && payload != rest {
				message = "/goal [" + sender + "] " + payload
			}
		}
	}
	if !a.hasSession(name) {
		if err := pending.Append(a.config.StateDir, name, pending.Entry{TS: time.Now().Unix(), From: sender, Kind: "msg", Text: message}); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "queued for %s (offline; delivered when it opens)\n", name)
		return nil
	}
	entries, dropped, err := pending.Load(a.config.StateDir, name)
	if err != nil {
		return err
	}
	attachPending := !strings.HasPrefix(message, "/")
	if attachPending {
		message = "[" + sender + "] " + message
		if len(entries) > 0 {
			message = formatDigest(entries, dropped) + "\n\n" + message
		}
	}
	queued, channelID, err := a.deliver(name, sender, message)
	notReady, unverified := errors.Is(err, bptmux.ErrNotReady), errors.Is(err, bptmux.ErrUnverified)
	if err != nil && !notReady && !unverified {
		if errors.Is(err, bptmux.ErrNotAgent) {
			// The pane is a shell, not an agent CLI: surface a clear error
			// rather than dropping the message into a root prompt.
			if cmd, cmdErr := a.tmux.PaneCommand(a.ctx, name); cmdErr == nil && cmd != "" {
				return fmt.Errorf("target %s is not an agent CLI (%s)", name, cmd)
			}
			return fmt.Errorf("target %s is not an agent CLI", name)
		}
		return err
	}
	// The digest travelled inside the message, whether that message reached the
	// pane or the queue, so pending is cleared in every one of those cases —
	// leaving it would repeat the whole digest on the next delivery.
	if attachPending && len(entries) > 0 {
		if err := pending.Clear(a.config.StateDir, name); err != nil {
			return err
		}
	}
	switch {
	case unverified:
		// Not a failure and not a delivery: the keystrokes went in and nothing
		// confirmed them. Never silently "sent" again (2026-08-01 incident).
		fmt.Fprintf(a.out, "gonderildi ama DOGRULANAMADI: %s — pane'de mesaj gorulemedi, tekrar gondermeden once bp peek %s ile bak\n", name, name)
		return errReported
	case notReady:
		fmt.Fprintf(a.out, "GONDERILEMEDI: %s — %s; mesaj kuyruga alindi (channel: %s). Durum: bp qstat %s\n", name, deliveryReason(err, bptmux.ErrNotReady), channelID, channelID)
		return nil
	case !queued:
		fmt.Fprintln(a.out, "sent")
		return nil
	}
	fmt.Fprintf(a.out, "BUSY: queued (channel: %s). Check: bp qstat %s\n", channelID, channelID)
	return nil
}

func (a *app) federatedMessage(target, peer, message string) error {
	if a.config.Fed == nil {
		return fmt.Errorf("federation is not configured on this machine")
	}
	sender := a.sender()
	switch a.config.Fed.Mode {
	case "hub":
		peers, err := fed.LoadPeers(a.config.StateDir)
		if err != nil {
			return err
		}
		id, err := fed.QueueOutbound(a.config.StateDir, peers, peer, target, sender+"@"+a.config.Fed.PeerName, message)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "queued for peer %s (channel: %s)\n", peer, id)
		return nil
	case "client":
		client := fed.NewClient(a.config.Fed.Hub, a.config.Fed.Token)
		id, err := client.Send(a.ctx, peer, target, sender, message)
		if err != nil {
			return err
		}
		_ = fed.Journal(a.config.StateDir, "out", id, sender+"@"+a.config.Fed.PeerName, target+"@"+peer, message)
		fmt.Fprintln(a.out, "sent via hub")
		return nil
	default:
		return fmt.Errorf("unsupported federation mode: %s", a.config.Fed.Mode)
	}
}

func (a *app) deliver(name, sender, message string) (queued bool, channelID string, err error) {
	if a.deliverMessage != nil {
		return a.deliverMessage(name, sender, message)
	}
	if !a.tmux.HasSession(a.ctx, name) {
		return false, "", fmt.Errorf("no open session named %s", name)
	}
	pane, err := a.tmux.CaptureAnsi(a.ctx, name)
	if err != nil {
		return false, "", err
	}
	// reason survives the enqueue below so the caller can name WHY the message
	// had to be queued instead of reporting a plain "busy" queue.
	var reason error
	if !bptmux.Typing(pane) && !bptmux.Busy(pane) {
		sendErr := a.tmux.Send(a.ctx, name, message)
		switch {
		case sendErr == nil:
			return false, "", nil
		case errors.Is(sendErr, bptmux.ErrUnverified):
			// Pasted and submitted, but unconfirmed. Queueing it would risk a
			// second copy, so it is reported and NOT retried.
			return false, "", sendErr
		case errors.Is(sendErr, bptmux.ErrNotReady):
			// Proven non-delivery: queue it exactly like a busy composer, and
			// carry the reason out with the channel id.
			reason = sendErr
		case errors.Is(sendErr, bptmux.ErrTyping):
			// Someone is typing: queue, as before.
		default:
			return false, "", sendErr
		}
	}
	channelID, err = a.queue.Enqueue(name, sender, message)
	if err != nil {
		return true, channelID, err
	}
	return true, channelID, reason
}

// deliveryTally accumulates per-target deliver() outcomes for the batch commands
// (announce, compact). A non-agent target (ErrNotAgent) is recorded as a SKIP
// with no hard error, so a single session that dropped to a shell neither fails
// the whole batch nor has the message injected into its shell prompt. An
// UNVERIFIED delivery gets its own bucket: it must never be tallied as sent, and
// it is not an error either — nobody may retry it.
type deliveryTally struct {
	sent       int
	channels   []string
	skipped    int
	unverified []string
	errs       []error
}

// record buckets one deliver() result and reports whether it counted as a real
// delivery (queued or sent), so callers can update per-target bookkeeping only
// on success. A provable non-delivery (ErrNotReady) arrives already queued, so
// it counts like any other queued message — the message is not lost and the
// queue will retry it.
func (t *deliveryTally) record(target string, queued bool, channelID string, err error) bool {
	switch {
	case errors.Is(err, bptmux.ErrUnverified):
		t.unverified = append(t.unverified, target)
		return false
	case errors.Is(err, bptmux.ErrNotAgent):
		t.skipped++
		return false
	case errors.Is(err, bptmux.ErrNotReady) && queued:
		t.channels = append(t.channels, channelID)
		return true
	case err != nil:
		t.errs = append(t.errs, fmt.Errorf("%s: %w", target, err))
		return false
	}
	if queued {
		t.channels = append(t.channels, channelID)
	} else {
		t.sent++
	}
	return true
}

// report prints the unverified bucket, if any. It is a separate line so the
// existing summaries stay byte-identical whenever every delivery was clean.
func (t *deliveryTally) report(out *os.File) {
	if len(t.unverified) > 0 {
		fmt.Fprintf(out, "dogrulanamadi: %d (%s) — bp peek ile bak\n", len(t.unverified), strings.Join(t.unverified, ", "))
	}
}

func (a *app) announce(args []string) error {
	dryRun := false
	words := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--dry-run" {
			dryRun = true
		} else {
			words = append(words, arg)
		}
	}
	if len(words) == 0 {
		return fmt.Errorf("usage: bp announce <message...>")
	}
	sender := a.sender()
	fleet, states, err := a.fleet()
	if err != nil {
		return err
	}
	if _, ok := fleet.Agents[sender]; !ok && sender != "server-main" {
		return fmt.Errorf("sender %s is not in the agentbook hierarchy", sender)
	}
	targets := announcementCandidates(fleet, sender)
	folders := make(map[string]string, len(targets))
	for _, target := range targets {
		folders[target] = fleet.Agents[target].Folder
	}
	cacheStates := a.readCache(folders)
	messageText := strings.Join(words, " ")
	message := fmt.Sprintf("[ANNOUNCE %s] %s", sender, messageText)
	var tally deliveryTally
	deferred, coldCost, warm := 0, 0, 0
	for _, target := range targets {
		cacheState := cacheStates[target]
		live := states[target].Alive
		if !live || !cacheState.Known || cacheState.Age >= time.Hour {
			deferred++
			coldCost += cacheState.CtxTokens
			if !dryRun {
				if appendErr := pending.Append(a.config.StateDir, target, pending.Entry{TS: time.Now().Unix(), From: sender, Kind: "announce", Text: messageText}); appendErr != nil {
					tally.errs = append(tally.errs, fmt.Errorf("%s: %w", target, appendErr))
				}
			}
			continue
		}
		warm++
		if dryRun {
			continue
		}
		queued, channelID, deliveryErr := a.deliver(target, sender, message)
		tally.record(target, queued, channelID, deliveryErr)
	}
	if dryRun {
		fmt.Fprintf(a.out, "would send: %d, would defer: %d\n", warm, deferred)
		fmt.Fprintf(a.out, "cold reread cost: ~%s tokens (%d targets: %d warm, %d cold)\n", humanTokens(coldCost), len(targets), warm, deferred)
		return nil
	}
	fmt.Fprintf(a.out, "sent: %d, deferred: %d\n", tally.sent+len(tally.channels), deferred)
	tally.report(a.out)
	return errors.Join(tally.errs...)
}

func announcementCandidates(fleet book.Fleet, sender string) []string {
	targets := make([]string, 0)
	for _, name := range fleet.Order {
		if name == sender || strings.HasPrefix(name, "lab-") {
			continue
		}
		if sender == "server-main" || sender == fleet.Root || fleet.IsDescendant(name, sender) {
			targets = append(targets, name)
		}
	}
	return targets
}

func announcementTargets(fleet book.Fleet, states map[string]book.State, sender string) []string {
	targets := make([]string, 0)
	for _, name := range fleet.Order {
		state, open := states[name]
		if name == sender || strings.HasPrefix(name, "lab-") || !open || !state.Alive {
			continue
		}
		if sender == "server-main" || sender == fleet.Root || fleet.IsDescendant(name, sender) {
			targets = append(targets, name)
		}
	}
	return targets
}

type compactOptions struct {
	minAge  time.Duration
	exclude []string
	// apply sends. Listing is the default so a mistyped compact can never
	// interrupt an agent; --dry-run is kept as a no-op synonym of the default.
	apply bool
	// all selects every descendant of the sender instead of the policy set.
	// The policy selection is the default; --policy is a no-op synonym of it.
	all    bool
	idle   time.Duration
	minCtx int
}

func defaultCompactOptions() compactOptions {
	return compactOptions{minAge: 30 * time.Minute, idle: 24 * time.Hour, minCtx: 200_000}
}

type compactSkip struct {
	Name string
	Age  time.Duration
}

type compactPlan struct {
	Send            []string
	SkippedRecent   []compactSkip
	Excluded        []string
	UnknownExcludes []string
}

// compactValue reads the value of a "--flag value" or "--flag=value" pair and
// reports the index of the last argument it consumed.
func compactValue(args []string, index int, name, want string) (string, int, error) {
	if args[index] == name {
		if index+1 >= len(args) {
			return "", index, fmt.Errorf("%s requires %s", name, want)
		}
		return args[index+1], index + 1, nil
	}
	return strings.TrimPrefix(args[index], name+"="), index, nil
}

func parseCompactArgs(args []string) (compactOptions, error) {
	opts := defaultCompactOptions()
	seen := map[string]bool{}
	// The two back-compat no-ops name the defaults that --apply and --all
	// override. Asking for both halves of such a pair is self-contradictory,
	// and letting the explicit flag win silently resolves it the dangerous
	// way: --apply --dry-run would send, --all --policy would sweep wider
	// than asked. Refuse instead, here in parsing, before anything is read
	// or delivered.
	dryRun, policy := false, false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		name, _, hasValue := strings.Cut(arg, "=")
		once := func() error {
			if seen[name] {
				return fmt.Errorf("%s may only be specified once", name)
			}
			seen[name] = true
			return nil
		}
		if hasValue {
			switch name {
			case "--apply", "--all", "--dry-run", "--policy":
				return opts, fmt.Errorf("%s takes no value", name)
			}
		}
		switch name {
		case "--apply":
			opts.apply = true
		case "--all":
			opts.all = true
		case "--dry-run":
			// Back-compat no-op: listing is already the default.
			dryRun = true
		case "--policy":
			// Back-compat no-op: the policy selection is already the default.
			policy = true
		case "--min-age":
			value, next, err := compactValue(args, index, name, "a value in minutes")
			if err != nil {
				return opts, err
			}
			index = next
			if err := once(); err != nil {
				return opts, err
			}
			minutes, err := strconv.Atoi(value)
			if err != nil || minutes < 0 {
				return opts, fmt.Errorf("invalid --min-age minutes: %s", value)
			}
			opts.minAge = time.Duration(minutes) * time.Minute
		case "--idle-hours":
			value, next, err := compactValue(args, index, name, "a value in hours")
			if err != nil {
				return opts, err
			}
			index = next
			if err := once(); err != nil {
				return opts, err
			}
			hours, err := strconv.Atoi(value)
			if err != nil || hours < 0 {
				return opts, fmt.Errorf("invalid --idle-hours: %s", value)
			}
			opts.idle = time.Duration(hours) * time.Hour
		case "--min-ctx":
			value, next, err := compactValue(args, index, name, "a token count")
			if err != nil {
				return opts, err
			}
			index = next
			if err := once(); err != nil {
				return opts, err
			}
			tokens, err := strconv.Atoi(value)
			if err != nil || tokens < 0 {
				return opts, fmt.Errorf("invalid --min-ctx tokens: %s", value)
			}
			opts.minCtx = tokens
		case "--exclude":
			value, next, err := compactValue(args, index, name, "a comma-separated list")
			if err != nil {
				return opts, err
			}
			index = next
			for _, part := range strings.Split(value, ",") {
				if part = strings.TrimSpace(part); part != "" {
					opts.exclude = append(opts.exclude, part)
				}
			}
		default:
			return opts, fmt.Errorf("unknown compact option: %s", arg)
		}
	}
	if opts.apply && dryRun {
		return opts, fmt.Errorf("--apply and --dry-run contradict each other: bp compact alone already lists without sending")
	}
	if opts.all && policy {
		return opts, fmt.Errorf("--all and --policy contradict each other: --all sweeps the whole subtree, --policy is the default selector")
	}
	return opts, nil
}

// selectCompactTargets computes the compact plan from the announce-style target
// set. server-main and the sender are hard-excluded regardless of flags; user
// --exclude names and min-age recency further trim the set.
func selectCompactTargets(fleet book.Fleet, states map[string]book.State, sender string, exclude []string, lastCompact map[string]time.Time, minAge time.Duration, now time.Time) compactPlan {
	excludeSet := map[string]bool{}
	for _, name := range exclude {
		if name = strings.TrimSpace(name); name != "" {
			excludeSet[name] = true
		}
	}
	base := announcementTargets(fleet, states, sender)
	baseSet := map[string]bool{}
	for _, name := range base {
		baseSet[name] = true
	}
	plan := compactPlan{}
	for _, name := range base {
		if name == "server-main" || name == sender {
			continue // hard safety exclusions, always applied
		}
		if excludeSet[name] {
			plan.Excluded = append(plan.Excluded, name)
			continue
		}
		if last, ok := lastCompact[name]; ok {
			if age := now.Sub(last); age < minAge {
				plan.SkippedRecent = append(plan.SkippedRecent, compactSkip{Name: name, Age: age})
				continue
			}
		}
		plan.Send = append(plan.Send, name)
	}
	for name := range excludeSet {
		if !baseSet[name] {
			plan.UnknownExcludes = append(plan.UnknownExcludes, name)
		}
	}
	sort.Strings(plan.UnknownExcludes)
	return plan
}

// Decision reasons, in the Turkish-without-diacritics style of the rest of the
// compact/announce output.
const (
	compactPending  = "gonderilecek"
	compactSent     = "gonderildi"
	compactBusy     = "MESGUL, atlandi"
	compactClosed   = "kapali"
	compactSmallCtx = "context kucuk"
	compactExcluded = "haric tutuldu"
	compactNoCache  = "transcript okunamadi"
	compactNotAgent = "agent CLI degil, atlandi"
	// Enter was pressed but nothing confirmed the composer cleared. Never
	// counted as "gonderildi": the operator has to look at the pane.
	compactUnverified = "gonderildi ama DOGRULANAMADI (bp peek)"
)

// compactDecision is one row of the decision table: what bp measured about an
// agent and what it did — or would do — about it.
type compactDecision struct {
	Name string
	// Age of the last real human turn; negative when unknown.
	Age time.Duration
	// Context tokens; negative when unknown.
	Tokens int
	// Send marks a row that passed every filter, i.e. a target.
	Send   bool
	Reason string
}

func decisionRow(name string, state bpcache.State) compactDecision {
	row := compactDecision{Name: name, Age: -1, Tokens: -1}
	if state.LastHumanAge >= 0 {
		row.Age = state.LastHumanAge
	}
	if state.Known {
		row.Tokens = state.CtxTokens
	}
	return row
}

// policyDecisions is the standing compaction policy: a live claude agent whose
// last human turn is older than --idle-hours and whose context is above
// --min-ctx gets /compact. server-main is never a target, and an agent compacted
// inside the --min-age window is left alone so a repeated run cannot double-send
// while the transcript still reports the pre-compact token count.
func policyDecisions(fleet book.Fleet, states map[string]book.State, cacheStates map[string]bpcache.State, commands map[string]string, lastCompact map[string]time.Time, opts compactOptions, now time.Time) []compactDecision {
	rows := make([]compactDecision, 0, len(fleet.Order))
	for _, name := range fleet.Order {
		if name == "server-main" {
			continue // hard safety exclusion, always applied
		}
		state := cacheStates[name]
		row := decisionRow(name, state)
		switch {
		case !states[name].Alive:
			row.Reason = compactClosed
		case states[name].Busy:
			row.Reason = compactBusy
		case commands[name] != "claude":
			command := commands[name]
			if command == "" {
				command = "?"
			}
			row.Reason = fmt.Sprintf("claude degil (%s)", command)
		case !state.Known:
			row.Reason = compactNoCache
		case state.LastHumanAge <= opts.idle:
			row.Reason = fmt.Sprintf("taze konusma (<%dsa)", int(opts.idle.Hours()))
		case state.CtxTokens <= opts.minCtx:
			row.Reason = compactSmallCtx
		default:
			if last, ok := lastCompact[name]; ok && now.Sub(last) < opts.minAge {
				row.Reason = fmt.Sprintf("yakinda compact edildi (%s once)", formatAge(now.Sub(last)))
				break
			}
			row.Send = true
			row.Reason = compactPending
		}
		rows = append(rows, row)
	}
	return rows
}

// allDecisions renders the --all sweep (every descendant of the sender) as the
// same table. The selection itself stays in selectCompactTargets so --all keeps
// behaving exactly as it did before listing became the default.
func allDecisions(fleet book.Fleet, states map[string]book.State, cacheStates map[string]bpcache.State, plan compactPlan, sender string) []compactDecision {
	send := map[string]bool{}
	for _, name := range plan.Send {
		send[name] = true
	}
	recent := map[string]time.Duration{}
	for _, skip := range plan.SkippedRecent {
		recent[skip.Name] = skip.Age
	}
	excluded := map[string]bool{}
	for _, name := range plan.Excluded {
		excluded[name] = true
	}
	candidates := announcementCandidates(fleet, sender)
	rows := make([]compactDecision, 0, len(candidates))
	for _, name := range candidates {
		if name == "server-main" {
			continue // hard safety exclusion, always applied
		}
		row := decisionRow(name, cacheStates[name])
		switch {
		case send[name]:
			row.Send, row.Reason = true, compactPending
		case excluded[name]:
			row.Reason = compactExcluded
		default:
			if age, ok := recent[name]; ok {
				row.Reason = fmt.Sprintf("yakinda compact edildi (%s once)", formatAge(age))
				break
			}
			if !states[name].Alive {
				row.Reason = compactClosed
				break
			}
			row.Reason = "atlandi"
		}
		rows = append(rows, row)
	}
	return rows
}

func formatAge(d time.Duration) string {
	minutes := int(d.Minutes())
	if minutes < 0 {
		minutes = 0
	}
	return fmt.Sprintf("%dm", minutes)
}

func loadCompactState(path string) map[string]time.Time {
	result := map[string]time.Time{}
	data, err := os.ReadFile(path)
	if err != nil {
		return result
	}
	raw := map[string]string{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return map[string]time.Time{}
	}
	for name, value := range raw {
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			result[name] = parsed.UTC()
		}
	}
	return result
}

func saveCompactState(path string, state map[string]time.Time) error {
	raw := make(map[string]string, len(state))
	for name, when := range state {
		raw[name] = when.UTC().Format(time.RFC3339)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".compact-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	err = encoder.Encode(raw)
	if syncErr := tmp.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

// paneBusy reports whether an agent is mid-turn or has someone typing into its
// composer. A capture failure counts as busy: a pane bp cannot see is never
// interrupted.
func (a *app) paneBusy(name string) bool {
	pane, err := a.capture(name)
	if err != nil {
		return true
	}
	return bptmux.Typing(pane) || bptmux.Busy(pane)
}

func (a *app) capture(name string) (string, error) {
	if a.capturePane != nil {
		return a.capturePane(name)
	}
	return a.tmux.CaptureAnsi(a.ctx, name)
}

// clearComposer settles a pane's composer before bp types a SLASH COMMAND into
// it. It is deliberately not part of deliver/Send, which also carry ordinary
// messages: clearing there would eventually wipe a half-typed line out of
// someone's composer. Only the two commands bp types itself (/rename, /compact)
// go through it, and for both the alternative is worse than a lost keystroke —
// a /compact that never lands leaves the fleet's biggest transcript unpruned.
func (a *app) clearComposer(name string) error {
	if a.clearPane != nil {
		return a.clearPane(name)
	}
	return a.tmux.ClearComposer(a.ctx, name)
}

func (a *app) paneCommands() (map[string]string, error) {
	if a.loadCommands != nil {
		return a.loadCommands()
	}
	return a.tmux.Commands(a.ctx)
}

func (a *app) printCompactTable(rows []compactDecision) {
	fmt.Fprintf(a.out, "%-24s %-10s %-10s %s\n", "AGENT", "KONUSMA", "CONTEXT", "KARAR")
	for _, row := range rows {
		age, tokens := "-", "-"
		if row.Age >= 0 {
			age = shortAge(row.Age)
		}
		if row.Tokens >= 0 {
			tokens = humanTokens(row.Tokens)
		}
		fmt.Fprintf(a.out, "%-24s %-10s %-10s %s\n", row.Name, age, tokens, row.Reason)
	}
}

func (a *app) compact(args []string) error {
	opts, err := parseCompactArgs(args)
	if err != nil {
		return err
	}
	sender := a.sender()
	fleet, states, err := a.fleet()
	if err != nil {
		return err
	}
	if _, ok := fleet.Agents[sender]; !ok && sender != "server-main" {
		return fmt.Errorf("sender %s is not in the agentbook hierarchy", sender)
	}
	compactStatePath := filepath.Join(a.config.StateDir, "compact.json")
	lastCompact := loadCompactState(compactStatePath)
	folders := make(map[string]string, len(fleet.Agents))
	for name, agent := range fleet.Agents {
		folders[name] = agent.Folder
	}
	cacheStates := a.readCache(folders)

	var rows []compactDecision
	var unknownExcludes []string
	if opts.all {
		plan := selectCompactTargets(fleet, states, sender, opts.exclude, lastCompact, opts.minAge, time.Now())
		rows = allDecisions(fleet, states, cacheStates, plan, sender)
		unknownExcludes = plan.UnknownExcludes
	} else {
		commands, err := a.paneCommands()
		if err != nil {
			return err
		}
		rows = policyDecisions(fleet, states, cacheStates, commands, lastCompact, opts, time.Now())
	}
	// The pane is the last word on "is this agent working": book state is a
	// snapshot taken before the fleet was walked, and someone may be typing.
	for index := range rows {
		if rows[index].Send && a.paneBusy(rows[index].Name) {
			rows[index].Send, rows[index].Reason = false, compactBusy
		}
	}

	if !opts.apply {
		pending := 0
		for _, row := range rows {
			if row.Send {
				pending++
			}
		}
		a.printCompactTable(rows)
		if len(unknownExcludes) > 0 {
			fmt.Fprintf(a.out, "bilinmeyen exclude: %s\n", strings.Join(unknownExcludes, ", "))
		}
		fmt.Fprintf(a.out, "gonderilecek: %d, atlanan: %d (gondermek icin: bp compact --apply)\n", pending, len(rows)-pending)
		return nil
	}

	var tally deliveryTally
	now := time.Now().UTC()
	updated := false
	for index := range rows {
		if !rows[index].Send {
			continue
		}
		target := rows[index].Name
		// Re-checked immediately before the send, not only during selection:
		// an agent that started working in between must not be interrupted,
		// and a busy target is skipped rather than queued — a /compact that
		// lands after the next turn compacts the wrong conversation.
		if a.paneBusy(target) {
			rows[index].Send, rows[index].Reason = false, compactBusy
			continue
		}
		// Same clearing step as bp rename, for the same reason: /compact is a
		// slash command bp types itself, and a composer left in a state that only
		// LOOKS empty makes the send bounce off with "composer is not empty".
		// ErrBusy/ErrTyping are the pane saying it is in use — skip it exactly
		// like the check above, never queue (a /compact delivered after the next
		// turn compacts the wrong conversation).
		if clearErr := a.clearComposer(target); clearErr != nil {
			rows[index].Send = false
			switch {
			case errors.Is(clearErr, bptmux.ErrBusy), errors.Is(clearErr, bptmux.ErrTyping):
				rows[index].Reason = compactBusy
			case errors.Is(clearErr, bptmux.ErrNotAgent):
				rows[index].Reason = compactNotAgent
			default:
				rows[index].Reason = fmt.Sprintf("gonderilemedi: %v", clearErr)
				tally.errs = append(tally.errs, fmt.Errorf("%s: %w", target, clearErr))
			}
			continue
		}
		queued, channelID, deliveryErr := a.deliver(target, sender, "/compact")
		counted := tally.record(target, queued, channelID, deliveryErr)
		if errors.Is(deliveryErr, bptmux.ErrUnverified) {
			// The keystrokes went in unconfirmed. Re-sending could compact the
			// same conversation twice, so the attempt is recorded like a send —
			// but the row says plainly that nobody verified it.
			lastCompact[target] = now
			updated = true
			rows[index].Send = false
			rows[index].Reason = compactUnverified
			continue
		}
		if counted {
			lastCompact[target] = now
			updated = true
			rows[index].Reason = compactSent
			if queued {
				rows[index].Reason = fmt.Sprintf("%s (kuyruk: %s)", compactSent, channelID)
			}
			if errors.Is(deliveryErr, bptmux.ErrNotReady) {
				// Queued because the pane provably could not take it.
				rows[index].Reason = fmt.Sprintf("GONDERILEMEDI: %s, kuyrukta (%s)", deliveryReason(deliveryErr, bptmux.ErrNotReady), channelID)
			}
			continue
		}
		rows[index].Send = false
		if deliveryErr != nil && errors.Is(deliveryErr, bptmux.ErrNotAgent) {
			rows[index].Reason = compactNotAgent
			continue
		}
		rows[index].Reason = fmt.Sprintf("gonderilemedi: %v", deliveryErr)
	}
	if updated {
		if err := saveCompactState(compactStatePath, lastCompact); err != nil {
			tally.errs = append(tally.errs, fmt.Errorf("save compact state: %w", err))
		}
	}

	sent := tally.sent + len(tally.channels)
	a.printCompactTable(rows)
	if len(unknownExcludes) > 0 {
		fmt.Fprintf(a.out, "bilinmeyen exclude: %s\n", strings.Join(unknownExcludes, ", "))
	}
	fmt.Fprintf(a.out, "gonderildi: %d, atlanan: %d\n", sent, len(rows)-sent)
	tally.report(a.out)
	return errors.Join(tally.errs...)
}

func (a *app) queueList(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: bp q")
	}
	rows, err := a.queue.List()
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(a.out, "(queue empty)")
	} else {
		for _, row := range rows {
			text := []rune(row.Msg)
			if len(text) > 60 {
				text = text[:60]
			}
			fmt.Fprintf(a.out, "%s %s -> %s : %s\n", row.ID, row.From, row.To, string(text))
		}
	}
	agents, items, err := pending.Counts(a.config.StateDir)
	if err != nil {
		return err
	}
	if items > 0 {
		fmt.Fprintf(a.out, "pending: %d agents, %d items\n", agents, items)
	}
	return nil
}

func (a *app) queueStatus(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: bp qstat <channel-id>")
	}
	status, err := a.queue.Status(args[0])
	if err == nil {
		fmt.Fprintln(a.out, status)
	}
	return err
}

func (a *app) peek(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("usage: bp peek <name> [n]")
	}
	if err := rejectFlag("peek", args[0]); err != nil {
		return err
	}
	count := 8
	var err error
	if len(args) == 2 {
		count, err = strconv.Atoi(args[1])
		if err != nil || count < 1 {
			return fmt.Errorf("invalid line count: %s", args[1])
		}
	}
	pane, err := a.tmux.Capture(a.ctx, args[0])
	if err != nil {
		return err
	}
	var lines []string
	for _, line := range strings.Split(pane, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if count < len(lines) {
		lines = lines[len(lines)-count:]
	}
	for _, line := range lines {
		fmt.Fprintln(a.out, line)
	}
	return nil
}

func (a *app) whatsapp(args []string) error {
	if a.config.WAOutbox == "" {
		return fmt.Errorf("wa is not configured on this machine")
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: bp wa send|read|chats ...")
	}
	switch args[0] {
	case "send":
		to, reply, index := "", "", 1
		for index < len(args) {
			switch args[index] {
			case "--to":
				if index+1 >= len(args) {
					return fmt.Errorf("--to requires a target")
				}
				to = args[index+1]
				index += 2
			case "--reply":
				if index+1 >= len(args) {
					return fmt.Errorf("--reply requires a msgId")
				}
				reply = args[index+1]
				index += 2
			default:
				goto message
			}
		}
	message:
		text := strings.Join(args[index:], " ")
		if text == "" {
			return fmt.Errorf("usage: bp wa send [--to <target>] [--reply <msgId>] <message...>")
		}
		agent := wa.Agent(a.ctx, a.tmux)
		waErr := wa.Send(a.config.WAOutbox, agent, to, reply, text)
		if err := ntfy.Send(a.ctx, a.config.Ntfy, wa.Format(agent, text)); err != nil {
			fmt.Fprintf(a.err, "WARNING: ntfy notification failed: %v\n", err)
		}
		if waErr != nil {
			return waErr
		}
		destination := to
		if destination == "" {
			destination = "<default channel>"
		}
		suffix := ""
		if reply != "" {
			suffix = " (reply: " + reply + ")"
		}
		fmt.Fprintf(a.out, "queued -> %s%s\n", destination, suffix)
		return nil
	case "read":
		if len(args) < 2 || len(args) > 3 {
			return fmt.Errorf("usage: bp wa read <group/person> [n]")
		}
		count := 15
		var err error
		if len(args) == 3 {
			count, err = strconv.Atoi(args[2])
			if err != nil || count < 1 {
				return fmt.Errorf("invalid message count: %s", args[2])
			}
		}
		lines, err := wa.Read(a.config.WAStore, args[1], count)
		if err != nil {
			return err
		}
		if len(lines) == 0 {
			fmt.Fprintln(a.out, "(no records)")
		} else {
			for _, line := range lines {
				fmt.Fprintln(a.out, line)
			}
		}
		return nil
	case "chats":
		if len(args) != 1 {
			return fmt.Errorf("usage: bp wa chats")
		}
		lines, err := wa.Chats(a.config.WAStore)
		if err != nil {
			return err
		}
		if len(lines) == 0 {
			fmt.Fprintln(a.out, "(no records)")
		} else {
			for _, line := range lines {
				fmt.Fprintln(a.out, line)
			}
		}
		return nil
	default:
		return fmt.Errorf("usage: bp wa send|read|chats ...")
	}
}

func (a *app) usage() error {
	if a.config.UsageHistory == "" {
		return fmt.Errorf("usage is not configured on this machine")
	}
	sample, err := usagecli.Latest(a.config.UsageHistory)
	if err != nil {
		return err
	}
	for _, line := range usagecli.Lines(sample) {
		fmt.Fprintln(a.out, line)
	}
	return nil
}

func (a *app) monitor(args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: bp monitor [usage|cost|agents|projects|services|radar]")
	}
	view := "overview"
	if len(args) == 1 {
		view = args[0]
	}
	valid := map[string]bool{"overview": true, "usage": true, "cost": true, "agents": true, "projects": true, "services": true, "radar": true}
	if !valid[view] {
		return fmt.Errorf("usage: bp monitor [usage|cost|agents|projects|services|radar]")
	}
	configPath, err := connectConfigPath()
	if err != nil {
		return err
	}
	dashboardURL, err := loadDashboardURL(configPath)
	if err != nil {
		return err
	}
	options := monitorcli.SourceOptions{BaseURL: dashboardURL}
	doc, source, err := monitorcli.Load(a.ctx, options)
	if err != nil {
		return err
	}
	renderOptions := monitorcli.RenderOptions{Now: time.Now()}
	var trailingNote string
	if view == "services" {
		jobs, jobsErr := monitorcli.LoadJobs(filepath.Join(a.config.StateDir, "jobs.json"))
		if jobsErr == nil {
			renderOptions.Jobs = jobs
		} else if !os.IsNotExist(jobsErr) {
			renderOptions.JobsNote = "daemon jobs state unavailable: " + jobsErr.Error()
		} else {
			renderOptions.JobsNote = "daemon jobs state not present"
		}
	}
	if view == "radar" {
		radar, radarSource, radarErr := monitorcli.LoadRadar(a.ctx, doc, options)
		if radarErr == nil {
			renderOptions.Radar = radar
			source = radarSource
		} else {
			trailingNote = "radar feed unavailable: " + radarErr.Error()
		}
	}
	if err := monitorcli.Render(a.out, doc, view, renderOptions); err != nil {
		return err
	}
	if trailingNote != "" {
		fmt.Fprintln(a.out, "Note:", trailingNote)
	}
	fmt.Fprintln(a.out, "Source:", source)
	return nil
}

func (a *app) policy(args []string) error {
	if !(len(args) == 1 && args[0] == "status") && !(len(args) == 2 && args[0] == "override") {
		return fmt.Errorf("usage: bp policy status|override <hours>")
	}
	if a.config.UsageBin == "" {
		return fmt.Errorf("usage policy is not configured on this machine")
	}
	cmd := exec.CommandContext(a.ctx, filepath.Join(a.config.UsageBin, "usage-policy"), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr, cmd.Stdin = &stdout, &stderr, os.Stdin
	err := cmd.Run()
	fmt.Fprint(a.out, translatePolicyOutput(stdout.String()))
	fmt.Fprint(a.err, translatePolicyOutput(stderr.String()))
	return err
}

var policyResetHours = regexp.MustCompile(`reset ([^,]+)s,`)

func translatePolicyOutput(output string) string {
	output = strings.ReplaceAll(output, "kullanim: usage-policy override <saat>  (0 = kaldir)", "usage: bp policy override <hours> (0 = clear)")
	output = strings.ReplaceAll(output, "kullanim: usage-policy [status | override <saat>]", "usage: bp policy status | override <hours>")
	output = strings.ReplaceAll(output, "override kaldirildi", "override cleared")
	output = strings.ReplaceAll(output, "usage: 7g %", "usage: 7d %")
	output = strings.ReplaceAll(output, ", 5s %", ", 5h %")
	return policyResetHours.ReplaceAllString(output, "reset ${1}h,")
}

func (a *app) service() error {
	jobs, err := daemon.LoadState(filepath.Join(a.config.StateDir, "jobs.json"))
	if os.IsNotExist(err) {
		fmt.Fprintln(a.out, "(no daemon state)")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%-18s %-9s %-25s %-25s %s\n", "JOB", "STATUS", "LAST", "NEXT", "ERROR")
	for _, name := range daemon.StateNames(jobs) {
		job := jobs[name]
		fmt.Fprintf(a.out, "%-18s %-9s %-25s %-25s %s\n", name, job.Status, job.LastRun, job.NextRun, job.Error)
	}
	return nil
}

func (a *app) federation(args []string) error {
	if len(args) == 0 || len(args) > 2 || (len(args) == 2 && args[0] != "log") {
		return fmt.Errorf("usage: bp fed status|ping|token|log [n]")
	}
	switch args[0] {
	case "status":
		return a.federationStatus()
	case "ping":
		return a.federationPing()
	case "log":
		return a.federationLog(args[1:])
	case "token":
		if a.config.Fed == nil || a.config.Fed.Mode != "hub" {
			return fmt.Errorf("bp fed token is only available in hub mode")
		}
		token, err := fed.GenerateToken()
		if err != nil {
			return err
		}
		fmt.Fprintln(a.out, token)
		return nil
	default:
		return fmt.Errorf("usage: bp fed status|ping|token|log [n]")
	}
}

// federationLog prints the message journal: every federation message that
// crossed this machine, content included, for after-the-fact inspection.
// Both fleets run bp, so each side can read its own traffic the same way.
func (a *app) federationLog(args []string) error {
	limit := 20
	if len(args) == 1 {
		parsed, err := strconv.Atoi(args[0])
		if err != nil || parsed < 1 {
			return fmt.Errorf("usage: bp fed log [n]")
		}
		limit = parsed
	}
	entries, err := fed.ReadJournal(a.config.StateDir, limit)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Fprintln(a.out, "no federation messages recorded")
		return nil
	}
	for _, entry := range entries {
		arrow := "->"
		if entry.Dir == "in" {
			arrow = "<-"
		}
		ts := entry.TS
		if parsed, err := time.Parse(time.RFC3339Nano, entry.TS); err == nil {
			ts = parsed.In(istanbul).Format("02 Jan 15:04")
		}
		fmt.Fprintf(a.out, "%s %s %s %s %s: %s\n", ts, entry.Dir, entry.From, arrow, entry.To, entry.Msg)
	}
	fmt.Fprintf(a.out, "\nfull journal: %s\n", fed.JournalPath(a.config.StateDir))
	return nil
}

func (a *app) federationStatus() error {
	if a.config.Fed == nil {
		fmt.Fprintln(a.out, "mode: disabled")
		return nil
	}
	fmt.Fprintf(a.out, "mode: %s\n", a.config.Fed.Mode)
	fmt.Fprintf(a.out, "peer name: %s\n", a.config.Fed.PeerName)
	switch a.config.Fed.Mode {
	case "hub":
		fmt.Fprintf(a.out, "listen: %s\n", a.config.Fed.Listen)
		peers, err := fed.LoadPeers(a.config.StateDir)
		if err != nil {
			return err
		}
		rates, err := fed.RecentRateCounts(a.config.StateDir, time.Now())
		if err != nil {
			return err
		}
		outbox := fed.NewOutbox(a.config.StateDir)
		names := fed.PeerNames(peers)
		if len(names) == 0 {
			fmt.Fprintln(a.out, "peers: (none)")
			return nil
		}
		fmt.Fprintln(a.out, "peers:")
		for _, name := range names {
			depth, err := outbox.Depth(name)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "  %s: outbox=%d rate=%d/%d (last hour)\n", name, depth, rates[name], fed.DefaultRate)
		}
	case "client":
		fmt.Fprintf(a.out, "hub: %s\n", a.config.Fed.Hub)
		data, err := os.ReadFile(fed.LastPollPath(a.config.StateDir))
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(a.out, "last poll: never")
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "last poll: %s\n", strings.TrimSpace(string(data)))
	}
	return nil
}

func (a *app) federationPing() error {
	if a.config.Fed == nil {
		return fmt.Errorf("federation is not configured on this machine")
	}
	started := time.Now()
	if a.config.Fed.Mode == "hub" {
		connection, err := net.DialTimeout("tcp", a.config.Fed.Listen, 3*time.Second)
		if err != nil {
			return fmt.Errorf("hub listener is down: %w", err)
		}
		_ = connection.Close()
		fmt.Fprintf(a.out, "hub listener is up (%s)\n", time.Since(started).Round(time.Millisecond))
		return nil
	}
	client := fed.NewClient(a.config.Fed.Hub, a.config.Fed.Token)
	peer, latency, err := client.Ping(a.ctx)
	if err != nil {
		return fmt.Errorf("hub ping failed: %w", err)
	}
	fmt.Fprintf(a.out, "hub %s is reachable (%s)\n", peer, latency.Round(time.Millisecond))
	return nil
}

func (a *app) daemon(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: bp daemon")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	service := daemon.New(log.New(a.err, "blueprint: ", log.LstdFlags), a.config)
	service.Run(ctx)
	return nil
}

var remoteURLPattern = regexp.MustCompile(`https://claude\.ai/code/\S+`)

// remote sends /remote-control to Claude sessions so each one gets (or re-prints)
// its claude.ai/code URL. Idempotent: an already-connected session just reports
// "is active" with the same URL. Codex sessions are skipped (no such command).
func (a *app) remote(args []string) error {
	sender := a.sender()
	var targets []string
	if len(args) > 0 {
		targets = args
	} else {
		fleet, states, err := a.fleet()
		if err != nil {
			return err
		}
		for name := range fleet.Agents {
			if state, ok := states[name]; ok && state.Alive {
				targets = append(targets, name)
			}
		}
		sort.Strings(targets)
	}
	commands, err := a.tmux.Commands(a.ctx)
	if err != nil {
		return err
	}
	type pending struct{ name string }
	var sent []pending
	queuedCount, skipped, unverified := 0, 0, 0
	for _, name := range targets {
		if !a.tmux.HasSession(a.ctx, name) {
			fmt.Fprintf(a.out, "  skip   %-28s oturum yok\n", name)
			skipped++
			continue
		}
		// Sadece claude panelerine gonder: codex bu komutu bilmez, shell'e (zsh/bash)
		// yazmak root prompt'una metin dusurur.
		if cmd := commands[name]; cmd != "claude" {
			fmt.Fprintf(a.out, "  skip   %-28s claude oturumu degil (%s)\n", name, cmd)
			skipped++
			continue
		}
		// Onceki turdan asili kalmis RC menusu pane'i "mesgul" gosterir ve
		// gonderimi kuyruga dusurur — once kapat.
		if pane, err := a.tmux.Capture(a.ctx, name); err == nil && bptmux.RemoteControlMenu(pane) {
			_ = a.tmux.PressEnter(a.ctx, name)
			time.Sleep(time.Second)
		}
		queued, channelID, err := a.deliver(name, sender, "/remote-control")
		if errors.Is(err, bptmux.ErrUnverified) {
			// Keystrokes went in, nothing confirmed them: not a send, not a
			// failure. It is never counted as gonderildi.
			fmt.Fprintf(a.out, "  ??     %-28s gonderildi ama DOGRULANAMADI (bp peek %s)\n", name, name)
			unverified++
			continue
		}
		if errors.Is(err, bptmux.ErrNotReady) && queued {
			fmt.Fprintf(a.out, "  kuyruk %-28s GONDERILEMEDI: %s (%s)\n", name, deliveryReason(err, bptmux.ErrNotReady), channelID)
			queuedCount++
			continue
		}
		if err != nil {
			fmt.Fprintf(a.out, "  hata   %-28s %v\n", name, err)
			skipped++
			continue
		}
		if queued {
			fmt.Fprintf(a.out, "  kuyruk %-28s mesgul, bosalinca gider (%s)\n", name, channelID)
			queuedCount++
			continue
		}
		sent = append(sent, pending{name})
	}
	if len(sent) > 0 {
		urls := map[string]string{}
		deadline := time.Now().Add(25 * time.Second) // baglanti kurulumu yavas olabiliyor
		for time.Now().Before(deadline) && len(urls) < len(sent) {
			time.Sleep(4 * time.Second)
			for _, p := range sent {
				if urls[p.name] != "" {
					continue
				}
				pane, err := a.tmux.Capture(a.ctx, p.name)
				if err != nil {
					continue
				}
				if matches := remoteURLPattern.FindAllString(pane, -1); len(matches) > 0 {
					urls[p.name] = strings.TrimRight(matches[len(matches)-1], ".,)")
				}
			}
		}
		// RC zaten aktif olan oturumlarda komut bir menu acar (Disconnect/QR/Continue,
		// imlec Continue'da) ve Enter bekler. Menu URL'den GEC render olabildigi icin
		// kapatma ayri bir supurme: menu goren herkese Enter, kalan var mi diye tekrar.
		// Ust uste ikinci bir submit gec de menu acabildigi icin: 2 ardisik temiz
		// tur gorene kadar supur (en fazla ~24sn).
		clean := 0
		for tries := 0; tries < 12 && clean < 2; tries++ {
			dismissed := false
			for _, p := range sent {
				pane, err := a.tmux.Capture(a.ctx, p.name)
				if err != nil {
					continue
				}
				if bptmux.RemoteControlMenu(pane) {
					_ = a.tmux.PressEnter(a.ctx, p.name)
					dismissed = true
				}
			}
			if dismissed {
				clean = 0
			} else {
				clean++
			}
			time.Sleep(2 * time.Second)
		}
		for _, p := range sent {
			if url := urls[p.name]; url != "" {
				fmt.Fprintf(a.out, "  aktif  %-28s %s\n", p.name, url)
			} else {
				fmt.Fprintf(a.out, "  gonder %-28s URL gorunmedi (bp peek %s ile bak)\n", p.name, p.name)
			}
		}
	}
	fmt.Fprintf(a.out, "remote: %d gonderildi, %d kuyrukta, %d atlandi\n", len(sent), queuedCount, skipped)
	if unverified > 0 {
		fmt.Fprintf(a.out, "        %d dogrulanamadi (bp peek ile bak)\n", unverified)
	}
	return nil
}
