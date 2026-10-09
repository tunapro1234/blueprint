package modules

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blueprint/internal/config"
)

type fixture struct {
	t        *testing.T
	home     string // user home
	bpHome   string
	cfg      config.Config
	env      Env
	tmuxOpts map[string]string
}

func newFixture(t *testing.T, configText string) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{t: t, home: filepath.Join(root, "home"), bpHome: filepath.Join(root, "home", ".blueprint"), tmuxOpts: map[string]string{}}
	must(t, os.MkdirAll(f.bpHome, 0700))
	if configText != "" {
		must(t, os.WriteFile(filepath.Join(f.bpHome, "config.yaml"), []byte(configText), 0600))
	}
	t.Setenv("BP_HOME", f.bpHome)
	t.Setenv("HOME", f.home)
	f.reload()
	return f
}

func (f *fixture) reload() {
	f.t.Helper()
	cfg, err := config.Load()
	must(f.t, err)
	f.cfg = cfg
	f.env = Env{Config: cfg, UserHome: f.home, Getenv: func(key string) string {
		if key == "SHELL" {
			return "/bin/zsh"
		}
		return ""
	}}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	must(t, err)
	return string(data)
}

func TestFreshConfigEnablesNothing(t *testing.T) {
	f := newFixture(t, "modules: {}\n")
	for _, name := range Names() {
		if EnabledIn(f.cfg, name) {
			t.Fatalf("%s enabled on a fresh install", name)
		}
	}
}

func TestNoConfigFileAndNoUseEnablesNothing(t *testing.T) {
	f := newFixture(t, "")
	if got := Detect(f.cfg, f.env); len(got) != 0 {
		t.Fatalf("detected %v", got)
	}
}

