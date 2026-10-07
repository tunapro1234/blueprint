package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blueprint/internal/book"
	bpconfig "blueprint/internal/config"
	bptmux "blueprint/internal/tmux"
)

func TestCodexPolicyDefaultsUnflaggedOpenToClaude(t *testing.T) {
	dir := t.TempDir()
	bookPath := openTestBook(t, dir, "closed")
	tmux, _, _ := openTestTmux(t, bookPath, "bypass permissions", false)
	a := openTestApp(t, bookPath, tmux)
	a.config.Codex = &bpconfig.CodexConfig{Disabled: true}

	// Without the policy this is the fresh-Codex --no-prompt refusal.
	if err := a.open([]string{"ghost", dir, "--no-prompt"}); err != nil {
		t.Fatalf("unflagged open under codex.disabled failed: %v", err)
	}
	fleet, err := book.LoadFleet([]string{bookPath})
	if err != nil {
		t.Fatal(err)
	}
	if launch := fleet.Agents["ghost"].Launch; launch == nil || launch.Codex || launch.Hermes || launch.OpenCode {
		t.Fatalf("launch = %+v, want a Claude launch", launch)
	}
}

func TestCodexPolicyRefusesExplicitCodexBeforeAnyTmuxCall(t *testing.T) {
	dir := t.TempDir()
	bookPath := openTestBook(t, dir, "closed")
	tmux, calls, marker := issueTmux(t, "› Ask Codex to do anything\n", false)
	a := issueApp(t, bookPath, filepath.Join(t.TempDir(), "state"), tmux)
	a.config.Codex = &bpconfig.CodexConfig{Disabled: true}

	for _, args := range [][]string{
		{"ghost", dir, "--codex"},
		{"ghost", dir, "--remote", "unix://"},
	} {
		err := a.open(args)
		if err == nil || !strings.Contains(err.Error(), "codex.disabled") || !strings.Contains(err.Error(), "--allow-codex") {
			t.Fatalf("open %v error=%v, want the codex.disabled refusal", args, err)
		}
	}
	if data, err := os.ReadFile(calls); err == nil && len(data) > 0 {
		t.Fatalf("refused open called tmux: %s", data)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("refused open created a tmux session: %v", err)
	}
	if got := bookStatus(t, bookPath, "ghost"); got != "closed" {
		t.Fatalf("refused open changed agentbook status to %q", got)
	}
}

func TestCodexPolicyRefusesRecordedCodexLaunchUnlessAllowed(t *testing.T) {
	const id = "01a0715e-a5c0-7171-adf3-c95343ce6d5b"
	dir := t.TempDir()
	writeCodexNoPromptResume(t, dir, id)
	bookPath := filepath.Join(dir, "agentbook.json")
	t.Setenv("AGENTBOOK", "")
	content := fmt.Sprintf("{\"agents\":[{\"name\":\"server-main\",\"folder\":\"%s\"},"+
		"{\"name\":\"ghost\",\"folder\":\"%s\",\"parent\":\"server-main\",\"status\":\"closed\","+
		"\"launch\":{\"codex\":true,\"resume\":true,\"resumeId\":\"%s\"}}]}\n", dir, dir, id)
	if err := os.WriteFile(bookPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	tmux, _, _ := openTestTmux(t, bookPath, "› Ask Codex to do anything", false)
	a := openTestApp(t, bookPath, tmux)
	a.config.Codex = &bpconfig.CodexConfig{Disabled: true}

	if err := a.open([]string{"ghost", dir, "--no-prompt"}); err == nil || !strings.Contains(err.Error(), "codex.disabled") {
		t.Fatalf("reopen of a recorded Codex launch error=%v, want the codex.disabled refusal", err)
	}
	if err := a.open([]string{"ghost", dir, "--no-prompt", "--allow-codex"}); err != nil {
		t.Fatalf("reopen with --allow-codex failed: %v", err)
	}
}

func TestCodexPolicyRefusesBpRunCodex(t *testing.T) {
	a := &app{config: bpconfig.Config{Codex: &bpconfig.CodexConfig{Disabled: true}}, out: testOutput(t), err: testOutput(t)}
	err := a.localRun([]string{"--name", "work", "codex"})
	if err == nil || !strings.Contains(err.Error(), "bp run codex") || !strings.Contains(err.Error(), "--allow-codex") {
		t.Fatalf("bp run codex error=%v, want the codex.disabled refusal", err)
	}
}

func TestDoctorListsRecordedCodexLaunchesUnderPolicy(t *testing.T) {
	fleet := book.Fleet{Agents: map[string]book.Agent{
		"old":    {Name: "old", Launch: &bptmux.OpenOptions{Codex: true}},
		"worker": {Name: "worker", Launch: &bptmux.OpenOptions{}},
	}}
	if _, ok := doctorCodexPolicyCheck(bpconfig.Config{}, fleet, ""); ok {
		t.Fatal("check reported without codex.disabled")
	}
	cfg := bpconfig.Config{Codex: &bpconfig.CodexConfig{Disabled: true}}
	check, ok := doctorCodexPolicyCheck(cfg, fleet, "")
	if !ok || !check.Warning || len(check.Related) != 1 || check.Related[0] != "old" {
		t.Fatalf("check = %+v, %v; want a warning naming only old", check, ok)
	}
	if _, ok := doctorCodexPolicyCheck(cfg, fleet, "worker"); ok {
		t.Fatal("check reported for a selected Claude agent")
	}
}
