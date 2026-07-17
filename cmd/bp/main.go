package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	"blueprint/internal/daemon"
	"blueprint/internal/dashboard"
	"blueprint/internal/monitorcli"
	"blueprint/internal/msgq"
	bptmux "blueprint/internal/tmux"
	"blueprint/internal/usagecli"
	"blueprint/internal/wa"
	"blueprint/internal/worktree"
)

const usage = `blueprint (bp) — agent infrastructure CLI

bp status | bp tree
bp open <name> <directory> [--worktree <topic>] [--resume] [--codex] [--no-prompt]
bp worktree add <repo-directory> <topic>
bp worktree list <repo-directory>
bp worktree rm <repo-directory> <topic> [--force]
bp close <name>
bp msg <name> <message...>
bp announce <message...>
bp compact [--min-age <minutes>] [--exclude <name,...>] [--dry-run]
bp remote [<name>...]        # /remote-control ac/goster (varsayilan: tum acik claude agentlari)
bp q | bp qstat <channel-id>
bp peek <name> [n]
bp wa send [--to <target>] [--reply <msgId>] <message...>
bp wa read <target> [n] | bp wa chats
bp usage
bp monitor [usage|cost|agents|projects|services|radar]
bp policy status|override <hours>
bp service
bp con [agent-name]
bp img [recv]
bp dash [--port N]
bp daemon`

type app struct {
	ctx   context.Context
	tmux  *bptmux.Client
	queue *msgq.Queue
	out   *os.File
	err   *os.File
}

func main() {
	ctx := context.Background()
	a := &app{ctx: ctx, tmux: bptmux.New(), queue: msgq.New(msgq.DefaultRoot), out: os.Stdout, err: os.Stderr}
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"status"}
	}
	if err := a.run(args); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