func TestEnableDisableSessionsUndoesExactly(t *testing.T) {
	f := newFixture(t, "modules: {}\n")
	zshrc := filepath.Join(f.home, ".zshrc")
	must(t, os.MkdirAll(f.home, 0700))
	original := "export EDITOR=vim\n"
	must(t, os.WriteFile(zshrc, []byte(original), 0644))

	result, err := Enable(f.env, Sessions, Options{})
	must(t, err)
	if !result.Changed || !strings.Contains(read(t, zshrc), RCLine) {
		t.Fatalf("enable did not add the rc line: %+v\n%s", result, read(t, zshrc))
	}
	if !strings.Contains(read(t, ShellPath(f.home)), ShellMarker) {
		t.Fatal("shell.sh missing")
	}
	f.reload()
	if !EnabledIn(f.cfg, Sessions) || EnabledIn(f.cfg, Bar) {
		t.Fatalf("config modules = %v", f.cfg.Modules)
	}
	// Enabling twice adds nothing.
	_, err = Enable(f.env, Sessions, Options{})
	must(t, err)
	if strings.Count(read(t, zshrc), RCLine) != 1 {
		t.Fatal("rc line duplicated")
	}

	result, err = Disable(f.env, Sessions, Options{})
	must(t, err)
	if got := read(t, zshrc); got != original {
		t.Fatalf("rc not restored:\n%q\nwant\n%q\nactions %v", got, original, result.Actions)
	}
	if _, err := os.Stat(ShellPath(f.home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("shell.sh left behind")
	}
	f.reload()
	if EnabledIn(f.cfg, Sessions) {
		t.Fatal("still enabled")
	}
	if _, err := os.Stat(journalPath(f.cfg, Sessions)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("journal left behind")
	}
}

func TestDisableKeepsUserModifiedFile(t *testing.T) {
	f := newFixture(t, "modules: {}\n")
	_, err := Enable(f.env, Sessions, Options{})
	must(t, err)
	must(t, os.WriteFile(ShellPath(f.home), []byte("my own shell file\n"), 0600))
	result, err := Disable(f.env, Sessions, Options{})
	must(t, err)
	if len(result.Kept) != 1 || read(t, ShellPath(f.home)) != "my own shell file\n" {
		t.Fatalf("user file not kept: %+v", result)
	}
}

func TestDisableKeepsSymlinkedRC(t *testing.T) {
	f := newFixture(t, "modules: {}\n")
	must(t, os.MkdirAll(f.home, 0700))
	real := filepath.Join(f.home, "dotfiles", "zshrc")
	must(t, os.MkdirAll(filepath.Dir(real), 0700))
	must(t, os.WriteFile(real, []byte("alias ll=ls\n"), 0644))
	must(t, os.Symlink(real, filepath.Join(f.home, ".zshrc")))
	_, err := Enable(f.env, Sessions, Options{})
	must(t, err)
	_, err = Disable(f.env, Sessions, Options{})
	must(t, err)
	info, err := os.Lstat(filepath.Join(f.home, ".zshrc"))
	must(t, err)
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced by a file")
	}
	if read(t, real) != "alias ll=ls\n" {
		t.Fatalf("real rc = %q", read(t, real))
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	f := newFixture(t, "modules: {}\n")
	before := read(t, f.cfg.Path)
	result, err := Enable(f.env, Sessions, Options{DryRun: true})
	must(t, err)
	if len(result.Actions) == 0 {
		t.Fatal("dry run described nothing")
	}
	if read(t, f.cfg.Path) != before {
		t.Fatal("dry run wrote config")
	}
	if _, err := os.Stat(ShellPath(f.home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dry run wrote shell.sh")
	}
}

func TestUnknownModule(t *testing.T) {
	f := newFixture(t, "modules: {}\n")
	if _, err := Enable(f.env, "nope", Options{}); err == nil || !strings.Contains(err.Error(), "sessions") {
		t.Fatalf("err = %v", err)
	}
}

func TestBarConflictWithCustomStatus(t *testing.T) {
	f := newFixture(t, "modules: {}\n")
	f.env.Tmux = func(args ...string) (string, error) {
		if args[len(args)-1] == "status-right" {
			return "#(my-status-script)\n", nil
		}
		return "[#S] \n", nil
	}
	_, err := Enable(f.env, Bar, Options{})
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !strings.Contains(err.Error(), "#(bp bar)") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Enable(f.env, Bar, Options{Force: true}); err != nil {
		t.Fatal(err)
	}
	// Default tmux status: no conflict.
	f.env.Tmux = func(args ...string) (string, error) {
		if args[len(args)-1] == "status-right" {
			return "#{?window_bigger,[#{window_offset_x}#,#{window_offset_y}] ,}\"#{=21:pane_title}\" %H:%M %d-%b-%y\n", nil
		}
		return "[#{session_name}] \n", nil
	}
	if got := barConflicts(f.env); len(got) != 0 {
		t.Fatalf("default status reported: %v", got)
	}
}

func TestAccountsConflicts(t *testing.T) {
	f := newFixture(t, "modules: {}\n")
	claude := filepath.Join(f.home, ".claude")
	must(t, os.MkdirAll(claude, 0700))
	must(t, os.WriteFile(filepath.Join(claude, "settings.json"), []byte(`{"apiKeyHelper":"/usr/bin/helper"}`), 0600))
	must(t, os.MkdirAll(filepath.Join(f.home, ".ccs"), 0700))
	f.env.Getenv = func(key string) string {
		if key == "CLAUDE_CODE_OAUTH_TOKEN" {
			return "x"
		}
		return ""
	}
	if got := accountsConflicts(f.env); len(got) != 3 {
		t.Fatalf("conflicts = %v", got)
	}
}

func TestWAConflictIgnoresOwnBridge(t *testing.T) {
	f := newFixture(t, "waOutbox: /srv/whatsapp/outbox\nmodules: {}\n")
	proc := t.TempDir()
	write := func(pid, cmd string) {
		must(t, os.MkdirAll(filepath.Join(proc, pid), 0700))
		must(t, os.WriteFile(filepath.Join(proc, pid, "cmdline"), []byte(strings.ReplaceAll(cmd, " ", "\x00")+"\x00"), 0600))
	}
	write("10", "node /srv/whatsapp/bridge.js")
	write("11", "bash")
	f.env.ProcRoot = proc
	if got := waConflicts(f.env); len(got) != 0 {
		t.Fatalf("own bridge reported: %v", got)
	}
	write("12", "node /home/x/my-baileys-bot/index.js")
	if got := waConflicts(f.env); len(got) != 1 {
		t.Fatalf("conflicts = %v", got)
	}
}

func TestMigrationRecordsInUseAndJournalsOldRCLine(t *testing.T) {
	// A local install made by an older bp setup: config without modules, an
	// rc line and shell.sh, one agent in the book.
	f := newFixture(t, "agentbooks: [agentbook.json]\nstateDir: state\n")
	must(t, os.WriteFile(filepath.Join(f.bpHome, "agentbook.json"), []byte(`{"orchestrator":"main","agents":[{"name":"main"}]}`), 0600))
	must(t, os.MkdirAll(filepath.Join(f.home, ".config", "bp"), 0700))
	must(t, os.WriteFile(ShellPath(f.home), []byte(LocalShell), 0600))
	zshrc := filepath.Join(f.home, ".zshrc")
	must(t, os.WriteFile(zshrc, []byte("export A=1\n\n"+RCLine+"\n"), 0644))
	f.reload()
	if f.cfg.ModulesSet {
		t.Fatal("fixture already has modules")
	}
	cfg := Init(f.cfg)
	if !cfg.Modules[Sessions] || !cfg.Modules[Bar] || cfg.Modules[Accounts] || cfg.Modules[WA] {
		t.Fatalf("migrated = %v", cfg.Modules)
	}
	f.reload()
	if !f.cfg.ModulesSet || !f.cfg.Modules[Sessions] {
		t.Fatalf("not persisted: %s", read(t, f.cfg.Path))
	}
	if !strings.HasPrefix(read(t, f.cfg.Path), "agentbooks: [agentbook.json]\nstateDir: state\n") {
		t.Fatalf("config rewritten: %s", read(t, f.cfg.Path))
	}
	// The old rc line is journaled, so disable removes it.
	_, err := Disable(f.env, Sessions, Options{})
	must(t, err)
	if strings.Contains(read(t, zshrc), RCLine) {
		t.Fatalf("old rc line kept: %q", read(t, zshrc))
	}
}

func TestMigrationFallsBackInMemoryWhenWriteFails(t *testing.T) {
	f := newFixture(t, "agentbooks: [agentbook.json]\nstateDir: state\nwaBridge: true\nwaOutbox: wa/outbox\n")
	must(t, os.WriteFile(filepath.Join(f.bpHome, "agentbook.json"), []byte(`{"orchestrator":"main","agents":[]}`), 0600))
	f.reload()
	must(t, os.Chmod(f.bpHome, 0500))
	t.Cleanup(func() { os.Chmod(f.bpHome, 0700) })
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	cfg := Init(f.cfg)
	if !EnabledIn(cfg, WA) || !EnabledIn(cfg, Sessions) {
		t.Fatalf("in-memory set = %v", cfg.Modules)
	}
}
