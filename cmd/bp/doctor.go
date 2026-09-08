package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"blueprint/internal/book"
	bpconfig "blueprint/internal/config"
	"blueprint/internal/release"
)

type doctorCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func doctor(cfg bpconfig.Config, configErr error, args []string) error {
	if len(args) > 1 || len(args) == 1 && args[0] != "--json" {
		return fmt.Errorf("usage: bp doctor [--json]")
	}
	if configErr == nil && cfg.InvalidConfig != "" {
		configErr = fmt.Errorf("%s", cfg.InvalidConfig)
	}
	checks := []doctorCheck{}
	add := func(name string, err error, detail string) {
		if err != nil {
			detail = err.Error()
		}
		checks = append(checks, doctorCheck{name, err == nil, detail})
	}
	add("config", configErr, cfg.Path)
	tmux, err := exec.LookPath("tmux")
	add("tmux", err, tmux)
	self, _ := os.Executable()
	found, err := exec.LookPath("bp")
	add("PATH", err, found+" (running "+self+")")
	shell := filepath.Base(os.Getenv("SHELL"))
	err = nil
	if shell != "bash" && shell != "zsh" {
		err = fmt.Errorf("unsupported shell %s", shell)
	}
	add("shell", err, shell)
	if configErr == nil {
		fleet, err := book.LoadFleet(book.Paths(cfg.Agentbooks))
		add("agentbook", err, fmt.Sprintf("%d agents, coordinator %s; bp book --json shows source paths", len(fleet.Agents), fleet.Root))
	}
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".config", "bp", "shell.sh")
	_, err = os.Stat(path)
	add("shell_integration", err, path)
	clis := []string{}
	for _, name := range []string{"codex", "claude", "opencode", "hermes"} {
		if p, err := exec.LookPath(name); err == nil {
			clis = append(clis, p)
		}
	}
	err = nil
	if len(clis) == 0 {
		err = fmt.Errorf("no supported agent CLI on PATH")
	}
	add("agent_cli", err, fmt.Sprint(clis))
	ok := true
	for _, c := range checks {
		if !c.OK {
			ok = false
		}
	}
	if len(args) == 1 {
		json.NewEncoder(os.Stdout).Encode(struct {
			Version string        `json:"version"`
			OK      bool          `json:"ok"`
			Checks  []doctorCheck `json:"checks"`
		}{release.Version(), ok, checks})
	} else {
		for _, c := range checks {
			mark := "OK"
			if !c.OK {
				mark = "FAIL"
			}
			fmt.Printf("%s %-18s %s\n", mark, c.Name, c.Detail)
		}
	}
	if !ok {
		return errReported
	}
	return nil
}
