package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

	f.run("uninstall", "--purge")
	if exists(f.bpHome) {
		t.Fatal("--purge kept bp's home")
	}
}

func TestUninstallKeepsReplacedBinary(t *testing.T) {
	f := newInstallFixture(t)
	f.run("setup")
	binary := filepath.Join(f.home, "bin", "bp")
	if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("someone else's bp"), 0755); err != nil {
		t.Fatal(err)
	}
	f.run("_install-record", "binary", binary)
	out := f.run("uninstall")
	if !exists(binary) || !strings.Contains(out, "kept: file "+binary) {
		t.Fatalf("foreign binary removed or not reported:\n%s", out)
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
