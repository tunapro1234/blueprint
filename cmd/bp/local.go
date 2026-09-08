package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/cache"
	"blueprint/internal/identity"
	bptmux "blueprint/internal/tmux"
)

func localHarness(name string) bool {
	return name == "codex" || name == "claude" || name == "opencode" || name == "hermes" || name == "custom"
}

// Administrative and batch commands must retain their pipes and exit status.
func batchCommand(harness string, args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" || arg == "--version" || arg == "-v" || (harness == "claude" && (arg == "-p" || arg == "--print")) {
			return true
		}
	}
	if len(args) == 0 {
		return false
	}
	commands := map[string]string{
		"codex":    " exec e review login logout mcp mcp-server app-server completion sandbox debug apply cloud features ",
		"claude":   " auth mcp doctor install update upgrade plugin plugins setup-token ",
		"opencode": " run serve web auth models mcp agent acp github export import stats upgrade uninstall debug ",
		"hermes":   " setup model tools config doctor update version ",
	}
	values := map[string]string{
		"codex":    " -c --config -m --model -p --profile -C --cd -i --image -a --ask-for-approval -s --sandbox --remote --enable --disable --add-dir ",
		"claude":   " --model --effort --permission-mode --settings --setting-sources --append-system-prompt --system-prompt --output-format --input-format ",
		"opencode": " -m --model --agent --port --hostname --log-level ",
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return false
		}
		if strings.HasPrefix(arg, "-") {
			if strings.Contains(values[harness], " "+arg+" ") {
				i++
			}
			continue
		}
		return strings.Contains(commands[harness], " "+arg+" ")
	}
	return false
}

func terminal(file *os.File) bool {
	probe := exec.Command("/bin/sh", "-c", "test -t 0")
	probe.Stdin = file
	return probe.Run() == nil
}

