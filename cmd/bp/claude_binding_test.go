package main

import (
	"blueprint/internal/book"
	bptmux "blueprint/internal/tmux"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestOpenClaudeSwitchReplacesCodexLaunchAndUsesExactResume(t *testing.T) {
	for _, flags := range [][]string{{"--claude", "--resume"}, {"--resume", "--claude"}, {"--thread", "69632f85-5244-4ec1-866c-31aa4adef0be", "--claude"}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			dir, home := t.TempDir(), t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", home)
			bookPath := openTestBook(t, dir, "closed")
			if e := book.SetStatus([]string{bookPath}, "ghost", "closed", dir, book.Registration{Launch: &bptmux.OpenOptions{Codex: true, ResumeID: "01a07246-1e79-73b0-9b69-74e791e68208", Remote: "unix://", NoSandbox: true}}); e != nil {
				t.Fatal(e)
			}
			id := "69632f85-5244-4ec1-866c-31aa4adef0be"
			project := filepath.Join(home, "projects", regexp.MustCompile(`[^a-zA-Z0-9-]`).ReplaceAllString(dir, "-"))
			if e := os.MkdirAll(project, 0700); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(project, id+".jsonl"), []byte("{\"type\":\"custom-title\",\"customTitle\":\"ghost\"}\n"), 0600); e != nil {
				t.Fatal(e)
			}
			client, _, _ := openTestTmux(t, bookPath, "bypass permissions", false)
			a := openTestApp(t, bookPath, client)
			if e := a.open(append([]string{"ghost", dir}, flags...)); e != nil {
				t.Fatal(e)
			}
			fleet, e := book.LoadFleet([]string{bookPath})
			if e != nil {
				t.Fatal(e)
			}
			launch := fleet.Agents["ghost"].Launch
			if launch == nil || launch.Codex || launch.ResumeID != id || launch.Remote != "" || launch.NoSandbox {
				t.Fatal(launch)
			}
			if len(fleet.Sources["ghost"]) != 1 || fleet.Sources["ghost"][0] != bookPath {
				t.Fatal(fleet.Sources)
			}
		})
	}
}

func TestOpenMissingClaudeResumeNeverCreatesFreshConversation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	bookPath := openTestBook(t, dir, "closed")
	client, session, _ := openTestTmux(t, bookPath, "bypass permissions", false)
	a := openTestApp(t, bookPath, client)
	if e := a.open([]string{"ghost", dir, "--claude", "--resume"}); e == nil {
		t.Fatal("missing resume opened fresh session")
	}
	if _, e := os.Stat(session); !os.IsNotExist(e) {
		t.Fatal("tmux session created", e)
	}
	if got := bookStatus(t, bookPath, "ghost"); got != "closed" {
		t.Fatal(got)
	}
}
