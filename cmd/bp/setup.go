package main

import (
	"blueprint/internal/bpskill"
	bpconfig "blueprint/internal/config"
	"fmt"
	"os"
	"strings"

	"blueprint/internal/modules"
)

// localSetup prepares bp's own home (config and book) and refreshes what the
// enabled modules own. It changes nothing outside bp's home unless a module
// is enabled: shell wrappers belong to sessions, tmux styling to bar.
// --shell and --wrappers are the old way to ask for wrappers; they enable
// sessions explicitly.
func (a *app) localSetup(args []string) error {
	if len(args) == 1 && args[0] == "--check" {
		return a.setupCheck()
	}
	if len(args) == 1 && args[0] == "--disable" {
		return a.disableSetup()
	}

	if a.config.InvalidConfig != "" {
		return fmt.Errorf("fix the existing config before setup: %s", a.config.InvalidConfig)
	}
	env := a.moduleEnv()
	wantSessions := false
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--shell":
			if index+1 >= len(args) {
				return fmt.Errorf("--shell requires bash or zsh")
			}
			index++
			env.Shell = args[index]
			wantSessions = true
		case "--wrappers":
			env.Wrappers = true
			wantSessions = true
		default:
			return fmt.Errorf("unknown setup option: %s", args[index])
		}
	}
	created := a.config.Path == ""
	// An install used before it had a config file (bp run alone) keeps the
	// modules it already used; a new one starts with none.
	inUse := map[string]bool{}
	if created {
		for _, name := range modules.Names() {
			if modules.EnabledIn(a.config, name) {
				inUse[name] = true
			}
		}
	}
	configPath, err := bpconfig.InitYAML(a.config.Home)
	if err != nil {
		return err
	}
	if created && len(inUse) > 0 {
		if err := bpconfig.SetModules(configPath, inUse); err != nil {
			return err
		}
	}
	if err := a.initLocalBook(); err != nil {
		return err
	}
	if created {
		if cfg, err := a.reloadConfig(); err == nil {
			a.config = cfg
			env.Config = cfg
		}
	}
	if wantSessions || a.moduleEnabled(modules.Sessions) {
		result, err := modules.Enable(env, modules.Sessions, modules.Options{})
		if err != nil {
			return err
		}
		for _, action := range result.Actions {
			fmt.Fprintln(a.out, action)
		}
		if cfg, err := a.reloadConfig(); err == nil {
			a.config = cfg
		}
		fmt.Fprintln(a.out, "bp wrappers ready; existing aliases and functions are preserved; 'command codex' bypasses the wrapper.")
		if env.Wrappers {
			fmt.Fprintln(a.out, "Optional lush (bp attach) and rush (bp shell) wrappers installed; existing aliases and functions were preserved.")
		}
		fmt.Fprintln(a.out, "Open a new terminal, or run: . \"$HOME/.config/bp/shell.sh\"")
	}
	env.Config = a.config
	hints, err := a.installAgentHint(env)
	if err != nil {
		return err
	}
	for _, line := range hints {
		fmt.Fprintln(a.out, line)
	}
	if a.moduleEnabled(modules.Bar) {
		if err := a.refreshLocalBars(); err != nil {
			return err
		}
	}
	fmt.Fprintln(a.out, "bp is ready. Nothing outside bp's home was changed unless a module is enabled.")
	fmt.Fprintln(a.out, "Modules (all optional): bp modules | bp enable <module>")
	fmt.Fprintf(a.out, "Settings: %s (check with: bp config check)\n", configPath)
	return nil
}

// installAgentHint tells the user's agents that bp exists through the least
// invasive channel each harness offers: a skill file. Each file bp writes is
// recorded so bp uninstall removes it.
func (a *app) installAgentHint(env modules.Env) ([]string, error) {
	// Installs that run agent sessions get the operational skill; a
	// communicate-only install gets the short generic hint.
	content := bpskill.Hint
	if a.config.Legacy || modules.EnabledIn(env.Config, modules.Sessions) {
		content = bpskill.Content
	}
	lines, err := bpskill.InstallContent(env.UserHome, os.Getenv("CODEX_HOME"), os.Getenv("CLAUDE_CONFIG_DIR"), content)
	if err != nil {
		return lines, err
	}
	var changes []modules.Change
	for _, line := range lines {
		for _, prefix := range []string{"skill installed: ", "skill ready: "} {
			if path, ok := strings.CutPrefix(line, prefix); ok {
				changes = append(changes, modules.Change{Kind: modules.KindFile, Path: path, Marker: bpskill.Marker})
			}
		}
	}
	if err := modules.RecordInstall(a.config, changes...); err != nil {
		return lines, err
	}
	return lines, nil
}

const (
	localShell            = modules.LocalShell
	compatibilityWrappers = modules.CompatibilityWrappers
)
