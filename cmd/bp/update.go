package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/buildinfo"
	"blueprint/internal/release"
)

type updateResult struct {
	Current   string    `json:"current"`
	Latest    string    `json:"latest,omitempty"`
	Available bool      `json:"available"`
	CheckedAt time.Time `json:"checked_at"`
	Error     string    `json:"error,omitempty"`
}

func printVersion(args []string) error {
	if len(args) > 1 || len(args) == 1 && args[0] != "--json" {
		return fmt.Errorf("usage: bp version [--json]")
	}
	if len(args) == 1 {
		return json.NewEncoder(os.Stdout).Encode(struct {
			Version string `json:"version"`
			buildinfo.Identity
		}{release.Version(), buildinfo.Current()})
	}
	fmt.Fprintln(os.Stdout, "bp", release.Version())
	return nil
}
func (a *app) update(args []string) error {
	check, jsonOutput := false, false
	for _, arg := range args {
		switch arg {
		case "--check":
			check = true
		case "--json":
			jsonOutput = true
		default:
			return fmt.Errorf("usage: bp update [--check] [--json]")
		}
	}
	if jsonOutput && !check {
		return fmt.Errorf("--json requires --check")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 45*time.Second)
	defer cancel()
	checker := release.Default()
	m, err := checker.Latest(ctx)
	result := updateResult{Current: release.Version(), Latest: m.Version, Available: release.Newer(m.Version, release.Version()), CheckedAt: time.Now().UTC()}
	if err != nil {
		result.Error = err.Error()
	}
	if a.config.StateDir != "" {
		dir := a.config.StateDir
		if os.MkdirAll(dir, 0700) == nil {
			data, _ := json.Marshal(result)
			f, e := os.CreateTemp(dir, ".update-")
			if e == nil {
				f.Write(data)
				f.Close()
				os.Rename(f.Name(), filepath.Join(dir, "update.json"))
			}
		}
	}
	if jsonOutput {
		_ = json.NewEncoder(a.out).Encode(result)
	}
	if err != nil {
		return err
	}
	if check || !result.Available {
		if !jsonOutput {
			if result.Available {
				fmt.Fprintf(a.out, "bp %s available (installed %s); run bp update\n", result.Latest, result.Current)
			} else {
				fmt.Fprintf(a.out, "bp %s is current; latest published %s\n", result.Current, result.Latest)
			}
		}
		return nil
	}
	if a.config.Legacy {
		return fmt.Errorf("server installation: use the host release workflow to update both CLI and daemon; agents will not be restarted automatically")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	data, err := checker.Download(ctx, m, release.Platform())
	if err != nil {
		return err
	}
	run := func(path string, args ...string) error {
		cmd := exec.CommandContext(ctx, path, args...)
		cmd.Stdout, cmd.Stderr = a.out, a.err
		return cmd.Run()
	}
	backup, err := release.Replace(self, data, func(path string) error { return run(path, "setup", "--check") }, func() error { return run(self, "setup") })
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Updated bp %s → %s. Backup: %s. Open agents keep running.\n", release.Version(), m.Version, backup)
	return nil
}

// Only a cached notice touches the terminal. A detached, bounded check refreshes
// it at most daily; no prompt is submitted to a model and offline startup proceeds.
func (a *app) updateNotice() {
	if !a.config.UpdateCheck || os.Getenv("BP_NO_UPDATE_CHECK") == "1" {
		return
	}
	var cached updateResult
	data, _ := os.ReadFile(filepath.Join(a.config.StateDir, "update.json"))
	_ = json.Unmarshal(data, &cached)
	if cached.Error == "" && release.Newer(cached.Latest, release.Version()) {
		fmt.Fprintf(a.out, "bp: %s available — run bp update\n", cached.Latest)
	}
	if !cached.CheckedAt.IsZero() && time.Since(cached.CheckedAt) < 24*time.Hour {
		return
	}
	if os.MkdirAll(a.config.StateDir, 0700) != nil {
		return
	}
	lock, err := os.OpenFile(filepath.Join(a.config.StateDir, ".update-notice.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	// Reserve today's attempt before spawning, including offline/crashed checks.
	data, _ = os.ReadFile(filepath.Join(a.config.StateDir, "update.json"))
	_ = json.Unmarshal(data, &cached)
	if !cached.CheckedAt.IsZero() && time.Since(cached.CheckedAt) < 24*time.Hour {
		return
	}
	cached.CheckedAt = time.Now().UTC()
	encoded, _ := json.Marshal(cached)
	f, err := os.CreateTemp(a.config.StateDir, ".update-reserve-")
	if err != nil {
		return
	}
	if _, err = f.Write(encoded); err != nil {
		f.Close()
		return
	}
	f.Close()
	if os.Rename(f.Name(), filepath.Join(a.config.StateDir, "update.json")) != nil {
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(self, "update", "--check", "--json")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return
	}
	defer null.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}

func (a *app) setupCheck() error {
	if a.config.Legacy {
		return fmt.Errorf("local setup is not for a server installation")
	}
	if a.config.InvalidConfig != "" {
		return fmt.Errorf("invalid configuration: %s", a.config.InvalidConfig)
	}
	shell := filepath.Base(os.Getenv("SHELL"))
	if shell != "bash" && shell != "zsh" {
		return fmt.Errorf("unsupported shell %q; select bash or zsh before installing", shell)
	}
	if _, err := exec.LookPath(a.tmux.Bin); err != nil {
		return fmt.Errorf("tmux is required: %w", err)
	}
	// Validate every existing book before replacing a usable binary.
	for _, path := range book.Paths(a.config.Agentbooks) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}
		if _, err := book.Load(path); err != nil {
			return err
		}
	}

	home, _ := os.UserHomeDir()
	for _, path := range []string{a.config.Home, filepath.Join(home, ".config", "bp"), filepath.Join(home, ".local", "bin")} {
		for {
			if _, err := os.Stat(path); err == nil {
				break
			}
			parent := filepath.Dir(path)
			if parent == path {
				return fmt.Errorf("no writable ancestor")
			}
			path = parent
		}
		if err := syscall.Access(path, 2); err != nil {
			return fmt.Errorf("not writable: %s: %w", path, err)
		}
	}
	return nil
}

func (a *app) disableSetup() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".config", "bp", "shell.sh")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		fmt.Fprintln(a.out, "bp shell integration is already disabled")
		return nil
	}
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(data), "# Generated by bp setup.") {
		return fmt.Errorf("refusing to overwrite an unrecognized shell integration file")
	}
	backup, err := os.CreateTemp(filepath.Dir(path), "shell.sh.before-disable-")
	if err != nil {
		return err
	}
	if _, err = backup.Write(data); err != nil {
		backup.Close()
		return err
	}
	backup.Close()
	if err = os.WriteFile(path, []byte("# Generated by bp setup. Disabled; native aliases and records are preserved.\n"), 0600); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "bp shell integration disabled. Open a new terminal; existing agents and aliases are preserved.")
	return nil
}
