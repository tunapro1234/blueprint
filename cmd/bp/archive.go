package main

import (
	"blueprint/internal/codexrpc"
	bptmux "blueprint/internal/tmux"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/pending"
)

func (a *app) archive(args []string, archive bool) error {
	if archive && len(args) > 0 && args[0] == "--stale" {
		return a.archiveStale(args[1:])
	}
	if archive && (len(args) == 1 || len(args) == 2) && args[0] == "--list" {
		if len(args) == 2 && args[1] != "--json" {
			return fmt.Errorf("usage: bp archive --list [--json]")
		}
		rows, err := book.Archives(a.config.Agentbooks)
		if err != nil {
			return err
		}
		if len(args) == 2 {
			return json.NewEncoder(a.out).Encode(rows)
		}
		for _, row := range rows {
			fmt.Fprintf(a.out, "%s\t%s\t%s\n", row.Agent.Name, row.Agent.ArchivedAt, row.Path)
		}
		return nil
	}
	verb := "archive"
	if !archive {
		verb = "restore"
	}
	if len(args) != 1 || !validAgentName(args[0]) {
		return fmt.Errorf("usage: bp %s <name>", verb)
	}
	name := args[0]
	release, err := a.lockPane(name)
	if err != nil {
		return err
	}
	defer release()
	err = book.SetArchived(a.config.Agentbooks, name, archive, func(agent book.Agent) error {
		return a.archiveGuard(agent, verb, archive)
	})
	if err != nil {
		return err
	}
	result := "archived"
	if !archive {
		result = "restored"
	}
	fmt.Fprintf(a.out, "%s %s; conversation files and launch metadata preserved\n", name, result)
	return nil
}

func (a *app) archiveGuard(agent book.Agent, verb string, archive bool) error {
	name := agent.Name
	if a.tmux == nil {
		return fmt.Errorf("cannot verify closed session: tmux is unavailable")
	}
	sessions, err := a.tmux.Sessions(a.ctx)
	if err != nil {
		// A missing tmux socket is no server; connection/access failures remain unknown.
		text := strings.ToLower(err.Error())
		if !strings.Contains(text, "error connecting") || !strings.Contains(text, "no such file or directory") {
			return fmt.Errorf("cannot verify closed session: %w", err)
		}
	}
	for _, session := range sessions {
		if session == name {
			return fmt.Errorf("%s has a tmux session; exit its CLI before %s (no process was stopped)", name, verb)
		}
	}
	if archive && agent.Local != nil && processIsLive(agent.Local.PID) {
		return fmt.Errorf("%s has a recorded native process still alive; verify it exited before %s (no record changed)", name, verb)
	}

	if archive && agent.Launch != nil && agent.Launch.Remote != "" {
		id := agent.IdentityThreadID
		if id == "" {
			id = agent.Launch.ResumeID
		}
		if !claudeResumeUUID.MatchString(id) {
			return fmt.Errorf("%s has no valid remote thread binding; inspect bp doctor --agent %s", name, name)
		}
		ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
		defer cancel()
		home := bptmux.CodexProcessInfo(0).Home
		if agent.Local != nil && agent.Local.Home != "" {
			home = agent.Local.Home
		}
		rpc, err := codexrpc.DialUnix(ctx, bptmux.CodexSocket(home, agent.Launch.Remote))
		if err != nil {
			return fmt.Errorf("cannot verify remote thread is unloaded: %w", err)
		}
		defer rpc.Close()
		thread, err := rpc.ThreadRead(ctx, id)
		if err != nil || thread.ID != id || thread.Status.Type != "notLoaded" {
			return fmt.Errorf("%s remote thread is loaded or unverified; no record changed", name)
		}
	}
	if archive && a.queue != nil {
		messages, err := a.queue.List()
		if err != nil {
			return err
		}
		for _, m := range messages {
			if m.To == name || strings.TrimSuffix(m.From, "?") == name {
				return fmt.Errorf("%s has pending channel %s; inspect bp qstat %s first", name, m.ID, m.ID)
			}
		}
	}
	if archive && a.config.StateDir != "" {
		status, err := pending.Stat(a.config.StateDir, name)
		if err != nil {
			return err
		}
		if status.Items+status.Dropped > 0 || len(status.Held) > 0 {
			return fmt.Errorf("%s has pending announcements (%d deliverable, %d held); preserve and resolve them before %s", name, status.Items, len(status.Held), verb)
		}
	}
	return nil
}

func (a *app) setLifetime(args []string, lifetime string) error {
	verb := "keep"
	result := "persistent"
	if lifetime == book.LifetimeEphemeral {
		verb, result = "release", "ephemeral"
	}
	if len(args) != 1 || !validAgentName(args[0]) {
		return fmt.Errorf("usage: bp %s <name>", verb)
	}
	if err := book.SetLifetime(a.config.Agentbooks, args[0], lifetime); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s is now %s\n", args[0], result)
	return nil
}

func (a *app) archiveClosedEphemerals() []string {
	results, err := book.ArchiveClosedEphemerals(a.config.Agentbooks, func(agent book.Agent) error {
		return a.archiveGuard(agent, "archive", true)
	})
	if err != nil {
		return []string{"automatic archive failed: " + err.Error()}
	}
	return results
}

func (a *app) archiveStale(args []string) error {
	dryRun := false
	for _, arg := range args {
		if arg != "--dry-run" || dryRun {
			return fmt.Errorf("usage: bp archive --stale [--dry-run]")
		}
		dryRun = true
	}
	records, err := book.Records(a.config.Agentbooks)
	if err != nil {
		return err
	}
	var names []string
	seen := map[string]bool{}
	for _, record := range records {
		agent := record.Agent
		if agent.ArchivedAt != "" || agent.Status != "closed" || agent.Lifetime == book.LifetimePersistent || seen[agent.Name] || !book.TranscriptMissing(agent) {
			continue
		}
		seen[agent.Name] = true
		names = append(names, agent.Name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := book.CheckArchive(a.config.Agentbooks, name); err != nil {
			fmt.Fprintf(a.out, "skipped %s: %v\n", name, err)
			continue
		}
		if dryRun {
			fmt.Fprintf(a.out, "would archive %s\n", name)
			continue
		}
		if err := book.SetArchived(a.config.Agentbooks, name, true, func(agent book.Agent) error {
			return a.archiveGuard(agent, "archive", true)
		}); err != nil {
			fmt.Fprintf(a.out, "skipped %s: %v\n", name, err)
			continue
		}
		fmt.Fprintf(a.out, "archived %s; conversation files and launch metadata preserved\n", name)
	}
	if len(names) == 0 {
		fmt.Fprintln(a.out, "no stale registrations found")
	}
	return nil
}
