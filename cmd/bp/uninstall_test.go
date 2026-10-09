package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blueprint/internal/bpskill"
	bpconfig "blueprint/internal/config"
	"blueprint/internal/modules"
)

type installFixture struct {
	t      *testing.T
	home   string
	bpHome string
	out    *os.File
}

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("ZDOTDIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENTBOOK", "")
	bpHome := filepath.Join(home, ".blueprint")
	t.Setenv("BP_HOME", bpHome)
	return &installFixture{t: t, home: home, bpHome: bpHome, out: testOutput(t)}
}

func (f *installFixture) app() *app {
	f.t.Helper()
	cfg, err := bpconfig.LoadHome(f.bpHome)
	if err != nil {
		f.t.Fatal(err)
	}
	return &app{ctx: context.Background(), config: modules.Init(cfg), out: f.out, err: f.out}
}

func (f *installFixture) run(args ...string) string {
	f.t.Helper()
	before, _ := f.out.Seek(0, 1)
	if err := f.app().run(args); err != nil {
		f.t.Fatalf("bp %v: %v\n%s", args, err, readTestOutput(f.t, f.out))
	}
	all := readTestOutput(f.t, f.out)
	return all[before:]
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestSetupChangesNothingOutsideBPHome(t *testing.T) {
	f := newInstallFixture(t)
	rc := filepath.Join(f.home, ".zshrc")
	if err := os.WriteFile(rc, []byte("export A=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.run("setup")
	if data, _ := os.ReadFile(rc); string(data) != "export A=1\n" {
		t.Fatalf("setup edited the rc file: %q", data)
	}
	if exists(filepath.Join(f.home, ".config", "bp", "shell.sh")) || exists(filepath.Join(f.home, ".tmux.conf")) {
		t.Fatal("setup wrote shell or tmux integration")
	}
	cfg := f.app().config
	for _, name := range modules.Names() {
		if modules.EnabledIn(cfg, name) {
			t.Fatalf("%s enabled by setup", name)
		}
	}
	// The agent hint is the one thing outside bp's home, and it is recorded.
	skill := filepath.Join(f.home, ".claude", "skills", "blueprint", "SKILL.md")
	if !exists(skill) {
		t.Fatal("agent hint missing")
	}
	journal, err := modules.LoadJournal(cfg, modules.Install)
	if err != nil || len(journal.Changes) != 2 {
		t.Fatalf("install journal = %+v, %v", journal, err)
	}
}

func TestUninstallRemovesOnlyWhatBPAdded(t *testing.T) {
	f := newInstallFixture(t)
	rc := filepath.Join(f.home, ".zshrc")
	const original = "export A=1\n"
	if err := os.WriteFile(rc, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	userSkill := filepath.Join(f.home, ".codex", "skills", "mine", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(userSkill), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userSkill, []byte("mine"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(f.home, ".local", "bin", "bp")
	if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("\x7fELF "+binaryMarker+" usage"), 0755); err != nil {
		t.Fatal(err)
	}
	tmuxConf := filepath.Join(f.home, ".tmux.conf")
	if err := os.WriteFile(tmuxConf, []byte("set -g prefix C-a\n\n# blueprint: clipboard, scroll, and mosh integration\nset -g mouse on\n"), 0600); err != nil {
		t.Fatal(err)
	}

	f.run("setup")
	f.run("_install-record", "binary", binary)
	f.run("_install-record", "line", tmuxConf, "# blueprint: clipboard, scroll, and mosh integration")
	f.run("_install-record", "line", tmuxConf, "set -g mouse on")
	f.run("enable", "sessions")
	if data, _ := os.ReadFile(rc); !strings.Contains(string(data), modules.RCLine) {
		t.Fatal("sessions did not add the rc line")
	}

	plan := f.run("uninstall", "--dry-run")
	if !strings.Contains(plan, "would") || !exists(binary) || !exists(modules.ShellPath(f.home)) {
		t.Fatalf("dry run changed files or printed nothing:\n%s", plan)
	}

	out := f.run("uninstall")
	if data, _ := os.ReadFile(rc); string(data) != original {
		t.Fatalf("rc = %q\n%s", data, out)
	}
	if data, _ := os.ReadFile(tmuxConf); string(data) != "set -g prefix C-a\n" {
		t.Fatalf("tmux.conf = %q", data)
	}
	for _, path := range []string{binary, modules.ShellPath(f.home), filepath.Join(f.home, ".claude", "skills", "blueprint", "SKILL.md"), filepath.Join(f.home, ".codex", "skills", "blueprint")} {
		if exists(path) {
			t.Errorf("left behind: %s\n%s", path, out)
		}
	}
	if !exists(userSkill) || !exists(filepath.Join(f.home, ".local", "bin")) {
		t.Fatal("removed something bp did not add")
	}
	if !exists(filepath.Join(f.bpHome, "agentbook.json")) || !strings.Contains(out, "kept: config") {
		t.Fatalf("state removed without --purge:\n%s", out)
	}
	if modules.EnabledIn(f.app().config, modules.Sessions) {
		t.Fatal("sessions still enabled")
	}

	f.run("uninstall", "--purge", "--yes")
	if exists(f.bpHome) {
		t.Fatal("--purge kept bp's home")
	}
}

func TestUninstallKeepsReplacedBinary(t *testing.T) {
	f := newInstallFixture(t)
	f.run("setup")
	binary := filepath.Join(f.home, ".local", "bin", "bp")
	if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("\x7fELF "+binaryMarker+" v1"), 0755); err != nil {
		t.Fatal(err)
	}
	f.run("_install-record", "binary", binary)
	// Another tool (or the user) puts a different bp there later.
	if err := os.WriteFile(binary, []byte("\x7fELF "+binaryMarker+" someone else's build"), 0755); err != nil {
		t.Fatal(err)
	}
	out := f.run("uninstall")
	if !exists(binary) || !strings.Contains(out, "kept: file "+binary) {
		t.Fatalf("foreign binary removed or not reported:\n%s", out)
	}
}

func TestInstallRecordRefusesAnythingTheInstallerDoesNotWrite(t *testing.T) {
	f := newInstallFixture(t)
	f.run("setup")
	keys := filepath.Join(f.home, ".ssh", "authorized_keys")
	if err := os.MkdirAll(filepath.Dir(keys), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keys, []byte("ssh-ed25519 AAAA user\n"), 0600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(f.home, "notes.txt")
	if err := os.WriteFile(other, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"line", keys, "ssh-ed25519 AAAA user"},
		{"line", filepath.Join(f.home, ".tmux.conf"), "run-shell evil"},
		{"file", other, "x"},
		{"binary", other},
		{"link", other, "/usr/bin/bp"},
	} {
		if err := f.app().run(append([]string{"_install-record"}, args...)); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
	f.run("uninstall")
	if data, _ := os.ReadFile(keys); string(data) != "ssh-ed25519 AAAA user\n" || !exists(other) {
		t.Fatal("uninstall touched a file the installer never wrote")
	}
}

func TestEnableRefusesConflictWithoutForce(t *testing.T) {
	f := newInstallFixture(t)
	f.run("setup")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "x")
	err := f.app().run([]string{"enable", "accounts"})
	var conflict *modules.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v", err)
	}
	if modules.EnabledIn(f.app().config, modules.Accounts) {
		t.Fatal("enabled despite the conflict")
	}
	f.run("enable", "accounts", "--force")
	if !modules.EnabledIn(f.app().config, modules.Accounts) {
		t.Fatal("--force did not enable")
	}
	out := f.run("modules")
	if !strings.Contains(out, "accounts  on") {
		t.Fatalf("bp modules:\n%s", out)
	}
	f.run("disable", "accounts")
	if modules.EnabledIn(f.app().config, modules.Accounts) {
		t.Fatal("still enabled")
	}
}

func TestSetupCheckRequiresOnlyWhatRequestedModulesNeed(t *testing.T) {
	f := newInstallFixture(t)
	t.Setenv("SHELL", "/usr/bin/fish")
	if err := f.app().run([]string{"setup", "--check"}); err != nil {
		t.Fatalf("plain preflight refused fish: %v", err)
	}
	if err := f.app().run([]string{"setup", "--check", "--enable", "ui,sessions"}); err == nil || !strings.Contains(err.Error(), "bash and zsh") {
		t.Fatalf("sessions preflight with fish = %v", err)
	}
	if err := f.app().run([]string{"setup", "--check", "--enable", "nope"}); err == nil || !strings.Contains(err.Error(), "unknown module") {
		t.Fatalf("unknown module preflight = %v", err)
	}
}

func TestHintKeepsUsersOwnBlueprintSkill(t *testing.T) {
	f := newInstallFixture(t)
	own := filepath.Join(f.home, ".claude", "skills", "blueprint", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(own), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(own, []byte("my notes"), 0600); err != nil {
		t.Fatal(err)
	}
	f.run("setup")
	codex := filepath.Join(f.home, ".codex", "skills", "blueprint", "SKILL.md")
	if data, _ := os.ReadFile(codex); !strings.Contains(string(data), "untrusted input") {
		t.Fatalf("hint missing or without the untrusted-input line: %q", data)
	}
	f.run("uninstall")
	if data, _ := os.ReadFile(own); string(data) != "my notes" {
		t.Fatalf("user's skill changed: %q", data)
	}
	if exists(codex) {
		t.Fatal("bp's hint left behind")
	}
}

func TestPurgeRefusesADirectoryBPCannotProveItOwns(t *testing.T) {
	f := newInstallFixture(t)
	project := filepath.Join(f.home, "work", "app")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	// Someone's project with a config.json of its own.
	if err := os.WriteFile(filepath.Join(project, "config.json"), []byte(`{"name":"app"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BP_HOME", project)
	f.bpHome = project
	f.run("setup")
	if bpconfig.IsMarkedHome(project) {
		t.Fatal("setup marked a pre-existing custom home as bp's")
	}
	err := f.app().run([]string{"uninstall", "--purge", "--yes"})
	if err == nil || !strings.Contains(err.Error(), bpconfig.HomeSentinel) {
		t.Fatalf("err = %v", err)
	}
	if !exists(filepath.Join(project, "config.json")) {
		t.Fatal("project deleted")
	}
	t.Setenv("BP_HOME", "relative/home")
	if err := (&app{ctx: context.Background(), config: bpconfig.Config{Home: "relative/home"}, out: f.out, err: f.out}).run([]string{"uninstall", "--purge", "--yes"}); err == nil || !strings.Contains(err.Error(), "relative") {
		t.Fatalf("relative home: %v", err)
	}
}

func TestTmuxLinesCountOnlyInsideBPBlock(t *testing.T) {
	f := newInstallFixture(t)
	f.run("setup")
	conf := filepath.Join(f.home, ".tmux.conf")
	const header = "# blueprint: clipboard, scroll, and mosh integration"
	original := "set -g mouse on\nset -g prefix C-a\n\n" + header + "\nset -g history-limit 100000\n\nset -g mouse on\n"
	if err := os.WriteFile(conf, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	// The user's own "set -g mouse on" (outside bp's block) cannot be claimed.
	if err := f.app().run([]string{"_install-record", "line", conf, "set -g mouse on"}); err == nil {
		t.Fatal("recorded a tmux line outside bp's block")
	}
	f.run("_install-record", "line", conf, header)
	f.run("_install-record", "line", conf, "set -g history-limit 100000")
	f.run("uninstall")
	if data, _ := os.ReadFile(conf); string(data) != "set -g mouse on\nset -g prefix C-a\n\nset -g mouse on\n" {
		t.Fatalf("tmux.conf = %q", data)
	}
}

// The owner's server (legacy) runs agent sessions and already has the
// operational skill; an upgrade must leave it in place, never swap in the
// short hint, and enabling or disabling modules must not touch it either.
func TestLegacyUpgradeKeepsOperationalSkill(t *testing.T) {
	f := newInstallFixture(t)
	var skills []string
	for _, root := range []string{".claude", ".codex"} {
		path := filepath.Join(f.home, root, "skills", "blueprint", "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, bpskill.Content, 0600); err != nil {
			t.Fatal(err)
		}
		skills = append(skills, path)
	}
	before := map[string]os.FileInfo{}
	for _, path := range skills {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = info
	}
	cfg, err := bpconfig.LoadHome(f.bpHome)
	if err != nil {
		t.Fatal(err)
	}
	// Legacy with sessions switched off still gets the operational skill.
	cfg.Legacy, cfg.Modules, cfg.ModulesSet = true, map[string]bool{}, true
	a := &app{ctx: context.Background(), config: cfg, out: f.out, err: f.out}
	lines, err := a.installAgentHint(a.moduleEnv())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range skills {
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, bpskill.Content) {
			t.Fatalf("%s is no longer the operational skill (%v):\n%s", path, err, data)
		}
		info, _ := os.Stat(path)
		if !info.ModTime().Equal(before[path].ModTime()) || !os.SameFile(info, before[path]) {
			t.Fatalf("%s was rewritten", path)
		}
	}
	if got := strings.Join(lines, "\n"); strings.Count(got, "skill ready: ") != 2 {
		t.Fatalf("got %q", got)
	}
}
