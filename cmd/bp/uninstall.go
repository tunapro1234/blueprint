package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"blueprint/internal/modules"
)

// binaryMarker is in every bp binary (the usage text), so uninstall removes a
// recorded binary only while it is still bp.
const binaryMarker = "blueprint (bp) — agent infrastructure CLI"

// installRecord is the installer's hook: install.sh calls
// bp _install-record <file|line|link|binary> <path> [marker|line|target]
// for every change it makes outside bp's home, so bp uninstall can undo it.
func (a *app) installRecord(args []string) error {
	if len(args) < 2 || len(args) > 3 || !filepath.IsAbs(args[1]) {
		return fmt.Errorf("usage: bp _install-record <file|line|link|binary> <absolute-path> [marker|line|target]")
	}
	extra := ""
	if len(args) == 3 {
		extra = args[2]
	}
	change := modules.Change{Path: filepath.Clean(args[1])}
	switch args[0] {
	case "file":
		change.Kind, change.Marker = modules.KindFile, extra
	case "binary":
		change.Kind, change.Marker = modules.KindFile, binaryMarker
	case "line":
		change.Kind, change.Line = modules.KindLine, extra
	case "link":
		change.Kind, change.Target = modules.KindLink, extra
	default:
		return fmt.Errorf("unknown install record kind %q", args[0])
	}
	if change.Kind != modules.KindFile && extra == "" || args[0] == "file" && extra == "" {
		return fmt.Errorf("%s records need a third argument", args[0])
	}
	return modules.RecordInstall(a.config, change)
}

// uninstall removes what bp added: every module's recorded changes, the agent
// hint, and the installer's records (binary, link, tmux.conf lines). State,
// books, config and logs stay unless --purge is given.
func (a *app) uninstall(args []string) error {
	purge, dryRun := false, false
	for _, arg := range args {
		switch arg {
		case "--purge":
			purge = true
		case "--dry-run":
			dryRun = true
		default:
			return fmt.Errorf("usage: bp uninstall [--purge] [--dry-run]")
		}
	}
	if purge && a.config.Legacy {
		return fmt.Errorf("--purge would delete %s, which is the server installation and its repository; remove what you want by hand", a.config.Home)
	}
	say := func(format string, values ...any) {
		prefix := ""
		if dryRun {
			prefix = "would "
		}
		fmt.Fprintf(a.out, prefix+format+"\n", values...)
	}
	env := a.moduleEnv()
	barWasOn := a.moduleEnabled(modules.Bar)
	options := modules.Options{DryRun: dryRun}
	all := modules.All()
	for index := len(all) - 1; index >= 0; index-- {
		name := all[index].Name
		journal, err := modules.LoadJournal(a.config, name)
		if err != nil {
			return err
		}
		if !modules.EnabledIn(a.config, name) && len(journal.Changes) == 0 {
			continue
		}
		result, err := modules.Disable(env, name, options)
		if err != nil {
			return fmt.Errorf("disable %s: %w", name, err)
		}
		for _, action := range result.Actions {
			say("%s", action)
		}
		for _, kept := range result.Kept {
			fmt.Fprintf(a.out, "kept: %s\n", kept)
		}
		if !dryRun {
			env.Config, _ = a.reloadConfig()
			a.config = env.Config
		}
	}
	if barWasOn {
		if dryRun {
			say("remove the bp bar from bp's tmux sessions")
		} else if err := a.clearLocalBars(); err != nil {
			fmt.Fprintf(a.err, "warning: bar cleanup: %v\n", err)
		} else {
			say("removed the bp bar from bp's tmux sessions")
		}
	}

	journal, err := modules.LoadJournal(a.config, modules.Install)
	if err != nil {
		return err
	}
	if dryRun {
		for index := len(journal.Changes) - 1; index >= 0; index-- {
			say("undo: %s", journal.Changes[index])
		}
	} else {
		done, kept := journal.Undo(env)
		for _, action := range done {
			say("%s", action)
		}
		for _, item := range kept {
			fmt.Fprintf(a.out, "kept: %s\n", item)
		}
		if err := journal.Save(a.config); err != nil {
			return err
		}
	}
	for _, unit := range []string{"/etc/systemd/system/blueprint.service", filepath.Join(env.UserHome, ".config/systemd/user/blueprint.service")} {
		if _, err := os.Lstat(unit); err == nil {
			fmt.Fprintf(a.out, "left in place: %s (bp did not install it; stop and remove it yourself if you no longer want it)\n", unit)
		}
	}
	if self, err := os.Executable(); err == nil && !dryRun {
		if _, statErr := os.Stat(self); statErr == nil {
			fmt.Fprintf(a.out, "left in place: %s (not installed by bp's installer; remove it with the tool that installed it, e.g. npm uninstall -g)\n", self)
		}
	}
	if !purge {
		fmt.Fprintf(a.out, "kept: config, books, state and logs in %s (bp uninstall --purge removes them)\n", a.config.Home)
		fmt.Fprintln(a.out, "Agents already running in tmux keep running; close them with bp close <name> before uninstalling if you want them gone.")
		return nil
	}
	targets := []string{a.config.Home, filepath.Join(env.UserHome, ".config", "bp")}
	for _, target := range targets {
		if target == "" || target == "/" || target == env.UserHome {
			continue
		}
		if _, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if target == a.config.Home && !looksLikeBPHome(target) {
			fmt.Fprintf(a.out, "kept: %s (no bp config or book in it; not removing a directory bp may not own)\n", target)
			continue
		}
		if dryRun {
			say("remove %s", target)
			continue
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		say("removed %s", target)
	}
	if !dryRun {
		fmt.Fprintln(a.out, "bp is uninstalled.")
	}
	return nil
}

func looksLikeBPHome(dir string) bool {
	for _, name := range []string{"config.yaml", "config.yml", "config.json", "agentbook.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}