func (a *app) run(args []string) error {
	switch args[0] {
	case "status":
		return a.status()
	case "tree":
		return a.tree()
	case "open":
		return a.open(args[1:])
	case "worktree":
		return a.worktree(args[1:])
	case "close":
		return a.close(args[1:])
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
	case "dash":
		return a.dashboard(args[1:])
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
	return dashboard.Serve(ctx, dashboard.Options{Port: port, URL: dashboardURL, Out: a.out})
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
	fleet, err := book.LoadFleet(book.Paths())
	if err != nil {
		return book.Fleet{}, nil, err
	}
	states, err := book.LiveStates(a.ctx, a.tmux, &fleet)
	return fleet, states, err
}

func (a *app) status() error {
	fleet, states, err := a.fleet()
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%-24s %-10s %s\n", "AGENT", "TMUX", "AGENTBOOK")
	for _, name := range fleet.SortedNames() {
		state, alive := states[name]
		tmuxState := "closed"
		if alive && state.Busy {
			tmuxState = "working"
		} else if alive {
			tmuxState = "idle"
		}
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
		fmt.Fprintf(a.out, "%-24s %-10s %-10s%s\n", name, tmuxState, bookState, flag)
	}
	return nil
}

func (a *app) tree() error {
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
			if state.Busy {
				live = "working"
			} else {
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
	return nil
}

func (a *app) open(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: bp open <name> <directory> [--worktree <topic>] [--resume] [--codex] [--no-prompt]")
	}
	name, dir := args[0], args[1]
	opts := bptmux.OpenOptions{}
	worktreeTopic := ""
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
		default:
			return fmt.Errorf("unknown open option: %s", arg)
		}
	}
	if a.tmux.HasSession(a.ctx, name) {
		fmt.Fprintf(a.out, "%s is already open\n", name)
		return nil
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
	if err := a.tmux.Open(a.ctx, name, dir, opts, func(text string) { fmt.Fprintln(a.out, text) }); err != nil {
		return err
	}
	if err := book.SetStatus(name, "open", dir); err != nil {
		return err
	}
	rc := ""
	if pane, err := a.tmux.Capture(a.ctx, name); err == nil {
		matches := regexp.MustCompile(`claude\.ai/code/session_[A-Za-z0-9]+`).FindAllString(pane, -1)
		if len(matches) > 0 {
			rc = "  rc:https://" + matches[len(matches)-1]
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
	if a.tmux.HasSession(a.ctx, name) {
		if err := a.tmux.Close(a.ctx, name); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s closed (history remains in JSONL)\n", name)
	} else {
		fmt.Fprintf(a.out, "%s is already closed\n", name)
	}
	return book.SetStatus(name, "closed", "")
}

func (a *app) sender() string {
	if value := os.Getenv("AGENT"); value != "" {
		return value
	}
	if value, err := a.tmux.DisplaySession(a.ctx); err == nil && value != "" {
		return value
	}
	return "server-main"
}

func (a *app) message(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: bp msg <name> <message...>")
	}
	name, message := args[0], strings.Join(args[1:], " ")
	queued, channelID, err := a.deliver(name, a.sender(), message)
	if err != nil {
		return err
	}
	if !queued {
		fmt.Fprintln(a.out, "sent")
		return nil
	}
	fmt.Fprintf(a.out, "BUSY: queued (channel: %s). Check: bp qstat %s\n", channelID, channelID)
	return nil
}

func (a *app) deliver(name, sender, message string) (queued bool, channelID string, err error) {
	if !a.tmux.HasSession(a.ctx, name) {
		return false, "", fmt.Errorf("no open session named %s", name)
	}
	pane, err := a.tmux.CaptureAnsi(a.ctx, name)
	if err != nil {
		return false, "", err
	}
	if !bptmux.Typing(pane) && !bptmux.Busy(pane) {
		err = a.tmux.Send(a.ctx, name, message)
		if err == nil {
			return false, "", nil
		}
		if !errors.Is(err, bptmux.ErrTyping) {
			return false, "", err
		}
	}
	channelID, err = a.queue.Enqueue(name, sender, message)
	return true, channelID, err
}

func (a *app) announce(args []string) error {
	if len(args) == 0 {
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
	targets := announcementTargets(fleet, states, sender)
	message := fmt.Sprintf("[ANNOUNCE %s] %s", sender, strings.Join(args, " "))
	sent := 0
	channels := make([]string, 0)
	var deliveryErrors []error
	for _, target := range targets {
		queued, channelID, deliveryErr := a.deliver(target, sender, message)
		if deliveryErr != nil {
			deliveryErrors = append(deliveryErrors, fmt.Errorf("%s: %w", target, deliveryErr))
			continue
		}
		if queued {
			channels = append(channels, channelID)
		} else {
			sent++
		}
	}
	fmt.Fprintf(a.out, "sent: %d, queued: %d", sent, len(channels))
	if len(channels) > 0 {
		fmt.Fprintf(a.out, " (%s)", strings.Join(channels, ", "))
	}
	fmt.Fprintln(a.out)
	return errors.Join(deliveryErrors...)
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

const compactStatePath = "/srv/blueprint/state/compact.json"

type compactOptions struct {
	minAge  time.Duration
	exclude []string
	dryRun  bool
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

func parseCompactArgs(args []string) (compactOptions, error) {
	opts := compactOptions{minAge: 30 * time.Minute}
	minAgeSeen := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--dry-run":
			opts.dryRun = true
		case arg == "--min-age" || strings.HasPrefix(arg, "--min-age="):
			value := ""
			if arg == "--min-age" {
				if index+1 >= len(args) {
					return opts, fmt.Errorf("--min-age requires a value in minutes")
				}
				value = args[index+1]
				index++
			} else {
				value = strings.TrimPrefix(arg, "--min-age=")
			}
			if minAgeSeen {
				return opts, fmt.Errorf("--min-age may only be specified once")
			}
			minAgeSeen = true
			minutes, err := strconv.Atoi(value)
			if err != nil || minutes < 0 {
				return opts, fmt.Errorf("invalid --min-age minutes: %s", value)
			}
			opts.minAge = time.Duration(minutes) * time.Minute
		case arg == "--exclude" || strings.HasPrefix(arg, "--exclude="):
			value := ""
			if arg == "--exclude" {
				if index+1 >= len(args) {
					return opts, fmt.Errorf("--exclude requires a comma-separated list")
				}
				value = args[index+1]
				index++
			} else {
				value = strings.TrimPrefix(arg, "--exclude=")
			}
			for _, part := range strings.Split(value, ",") {
				if part = strings.TrimSpace(part); part != "" {
					opts.exclude = append(opts.exclude, part)
				}
			}
		default:
			return opts, fmt.Errorf("unknown compact option: %s", arg)
		}
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
	lastCompact := loadCompactState(compactStatePath)
	plan := selectCompactTargets(fleet, states, sender, opts.exclude, lastCompact, opts.minAge, time.Now())

	if opts.dryRun {
		fmt.Fprintf(a.out, "[dry-run] targets: %d\n", len(plan.Send))
		for _, name := range plan.Send {
			fmt.Fprintf(a.out, "  send   %s\n", name)
		}
		for _, skip := range plan.SkippedRecent {
			fmt.Fprintf(a.out, "  skip   %s: recent (%s ago)\n", skip.Name, formatAge(skip.Age))
		}
		for _, name := range plan.Excluded {
			fmt.Fprintf(a.out, "  skip   %s: excluded\n", name)
		}
		if len(plan.UnknownExcludes) > 0 {
			fmt.Fprintf(a.out, "  ignored unknown excludes: %s\n", strings.Join(plan.UnknownExcludes, ", "))
		}
		fmt.Fprintf(a.out, "[dry-run] would send: %d, skipped recent: %d, excluded: %d\n", len(plan.Send), len(plan.SkippedRecent), len(plan.Excluded))
		return nil
	}

	sent := 0
	channels := make([]string, 0)
	var deliveryErrors []error
	now := time.Now().UTC()
	updated := false
	for _, target := range plan.Send {
		queued, channelID, deliveryErr := a.deliver(target, sender, "/compact")
		if deliveryErr != nil {
			deliveryErrors = append(deliveryErrors, fmt.Errorf("%s: %w", target, deliveryErr))
			continue
		}
		if queued {
			channels = append(channels, channelID)
		} else {
			sent++
		}
		lastCompact[target] = now
		updated = true
	}
	if updated {
		if err := saveCompactState(compactStatePath, lastCompact); err != nil {
			deliveryErrors = append(deliveryErrors, fmt.Errorf("save compact state: %w", err))
		}
	}

	fmt.Fprintf(a.out, "compact sent: %d, queued: %d", sent, len(channels))
	if len(channels) > 0 {
		fmt.Fprintf(a.out, " (%s)", strings.Join(channels, ", "))
	}
	fmt.Fprintf(a.out, ", skipped recent: %d, excluded: %d\n", len(plan.SkippedRecent), len(plan.Excluded))
	if len(plan.UnknownExcludes) > 0 {
		fmt.Fprintf(a.out, "ignored unknown excludes: %s\n", strings.Join(plan.UnknownExcludes, ", "))
	}
	return errors.Join(deliveryErrors...)
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
		return nil
	}
	for _, row := range rows {
		text := []rune(row.Msg)
		if len(text) > 60 {
			text = text[:60]
		}
		fmt.Fprintf(a.out, "%s %s -> %s : %s\n", row.ID, row.From, row.To, string(text))
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
		if err := wa.Send(wa.DefaultOutbox, wa.Agent(a.ctx, a.tmux), to, reply, text); err != nil {
			return err
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
		lines, err := wa.Read(wa.DefaultStore, args[1], count)
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
		lines, err := wa.Chats(wa.DefaultStore)
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
	sample, err := usagecli.Latest(usagecli.HistoryPath)
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
		jobs, jobsErr := monitorcli.LoadJobs(monitorcli.DefaultJobsPath)
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
	cmd := exec.CommandContext(a.ctx, "/srv/server-main/bin/usage-policy", args...)
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
	jobs, err := daemon.LoadState(daemon.StatePath)
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

func (a *app) daemon(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: bp daemon")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	service := daemon.New(log.New(a.err, "blueprint: ", log.LstdFlags))
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
	queuedCount, skipped := 0, 0
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
		queued, channelID, err := a.deliver(name, sender, "/remote-control")
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
		for _, p := range sent {
			if url := urls[p.name]; url != "" {
				fmt.Fprintf(a.out, "  aktif  %-28s %s\n", p.name, url)
			} else {
				fmt.Fprintf(a.out, "  gonder %-28s URL gorunmedi (bp peek %s ile bak)\n", p.name, p.name)
			}
		}
	}
	fmt.Fprintf(a.out, "remote: %d gonderildi, %d kuyrukta, %d atlandi\n", len(sent), queuedCount, skipped)
	return nil
}
