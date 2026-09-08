package main

import (
	"blueprint/internal/messagetext"
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
	"blueprint/internal/buildinfo"
	bpcache "blueprint/internal/cache"
	"blueprint/internal/codexauth"
	"blueprint/internal/codexrpc"
	bpconfig "blueprint/internal/config"
	"blueprint/internal/daemon"
	"blueprint/internal/dashboard"
	"blueprint/internal/fed"
	"blueprint/internal/identity"
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

bp version [--json] | bp update [--check] [--json] | bp doctor [--agent <name>] [--json]
bp archive <name> | bp archive --list [--json] | bp restore <name>
bp status [--json] | bp tree
bp color <agent> [--json|auto|color] # read HEX or set accent (blue, red, 0–255)
bp whoami                     # sender identity and authority evidence (JSON)
bp setup [--check|--disable]   # local shell integration (bash/zsh)
bp onboard [--cli <command>] [--prepare] [-- arguments...]
bp book [--json]              # configured books and coordinator
bp config path|check           # settings file location / validation
bp run [--name <name>] <codex|claude|opencode|hermes> [arguments...]
bp open <name> <directory> [--worktree <topic>] [--parent <name>] [--role <text>] [--resume] [--codex|--claude|--hermes] [--remote unix://] [--thread <id>] [--no-sandbox] [--no-prompt]
bp worktree add <repo-directory> <topic>
bp worktree list <repo-directory>
bp worktree rm <repo-directory> <topic> [--force]
bp close <name>
bp rename <old-name> <new-name> [--dry-run] [--no-retitle]
                             # --no-retitle: skip typing /rename into the pane, so an
                             # agent can rename ITSELF (its own pane is always busy).
                             # The transcript keeps the OLD title until that agent
                             # types /rename <new> — bp cannot read its context (blank
                             # CACHE), compact and open --resume stop finding it.
bp msg [--force|--force-busy] <name> <message...>
                             # bp stamps a [sender] envelope; never write your own
                             # a /slash command goes bare, and only down the hierarchy
                             # --force-busy jumps the queue on a busy agent (root/bp/wa only)
bp announce <message...> [--dry-run]
bp compact [--idle-hours N] [--min-ctx N] [--apply]   # policy: idle+full claude agents
bp compact --all [--min-age <minutes>] [--exclude <name,...>] [--apply]
                             # lists by default; nothing is sent without --apply
bp remote [<name>...]        # print or open /remote-control (default: every live claude agent)
bp q [--retry] | bp qstat <channel-id> | bp qcancel <channel-id>
bp peek <name> [n]
bp wa send [--to <target>] [--reply <msgId>] [--from <label>] <message...>
                             # --from states the sender outside tmux (cron, scripts);
                             # inside a pane the session name is the sender
bp wa read <target> [n] | bp wa chats
bp usage
bp tokens [--day YYYY-MM-DD | --since 7d] [--hours | --prompts] [--agent <name>] [--json]
bp tokens collect | bp tokens gc
bp monitor [usage|cost|agents|projects|services|radar]
bp policy status|override <hours>
bp service
bp p2p id|start|stop|status [--json]|channels [--json]|ping <peer>
bp con [agent-name]
bp img [recv]
bp dash [--port N]
bp fed status|ping|token|log [n]
bp daemon`

type app struct {
	resolveSender func() identity.Identity
	originProbe   func(context.Context) identity.Origin
	ctx           context.Context
	config        bpconfig.Config
	tmux          *bptmux.Client
	queue         *msgq.Queue
	out           *os.File
	err           *os.File

	loadFleet      func() (book.Fleet, map[string]book.State, error)
	loadCodex      func() []codexrpc.Thread
	deliverMessage func(string, string, string) (bool, string, error)
	sessionExists  func(string) bool
	loadCache      func(map[string]string) map[string]bpcache.State
	loadCommands   func() (map[string]string, error)
	capturePane    func(string) (string, error)
	clearPane      func(string) error
	turnOpenProbe  func(string) bool

	// paneLocks counts the pane locks this process is holding, per session. It
	// exists because the lock is an flock and flock is NOT reentrant even within one
	// process: a command that already holds a pane (compact clears the composer,
	// then delivers; rename does the same) would otherwise wait out the whole
	// acquire budget against itself and then report the pane as busy. bp is a
	// single-threaded CLI, so a counter is the whole of the bookkeeping.
	paneLocks map[string]*heldPaneLock
}

// heldPaneLock is one flock this process owns, with the number of nested holders.
type heldPaneLock struct {
	release func()
	depth   int
}

// lockPane makes this process the only bp allowed to type into one pane, and
// returns the function that gives it back.
//
// Every path that captures a composer and then presses keys on it must hold this:
// `bp msg`'s delivery, `bp open`'s pending-digest flush, /rename and /compact. On
// 2026-08-17 an open-flush and a msg-paste hit one composer at the same moment and
// the agent read both as a single 590-character message from two senders.
//
// The lock file lives under the shared queue root, so the daemon's dispatch loop
// and every CLI process resolve the same file from the same config key. Without a
// queue there is nothing shared to coordinate through (tests, an app built without
// config), and the lock degrades to a no-op rather than to an error.
func (a *app) lockPane(name string) (func(), error) {
	root := ""
	if a.queue != nil {
		root = a.queue.Root
	}
	if root == "" || name == "" {
		return func() {}, nil
	}
	if a.paneLocks == nil {
		a.paneLocks = map[string]*heldPaneLock{}
	}
	if held, ok := a.paneLocks[name]; ok {
		held.depth++
		return func() { a.releasePane(name) }, nil
	}
	release, err := bptmux.AcquirePaneLock(root, name)
	if err != nil {
		return nil, err
	}
	a.paneLocks[name] = &heldPaneLock{release: release, depth: 1}
	return func() { a.releasePane(name) }, nil
}

func (a *app) releasePane(name string) {
	held, ok := a.paneLocks[name]
	if !ok {
		return
	}
	held.depth--
	if held.depth > 0 {
		return
	}
	delete(a.paneLocks, name)
	held.release()
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		if err := printVersion(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "_observe" {
		if err := observe(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "bp observation:", err)
			os.Exit(1)
		}
		return
	}
	ctx := context.Background()
	config, err := bpconfig.Load()
	if len(os.Args) > 1 && os.Args[1] == "doctor" {
		if err := doctor(config, err, os.Args[2:]); err != nil {
			if !errors.Is(err, errReported) {
				fmt.Fprintln(os.Stderr, err)
			}
			os.Exit(1)
		}
		return
	}
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
	case "run", "_session", "_local-worker":
		return false // remaining flags belong to the wrapped CLI
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
	case "config":
		if len(args) != 2 || (args[1] != "path" && args[1] != "check") {
			return fmt.Errorf("usage: bp config path|check")
		}
		if a.config.InvalidConfig != "" {
			return fmt.Errorf("invalid configuration: %s", a.config.InvalidConfig)
		}
		if a.config.Path == "" {
			fmt.Fprintf(a.out, "No config file; using defaults. bp setup creates %s\n", filepath.Join(a.config.Home, "config.yaml"))
		} else if args[1] == "path" {
			fmt.Fprintln(a.out, a.config.Path)
		} else {
			fmt.Fprintf(a.out, "OK: %s\n", a.config.Path)
		}
		return nil
	case "run":
		return a.localRun(args[1:])
	case "_session":
		return a.localSession(args[1:])
	case "_open-session":
		return a.managedSession(args[1:])
	case "_local-worker":
		return a.localWorker(args[1:])
	case "whoami":
		if len(args) != 1 {
			return fmt.Errorf("usage: bp whoami")
		}
		who := a.senderIdentity()
		return json.NewEncoder(a.out).Encode(struct {
			identity.Identity
			Authority bool `json:"authority"`
		}{who, who.Authoritative()})
	case "update":
		return a.update(args[1:])
	case "onboard":
		return a.onboard(args[1:])
	case "book":
		return a.showBook(args[1:])
	case "setup":
		return a.localSetup(args[1:])
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
	case "archive":
		return a.archive(args[1:], true)
	case "restore":
		return a.archive(args[1:], false)
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
		if len(args) == 2 && args[1] == "--retry" {
			a.dispatchNow() // Existing queue only; normal runtime/composer gates apply.
		} else if len(args) != 1 {
			return fmt.Errorf("usage: bp q [--retry]")
		}
		return a.queueList(nil)
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
	case "color":
		return a.color(args[1:])
	case "name":
		if len(args) != 2 {
			return fmt.Errorf("usage: bp name <agent>")
		}
		fmt.Fprintln(a.out, a.barName(args[1]))
		return nil
	case "dash":
		return a.dashboard(args[1:])
	case "p2p":
		return a.p2pCommand(args[1:])
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

// Mismatch kinds: the two ways tmux and the agentbook can contradict each other.
// Both are reported rather than silently reconciled — bp cannot know which side
// is stale, and a hidden disagreement is what let a running agent sit behind a
// "closed" entry for weeks.
const (
	mismatchTmuxOpenBookClosed = "tmux-open-book-closed"
	mismatchTmuxClosedBookOpen = "tmux-closed-book-open"
)

// bookMismatch names the disagreement between a live tmux session and the book
// entry, or "" when they agree. "opening" is never a mismatch: it is an honest
// intermediate state that already admits it does not know.
func bookMismatch(bookState string, alive bool) string {
	switch {
	case alive && bookState == "closed":
		return mismatchTmuxOpenBookClosed
	case !alive && bookState == "open":
		return mismatchTmuxClosedBookOpen
	}
	return ""
}

// mismatchMark is the operator-facing tail of the AGENTBOOK cell. It follows the
// padded status so the marks line up down the column, and the column is last, so
// a long one cannot push anything out of place.
func mismatchMark(mismatch string) string {
	switch mismatch {
	case mismatchTmuxOpenBookClosed:
		return " !TMUX ACIK"
	case mismatchTmuxClosedBookOpen:
		return " !TMUX YOK"
	}
	return ""
}

// tmuxStateLabel names what tmux shows for an agent: the same four words the
// human table and the JSON output both report.
func tmuxStateLabel(state book.State, alive bool) string {
	if alive && state.Runtime != nil && state.Runtime.Activity != nil {
		return state.Runtime.Activity.State
	}
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
	cacheStates := a.cacheStates(fleet, states)
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
		mark := mismatchMark(bookMismatch(bookState, alive))
		cacheText, talkText := "-", "-"
		if state, ok := cacheStates[name]; ok {
			if state.Known {
				temperature, elapsed := state.CacheHint()
				cacheText = fmt.Sprintf("%s %s %s", temperature, shortAge(elapsed), humanTokens(state.CtxTokens))
			}
			if state.LastHumanAge >= 0 {
				talkText = shortAge(state.LastHumanAge)
			}
		}
		label := a.nativeName(fleet.Agents[name], state.Runtime)
		if label != name {
			label += " (" + name + ")"
		}
		fmt.Fprintf(a.out, "%-24s %-10s %-20s %-10s %-10s%s\n", label, tmuxState, cacheText, talkText, bookState, mark)
	}
	a.renderCodexStatus(a.codexThreads())
	return nil
}

// statusReport is the machine-readable shape of bp status. Numbers that are
// merely unknown are omitted rather than sent as zeros: a zero token count or a
// zero age would read as a measured fact.
type statusReport struct {
	Daemon             *buildinfo.Identity `json:"daemon,omitempty"`
	DaemonVerification string              `json:"daemon_verification"`
	SchemaVersion      int                 `json:"schema_version"`
	ObservedAt         time.Time           `json:"observed_at"`
	Producer           runtimeProducer     `json:"producer"`
	Agents             []statusAgent       `json:"agents"`
	Codex              []statusThread      `json:"codex,omitempty"`
	CodexUnloaded      int                 `json:"codex_unloaded,omitempty"`
}

type statusAgent struct {
	Activity    *bpcache.Activity `json:"activity,omitempty"`
	UsageAt     *time.Time        `json:"usage_observed_at,omitempty"`
	UsageScope  string            `json:"usage_scope,omitempty"`
	Name        string            `json:"name"`
	DisplayName string            `json:"display_name,omitempty"`
	Tmux        string            `json:"tmux"`
	// Mismatch is written only when tmux and the book disagree, so a consumer can
	// treat the field's presence as the alarm.
	Mismatch string `json:"mismatch,omitempty"`
	// BusyScreen and BusyTurnOpen decompose the busy verdict into its two gates
	// (the pane's spinner row and the transcript's open turn), present only for
	// live sessions. They exist so the next "has the screen signature drifted?"
	// question can be answered from outside with one `bp status --json` instead
	// of by measuring the composite and guessing which gate spoke (2026-08-18).
	BusyScreen          *bool    `json:"busy_screen,omitempty"`
	BusyTurnOpen        *bool    `json:"busy_turnopen,omitempty"`
	Status              string   `json:"status,omitempty"`
	Folder              string   `json:"folder,omitempty"`
	Parent              string   `json:"parent,omitempty"`
	AgentbookPaths      []string `json:"agentbook_paths,omitempty"`
	CtxTokens           *int     `json:"ctx_tokens,omitempty"`
	CacheAgeSeconds     *int64   `json:"cache_age_seconds,omitempty"`
	CacheTTLSeconds     int64    `json:"cache_ttl_seconds,omitempty"`
	CacheEstimate       string   `json:"cache_estimate,omitempty"`
	LastHumanAgeSeconds *int64   `json:"last_human_age_seconds,omitempty"`
	Model               string   `json:"model,omitempty"`
	Effort              string   `json:"effort,omitempty"`
	ServiceTier         string   `json:"service_tier,omitempty"`
	ThreadID            string   `json:"thread_id,omitempty"`
	Runtime             string   `json:"runtime,omitempty"`
	RuntimeError        string   `json:"runtime_error,omitempty"`
	ContextWindow       int      `json:"context_window,omitempty"`
}

type statusThread struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	CWD       string `json:"cwd,omitempty"`
	CtxTokens *int64 `json:"ctx_tokens,omitempty"`
	Window    *int64 `json:"context_window,omitempty"`
}

func (a *app) statusJSON(fleet book.Fleet, states map[string]book.State, cacheStates map[string]bpcache.State) error {
	report := statusReport{SchemaVersion: 2, ObservedAt: time.Now().UTC(), Producer: currentProducer(), Agents: make([]statusAgent, 0, len(fleet.Agents))}
	report.Daemon, report.DaemonVerification = buildinfo.Recorded(filepath.Join(a.config.StateDir, "daemon-runtime.json"))
	for _, name := range fleet.SortedNames() {
		state, alive := states[name]
		agent := fleet.Agents[name]
		row := statusAgent{
			Name:           name,
			Tmux:           tmuxStateLabel(state, alive),
			Mismatch:       bookMismatch(agent.Status, alive),
			Status:         agent.Status,
			Folder:         agent.Folder,
			Parent:         fleet.Parents[name],
			AgentbookPaths: fleet.Sources[name],
		}
		if state.Runtime != nil {
			row.DisplayName = a.nativeName(agent, state.Runtime)
			row.Activity = state.Runtime.Activity
		}
		if alive {
			screen, turn := state.ScreenBusy, state.TurnBusy
			row.BusyScreen, row.BusyTurnOpen = &screen, &turn
			if row.Activity != nil {
				row.BusyScreen, row.BusyTurnOpen = row.Activity.ScreenBusy, row.Activity.TurnBusy
			}
		}
		if cacheState, ok := cacheStates[name]; ok {
			if !cacheState.UsageAt.IsZero() {
				stamp := cacheState.UsageAt
				row.UsageAt = &stamp
			}
			if cacheState.Known {
				row.UsageScope = "last_context_snapshot"
				if row.Activity != nil && len(row.Activity.BindingConflicts) > 0 {
					row.UsageScope = "shared_thread_snapshot"
				}
			}
			if cacheState.Known {
				tokens := cacheState.CtxTokens
				age := int64(cacheState.Age / time.Second)
				row.CtxTokens, row.CacheAgeSeconds = &tokens, &age
				if cacheState.CacheTTL > 0 {
					row.CacheTTLSeconds = int64(cacheState.CacheTTL / time.Second)
					row.CacheEstimate, _ = cacheState.CacheHint()
				}
			}
			if cacheState.LastHumanAge >= 0 {
				lastHuman := int64(cacheState.LastHumanAge / time.Second)
				row.LastHumanAgeSeconds = &lastHuman
			}
			row.Model, row.ServiceTier = cacheState.Model, cacheState.ServiceTier
			row.Effort, row.ThreadID, row.Runtime, row.RuntimeError, row.ContextWindow = cacheState.Effort, cacheState.ThreadID, cacheState.Runtime, cacheState.RuntimeError, cacheState.Window
		}
		report.Agents = append(report.Agents, row)
	}
	live, unloaded := liveCodexThreads(a.codexThreads())
	report.CodexUnloaded = unloaded
	for _, thread := range live {
		row := statusThread{Name: codexName(thread), State: codexState(thread.Status), CWD: thread.CWD}
		if usage := thread.TokenUsage; usage != nil {
			used := usage.Last.TotalTokens
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

func (a *app) cacheStates(fleet book.Fleet, observed map[string]book.State) map[string]bpcache.State {
	states := make(map[string]bpcache.State, len(fleet.Agents))
	folders := map[string]string{}
	for name, agent := range fleet.Agents {
		if runtime := observed[name].Runtime; runtime != nil {
			states[name] = *runtime
		} else {
			folders[name] = agent.Folder
		}
	}
	for name, state := range a.readCache(folders) {
		states[name] = state
	}
	return states
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
	if usage.ModelContextWindow != nil && *usage.ModelContextWindow > 0 {
		return humanTokens(int(used)) + "/" + humanTokens(int(*usage.ModelContextWindow))
	}
	return humanTokens(int(used))
}

// unverifiedCause pulls the named cause off an ErrUnverified, falling back to a
// plain description for an error that carries none.
func unverifiedCause(err error) string {
	if err == nil {
		return "pane'de dogrulanamadi"
	}
	if text := err.Error(); strings.Contains(text, ": ") {
		if _, cause, ok := strings.Cut(text, ": "); ok && cause != "" {
			return cause
		}
	}
	return "pane'de dogrulanamadi"
}

func (a *app) open(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: bp open <name> <directory> [--worktree <topic>] [--parent <name>] [--role <text>] [--resume] [--codex|--claude|--hermes] [--remote unix://] [--thread <id>] [--no-sandbox] [--no-prompt]")
	}
	name, dir := args[0], args[1]
	if err := book.RequireUnarchived(a.config.Agentbooks, name); err != nil {
		return err
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	dir, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return fmt.Errorf("open directory: %w", err)
	}
	for _, positional := range []string{name, dir} {
		if err := rejectFlag("open", positional); err != nil {
			return err
		}
	}
	opts := bptmux.OpenOptions{Legacy: a.config.Legacy, Codex: true}
	// Reopening preserves a registered launch; explicit flags can select a new one.
	stored, _ := book.LoadFleet(book.Paths(a.config.Agentbooks))
	if launch := stored.Agents[name].Launch; launch != nil {
		opts = *launch
		opts.Legacy = a.config.Legacy
		opts.Resume = opts.ResumeID != ""
	}
	harness := ""
	resumeRequested, threadExplicit := false, false
	worktreeTopic := ""
	reg := book.Registration{}
	for index := 2; index < len(args); index++ {
		arg := args[index]
		switch arg {
		case "--resume":
			opts.Resume = true
			resumeRequested = true
		case "--codex", "--claude", "--hermes":
			if harness != "" && harness != arg {
				return fmt.Errorf("choose one harness")
			}
			harness = arg
			changed := opts.Codex != (arg == "--codex") || opts.Hermes != (arg == "--hermes")
			if changed {
				if !threadExplicit {
					opts.ResumeID = ""
				}
				opts.Resume = resumeRequested
				opts.Remote, opts.NoSandbox = "", false
			}
			opts.Codex, opts.Hermes = arg == "--codex", arg == "--hermes"
			if !opts.Codex {
				opts.Remote, opts.NoSandbox = "", false
			}
		case "--remote":
			if index+1 >= len(args) {
				return fmt.Errorf("--remote requires a unix:// endpoint")
			}
			index++
			opts.Remote = args[index]
		case "--thread":
			if index+1 >= len(args) {
				return fmt.Errorf("--thread requires a thread id")
			}
			index++
			opts.ResumeID = args[index]
			opts.Resume = true
			resumeRequested, threadExplicit = true, true
		case "--no-sandbox":
			// Codex only, and opt-in on purpose: it turns off BOTH sandboxes (our
			// bwrap wrapper and codex's own). The default stays as it is for every
			// other agent. See OpenOptions.NoSandbox for what it costs.
			opts.NoSandbox = true
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
	if err := opts.Validate(); err != nil {
		return err
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
	if !a.config.Legacy && fleetErr == nil && reg.Parent == "" {
		if _, registered := fleet.Agents[name]; !registered {
			// Local users routinely share a cwd. It cannot select a parent:
			// an unrelated, closed record may have the exact same folder.
			reg.Parent = fleet.Root
			who := a.senderIdentity()
			if _, known := fleet.Agents[who.Label]; known && who.Authoritative() {
				reg.Parent = who.Label
			}
		}
	}
	if a.tmux.HasSession(a.ctx, name) {
		// "Session exists" is not "agent running": a crashed CLI leaves the
		// tmux session up as a bare shell. Only a live agent pane counts as
		// already open; a dead shell falls through to Open, which relaunches
		// the agent in place (or errors if the pane runs something else).
		process, perr := a.tmux.PaneProcess(a.ctx, name)
		// The screen is part of the answer: a live Hermes pane reports "python"
		// (measured 2026-08-22), and on the command alone `bp open` would decide the
		// agent had crashed and try to relaunch a CLI on top of a working one. An
		// unreadable pane leaves this exactly as it was.
		pane, _ := a.tmux.Capture(a.ctx, name)
		if perr != nil || bptmux.IsAgentPane(process.Command, pane) {
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
			// A half-finished open leaves the book at "opening" (see below) with a
			// live session behind it. Reopening is the natural reflex and this is
			// the one moment both facts are in hand, so settle the entry rather
			// than answering "already open" and leaving the record undecided.
			if fleetErr == nil && fleet.Agents[name].Status == "opening" {
				reg.Sender = a.sender()
				if err := book.SetStatus(a.config.Agentbooks, name, "open", dir, reg); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "%s is already open (agentbook: opening -> open)\n", name)
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
	if !opts.Codex && !opts.Hermes && opts.Resume {
		path, err := bptmux.ResolveSessionPath(bptmux.ClaudeProjectsRoot(), dir, name, opts.ResumeID)
		if err != nil {
			return err
		}
		opts.ResumeID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
		opts.NoPrompt = true
	}
	if opts.Codex && opts.Resume {
		path, ok := bpcache.CodexPath(bptmux.CodexProcessInfo(0).Home, dir, opts.ResumeID)
		if !ok {
			return fmt.Errorf("no matching Codex thread for %s; refusing to open a different conversation", name)
		}
		opts.ResumeID = bpcache.CodexID(path)
		if opts.ResumeID == "" {
			return fmt.Errorf("Codex thread has no readable identity")
		}
		opts.NoPrompt = true
	}
	if err := opts.Validate(); err != nil {
		return err
	}
	reg.Launch = &opts
	// The session comes up before the book can record it, so the gap between the
	// two used to be a lie: a timeout or a Ctrl-C in between left the book saying
	// "closed" over an agent that was really running, and a reader of bp status
	// cannot tell that apart from an agent that was never started. "opening" is
	// written first so the crash window says "I don't know" instead — the entry is
	// settled to "open" below, or rolled back when tmux refuses.
	reg.Sender = a.sender()
	if err := book.SetStatus(a.config.Agentbooks, name, "opening", dir, reg); err != nil {
		return err
	}
	previousRollout := ""
	if opts.Codex && !opts.Resume {
		previousRollout, _ = bpcache.CodexPath(bptmux.CodexProcessInfo(0).Home, dir, "")
	}
	if !a.config.Legacy && opts.Codex && opts.Remote == "" {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		opts.Launcher = "env BP_HOME=" + quoteShell(a.config.Home)
		for _, key := range []string{"HOME", "PATH", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "AGENTBOOK"} {
			if value, ok := os.LookupEnv(key); ok {
				opts.Launcher += " " + key + "=" + quoteShell(value)
			}
		}
		opts.Launcher += " " + quoteShell(self) + " _open-session " + quoteShell(name) + " codex"
	}
	if err := a.tmux.Open(a.ctx, name, dir, opts, func(text string) { fmt.Fprintln(a.out, text) }); err != nil {
		// Roll back only while tmux can still be believed. A cancelled or timed-out
		// open cannot ask it anything (HasSession reports "no" for an unreachable
		// tmux exactly as it does for a missing session), and writing "closed" over
		// a session that is actually up is the very lie this change removes.
		if a.ctx.Err() == nil && !a.tmux.HasSession(a.ctx, name) {
			if backErr := book.SetStatus(a.config.Agentbooks, name, "closed", dir, reg); backErr != nil {
				return errors.Join(err, backErr)
			}
		}
		return err
	}
	if opts.Codex && !opts.Resume {
		if path, ok := bpcache.CodexPath(bptmux.CodexProcessInfo(0).Home, dir, ""); ok && path != previousRollout {
			opts.ResumeID = bpcache.CodexID(path)
		}
	}
	if !opts.Codex && !opts.Hermes && opts.ResumeID == "" {
		if process, err := a.tmux.PaneProcess(a.ctx, name); err == nil {
			if id, err := bptmux.ClaudeProcessSession(process.PID, dir); err == nil && id != "" {
				if _, err := bptmux.ResolveSessionPath(bptmux.ClaudeProjectsRoot(), dir, name, id); err == nil {
					opts.ResumeID = id
				}
			}
		}
	}
	if opts.Launcher != "" {
		fresh, err := book.LoadFleet(book.Paths(a.config.Agentbooks))
		if err != nil {
			return err
		}
		reg.Local = fresh.Agents[name].Local
	}
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

// sender uses the same verified identity for envelopes and hierarchy gates.
func (a *app) sender() string { return a.senderIdentity().Label }

func (a *app) identityOptions() identity.Options {
	return identity.Options{Known: a.knownAgent, Origin: a.originProbe, Pane: func(ctx context.Context) (string, error) {
		if a.tmux == nil {
			return "", fmt.Errorf("tmux client unavailable")
		}
		return a.tmux.CallingSession(ctx)
	}, Thread: func(ctx context.Context, id string) identity.Identity {
		home := bptmux.CodexProcessInfo(0).Home
		who := book.ThreadIdentity(ctx, a.config.Agentbooks, home, id)
		if !who.Certain {
			if hint := book.LocalThreadHint(ctx, a.tmux, a.config.Agentbooks, home, id); hint.Label != "" {
				return hint
			}
		}
		return who
	}}
}

func (a *app) senderIdentity() identity.Identity {
	if a.resolveSender != nil {
		return a.resolveSender()
	}
	who := identity.Resolve(a.ctx, a.session(), a.identityOptions())
	// A read-only book override must not redefine the hierarchy for a verified
	// sender. Use the installation's configured books for authority.
	if override := os.Getenv("AGENTBOOK"); override != "" {
		configured := false
		for _, path := range a.config.Agentbooks {
			if filepath.Clean(path) == filepath.Clean(override) {
				configured = true
			}
		}
		if !configured {
			who.Label = "scope?:" + who.Label
			who.Certain = false
			who.Source = "unverified-book-override"
		}
	}
	return who
}

// session hands the resolver a tmux client, or a genuinely nil interface when
// there is none: a nil *tmux.Client stored in an interface is not nil, and the
// resolver would call through it.
func (a *app) session() identity.Sessioner {
	if a.tmux == nil {
		return nil
	}
	return a.tmux
}

// knownAgent reports agentbook membership. Used to stop a login name that
// happens to match an agent's name from inheriting that agent's standing; an
// unreadable book means "unknown", never "known".
func (a *app) knownAgent(name string) bool {
	fleet, err := book.LoadFleet(book.Paths(a.config.Agentbooks))
	if err != nil {
		return false
	}
	_, ok := fleet.Agents[name]
	return ok
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
	states := make(map[string]bpcache.State, len(folders))
	fleet, _ := book.LoadFleet(book.Paths(a.config.Agentbooks))
	for name, folder := range folders {
		agent := fleet.Agents[name]
		agent.Name = name
		agent.Folder = folder
		if a.tmux != nil {
			fleet.Agents[name] = agent
			state := book.RuntimeFor(a.ctx, a.tmux, fleet, name)
			states[name] = state
			continue
		}
		states[name] = bpcache.Read(bptmux.ClaudeProjectsRoot(), folder, name)
	}
	return states
}

// flushPending delivers the announcements that piled up while an agent was closed,
// as one digest, and clears the spool only for an outcome that could have put the
// text in the pane.
//
// It holds the pane lock for the whole Send. This is the OTHER half of the merge
// that cost a delivery on 2026-08-17: `bp open` was flushing this digest while
// another process pasted a message into the same composer, and one Enter submitted
// both. Send does not return until it has observed the composer clear again (or
// reported that it could not), so any bp waiting on this lock afterwards captures a
// pane that is genuinely past the digest — the handshake asked for in the incident
// review needs no extra probe of its own.
func (a *app) flushPending(name string) error {
	entries, dropped, err := pending.Load(a.config.StateDir, name)
	if errors.Is(err, pending.ErrReadOnly) {
		// This client cannot prune or clear the spool (a sandboxed agent with the
		// state tree mounted read-only — measured on probot-out-codex,
		// 2026-08-25). Delivering the digest anyway would repeat it on every
		// later message, since nothing could ever clear it; failing the whole
		// send would stop the agent talking at all, which is what happened.
		// So: the message goes through alone and the digest stays spooled for a
		// client that can write.
		fmt.Fprintf(a.err, "NOT: %s icin bekleyen duyurular EKLENMEDI — spool bu ortamdan salt-okunur (%v). Mesaj tek basina gonderiliyor; duyurular yazma izni olan bir istemcide teslim edilecek.\n", name, err)
		return nil
	}
	if err != nil || len(entries) == 0 {
		return err
	}
	release, lockErr := a.lockPane(name)
	if lockErr != nil {
		// Somebody else is typing into this pane. The spool is deliberately left
		// alone: an undelivered digest must stay pending, and the next `bp open` or
		// message delivery carries it.
		return lockErr
	}
	defer release()
	digest := formatDigest(entries, dropped)
	if err := a.tmux.Send(a.ctx, name, digest); err != nil {
		if !errors.Is(err, bptmux.ErrUnverified) {
			return err
		}
		// Unconfirmed, but the digest may well be in the pane: keeping the
		// entries would repeat every announcement on the next flush. Clear them
		// and let the caller report the doubt.
		if clearErr := pending.Acknowledge(a.config.StateDir, name, entries); clearErr != nil {
			return clearErr
		}
		// And leave the queue something it can recognise. If that paste is HANGING
		// in the composer, clearing the spool has just destroyed the only proof the
		// text is bp's: every later message would queue behind what now reads as a
		// stranger's line, and only a human could unblock the pane. A
		// never-paste-again record keeps the identity alive instead — the transcript
		// witness closes it if the digest did arrive, and the same dispatch pass
		// erases the copy left behind. The sender is "bp", so nobody is notified
		// about a digest that has no author to tell.
		if a.queue != nil && book.CanWitness(digest) {
			if _, enqueueErr := a.queue.EnqueueUnverified(name, "bp", digest); enqueueErr != nil {
				fmt.Fprintf(a.err, "WARNING: %s icin dogrulanamayan digest kuyruga islenemedi: %v\n", name, enqueueErr)
			}
		}
		return err
	}
	return pending.Acknowledge(a.config.StateDir, name, entries)
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

// forceBusyFlag asks for a delivery that does not wait for the target to be
// free. It is spelled out rather than abbreviated because it is meant to be
// typed deliberately.
const forceBusyFlag = "--force-busy"

// cutForceBusy takes the flag out of msg's arguments, wherever it stands BEFORE
// the message text begins.
//
// The window closes after the target name: the first non-flag argument is the
// name, the second is the first word of the message, and from there on nothing
// is read as an option. So `bp msg ada su komutu dene: --force-busy` delivers
// those words verbatim instead of quietly forcing itself.
func cutForceBusy(args []string) ([]string, bool) {
	rest, force := make([]string, 0, len(args)), false
	for _, arg := range args {
		if len(rest) < 2 && (arg == forceBusyFlag || arg == "--force") {
			force = true
			continue
		}
		rest = append(rest, arg)
	}
	return rest, force
}

func (a *app) message(args []string) error {
	args, force := cutForceBusy(args)
	if len(args) < 2 {
		return fmt.Errorf("usage: bp msg [--force|--force-busy] <name> <message...>")
	}
	name, raw := args[0], strings.Join(args[1:], " ")
	if err := messagetext.Validate(raw); err != nil {
		return err
	}
	message := strings.TrimSpace(raw)
	if err := rejectFlag("msg", name); err != nil {
		return err
	}
	if message == "" {
		return fmt.Errorf("empty message")
	}
	if strings.Contains(name, "@") {
		if force {
			// A federated target is a pane on somebody else's machine: its busy
			// state is not visible from here and its queue is not this queue, so
			// there is nothing here that could jump it.
			return fmt.Errorf("%s federe adreste calismaz: uzak pane'in mesguliyeti buradan gorulmuyor", forceBusyFlag)
		}
		target, peer, _, err := fed.ParseAddress(name)
		if err != nil {
			return err
		}
		return a.federatedMessage(target, peer, message)
	}
	resolved, err := a.resolveNativeTarget(name)
	if err != nil {
		return err
	}
	name = resolved
	who := a.senderIdentity()
	sender := who.Label
	if err := messagetext.Label(sender); err != nil {
		return err
	}
	if force {
		if err := a.allowForceBusy(who); err != nil {
			return err
		}
	}
	if sender == "" || sender == identity.Unknown {
		return fmt.Errorf("sender identity unavailable (%s: %s); message not sent or queued; inspect bp whoami", who.Source, who.Reason)
	}
	if a.queue != nil {
		a.queue.Sender = &msgq.SenderEvidence{Label: who.Label, ThreadID: who.ThreadID, Parent: who.Parent, Source: who.Source, Certain: who.Certain, Authority: who.Authoritative(), PID: os.Getpid()}
	}
	if strings.HasPrefix(message, "/") {
		if !who.Authoritative() {
			return fmt.Errorf("slash command refused: sender identity is not verified (%s)", who.Source)
		}
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
			if errors.Is(err, pending.ErrReadOnly) {
				// Honest refusal beats a silent drop: the target is closed, the
				// only store for it is unwritable here, so the message CANNOT be
				// kept and the sender has to know that now.
				return fmt.Errorf("%s kapali ve mesaj saklanamiyor: spool bu ortamdan salt-okunur (%w). Mesaji yazma izni olan bir agent/istemci uzerinden gonder", name, err)
			}
			return err
		}
		fmt.Fprintf(a.out, "queued for %s (offline; delivered when it opens)\n", name)
		return nil
	}
	attachPending := !strings.HasPrefix(message, "/")
	var entries []pending.Entry
	if attachPending {
		// Load prunes the spool, so it runs only on the branch that actually
		// delivers the digest — the one place the drop count is shown. A slash
		// command carries no digest: loading for it would trim the queue with
		// nobody ever told what went missing.
		loaded, dropped, err := pending.Load(a.config.StateDir, name)
		if err != nil {
			return err
		}
		entries = loaded
		message = "[" + sender + "] " + message
		if len(entries) > 0 {
			message = formatDigest(entries, dropped) + "\n\n" + message
		}
	}
	// Nothing goes into the pane while an identical message is still in flight. This
	// is the only guard that can stop the measured duplicate: an agent whose first
	// send came back "TESLIMAT BELIRSIZ" re-sent the same text twice within 33
	// seconds, all three pastes went into a streaming pane, and the recipient read
	// the same 441 characters three times. Neither the screen nor the transcript can
	// see that while it happens — the copies sit in the CLI's own input queue — so
	// the duplicate has to be refused at the source.
	if existing, ok := a.identicalInFlight(name, message); ok {
		a.reportInFlight(existing)
		return nil
	}
	if force {
		return a.forceMessage(name, sender, message, entries)
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
		if err := pending.Acknowledge(a.config.StateDir, name, entries); err != nil {
			return err
		}
	}
	switch {
	case unverified:
		if channelID != "" {
			fmt.Fprintf(a.out, "TESLIMAT BELIRSIZ: %s; transcript tanigi bekleniyor; durum: bp qstat %s\n", name, channelID)
			a.resultLine("unverified", channelID)
			return errReported
		}

		// Not a failure and not a delivery: the keystrokes went in and nothing
		// confirmed them. Never silently "sent" again (2026-08-01 incident).
		//
		// Where the message is long enough for the transcript witness to identify,
		// the doubt is handed to something that can actually resolve it: a queue
		// record marked never-paste-again. It can only be closed by the witness
		// (or, after a wait, by an honest "could not verify" back to the sender) —
		// and it can never produce a second copy, which the ordinary queue path
		// could.
		if book.CanWitness(message) {
			// One unresolved record per message, never two. A second unverified
			// record for the same text would double every later notice and would
			// make the duplicate guard above point at whichever copy it read first.
			if existing, ok := a.identicalInFlight(name, message); ok {
				a.reportInFlight(existing)
				return errReported
			}
			if channelID, enqueueErr := a.queue.EnqueueUnverified(name, sender, message); enqueueErr == nil {
				// The cause travels with the message: "Enter was never pressed"
				// and "Enter went in but nothing confirmed it" need different
				// things from a human, and until 2026-08-23 both printed the same
				// sentence (six hanging pastes in one salvo, no way to tell which).
				fmt.Fprintf(a.out, "TESLIMAT BELIRSIZ: %s — %s; tekrar gonderilmeyecek, VARSA transcript tanigi kontrol edecek (channel: %s). Durum: bp qstat %s\n", name, unverifiedCause(err), channelID, channelID)
				a.resultLine("unverified", channelID)
				return errReported
			}
		}
		fmt.Fprintf(a.out, "gonderildi ama DOGRULANAMADI: %s — pane'de mesaj gorulemedi, tekrar gondermeden once bp peek %s ile bak\n", name, name)
		a.resultLine("unverified", "")
		return errReported
	case notReady:
		fmt.Fprintf(a.out, "GONDERILEMEDI: %s — %s; mesaj kuyruga alindi (channel: %s). Durum: bp qstat %s\n", name, deliveryReason(err, bptmux.ErrNotReady), channelID, channelID)
		a.resultLine("queued", channelID)
		return nil
	case !queued:
		fmt.Fprintln(a.out, "sent")
		a.resultLine("delivered", channelID)
		return nil
	}
	fmt.Fprintf(a.out, "QUEUED (channel: %s). Check: bp qstat %s\n", channelID, channelID)
	// A queued message is not evidence the agent is working; print the cause.
	if why := a.queue.Reason(channelID); why != "" {
		fmt.Fprintf(a.out, "BEKLEME SEBEBI: %s — bak: bp peek %s\n", why, name)
	}
	a.resultLine("queued", channelID)
	return nil
}

// allowForceBusy decides who may put a message in front of a busy agent.
//
// It accepts only a verified main-agent identity. AGENT/--from/login labels,
// inherited app-server panes and CLI subagents do not establish that authority.
// Same-UID/root processes still require OS isolation for an adversarial boundary.
//
// What it protects is the property that makes the flag safe at all: forced
// messages are RARE. The plumbing that carries Tuna's own words (the WhatsApp
// bridge, root, bp itself) may interrupt a working agent; ordinary agent-to-agent
// traffic queues like everything else, or the queue's ordering guarantees mean
// nothing.
func (a *app) allowForceBusy(who identity.Identity) error {
	// Only the agentbook is read here, never the live fleet: the gate needs the
	// root's NAME, and asking tmux for states would make a refusal depend on which
	// panes happen to be open.
	root := "server-main"
	if fleet, err := book.LoadFleet(book.Paths(a.config.Agentbooks)); err == nil && fleet.Root != "" {
		root = fleet.Root
	}
	if who.Authoritative() {
		// These are the existing plumbing labels. The bridge's AGENT value
		// alone no longer establishes one of these identities.
		for _, allowed := range []string{root, "bp", "wa", "whatsapp"} {
			if who.Label == allowed {
				return nil
			}
		}
	}
	return fmt.Errorf("force-busy tesisata ayrilmis (root/bp/wa/whatsapp); gerekceni server-main'e yaz (kimlik: %s, kaynak: %s)",
		who.Label, who.Source)
}

// forceMessage queues a forced message and then makes the queue look at it at
// once, instead of waiting up to 30 seconds for the daemon's next pass.
//
// It goes through the QUEUE on purpose. Everything that keeps two writers out of
// one composer — the pane lock, the duplicate guard, one paste per pass, the
// transcript witness — lives in the dispatch path, and the bridge that used to
// paste into busy panes by hand had none of it. Forcing is therefore a flag on a
// record, never a shortcut around the delivery.
func (a *app) forceMessage(name, sender, message string, entries []pending.Entry) error {
	waiting := 0
	if rows, err := a.queue.List(); err == nil {
		for _, row := range rows {
			// Forced records already in the line are not "in the way": the new one
			// falls in behind them, in send order.
			if row.To == name && !row.ForceBusy {
				waiting++
			}
		}
	}
	channelID, err := a.queue.EnqueueUnique(name, sender, message, true, dedupWindow)
	if err != nil {
		return err
	}
	// The digest travelled inside this message, so the spool is cleared exactly as
	// on the ordinary path — leaving it would repeat every announcement.
	if len(entries) > 0 {
		if err := pending.Acknowledge(a.config.StateDir, name, entries); err != nil {
			return err
		}
	}
	if waiting > 0 {
		fmt.Fprintf(a.out, "uyari: hedefte %d bekleyen mesaj var; force sira disi teslim edilecek\n", waiting)
	}
	fmt.Fprintf(a.out, "FORCE kuyrukta: %s (channel: %s). Durum: bp qstat %s\n", name, channelID, channelID)
	a.dispatchNow()
	// The verdict is read back from the record itself, not from the pass's report
	// lines: the pass may have been a no-op (daemon held the dispatch lock) and
	// the record then delivers within the daemon's next tick. "queued" therefore
	// means "in flight", never "failed".
	if status, done := a.queue.Finished(channelID); done {
		if status == "delivered (unverified)" {
			a.resultLine("unverified", channelID)
			return errReported
		}
		if strings.HasPrefix(status, "delivered") {
			a.resultLine("delivered", channelID)
			return nil
		}
	}
	a.resultLine("queued", channelID)
	return nil
}

// dispatchNow runs one dispatch pass from the CLI so a forced message does not
// wait for the daemon's tick.
//
// The pass is the daemon's own, probes included: without the transcript witness
// and the turn probe this pass would be a WEAKER writer than the daemon — it
// would paste into a streaming agent and re-paste a message the transcript has
// already seen. The dispatch lock is non-blocking, so if the daemon happens to be
// mid-pass this call does nothing at all and the daemon delivers within its
// tick; that race needs no coordination beyond the lock itself.
func (a *app) dispatchNow() {
	if a.queue == nil || a.tmux == nil {
		return
	}
	a.queue.CanWitness = book.CanWitness
	if len(a.config.Agentbooks) > 0 {
		projects := bptmux.ClaudeProjectsRoot()
		a.queue.Witness = book.DeliveryWitness(a.config.Agentbooks, projects)
		a.queue.CanWitness = book.CanWitness
		a.queue.HasTranscript = book.TranscriptExists(a.config.Agentbooks, projects)
		a.queue.TurnOpen = book.TurnOpenProbe(a.config.Agentbooks, projects)
		a.queue.RuntimeBlock = book.RuntimeBlockProbe(a.config.Agentbooks)
		a.queue.Binding = book.DeliveryBindingProbe(a.config.Agentbooks)
	}
	if err := a.queue.Dispatch(a.ctx, a.tmux, func(line string) { fmt.Fprintln(a.err, line) }); err != nil {
		fmt.Fprintf(a.err, "WARNING: teslim pass'i calistirilamadi: %v\n", err)
	}
}

// dedupWindow is how long an identical message to the same target counts as still
// on the way. It is sized on the measured retry burst — three sends of the same 441
// characters inside 33 seconds — with room for the slower version of the same
// mistake: an agent that re-sends after a minute or two of silence. Ten minutes is
// also the horizon inside which a queued message is normally either delivered or
// settled, so a legitimate "say it again, it never arrived" after that still goes
// through untouched.
const dedupWindow = 10 * time.Minute

// identicalInFlight reports an existing queue record for this target carrying
// exactly this text, queued within dedupWindow. Records that will never be pasted
// again count too: their text may already be in the agent, which is the strongest
// possible reason not to send a second copy.
func (a *app) identicalInFlight(name, message string) (msgq.Message, bool) {
	if a.queue == nil {
		return msgq.Message{}, false
	}
	return a.queue.RecentIdentical(name, message, dedupWindow)
}

// reportInFlight refuses a duplicate OUT LOUD, and with the existing record's live
// status in the same breath.
//
// That second half is the point, and it is what keeps this guard from backfiring.
// The sender did not repeat itself out of stubbornness — it repeated itself because
// it could not SEE that the message had landed. Forbidding the retry without
// showing the state would push the same sender onto a channel bp cannot see at all
// (WhatsApp, a file, keys typed into tmux by hand), and the duplicate would simply
// become invisible. So the record's status is quoted verbatim, together with the
// one command that overrides the refusal: a silent success is worse than a loud
// failure.
func (a *app) reportInFlight(existing msgq.Message) {
	status, err := a.queue.Status(existing.ID)
	if err != nil || status == "" {
		status = existing.Reason
	}
	fmt.Fprintf(a.out, "AYNI METIN ZATEN YOLDA — kanal %s. Durum: %s\n", existing.ID, status)
	fmt.Fprintf(a.out, "Bekle ya da israr icin: bp qcancel %s && bp msg ...\n", existing.ID)
	a.resultLine("duplicate", existing.ID)
}

// resultLine is the STABLE machine-readable outcome of a `bp msg` run, printed
// as the LAST line of output. It exists under a contract (server-whatsapp,
// 2026-08-21): the bridge used to branch on the Turkish prose above it with
// regexes, and a rewording would have silently sent it down the wrong path.
//
// The contract: the final line is `RESULT=<verdict>` with an optional
// ` CHANNEL=<id>`, verdict is one of delivered|queued|unverified|duplicate, and
// neither the keys nor the verdict words ever change — new information arrives
// as NEW keys appended to the line, never by renaming these. Failures that
// return an error (unknown target, refused flag) keep signalling through the
// exit code, as they always have.
func (a *app) resultLine(verdict, channel string) {
	if channel != "" {
		fmt.Fprintf(a.out, "RESULT=%s CHANNEL=%s\n", verdict, channel)
		return
	}
	fmt.Fprintf(a.out, "RESULT=%s\n", verdict)
}

func (a *app) federatedMessage(target, peer, message string) error {
	who := a.senderIdentity()
	if who.Label == "" || who.Label == identity.Unknown {
		return fmt.Errorf("sender identity unavailable (%s: %s); message not sent or queued; inspect bp whoami", who.Source, who.Reason)
	}
	if a.config.P2P != nil && a.config.P2P.Enabled {
		return a.p2pMessage(target, peer, message)
	}
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
	if err := messagetext.Validate(message); err != nil {
		return false, "", err
	}
	if err := messagetext.Label(sender); err != nil {
		return false, "", err
	}
	if a.deliverMessage != nil {
		return a.deliverMessage(name, sender, message)
	}
	if !a.tmux.HasSession(a.ctx, name) {
		return false, "", fmt.Errorf("no open session named %s", name)
	}
	channelID, err = a.queue.EnqueueUnique(name, sender, message, false, dedupWindow)
	if err != nil {
		return false, "", err
	}
	a.dispatchNow()
	record, readErr := a.queue.Record(channelID)
	if readErr != nil {
		return true, channelID, readErr
	}
	if record.Status == "delivered (unverified)" || (record.Status == "" && record.NoRepaste) {
		return true, channelID, bptmux.ErrUnverified
	}
	if strings.HasPrefix(record.Status, "delivered") {
		return false, channelID, nil
	}
	if record.Attempts > 0 {
		return true, channelID, fmt.Errorf("%w: %s", bptmux.ErrNotReady, record.Reason)
	}
	return true, channelID, nil
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
	if err := messagetext.Validate(strings.Join(words, " ")); err != nil {
		return err
	}
	who := a.senderIdentity()
	if !who.Authoritative() {
		return fmt.Errorf("sender identity is not verified (%s)", who.Source)
	}
	sender := who.Label
	if err := messagetext.Label(sender); err != nil {
		return err
	}
	fleet, states, err := a.fleet()
	if err != nil {
		return err
	}
	if _, ok := fleet.Agents[sender]; !ok && sender != "server-main" {
		return fmt.Errorf("sender %s is not in the agentbook hierarchy", sender)
	}
	targets := announcementCandidates(fleet, sender)
	cacheStates := a.cacheStates(fleet, states)
	messageText := strings.Join(words, " ")
	message := fmt.Sprintf("[ANNOUNCE %s] %s", sender, messageText)
	var tally deliveryTally
	deferred, coldCost, warm := 0, 0, 0
	for _, target := range targets {
		cacheState := cacheStates[target]
		live := states[target].Alive
		hint, _ := cacheState.CacheHint()
		if !live || hint != "warm~" {
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
		fmt.Fprintf(a.out, "deferred context: ~%s tokens (%d targets: %d estimated warm, %d cold/unknown)\n", humanTokens(coldCost), len(targets), warm, deferred)
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
		if name == "server-main" || name == fleet.Root {
			continue // never compact the orchestrator
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
//
// The transcript gate is part of the answer because the frame alone misses a
// streaming turn entirely, and this predicate guards /compact — a slash command
// typed into a pane that is mid-answer, which is the worst interruption bp can
// deliver.
func (a *app) paneBusy(name string) bool {
	pane, err := a.capture(name)
	if err != nil {
		return true
	}
	return bptmux.Typing(pane) || bptmux.Busy(pane) || a.turnOpen(name)
}

// turnOpen asks the target's own transcript whether a turn is running. The probe
// is built on first use rather than in main, so the commands that never ask
// never load an agentbook, and so a test can stub it. A missing probe (nothing
// resolvable, an app assembled without config) answers false and leaves the
// screen's verdict standing.
func (a *app) deliveryRuntimeReason(name string) string {
	if a.turnOpenProbe != nil || len(a.config.Agentbooks) == 0 {
		if a.turnOpen(name) {
			return "runtime blocked"
		}
		return ""
	}
	return book.RuntimeBlockProbe(a.config.Agentbooks)(name, false)
}

func (a *app) turnOpen(name string) bool {
	if a.turnOpenProbe == nil {
		if len(a.config.Agentbooks) == 0 {
			return false
		}
		a.turnOpenProbe = book.TurnOpenProbe(a.config.Agentbooks, bptmux.ClaudeProjectsRoot())
	}
	return a.turnOpenProbe(name)
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
	who := a.senderIdentity()
	if !who.Authoritative() {
		return fmt.Errorf("sender identity is not verified (%s)", who.Source)
	}
	sender := who.Label
	if err := messagetext.Label(sender); err != nil {
		return err
	}
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
		for index := range rows {
			if sender != fleet.Root && !fleet.IsDescendant(rows[index].Name, sender) {
				rows[index].Send, rows[index].Reason = false, "hiyerarsi disinda"
			}
		}
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
		// The clearing and the send are ONE operation on one composer: bp empties it
		// and then types into it, and another bp pasting in between would be typing
		// into a composer this command has just wiped. So both happen under a single
		// pane lock, which deliver() then re-enters instead of fighting.
		release, lockErr := a.lockPane(target)
		if lockErr != nil {
			// Another bp owns the pane. Skip, never queue: a /compact that lands
			// after the next turn compacts the wrong conversation.
			rows[index].Send, rows[index].Reason = false, compactBusy
			continue
		}
		// Same clearing step as bp rename, for the same reason: /compact is a
		// slash command bp types itself, and a composer left in a state that only
		// LOOKS empty makes the send bounce off with "composer is not empty".
		// ErrBusy/ErrTyping are the pane saying it is in use — skip it exactly
		// like the check above, never queue (a /compact delivered after the next
		// turn compacts the wrong conversation).
		clearErr := a.clearComposer(target)
		var queued bool
		var channelID string
		var deliveryErr error
		if clearErr == nil {
			queued, channelID, deliveryErr = a.deliver(target, sender, "/compact")
		}
		release()
		if clearErr != nil {
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
			// A waiting message says why it waits. Without this line a target
			// blocked by an unreadable paste (or a fragment too short to identify)
			// looks exactly like an agent that is merely working.
			if row.Reason != "" {
				fmt.Fprintf(a.out, "    ! %s — bak: bp peek %s\n", row.Reason, row.To)
			}
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
	if len(args) > 0 && strings.HasPrefix(args[0], "p") {
		return a.p2pChannelStatus(args)
	}
	if len(args) == 2 && args[1] == "--json" {
		m, err := a.queue.Record(args[0])
		if err != nil {
			return err
		}
		return json.NewEncoder(a.out).Encode(m)
	}
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
	name, err := a.resolveNativeTarget(args[0])
	if err != nil {
		return err
	}
	pane, err := a.tmux.Capture(a.ctx, name)
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
		to, reply, from, index := "", "", "", 1
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
			case "--from":
				if index+1 >= len(args) {
					return fmt.Errorf("--from requires a label")
				}
				from = args[index+1]
				index += 2
			default:
				goto message
			}
		}
	message:
		text := strings.Join(args[index:], " ")
		if text == "" {
			return fmt.Errorf("usage: bp wa send [--to <target>] [--reply <msgId>] [--from <label>] <message...>")
		}
		if from != "" {
			// Inside a pane the tmux session is the sender and it wins, so a
			// --from here could only be an attempt to sign as somebody else —
			// refuse it out loud instead of silently ignoring it.
			if os.Getenv("TMUX") != "" {
				return fmt.Errorf("--from is not accepted inside tmux: the pane's own session is the sender")
			}
			if err := identity.ValidFrom(from); err != nil {
				return err
			}
		}
		identityOpts := a.identityOptions()
		identityOpts.From = from
		who := wa.Agent(a.ctx, a.session(), identityOpts)
		agent := who.Label
		if !who.Certain {
			// The label is all the attribution a phone gets. Say so on stderr
			// rather than let a guess pass for a signature.
			fmt.Fprintf(a.err, "WARNING: sender not established (%s); sending as [%s]. Use --from <label> to state who is sending.\n", who.Source, agent)
		}
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
		// The label is echoed because it is what the recipient will read.
		fmt.Fprintf(a.out, "queued -> %s as [%s]%s\n", destination, agent, suffix)
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
	// The collector writes a null both when codex was idle and when its OAuth
	// session is dead, so the local session file is what lets the renderer name
	// which one happened. Reading it is offline and cheap.
	opts := usagecli.Options{Now: time.Now(), Auth: codexauth.Check()}
	for _, line := range usagecli.Lines(sample, opts) {
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
	renderOptions := monitorcli.RenderOptions{Now: time.Now(), CodexAuth: codexauth.Check()}
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
