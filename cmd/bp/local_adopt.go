package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"syscall"

	"blueprint/internal/book"
	bptmux "blueprint/internal/tmux"
)

func (a *app) hasTmuxSession(name string) bool {
	if a.tmux == nil {
		return false
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.tmux.HasSession(ctx, name)
}

func (a *app) checkThreadBinding(target, thread string, adopt bool) error {
	return book.CheckThreadBinding(a.config.Agentbooks, target, thread, adopt, a.hasTmuxSession)
}

func (a *app) adoptThreadBinding(target, thread string) error {
	moved, err := book.AdoptThread(a.config.Agentbooks, target, thread, a.hasTmuxSession)
	if err != nil {
		return err
	}
	out := a.out
	if out == nil {
		out = os.Stdout
	}
	for _, binding := range moved {
		state := binding.Status
		if binding.Archived {
			state = "archived"
		} else if state == "" {
			state = "unknown"
		}
		fmt.Fprintf(out, "adopted thread %s from %s (%s) in %s\n", thread, binding.Name, state, binding.Path)
	}
	return nil
}

// adoptNativeThread reuses a registration carrying the exact native title
// thread. It never mints a second record for a conversation already known to
// bp. A live open owner is refused; stale/closed owners can be recovered.
func (a *app) adoptNativeThread(thread, requested string) (string, bool, error) {
	records, err := book.Records(a.config.Agentbooks)
	if err != nil {
		return "", false, err
	}
	var matches []book.Agent
	seen := map[string]bool{}
	for _, record := range records {
		agent := record.Agent
		if agent.ArchivedAt != "" || agent.NativeTitle == nil || agent.NativeTitle.ThreadID != thread || seen[agent.Name] {
			continue
		}
		seen[agent.Name] = true
		matches = append(matches, agent)
	}
	if len(matches) == 0 {
		return "", false, nil
	}
	for _, agent := range matches {
		if agent.Status == "open" && a.localRecordProcessLive(agent) {
			return "", false, fmt.Errorf("native thread %s is held by live agent %s; use bp attach %s instead of creating a second session", thread, agent.Name, agent.Name)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Name == requested && matches[j].Name != requested {
			return true
		}
		if matches[j].Name == requested && matches[i].Name != requested {
			return false
		}
		if matches[i].Status == "opening" && matches[j].Status != "opening" {
			return true
		}
		if matches[j].Status == "opening" && matches[i].Status != "opening" {
			return false
		}
		return matches[i].Name < matches[j].Name
	})
	chosen := matches[0]
	if a.tmux != nil && a.tmux.HasSession(a.ctx, chosen.Name) {
		process, err := a.tmux.PaneProcess(a.ctx, chosen.Name)
		pane, _ := a.tmux.Capture(a.ctx, chosen.Name)
		if err != nil || !bptmux.IsAgentPane(process.Command, pane) {
			return "", false, fmt.Errorf("thread %s is registered as %s, but its tmux name is occupied by an unverified pane; no second session was opened", thread, chosen.Name)
		}
		return chosen.Name, true, nil
	}
	return chosen.Name, false, nil
}

func (a *app) localRecordProcessLive(agent book.Agent) bool {
	if agent.Local != nil && processIsLive(agent.Local.PID) {
		return true
	}
	if a.tmux == nil || !a.tmux.HasSession(a.ctx, agent.Name) {
		return false
	}
	process, err := a.tmux.PaneProcess(a.ctx, agent.Name)
	if err != nil || process.PID <= 0 {
		return false
	}
	pane, _ := a.tmux.Capture(a.ctx, agent.Name)
	return bptmux.IsAgentPane(process.Command, pane)
}

func processIsLive(pid int) bool {
	if pid <= 1 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
