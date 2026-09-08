package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blueprint/internal/book"
	bpconfig "blueprint/internal/config"
)

func TestOnboardPreparationIsGenericRepeatableAndReadOnlyForExistingFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTBOOK", "")
	bin := filepath.Join(home, "bin")
	os.Mkdir(bin, 0700)
	os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nexit 99\n"), 0700)
	t.Setenv("PATH", bin)
	bpHome := filepath.Join(home, ".blueprint")
	a := &app{ctx: context.Background(), config: bpconfig.Config{Home: bpHome, Agentbooks: []string{filepath.Join(bpHome, "agentbook.json")}}, out: testOutput(t), err: testOutput(t)}
	if err := a.onboard([]string{"--cli", "claude", "--prepare"}); err != nil {
		t.Fatal(err)
	}
	machine := filepath.Join(bpHome, "main", "MACHINE.md")
	os.WriteFile(machine, []byte("user rules"), 0600)
	promptPath := filepath.Join(bpHome, "main", "ONBOARDING.md")
	prompt, _ := os.ReadFile(promptPath)
	if strings.Contains(string(prompt), "/srv/") || strings.Contains(string(prompt), "server-main") || !strings.Contains(string(prompt), "Hyprland") {
		t.Fatal("nonportable prompt")
	}
	os.WriteFile(promptPath, []byte("custom onboarding"), 0600)
	if err := a.onboard([]string{"--prepare"}); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(promptPath)
	if string(saved) != "custom onboarding" {
		t.Fatal("overwrote user prompt")
	}
	saved, _ = os.ReadFile(machine)
	if string(saved) != "user rules" {
		t.Fatal("overwrote machine instructions")
	}
	fleet, err := book.LoadFleet(a.config.Agentbooks)
	if err != nil || fleet.Root != "main" || len(fleet.Agents) != 1 {
		t.Fatal(fleet, err)
	}
	if err := a.showBook([]string{"--json"}); err != nil {
		t.Fatal(err)
	}
	if err := a.run([]string{"does-not-exist"}); err == nil {
		t.Fatal("unknown command succeeded")
	}
}
