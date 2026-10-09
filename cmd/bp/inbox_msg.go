package main

import (
	"fmt"
	"strings"

	"blueprint/internal/api"
	"blueprint/internal/book"
	"blueprint/internal/identity"
)

// inboxTarget reports whether bp msg should hand name to the API inbox
// instead of a terminal. It is true only when the name has no live session,
// is not an agentbook agent (a closed one keeps the pending spool), and is a
// registered inbox agent. Anything uncertain (an unreadable book or
// registry) keeps the terminal path, as before inboxes existed.
func (a *app) inboxTarget(name string) bool {
	if a.config.StateDir == "" || ((a.tmux != nil || a.sessionExists != nil) && a.hasSession(name)) {
		return false
	}
	var fleet book.Fleet
	var err error
	if a.loadFleet != nil {
		fleet, _, err = a.loadFleet()
	} else {
		fleet, err = book.LoadFleet(book.Paths(a.config.Agentbooks))
	}
	if err != nil {
		return false
	}
	if _, ok := fleet.Agents[name]; ok {
		return false
	}
	_, ok, err := api.InboxAgent(a.config.StateDir, name)
	return err == nil && ok
}

// inboxMessage stores a bp msg for an inbox agent through api.Core.Send, the
// path bp_send and the HTTP API use, so the same validation and audit apply.
// An inbox agent reads its messages with bp_inbox; nothing is pushed to it,
// so the receipt says accepted, never delivered.
func (a *app) inboxMessage(who identity.Identity, name, text string, force bool) error {
	if force {
		return fmt.Errorf("%s is an inbox agent: it is never busy, so %s does not apply; message not sent", name, forceBusyFlag)
	}
	caller, err := inboxCaller(who)
	if err != nil {
		return fmt.Errorf("message to inbox agent %s not stored: %w", name, err)
	}
	result, err := a.apiCore().Send(a.ctx, caller, api.SendRequest{To: name, Text: text})
	if err != nil {
		return fmt.Errorf("message to inbox agent %s not stored: %w", name, err)
	}
	fmt.Fprintf(a.out, "accepted into the inbox of %s (an inbox agent: it reads messages with bp_inbox; not delivered until it does)\n", name)
	fmt.Fprintf(a.out, "Check: bp qstat %s\n", result.ID)
	// The RESULT contract: verdict words never change; new facts are new keys.
	fmt.Fprintf(a.out, "RESULT=queued CHANNEL=%s ROUTE=inbox\n", result.ID)
	return nil
}

// inboxCaller maps bp msg's sender to an API caller. A certain identity is
// a verified caller and its messages carry the bare name. Anything else is
// one of the resolver's guesses ("<name>?" or "<source>?:<name>", nested for
// a book override); it carries cli:<name>, the API's mark for an unverified
// sender.
func inboxCaller(who identity.Identity) (api.Caller, error) {
	if who.Certain && identity.ValidName(who.Label) {
		return api.Caller{Name: who.Label, Transport: "cli", Verified: true}, nil
	}
	name := who.Label
	if i := strings.LastIndex(name, identity.InferMark+":"); i >= 0 {
		name = name[i+len(identity.InferMark)+1:]
	}
	name = strings.TrimSuffix(name, identity.InferMark)
	if !identity.ValidName(name) {
		return api.Caller{}, fmt.Errorf("sender %q is not an agent name the inbox can record", who.Label)
	}
	return api.Caller{Name: name, Transport: "cli"}, nil
}

// inboxStatus is bp qstat for an inbox message id (ib...).
func (a *app) inboxStatus(id string) error {
	result, err := a.apiCore().Status(id)
	if err != nil {
		return err
	}
	switch result.State {
	case api.StateDelivered:
		fmt.Fprintf(a.out, "delivered (read by %s with bp_inbox)\n", result.To)
	default:
		fmt.Fprintf(a.out, "%s: waiting in the inbox of %s until it reads it with bp_inbox\n", result.State, result.To)
	}
	return nil
}

// inboxAgents lists API inbox agents for bp status. An unreadable registry
// is reported on stderr and leaves the list out; it never fails bp status.
func (a *app) inboxAgents() []api.InboxSummary {
	if a.config.StateDir == "" {
		return nil
	}
	agents, err := api.InboxAgents(a.config.StateDir)
	if err != nil {
		if a.err != nil {
			fmt.Fprintf(a.err, "bp status: inbox agents unavailable: %v\n", err)
		}
		return nil
	}
	return agents
}

// renderInboxAgents prints the inbox agents under the terminal table. They
// are reachable with bp msg like any agent, but read with bp_inbox.
func (a *app) renderInboxAgents() {
	agents := a.inboxAgents()
	if len(agents) == 0 {
		return
	}
	fmt.Fprintf(a.out, "\n%-24s %-10s %s\n", "INBOX AGENT", "UNREAD", "DESCRIPTION")
	for _, agent := range agents {
		fmt.Fprintf(a.out, "%-24s %-10d %s\n", agent.Name, agent.Unread, agent.Description)
	}
}
