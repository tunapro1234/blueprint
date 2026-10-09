package main

import (
	bptmux "blueprint/internal/tmux"
)

// sharedTexts are offered to the text-taking verdicts on every screen, after
// the case's own messages.
var sharedTexts = []string{
	"deploy the new bar chips",
	"[bp] a completely different message, extended to make it long enough.",
}

var paneCommands = []string{"claude", "codex", "bwrap", "node", "python3", "hermes", "opencode", "zsh", ""}

type screenResult struct {
	Name                       string          `json:"name"`
	Pane                       string          `json:"pane"`
	Texts                      []string        `json:"texts"`
	Typing                     bool            `json:"typing"`
	Busy                       bool            `json:"busy"`
	StripDim                   string          `json:"stripDim"`
	ComposerBlockReason        string          `json:"composerBlockReason"`
	ComposerContentBlockReason string          `json:"composerContentBlockReason"`
	StuckPasteText             string          `json:"stuckPasteText"`
	StuckPasteOurs             bool            `json:"stuckPasteOurs"`
	ExactPaste                 bool            `json:"exactPaste"`
	ComposerEmpty              bool            `json:"composerEmpty"`
	DamagedPaste               bool            `json:"damagedPaste"`
	AuthExpired                bool            `json:"authExpired"`
	UsageLimitReason           string          `json:"usageLimitReason"`
	RemoteControlMenu          bool            `json:"remoteControlMenu"`
	RemoteState                int             `json:"remoteState"`
	RemoteURL                  string          `json:"remoteURL"`
	RemoteReason               string          `json:"remoteReason"`
	Dialog                     bool            `json:"dialog"`
	CodexPane                  bool            `json:"codexPane"`
	HermesPane                 bool            `json:"hermesPane"`
	HermesIdle                 bool            `json:"hermesIdle"`
	OpenCodePane               bool            `json:"openCodePane"`
	ClaudeEmptyComposer        bool            `json:"claudeEmptyComposer"`
	ClaudeHoldsOnboarding      bool            `json:"claudeHoldsOnboarding"`
	LaunchWait                 string          `json:"launchWait"`
	IsAgentPane                map[string]bool `json:"isAgentPane"`
}

type commandResult struct {
	Command           string `json:"command"`
	IsAgentCommand    bool   `json:"isAgentCommand"`
	IsShellCommand    bool   `json:"isShellCommand"`
	IsCodexCommand    bool   `json:"isCodexCommand"`
	IsHermesCommand   bool   `json:"isHermesCommand"`
	IsOpenCodeCommand bool   `json:"isOpenCodeCommand"`
}

type launchResult struct {
	Options            bptmux.OpenOptions `json:"options"`
	ClaudeConfigPrefix string             `json:"claudeConfigPrefix"`
}

type socketResult struct {
	Home     string `json:"home"`
	Endpoint string `json:"endpoint"`
	Socket   string `json:"socket"`
}

func writeGoldens(dir string) {
	var screens []screenResult
	for _, c := range variants(basePanes()) {
		texts := append(append([]string(nil), c.texts...), sharedTexts...)
		stuck, ours := bptmux.StuckPaste(c.pane, texts)
		state, url, reason := bptmux.RemoteControlStatus(c.pane)
		agentPane := map[string]bool{}
		for _, cmd := range paneCommands {
			agentPane[cmd] = bptmux.IsAgentPane(cmd, c.pane)
		}
		screens = append(screens, screenResult{
			Name:                       c.name,
			Pane:                       c.pane,
			Texts:                      texts,
			Typing:                     bptmux.Typing(c.pane),
			Busy:                       bptmux.Busy(c.pane),
			StripDim:                   bptmux.StripDim(c.pane),
			ComposerBlockReason:        bptmux.ComposerBlockReason(c.pane, texts),
			ComposerContentBlockReason: bptmux.ComposerContentBlockReason(c.pane, texts),
			StuckPasteText:             stuck,
			StuckPasteOurs:             ours,
			ExactPaste:                 bptmux.ExactPaste(c.pane, texts),
			ComposerEmpty:              bptmux.ComposerEmpty(c.pane),
			DamagedPaste:               bptmux.DamagedPaste(c.pane, texts),
			AuthExpired:                bptmux.AuthExpired(c.pane),
			UsageLimitReason:           bptmux.UsageLimitReason(c.pane),
			RemoteControlMenu:          bptmux.RemoteControlMenu(c.pane),
			RemoteState:                int(state),
			RemoteURL:                  url,
			RemoteReason:               reason,
			Dialog:                     bptmux.Dialog(c.pane),
			CodexPane:                  bptmux.CodexPane(c.pane),
			HermesPane:                 bptmux.HermesPane(c.pane),
			HermesIdle:                 bptmux.HermesIdle(c.pane),
			OpenCodePane:               bptmux.OpenCodePane(c.pane),
			ClaudeEmptyComposer:        bptmux.ClaudeEmptyComposer(c.pane),
			ClaudeHoldsOnboarding:      bptmux.ClaudeComposerHoldsOnboarding(c.pane, "agent"),
			LaunchWait:                 bptmux.LaunchWait(c.pane),
			IsAgentPane:                agentPane,
		})
	}
	write(dir, "screens.json", screens)

	var commands []commandResult
	for _, cmd := range append(paneCommands, "bash", "sh", "dash", "fish", "tmux", "vim", "python", "hermes-agent", "Codex") {
		commands = append(commands, commandResult{
			Command:           cmd,
			IsAgentCommand:    bptmux.IsAgentCommand(cmd),
			IsShellCommand:    bptmux.IsShellCommand(cmd),
			IsCodexCommand:    bptmux.IsCodexCommand(cmd),
			IsHermesCommand:   bptmux.IsHermesCommand(cmd),
			IsOpenCodeCommand: bptmux.IsOpenCodeCommand(cmd),
		})
	}
	write(dir, "commands.json", commands)

	var launches []launchResult
	for _, opts := range []bptmux.OpenOptions{
		{},
		{ClaudeConfigDir: "/home/u/.claude-b"},
		{ClaudeConfigDir: "/home/u/it's here"},
		{ClaudeConfigDir: "/x", Codex: true},
		{ClaudeConfigDir: "/x", Hermes: true},
		{ClaudeConfigDir: "/x", OpenCode: true},
		{Resume: true, ResumeID: "019a0d02-a847-76d1-ba01-8b67fbe755c1", Codex: true, Remote: "unix://", NoSandbox: true, Args: []string{"-m", "x"}, ClaudeAccount: "b"},
	} {
		launches = append(launches, launchResult{Options: opts, ClaudeConfigPrefix: bptmux.ClaudeConfigPrefix(opts)})
	}
	write(dir, "launch.json", launches)

	var sockets []socketResult
	for _, endpoint := range []string{"unix://", "unix:///run/x.sock", "unix://rel", "tcp://x", ""} {
		sockets = append(sockets, socketResult{Home: "/home/u", Endpoint: endpoint, Socket: bptmux.CodexSocket("/home/u", endpoint)})
	}
	write(dir, "sockets.json", sockets)

	write(dir, "onboarding.json", map[string]string{
		"agent":       bptmux.OnboardingPrompt("agent"),
		"server-main": bptmux.OnboardingPrompt("server-main"),
		"%s":          bptmux.OnboardingPrompt("%s"),
	})
}