func quoteShell(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func (a *app) initLocalBook() error {
	if a.config.Legacy {
		return fmt.Errorf("bp run/setup is for local installations; use bp open here, or set a separate BP_HOME")
	}
	paths := book.Paths(a.config.Agentbooks)
	if len(paths) == 0 {
		return fmt.Errorf("agentbook is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(paths[0]), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(paths[0], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = f.WriteString("{\"orchestrator\":\"local\",\"agents\":[]}\n")
	return errors.Join(err, f.Close())
}

func (a *app) localRun(args []string) error {
	name := ""
	if len(args) >= 2 && args[0] == "--name" {
		name, args = args[1], args[2:]
	}
	if len(args) == 0 || !localHarness(args[0]) {
		return fmt.Errorf("usage: bp run [--name <name>] <codex|claude|opencode|hermes> [arguments...]")
	}
	programName := args[0]
	if programName == "custom" {
		if len(args) < 2 {
			return fmt.Errorf("bp run custom requires an executable")
		}
		programName = args[1]
		args = append([]string{"custom"}, args[2:]...)
	}
	program, err := exec.LookPath(programName)
	if err != nil {
		return fmt.Errorf("install %s first: %w", args[0], err)
	}
	program, err = filepath.Abs(program)
	if err != nil {
		return err
	}
	if os.Getenv("TMUX") != "" || !terminal(os.Stdin) || !terminal(a.out) || batchCommand(args[0], args[1:]) {
		return syscall.Exec(program, append([]string{program}, args[1:]...), os.Environ())
	}
	a.updateNotice()
	if _, err := exec.LookPath(a.tmux.Bin); err != nil {
		return fmt.Errorf("tmux is required; rerun the local installer: %w", err)
	}
	if name != "" && (!identity.ValidName(name) || len(name) > 64) {
		return fmt.Errorf("invalid agent name: %s", name)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	logicalCWD := cwd
	cwd = physicalPath(cwd)
	if err := a.initLocalBook(); err != nil {
		return err
	}
	var resumeGuard *localResumeGuard
	if args[0] == "claude" {
		thread, resolved, err := claudeResumeArgs(args[1:], logicalCWD, bptmux.ClaudeProjectsRoot())
		if err != nil {
			return err
		}
		if thread != "" {
			var existing string
			resumeGuard, existing, err = a.guardClaudeResume(thread, name, cwd)
			if err != nil {
				return err
			}
			defer resumeGuard.close()
			if existing != "" {
				resumeGuard.close()
				return a.attachLocal(existing)
			}
			name = resumeGuard.name
			args = append([]string{"claude"}, resolved...)
		}
	}
	if name == "" {
		var suffix [3]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return err
		}
		folder := strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
				return r
			}
			return '-'
		}, filepath.Base(cwd))
		if len(folder) > 28 {
			folder = folder[:28]
		}
		name = args[0] + "-" + folder + "-" + hex.EncodeToString(suffix[:])
	}
	if err := a.initLocalBook(); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	// Only our window runs this launcher. Exiting the agent exits the pane;
	// there is no interactive shell underneath it and no global tmux changes.
	cliArgs, observationPath, err := a.prepareLocalObservation(args[0], args[1:])
	if err != nil {
		return err
	}
	command := []string{self, "_session", name, args[0], observationPath, program}
	command = append(command, cliArgs...)
	for index := range command {
		command[index] = quoteShell(command[index])
	}
	argv := []string{"new-session", "-s", name, "-c", cwd}
	if resumeGuard != nil {
		argv = append(argv, "-d")
	}
	for _, key := range []string{"BP_HOME", "PATH", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "AGENTBOOK"} {
		value, ok := os.LookupEnv(key)
		if key == "BP_HOME" {
			value, ok = a.config.Home, true
		}
		if ok {
			argv = append(argv, "-e", key+"="+value)
		}
	}
	argv = append(argv, "exec "+strings.Join(command, " "))
	fmt.Fprintf(a.out, "bp: %s — messages: bp msg %s <text>\n", name, name)
	cmd := exec.Command(a.tmux.Bin, argv...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, a.out, a.err
	if err := cmd.Run(); err != nil {
		return err
	}
	if resumeGuard != nil {
		if err := a.recordClaudeResume(resumeGuard); err != nil {
			return err
		}
		resumeGuard.close()
		return a.attachLocal(name)
	}
	return nil
}

// localSession replaces itself with the CLI. tmux sees the real harness as its
// foreground process, and no supervising shell remains when that process exits.
func (a *app) localSession(args []string) error {
	if len(args) < 4 || os.Getenv("TMUX") == "" || !identity.ValidName(args[0]) || !localHarness(args[1]) || !filepath.IsAbs(args[3]) {
		return fmt.Errorf("invalid internal session invocation")
	}
	name := args[0]
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cwd = physicalPath(cwd)
	if err := a.initLocalBook(); err != nil {
		return err
	}
	cmd := exec.Command(a.tmux.Bin, "set-option", "-w", "-t", "="+name+":", "remain-on-exit", "off")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("configure own pane: %w: %s", err, output)
	}
	fleet, err := book.LoadFleet(book.Paths(a.config.Agentbooks))
	if err != nil {
		return err
	}
	var local *cache.LocalBinding
	if args[2] != "" {
		nativeHome := os.Getenv("CODEX_HOME")
		if nativeHome == "" {
			userHome, _ := os.UserHomeDir()
			nativeHome = filepath.Join(userHome, ".codex")
		}
		if absolute, err := filepath.Abs(nativeHome); err == nil {
			nativeHome = absolute
		}
		if realHome, err := filepath.EvalSymlinks(nativeHome); err == nil {
			nativeHome = realHome
		}
		localCWD := cwd
		if realCWD, err := filepath.EvalSymlinks(cwd); err == nil {
			localCWD = realCWD
		}
		local = &cache.LocalBinding{Path: args[2], PID: os.Getpid(), Harness: args[1], Home: nativeHome, CWD: localCWD}
		if err := os.WriteFile(filepath.Join(filepath.Dir(args[2]), "pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			return err
		}
	}
	if err := book.SetStatus(a.config.Agentbooks, name, "open", cwd, book.Registration{Role: "local CLI", Parent: fleet.Root, Local: local}); err != nil {
		return err
	}
	defer book.SetStatus(a.config.Agentbooks, name, "closed", "", book.Registration{}) // exec failure only
	if err := a.configureLocalBar(name); err != nil {
		return err
	}
	if err := os.MkdirAll(a.config.StateDir, 0700); err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(a.config.StateDir, "local-delivery.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	self, err := os.Executable()
	if err != nil {
		return err
	}
	worker := exec.Command(self, "_local-worker", name, strconv.Itoa(os.Getpid()))
	worker.Stdout, worker.Stderr = log, log
	worker.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := worker.Start(); err != nil {
		return err
	}
	// The worker belongs to this process, whose PID exec preserves. It exits
	// when its parent dies, even if the terminal was detached in the meantime.
	_ = worker.Process.Release()
	env := append(os.Environ(), "BP_SESSION="+name, "AGENT="+name)
	return syscall.Exec(args[3], args[3:], env)
}

// configureLocalBar changes only this bp-owned session's display. Explicit env
// keeps status jobs on the same installation even when a tmux server is older
// than the invoking terminal's PATH/BP_HOME. No global options or key bindings.
func (a *app) configureLocalBar(name string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	env := "env BP_HOME=" + quoteShell(a.config.Home)
	for _, key := range []string{"HOME", "PATH", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "AGENTBOOK"} {
		if value, ok := os.LookupEnv(key); ok {
			env += " " + key + "=" + quoteShell(value)
		}
	}
	command := func(sub string) string {
		text := env + " " + quoteShell(self) + " " + sub + " " + quoteShell(name)
		return "#(" + strings.ReplaceAll(text, "#", "##") + ")"
	}
	mouse := "off"
	if a.config.LocalMouse {
		mouse = "on"
	}
	for _, option := range [][2]string{
		{"mouse", mouse},
		{"status", "on"}, {"status-style", "bg=" + barGap + ",fg=colour231"},
		{"status-left", command("name")}, {"status-right", command("bar")},
		{"status-left-length", "48"}, {"status-right-length", "120"}, {"status-interval", "2"},
	} {
		if err := a.tmux.SetOption(a.ctx, "="+name+":", option[0], option[1]); err != nil {
			return fmt.Errorf("configure bp bar for %s: %w", name, err)
		}
	}
	return nil
}

func (a *app) refreshLocalBars() error {
	if a.tmux == nil {
		return nil
	}
	fleet, err := book.LoadFleet(book.Paths(a.config.Agentbooks))
	if err != nil {
		return err
	}
	for name, agent := range fleet.Agents {
		if agent.Role != "local CLI" || !a.tmux.HasSession(a.ctx, name) {
			continue
		}
		// Old local launches already set BP_HOME on their own tmux session.
		out, err := exec.CommandContext(a.ctx, a.tmux.Bin, "show-environment", "-t", "="+name, "BP_HOME").Output()
		if err != nil || strings.TrimSpace(string(out)) != "BP_HOME="+a.config.Home {
			continue
		}
		if err := a.configureLocalBar(name); err != nil {
			return err
		}
		if agent.Local == nil && a.config.LocalObservation {
			fmt.Fprintf(a.out, "%s: automatic model/context observation starts on its next CLI launch; the running session was preserved.\n", name)
		}
	}
	return nil
}

func (a *app) localWorker(args []string) error {
	if len(args) != 2 || !identity.ValidName(args[0]) {
		return fmt.Errorf("invalid local worker")
	}
	parent, err := strconv.Atoi(args[1])
	if err != nil || parent <= 1 {
		return fmt.Errorf("invalid worker parent")
	}
	defer func() {
		if err := book.SetStatus(a.config.Agentbooks, args[0], "closed", "", book.Registration{}); err != nil {
			fmt.Fprintln(a.err, "record closed session:", err)
		}
	}()
	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	a.ctx = ctx
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for os.Getppid() == parent {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
		cancel()
	}()
	defer func() { cancel(); <-done }()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			a.dispatchNow()
		}
	}
}
