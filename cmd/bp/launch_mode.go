package main

import (
	"regexp"
	"strings"

	bptmux "blueprint/internal/tmux"
)

var launchThreadID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Native flags that consume the next argument. Anything else starting with "-"
// is a switch; a bare word that no flag consumed is a subcommand or a prompt,
// neither of which belongs to the launch MODE.
var launchValueFlags = map[string]map[string]bool{
	"codex": {"-m": true, "--model": true, "-c": true, "--config": true, "-p": true, "--profile": true,
		"-s": true, "--sandbox": true, "-a": true, "--ask-for-approval": true, "-C": true, "--cd": true,
		"-i": true, "--image": true, "--add-dir": true, "--enable": true, "--disable": true,
		"--local-provider": true},
	"claude": {"--model": true, "--effort": true, "--permission-mode": true, "--add-dir": true,
		"--append-system-prompt": true, "--system-prompt": true, "--allowedTools": true,
		"--allowed-tools": true, "--disallowedTools": true, "--disallowed-tools": true,
		"--mcp-config": true, "--agent": true, "--fallback-model": true},
	"opencode": {"-m": true, "--model": true, "--agent": true},
}

// Flags that bp owns or that select a conversation rather than a mode. They
// are dropped from the recorded mode; the conversation lives in ResumeID.
var launchDropped = map[string]map[string]bool{
	"codex":  {"--remote": true, "--last": true, "--all": true},
	"claude": {"--settings": true, "--session-id": true, "-c": true, "--continue": true, "--fork-session": true},
}

// nativeLaunch records how `bp run <harness> <args…>` started the agent so that
// a later bp open --resume reproduces it (#25): the conversation id, and every
// native flag with its value. Prompts and subcommands are not part of it.
func nativeLaunch(harness string, args []string) *bptmux.OpenOptions {
	if harness != "codex" && harness != "claude" && harness != "opencode" {
		return nil // OpenOptions cannot express other harnesses
	}
	opts := &bptmux.OpenOptions{Codex: harness == "codex", OpenCode: harness == "opencode"}
	values, dropped := launchValueFlags[harness], launchDropped[harness]
	resumeNext := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		name, _, inline := strings.Cut(arg, "=")
		switch {
		case harness == "claude" && (name == "--resume" || name == "-r"):
			if !inline && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				arg = name + "=" + args[i]
			}
			if _, id, _ := strings.Cut(arg, "="); launchThreadID.MatchString(id) {
				opts.ResumeID = id
			}
		case dropped[name]:
			if !inline && (name == "--remote" || name == "--settings" || name == "--session-id") && i+1 < len(args) {
				i++
			}
		case strings.HasPrefix(arg, "-"):
			opts.Args = append(opts.Args, arg)
			if values[name] && !inline && i+1 < len(args) {
				i++
				opts.Args = append(opts.Args, args[i])
			}
		case harness == "codex" && (arg == "resume" || arg == "fork") && opts.ResumeID == "":
			resumeNext = arg == "resume"
		case resumeNext && launchThreadID.MatchString(arg):
			opts.ResumeID, resumeNext = arg, false
		}
	}
	opts.Resume = opts.ResumeID != ""
	for _, arg := range opts.Args {
		if arg == "--dangerously-bypass-approvals-and-sandbox" || arg == "--yolo" {
			opts.NoSandbox = opts.Codex
		}
	}
	return opts
}
