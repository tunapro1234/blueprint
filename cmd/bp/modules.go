package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	bpconfig "blueprint/internal/config"
	"blueprint/internal/modules"
)

// moduleEnv builds the environment module hooks inspect and change.
func (a *app) moduleEnv() modules.Env {
	home, _ := os.UserHomeDir()
	env := modules.Env{Config: a.config, UserHome: home, Getenv: os.Getenv, ProcRoot: "/proc", Shell: filepath.Base(os.Getenv("SHELL")), Actor: a.moduleActor()}
	if a.tmux != nil {
		env.Tmux = func(args ...string) (string, error) { return a.tmux.Exec(a.ctx, args...) }
	}
	return env
}

func (a *app) moduleEnabled(name string) bool {
	return modules.EnabledIn(a.config, name)
}

func (a *app) modulesCommand(args []string) error {
	asJSON := false
	for _, arg := range args {
		switch arg {
		case "--json":
			asJSON = true
		default:
			return fmt.Errorf("usage: bp modules [--json]")
		}
	}
	rows := modules.List(a.moduleEnv())
	if asJSON {
		encoder := json.NewEncoder(a.out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(rows)
	}
	width := 0
	for _, row := range rows {
		width = max(width, len(row.Name))
	}
	indent := width + 7
	for _, row := range rows {
		state := "off"
		if row.Enabled {
			state = "on"
		}
		fmt.Fprintf(a.out, "%-*s %-3s  %s\n", width, row.Name, state, row.Description)
		for _, change := range row.Changes {
			fmt.Fprintf(a.out, "%*s changed: %s\n", indent, "", change)
		}
		for _, conflict := range row.Conflicts {
			fmt.Fprintf(a.out, "%*s conflict: %s\n", indent, "", conflict)
		}
	}
	if unknown := modules.Unknown(a.config); len(unknown) > 0 {
		fmt.Fprintf(a.out, "unknown modules in %s (kept as they are): %s\n", a.config.Path, strings.Join(unknown, ", "))
	}
	fmt.Fprintln(a.out, "Change with: bp enable <module> | bp disable <module>")
	return nil
}

func (a *app) moduleSwitch(enable bool, args []string) error {
	verb := "disable"
	if enable {
		verb = "enable"
	}
	var name string
	var options modules.Options
	env := a.moduleEnv()
	for index := 0; index < len(args); index++ {
		switch arg := args[index]; arg {
		case "--force":
			if !enable {
				return fmt.Errorf("usage: bp disable <module> [--dry-run]")
			}
			options.Force = true
		case "--dry-run":
			options.DryRun = true
		case "--shell":
			if !enable || index+1 >= len(args) {
				return fmt.Errorf("--shell requires bash or zsh")
			}
			index++
			env.Shell = args[index]
		case "--wrappers":
			if !enable {
				return fmt.Errorf("--wrappers belongs to bp enable sessions")
			}
			env.Wrappers = true
		default:
			if strings.HasPrefix(arg, "-") || name != "" {
				return fmt.Errorf("usage: bp %s <module> [--dry-run]%s", verb, map[bool]string{true: " [--force]"}[enable])
			}
			name = arg
		}
	}
	if name == "" {
		return fmt.Errorf("usage: bp %s <module>; modules: %s", verb, strings.Join(modules.Names(), ", "))
	}
	if a.config.InvalidConfig != "" {
		return fmt.Errorf("fix the config before changing modules: %s", a.config.InvalidConfig)
	}
	wasEnabled := a.moduleEnabled(name)
	var result modules.Result
	var err error
	if enable {
		result, err = modules.Enable(env, name, options)
	} else {
		result, err = modules.Disable(env, name, options)
	}
	var conflict *modules.ConflictError
	if errors.As(err, &conflict) {
		return err
	}
	if err != nil {
		return err
	}
	prefix := ""
	if options.DryRun {
		prefix = "would "
	}
	for _, action := range result.Actions {
		fmt.Fprintf(a.out, "%s%s\n", prefix, action)
	}
	for _, kept := range result.Kept {
		fmt.Fprintf(a.out, "kept: %s\n", kept)
	}
	if options.DryRun {
		return nil
	}
	if err := a.afterModuleSwitch(name, enable, wasEnabled); err != nil {
		fmt.Fprintf(a.err, "warning: %v\n", err)
	}
	switch {
	case enable && !result.Changed:
		fmt.Fprintf(a.out, "%s is already enabled\n", name)
	case enable:
		fmt.Fprintf(a.out, "%s enabled\n", name)
		if result.Notes != "" {
			fmt.Fprintln(a.out, result.Notes)
		}
	case !result.Changed:
		fmt.Fprintf(a.out, "%s is already disabled\n", name)
	default:
		fmt.Fprintf(a.out, "%s disabled\n", name)
	}
	if len(result.Conflicts) > 0 {
		fmt.Fprintf(a.out, "enabled despite: %s\n", strings.Join(result.Conflicts, "; "))
	}
	if name != modules.Bar || a.config.Legacy {
		fmt.Fprintln(a.out, "A running bp daemon reads modules when it starts; restart it to apply this there.")
	}
	return nil
}

// afterModuleSwitch applies live effects that belong to the running tmux
// server rather than to files: bar styling on bp's own sessions.
func (a *app) afterModuleSwitch(name string, enable, wasEnabled bool) error {
	cfg, err := a.reloadConfig()
	if err == nil {
		a.config = cfg
	}
	if name == modules.Sessions && enable != wasEnabled && !a.config.Legacy {
		// The agent hint follows: sessions installs get the operational skill.
		if _, err := a.installAgentHint(a.moduleEnv()); err != nil {
			return err
		}
	}
	if name != modules.Bar || enable == wasEnabled {
		return nil
	}
	if enable {
		return a.refreshLocalBars()
	}
	return a.clearLocalBars()
}

// reloadConfig reads this installation's config again after a change. It
// re-reads the same home, never whatever BP_HOME or /etc/blueprint/home say.
func (a *app) reloadConfig() (bpconfig.Config, error) {
	cfg, err := bpconfig.LoadHome(a.config.Home)
	if err != nil {
		return cfg, err
	}
	return modules.Init(cfg), nil
}

// moduleActor names the caller for the audit log: the bp agent when the
// command runs inside one, otherwise "owner" (the person at the terminal).
func (a *app) moduleActor() string {
	if a.resolveSender != nil || os.Getenv("TMUX") != "" || os.Getenv("BP_SESSION") != "" {
		if who := a.senderIdentity(); who.Label != "" {
			return who.Label
		}
	}
	// Run outside any agent session: the person at the terminal.
	return "owner"
}
