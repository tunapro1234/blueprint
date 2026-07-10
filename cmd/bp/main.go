package main

import (
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

const usage = `blueprint (bp) — agentic altyapi CLI

bp status | bp tree
bp open <ad> <dizin> [--resume] [--codex] [--no-prompt]
bp close <ad>
bp msg <ad> <mesaj...>
bp q | bp qstat <kanal-id>
bp peek <ad> [n]
bp wa send [--to <hedef>] [--reply <msgId>] <mesaj...>
bp wa read <hedef> [n] | bp wa chats
bp usage
bp policy status|override <saat>
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
		fmt.Fprintln(os.Stderr, "HATA:", err)
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
		return fmt.Errorf("bilinmeyen komut: %s", args[0])
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
		tmuxState := "-"
		if alive && state.Busy {
			tmuxState = "CALISIYOR"
		} else if alive {
			tmuxState = "bosta"
		}
		bookState := fleet.Agents[name].Status
		if bookState == "" {
			bookState = "?"
		}
		flag := ""
		if !alive && bookState == "open" {
			flag = "  <-- book:open ama tmux YOK"
		}
		if alive && bookState == "closed" {
			flag = "  <-- tmux acik ama book:closed"
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
		live := "kapali"
		if state, ok := states[name]; ok {
			if state.Busy {
				live = "CALISIYOR"
			} else {
				live = "bosta"
			}
		}
		label := name
		if agent.Nickname != "" {
			label += " (" + agent.Nickname + ")"
		}
		status := agent.Status
		if status == "" {
			status = "kayitsiz"
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
		return fmt.Errorf("kullanim: bp open <ad> <dizin> [--resume] [--codex] [--no-prompt]")
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
			return fmt.Errorf("bilinmeyen open secenegi: %s", arg)
		}
	}
	if a.tmux.HasSession(a.ctx, name) {
		fmt.Fprintf(a.out, "%s zaten acik\n", name)
		return nil
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("dizin yok: %s", dir)
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
	fmt.Fprintf(a.out, "%s ACIK%s\n", name, rc)
	return nil
}

func (a *app) close(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("kullanim: bp close <ad>")
	}
	name := args[0]
	if a.tmux.HasSession(a.ctx, name) {
		if err := a.tmux.Close(a.ctx, name); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s kapatildi (gecmis jsonl'de durur)\n", name)
	} else {
		fmt.Fprintf(a.out, "%s zaten kapali\n", name)
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
		return fmt.Errorf("kullanim: bp msg <ad> <mesaj...>")
	}
	name, message := args[0], strings.Join(args[1:], " ")
	if !a.tmux.HasSession(a.ctx, name) {
		return fmt.Errorf("%s diye acik oturum yok", name)
	}
	pane, err := a.tmux.Capture(a.ctx, name)
	if err != nil {
		return err
	}
	if bptmux.Typing(pane) || bptmux.Busy(pane) {
		id, enqueueErr := a.queue.Enqueue(name, a.sender(), message)
		if enqueueErr != nil {
			return enqueueErr
		}
		fmt.Fprintf(a.out, "MESGUL: kuyruga alindi (kanal: %s).\n%s bosalinca otomatik gonderilecek. Teslim kontrolu (istedigin zaman):\n  bp qstat %s\n(bekliyor / iletildi / iptal doner. Bildirim GELMEZ - merak edersen bakarsin.)\n", id, name, id)
		return nil
	}
	if err = a.tmux.Send(a.ctx, name, message); err == nil {
		fmt.Fprintln(a.out, "gonderildi")
		return nil
	}
	if !errors.Is(err, bptmux.ErrTyping) {
		return err
	}
	id, enqueueErr := a.queue.Enqueue(name, a.sender(), message)
	if enqueueErr != nil {
		return enqueueErr
	}
	fmt.Fprintf(a.out, "son anda doldu -> kuyruga alindi (kanal: %s). Kontrol: bp qstat %s\n", id, id)
	return nil
}

func (a *app) queueList(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("kullanim: bp q")
	}
	rows, err := a.queue.List()
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(a.out, "(kuyruk bos)")
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
		return fmt.Errorf("kullanim: bp qstat <kanal-id>")
	}
	status, err := a.queue.Status(args[0])
	if err == nil {
		fmt.Fprintln(a.out, status)
	}
	return err
}

func (a *app) peek(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("kullanim: bp peek <ad> [n]")
	}
	count := 8
	var err error
	if len(args) == 2 {
		count, err = strconv.Atoi(args[1])
		if err != nil || count < 1 {
			return fmt.Errorf("gecersiz satir sayisi: %s", args[1])
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
		return fmt.Errorf("kullanim: bp wa send|read|chats ...")
	}
	switch args[0] {
	case "send":
		to, reply, index := "", "", 1
		for index < len(args) {
			switch args[index] {
			case "--to":
				if index+1 >= len(args) {
					return fmt.Errorf("--to hedef bekliyor")
				}
				to = args[index+1]
				index += 2
			case "--reply":
				if index+1 >= len(args) {
					return fmt.Errorf("--reply msgId bekliyor")
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
			return fmt.Errorf("kullanim: bp wa send [--to <hedef>] [--reply <msgId>] <mesaj...>")
		}
		if err := wa.Send(wa.DefaultOutbox, wa.Agent(a.ctx, a.tmux), to, reply, text); err != nil {
			return err
		}
		destination := to
		if destination == "" {
			destination = "<varsayilan kanal>"
		}
		suffix := ""
		if reply != "" {
			suffix = " (reply: " + reply + ")"
		}
		fmt.Fprintf(a.out, "kuyruga alindi -> %s%s\n", destination, suffix)
		return nil
	case "read":
		if len(args) < 2 || len(args) > 3 {
			return fmt.Errorf("kullanim: bp wa read <grup/kisi> [n]")
		}
		count := 15
		var err error
		if len(args) == 3 {
			count, err = strconv.Atoi(args[2])
			if err != nil || count < 1 {
				return fmt.Errorf("gecersiz mesaj sayisi: %s", args[2])
			}
		}
		lines, err := wa.Read(wa.DefaultStore, args[1], count)
		if err != nil {
			return err
		}
		if len(lines) == 0 {
			fmt.Fprintln(a.out, "(kayit yok)")
		} else {
			for _, line := range lines {
				fmt.Fprintln(a.out, line)
			}
		}
		return nil
	case "chats":
		if len(args) != 1 {
			return fmt.Errorf("kullanim: bp wa chats")
		}
		lines, err := wa.Chats(wa.DefaultStore)
		if err != nil {
			return err
		}
		if len(lines) == 0 {
			fmt.Fprintln(a.out, "(kayit yok)")
		} else {
			for _, line := range lines {
				fmt.Fprintln(a.out, line)
			}
		}
		return nil
	default:
		return fmt.Errorf("kullanim: bp wa send|read|chats ...")
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
		return fmt.Errorf("kullanim: bp policy status|override <saat>")
	}
	cmd := exec.CommandContext(a.ctx, "/srv/server-main/bin/usage-policy", args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = a.out, a.err, os.Stdin
	return cmd.Run()
}

func (a *app) service() error {
	jobs, err := daemon.LoadState(daemon.StatePath)
	if os.IsNotExist(err) {
		fmt.Fprintln(a.out, "(daemon durumu yok)")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%-18s %-9s %-25s %-25s %s\n", "IS", "DURUM", "SON", "SONRAKI", "HATA")
	for _, name := range daemon.StateNames(jobs) {
		job := jobs[name]
		fmt.Fprintf(a.out, "%-18s %-9s %-25s %-25s %s\n", name, job.Status, job.LastRun, job.NextRun, job.Error)
	}
	return nil
}

func (a *app) daemon(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("kullanim: bp daemon")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	service := daemon.New(log.New(a.err, "blueprint: ", log.LstdFlags))
	service.Run(ctx)
	return nil
}
