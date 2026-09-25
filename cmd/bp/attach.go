package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/identity"
)

type attachMatch struct {
	record book.Record
	live   bool
}

func validLocalAttachQuery(query string) bool {
	return strings.TrimSpace(query) != "" && !strings.ContainsAny(query, "\x00\r\n")
}

func validateRemoteAgentName(name string) error {
	if !identity.ValidName(name) {
		return fmt.Errorf("invalid remote agent name: %q", name)
	}
	return nil
}

func resolveAttachRecord(records []book.Record, requested string, live func(string) bool) (book.Record, bool, error) {
	var canonical, titled []attachMatch
	for _, record := range records {
		match := attachMatch{record: record}
		if record.Agent.Name == requested {
			canonical = append(canonical, match)
		} else if record.Agent.NativeTitle != nil && record.Agent.NativeTitle.Text == requested {
			titled = append(titled, match)
		}
	}
	matches := canonical
	if len(matches) == 0 {
		matches = titled
	}
	if len(matches) == 0 {
		return book.Record{}, false, fmt.Errorf("unknown agent: %q", requested)
	}
	for index := range matches {
		matches[index].live = live(matches[index].record.Agent.Name)
	}
	var preferred []attachMatch
	for _, match := range matches {
		if match.live {
			preferred = append(preferred, match)
		}
	}
	if len(preferred) == 0 {
		for _, match := range matches {
			if match.record.Agent.ArchivedAt == "" {
				preferred = append(preferred, match)
			}
		}
	}
	if len(preferred) > 0 {
		matches = preferred
	}
	if len(matches) != 1 {
		candidates := make([]string, 0, len(matches))
		for _, match := range matches {
			state := "active"
			if match.live {
				state = "live"
			} else if match.record.Agent.ArchivedAt != "" {
				state = "archived"
			}
			candidates = append(candidates, fmt.Sprintf("%s (%s; %s)", match.record.Agent.Name, state, match.record.Path))
		}
		sort.Strings(candidates)
		return book.Record{}, false, fmt.Errorf("ambiguous agent %q: %s", requested, strings.Join(candidates, ", "))
	}
	return matches[0].record, matches[0].live, nil
}

func attachCommand(tmuxBin, name string, insideTmux bool) commandSpec {
	operation := "attach-session"
	if insideTmux {
		operation = "switch-client"
	}
	return commandSpec{Path: tmuxBin, Args: []string{tmuxBin, operation, "-t", "=" + name}}
}

func (a *app) attach(args []string) error {
	if len(args) == 0 || len(args) > 2 {
		return fmt.Errorf("usage: bp attach <agent> [--no-revive]")
	}
	if err := rejectFlag("attach", args[0]); err != nil {
		return err
	}
	noRevive := false
	if len(args) == 2 {
		if args[1] != "--no-revive" {
			return fmt.Errorf("unknown attach option: %s", args[1])
		}
		noRevive = true
	}
	if agent, server, ok := strings.Cut(args[0], "@"); ok {
		if agent == "" || server == "" || strings.Contains(server, "@") {
			return fmt.Errorf("invalid remote agent: %q", args[0])
		}
		if err := validateRemoteAgentName(agent); err != nil {
			return err
		}
		remote, ok := a.config.Remotes[server]
		if !ok {
			return fmt.Errorf("unknown remote: %q", server)
		}
		remoteArgs := []string{"attach", agent}
		if noRevive {
			remoteArgs = append(remoteArgs, "--no-revive")
		}
		spec, err := remoteBPCommand(remote, remoteArgs, execLookPath)
		if err != nil {
			return err
		}
		return replaceWith(spec)
	}
	if !validLocalAttachQuery(args[0]) {
		return fmt.Errorf("invalid agent name or title: %q", args[0])
	}
	records, err := book.Records(a.config.Agentbooks)
	if err != nil {
		return err
	}
	record, live, err := resolveAttachRecord(records, args[0], func(name string) bool {
		return a.tmux.HasSession(a.ctx, name)
	})
	if err != nil {
		if strings.HasPrefix(err.Error(), "unknown agent:") {
			a.writeP2PLookupTips(args[0])
		}
		return err
	}
	name := record.Agent.Name
	if record.Agent.ArchivedAt != "" {
		return fmt.Errorf("%s is archived; use bp restore %s first", name, name)
	}
	fmt.Fprintf(a.err, "bp attach %q: resolved to %s (%s)\n", args[0], name, record.Path)
	if !live {
		if noRevive {
			return fmt.Errorf("%s is not running (--no-revive)", name)
		}
		if record.Agent.Launch == nil {
			return fmt.Errorf("stale record for %s: launch metadata is missing; refusing to create an empty session", name)
		}
		if record.Agent.Launch.OpenCode {
			return fmt.Errorf("cannot revive %s: opencode conversation resume is unavailable; refusing to open a different conversation", name)
		}
		folder := book.FirstPath(record.Agent.Folder)
		if folder == "" {
			return fmt.Errorf("stale record for %s: working directory is missing; refusing to create an empty session", name)
		}
		openArgs := []string{name, folder, "--resume", "--no-prompt"}
		if record.Agent.Launch.ResumeID == "" && !record.Agent.Launch.Hermes {
			thread := record.Agent.IdentityThreadID
			if thread == "" && record.Agent.NativeTitle != nil {
				thread = record.Agent.NativeTitle.ThreadID
			}
			if thread == "" {
				return fmt.Errorf("stale record for %s: conversation ID is missing; refusing to open a different conversation", name)
			}
			openArgs = append(openArgs, "--thread", thread)
		}
		fmt.Fprintf(a.err, "bp attach %q: reviving %s in %s\n", args[0], name, folder)
		if err := a.open(openArgs); err != nil {
			return err
		}
	}
	a.applyAttachBar(name)
	fmt.Fprintf(a.err, "bp attach %q: attaching to exact tmux session %s\n", args[0], name)
	if err := a.markLooked(name, time.Now().UTC()); err != nil {
		return fmt.Errorf("record last looked: %w", err)
	}
	spec := attachCommand(a.tmux.Bin, name, os.Getenv("TMUX") != "")
	if a.replaceProcess != nil {
		return a.replaceProcess(spec)
	}
	return replaceWith(spec)
}

func (a *app) applyAttachBar(name string) {
	a.applyOpenBar(name)
	a.barApplyStyle(name, a.barAccent(name))
}
