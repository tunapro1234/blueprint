package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"blueprint/internal/audit"
	bpconfig "blueprint/internal/config"
	"blueprint/internal/modules"
)

// binaryMarker is in every bp binary (the usage text), so uninstall removes a
// recorded binary only while it is still bp.
const binaryMarker = "blueprint (bp) — agent infrastructure CLI"

// installerTmuxLines are the only lines install.sh appends to ~/.tmux.conf.
var installerTmuxLines = map[string]bool{
	"# blueprint: clipboard, scroll, and mosh integration": true,
	"set -g mouse on":                               true,
	"set -g history-limit 100000":                   true,
	"setw -g mode-keys vi":                          true,
	"set -sg escape-time 10":                        true,
	"set -s set-clipboard on":                       true,
	"set -as terminal-features ',xterm*:clipboard'": true,
	"set -g allow-passthrough on":                   true,
}

// installRecord is the installer's hook: install.sh calls
// bp _install-record <file|line|link|binary> <path> [marker|line|target]
// for every change it makes outside bp's home, so bp uninstall can undo it.
// Only the exact changes install.sh makes are accepted: anything that can
// run one bp command must not be able to schedule a deletion of an
// arbitrary file or line for the next bp uninstall.
func (a *app) installRecord(args []string) error {
	if len(args) < 2 || len(args) > 3 || !filepath.IsAbs(args[1]) {
		return fmt.Errorf("usage: bp _install-record <file|line|link|binary> <absolute-path> [marker|line|target]")
	}
	extra := ""
	if len(args) == 3 {
		extra = args[2]
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Clean(args[1])
	change := modules.Change{Path: path}
	refuse := func() error {
		return fmt.Errorf("bp _install-record only records the changes install.sh makes; refusing %s %s", args[0], path)
	}
	switch args[0] {
	case "binary":
		// The installer's own copy, recorded with the hash of what is there now.
		if path != filepath.Join(home, ".local", "bin", "bp") || extra != "" {
			return refuse()
		}
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(data, []byte(binaryMarker)) {
			return refuse()
		}
		change.Kind, change.Marker, change.SHA256 = modules.KindFile, binaryMarker, modules.SHA256(data)
	case "file":
		// /etc/blueprint/home selects the server home; its content is the marker.
		data, err := os.ReadFile(path)
		if path != "/etc/blueprint/home" || extra == "" || err != nil || strings.TrimSpace(string(data)) != extra {
			return refuse()
		}
		change.Kind, change.Marker, change.SHA256 = modules.KindFile, extra, modules.SHA256(data)
	case "line":
		if path != filepath.Join(home, ".tmux.conf") || !installerTmuxLines[extra] {
			return refuse()
		}
		change.Kind, change.Line = modules.KindLine, extra
	case "link":
		// /usr/local/bin/bp -> the server's bp binary.
		target, err := os.Readlink(path)
		if path != "/usr/local/bin/bp" || !filepath.IsAbs(extra) || filepath.Base(extra) != "bp" || err != nil || target != extra {
			return refuse()
		}
		change.Kind, change.Target = modules.KindLink, extra
	default:
		return fmt.Errorf("unknown install record kind %q", args[0])
	}
	return modules.RecordInstall(a.config, change)
}

// uninstall removes what bp added: every module's recorded changes, the agent
// hint, and the installer's records (binary, link, tmux.conf lines). State,
// books, config and logs stay unless --purge is given.
func (a *app) uninstall(args []string) error {
	purge, dryRun, yes := false, false, false
	for _, arg := range args {
		switch arg {
		case "--purge":
			purge = true
		case "--dry-run":
			dryRun = true
		case "--yes":
			yes = true
		default:
			return fmt.Errorf("usage: bp uninstall [--purge [--yes]] [--dry-run]")
		}
	}
	var purgeTargets []string
	if purge {
		targets, err := a.purgeTargets()
		if err != nil {
			return err
		}
		if !dryRun && !yes {
			if err := confirmPurge(targets); err != nil {
				return err
			}
		}
		purgeTargets = targets
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
	// The installer's backups of earlier binaries are the user's to delete.
	for _, pattern := range []string{"bp.before-*", "bp.failed-install.*", "bp.before-update-*"} {
		matches, _ := filepath.Glob(filepath.Join(env.UserHome, ".local", "bin", pattern))
		for _, match := range matches {
			fmt.Fprintf(a.out, "left in place: %s (a backup the installer made; delete it when you no longer need it)\n", match)
		}
	}
	for _, unit := range []string{"/etc/systemd/system/blueprint.service", filepath.Join(env.UserHome, ".config/systemd/user/blueprint.service")} {
		if _, err := os.Lstat(unit); err == nil {
			fmt.Fprintf(a.out, "left in place: %s (bp did not install it; stop and remove it yourself if you no longer want it)\n", unit)
		}
	}
	if !dryRun {
		if err := audit.Append(a.config.StateDir, audit.Event{Kind: "bp.uninstall", Actor: a.moduleActor(), Fields: map[string]string{"purge": fmt.Sprint(purge)}}); err != nil {
			fmt.Fprintf(a.err, "warning: audit log: %v\n", err)
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
	for _, target := range purgeTargets {
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

// purgeTargets lists what --purge deletes, refusing anything bp cannot
// prove it owns.
func (a *app) purgeTargets() ([]string, error) {
	home := a.config.Home
	if a.config.Legacy {
		return nil, fmt.Errorf("--purge would delete %s, which is the server installation and its repository; remove what you want by hand", home)
	}
	if !filepath.IsAbs(home) {
		return nil, fmt.Errorf("--purge refuses the relative bp home %q; set BP_HOME to an absolute path", home)
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	clean := filepath.Clean(home)
	if clean == "/" || clean == filepath.Clean(userHome) {
		return nil, fmt.Errorf("--purge refuses to delete %s", clean)
	}
	var targets []string
	if _, err := os.Lstat(clean); err == nil {
		if !bpconfig.IsMarkedHome(clean) {
			return nil, fmt.Errorf("--purge refuses %s: it has no %s file, so bp cannot tell it created this directory; delete it by hand if it is bp's", clean, bpconfig.HomeSentinel)
		}
		targets = append(targets, clean)
	}
	if dir := filepath.Join(userHome, ".config", "bp"); dir != clean {
		if _, err := os.Lstat(dir); err == nil {
			targets = append(targets, dir)
		}
	}
	return targets, nil
}

func confirmPurge(targets []string) error {
	if len(targets) == 0 {
		return nil
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("--purge deletes %s; rerun with --yes to confirm without a terminal", strings.Join(targets, " and "))
	}
	defer tty.Close()
	fmt.Fprintln(tty, "bp uninstall --purge permanently deletes:")
	for _, target := range targets {
		fmt.Fprintln(tty, "  "+target)
	}
	fmt.Fprint(tty, "Delete these? [y/N] ")
	answer, _ := bufio.NewReader(tty).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	}
	return errors.New("purge cancelled; nothing was changed")
}
