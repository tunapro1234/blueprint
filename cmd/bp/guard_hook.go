package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	bpconfig "blueprint/internal/config"
	"blueprint/internal/guard"
	"blueprint/internal/identity"
	"blueprint/internal/msgq"
	bptmux "blueprint/internal/tmux"
)

const guardHookUsage = "usage: bp guard status | bp guard hook claude [--ask] [--canary <path>]... (reads the PreToolUse payload on stdin)"

// guardHookMain runs `bp guard hook <harness>` before the normal startup, so
// a tool call that touches nothing sensitive costs one stdin read and a
// regexp. It always exits 0: a broken guard must never break the agent, so
// errors go to stderr only.
func guardHookMain(args []string, stdin io.Reader, stdout, stderr io.Writer) {
	cfg, err := parseGuardHookArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return
	}
	in, err := guard.ParseHook(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "bp guard hook: unreadable payload:", err)
		return
	}
	what, canary, hit := cfg.Match(in)
	if !hit {
		return
	}
	config, err := bpconfig.Load()
	if err != nil {
		fmt.Fprintln(stderr, "bp guard hook:", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Hook stdout is read by the harness, so nothing from the app may reach it.
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		fmt.Fprintln(stderr, "bp guard hook:", err)
		return
	}
	defer null.Close()
	a := &app{ctx: ctx, config: config, tmux: bptmux.New(), queue: msgq.New(config.MsgqRoot), out: null, err: null}
	agent := strings.TrimSuffix(a.sender(), identity.InferMark)
	if agent == identity.Unknown || !identity.ValidName(agent) {
		agent = ""
	}
	now := time.Now()
	var taint *guard.Taint
	if agent != "" {
		t, found, err := guard.TaintFrom(msgq.MessageLogPath(config.MsgqRoot), agent, now.Add(-cfg.Window))
		if err != nil {
			fmt.Fprintln(stderr, "bp guard hook: message log:", err)
		}
		if found {
			taint = &t
		}
	}
	result := cfg.Evaluate(in, agent, what, canary, taint, now)
	if result.Alert != nil && config.StateDir != "" {
		guard.AuditSink{StateDir: config.StateDir, Errors: stderr}.Alert(*result.Alert)
	}
	if len(result.Output) > 0 {
		stdout.Write(append(result.Output, '\n'))
	}
}

func parseGuardHookArgs(args []string) (guard.HookConfig, error) {
	cfg := guard.HookConfig{Mode: guard.HookObserve, Window: guard.DefaultTaintWindow}
	if len(args) < 2 || args[0] != "hook" || args[1] != "claude" {
		return cfg, fmt.Errorf("%s", guardHookUsage)
	}
	cfg.Home, _ = os.UserHomeDir()
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--ask":
			cfg.Mode = guard.HookAsk
		case "--canary":
			if i+1 >= len(args) || args[i+1] == "" {
				return cfg, fmt.Errorf("%s", guardHookUsage)
			}
			i++
			cfg.Canaries = append(cfg.Canaries, args[i])
		default:
			return cfg, fmt.Errorf("%s", guardHookUsage)
		}
	}
	return cfg, nil
}

// guardHookMatcher lists the Claude tools whose input can name a file, a
// command or a URL; other tools never reach the hook.
const guardHookMatcher = "Read|Bash|Grep|Glob|Edit|Write|WebFetch"

// guardHookGroup is the PreToolUse group the guard-hooks module adds to bp's
// per-agent Claude settings layer. The mode and canaries go on the command
// line, so the hook never reads bp's config on a miss.
func guardHookGroup(self string, cfg *bpconfig.GuardHooksConfig) map[string]any {
	command := quoteShell(self) + " guard hook claude"
	if cfg != nil {
		if cfg.Mode == string(guard.HookAsk) {
			command += " --ask"
		}
		for _, canary := range cfg.Canaries {
			command += " --canary " + quoteShell(canary)
		}
	}
	return map[string]any{"matcher": guardHookMatcher, "hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 5}}}
}
