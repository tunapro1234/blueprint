package main

import (
	"fmt"
	"sort"
	"strings"

	"blueprint/internal/book"
	"blueprint/internal/cache"
)

// nativeName uses an already-bound runtime, never title/cwd to discover a thread.
func (a *app) nativeName(agent book.Agent, state *cache.State) string {
	if state == nil || state.Runtime != "claude" || state.Activity == nil || state.Activity.ThreadID == "" || state.Activity.TranscriptPath == "" {
		return agent.Name
	}
	value, err := book.ReadNativeTitle(state.Activity.TranscriptPath, state.Activity.ThreadID, agent.NativeTitle)
	if err != nil {
		return agent.Name
	}
	if agent.NativeTitle == nil || agent.NativeTitle.ThreadID != value.ThreadID || agent.NativeTitle.Path != value.Path || agent.NativeTitle.Text != value.Text || value.Offset < agent.NativeTitle.Offset || value.Offset-agent.NativeTitle.Offset >= 512*1024 {
		_ = book.SetNativeTitle(a.config.Agentbooks, agent.Name, value)
	}
	if !validAgentName(value.Text) {
		return agent.Name
	}
	fleet, err := book.LoadFleet(book.Paths(a.config.Agentbooks))
	if err != nil {
		return agent.Name
	}
	if value.Text != agent.Name {
		if _, taken := fleet.Agents[value.Text]; taken || value.Text == fleet.Root || value.Text == "server-main" {
			return agent.Name
		}
	}
	return value.Text
}

func (a *app) liveName(name string) string {
	if a.tmux == nil {
		return name
	}
	fleet, err := book.LoadFleet(book.Paths(a.config.Agentbooks))
	if err != nil {
		return name
	}
	agent, ok := fleet.Agents[name]
	if !ok {
		return name
	}
	if agent.Local != nil {
		if agent.Local.Harness != "claude" {
			return name
		}
	} else {
		process, err := a.tmux.PaneProcess(a.ctx, name)
		if err != nil || process.Command != "claude" {
			return name
		}
	}
	state, _ := book.RuntimeState(a.ctx, a.tmux, agent)
	return a.nativeName(agent, &state)
}

// Canonical names always win. A unique live title is a convenience address;
// resolve it before hierarchy and delivery gates, keeping the canonical identity.
func (a *app) resolveNativeTarget(name string) (string, error) {
	if a.tmux == nil || a.hasSession(name) {
		return name, nil
	}
	fleet, err := book.LoadFleet(book.Paths(a.config.Agentbooks))
	if err != nil {
		return "", err
	}
	if _, ok := fleet.Agents[name]; ok {
		return name, nil
	}
	var matches []string
	for canonical := range fleet.Agents {
		if a.liveName(canonical) == name {
			matches = append(matches, canonical)
		}
	}
	if len(matches) > 1 {
		sort.Strings(matches)
		return "", fmt.Errorf("ambiguous agent title %q: %s; use the session name", name, strings.Join(matches, ", "))
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return name, nil
}
