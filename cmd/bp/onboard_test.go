package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
	os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 99\n"), 0700)
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
	if err := a.onboard([]string{"--cli", "codex", "--prepare"}); err != nil {
		t.Fatal(err)
	}
	var state onboardingState
	stateData, err := os.ReadFile(filepath.Join(bpHome, "main", "onboarding.json"))
	if err != nil || json.Unmarshal(stateData, &state) != nil || state.CLI != "codex" || state.Agent != "main" {
		t.Fatalf("updated onboarding state=%+v, read error=%v", state, err)
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

func TestOnboardPreparationReusesPersistedCustomCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTBOOK", "")
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(bin, "my-agent")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	bpHome := filepath.Join(home, ".blueprint")
	a := &app{ctx: context.Background(), config: bpconfig.Config{Home: bpHome, Agentbooks: []string{filepath.Join(bpHome, "agentbook.json")}}, out: testOutput(t), err: testOutput(t)}
	wantArgs := []string{command, "--prompt", "{prompt}"}
	if err := a.onboard(append([]string{"--cli", "custom", "--prepare", "--"}, wantArgs...)); err != nil {
		t.Fatal(err)
	}
	if err := a.onboard([]string{"--cli", "custom", "--prepare", "--", command, "--prompt"}); err == nil {
		t.Fatal("custom command without prompt placeholder was accepted")
	}
	if err := a.onboard([]string{"--prepare"}); err != nil {
		t.Fatalf("reusing persisted custom command: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(bpHome, "main", "onboarding.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state onboardingState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.CLI != "custom" || state.Agent != "main" || !reflect.DeepEqual(state.Args, wantArgs) {
		t.Fatalf("onboarding state=%+v", state)
	}
}
