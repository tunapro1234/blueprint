package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"blueprint/internal/book"
	"blueprint/internal/daemon"
	"blueprint/internal/msgq"
	bptmux "blueprint/internal/tmux"
	"blueprint/internal/usagecli"
	"blueprint/internal/wa"
)

const usage = `blueprint (bp) — agent infrastructure CLI

bp status | bp tree
bp open <name> <directory> [--resume] [--codex] [--no-prompt]
bp close <name>
bp msg <name> <message...>
bp announce <message...>
bp q | bp qstat <channel-id>
bp peek <name> [n]
bp wa send [--to <target>] [--reply <msgId>] <message...>
bp wa read <target> [n] | bp wa chats
bp usage
bp policy status|override <hours>
bp service
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
	case "close":
		return a.close(args[1:])
	case "msg":
		return a.message(args[1:])
	case "announce":
		return a.announce(args[1:])
	case "q":
		return a.queueList(args[1:])
	case "qstat":
		return a.queueStatus(args[1:])
	case "peek":
		return a.peek(args[1:])
	case "wa":
		return a.whatsapp(args[1:])
	case "usage":
		return a.usage()
	case "policy":
		return a.policy(args[1:])
	case "service":
		return a.service()
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
		return fmt.Errorf("usage: bp open <name> <directory> [--resume] [--codex] [--no-prompt]")
	}
	name, dir := args[0], args[1]
	opts := bptmux.OpenOptions{}
	for _, arg := range args[2:] {
		switch arg {
		case "--resume":
			opts.Resume = true
		case "--codex":
			opts.Codex = true
		case "--no-prompt":
			opts.NoPrompt = true
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
	pane, err := a.tmux.Capture(a.ctx, name)
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
