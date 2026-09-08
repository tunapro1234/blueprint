package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bpconfig "blueprint/internal/config"
)

func TestBatchCommandsBypassTmux(t *testing.T) {
	for _, tc := range []struct {
		cli   string
		args  []string
		batch bool
	}{
		{"codex", nil, false}, {"codex", []string{"resume", "a-thread"}, false},
		{"codex", []string{"exec", "test"}, true}, {"codex", []string{"--help"}, true},
		{"codex", []string{"app-server"}, true}, {"claude", []string{"-p", "test"}, true},
		{"claude", []string{"--resume"}, false}, {"opencode", []string{"run", "test"}, true},
		{"opencode", nil, false}, {"hermes", []string{"setup"}, true},
		{"codex", []string{"-c", "model=example", "exec", "test"}, true},
		{"codex", []string{"--model", "example", "resume"}, false},
		{"codex", []string{"--", "review"}, false},
		{"opencode", []string{"--model=example", "run", "test"}, true},
	} {
		if got := batchCommand(tc.cli, tc.args); got != tc.batch {
			t.Errorf("%s %v: batch=%v", tc.cli, tc.args, got)
		}
	}
}

func TestLocalSetupPreservesFilesAndCanBeRepeated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", "")
	t.Setenv("AGENTBOOK", "")
	rc := filepath.Join(home, ".zshrc")
	const original = "export MY_SETTING='keep me'\n"
	if err := os.WriteFile(rc, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	book := filepath.Join(home, ".blueprint/agentbook.json")
	a := &app{ctx: context.Background(), config: bpconfig.Config{Home: filepath.Join(home, ".blueprint"), Agentbooks: []string{book}}, out: testOutput(t)}
	for i := 0; i < 2; i++ {
		if err := a.localSetup([]string{"--shell", "zsh"}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), original) || strings.Count(string(data), "# bp local agents") != 1 {
		t.Fatalf("startup file overwritten/duplicated: %s", data)
	}
	backups, _ := filepath.Glob(rc + ".before-bp-*")
	if len(backups) != 1 {
		t.Fatalf("backups=%v", backups)
	}
	if _, err := os.Stat(filepath.Join(home, ".tmux.conf")); !os.IsNotExist(err) {
		t.Fatal("setup touched tmux config")
	}
	if err := os.WriteFile(book, []byte(`{"orchestrator":"mine","agents":[],"custom":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.localSetup([]string{"--shell", "zsh"}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(book)
	if !strings.Contains(string(data), `"custom":true`) {
		t.Fatal("setup replaced existing book")
	}
}
