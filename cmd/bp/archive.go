package main

import (
	"blueprint/internal/codexrpc"
	bptmux "blueprint/internal/tmux"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/pending"
)

func (a *app) archive(args []string, archive bool) error {
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
			count, over, err := pending.Stat(a.config.StateDir, name)
			if err != nil {
				return err
			}
			if count+over > 0 {
				return fmt.Errorf("%s has pending announcements; preserve and resolve them before %s", name, verb)
			}
		}
		return nil
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
