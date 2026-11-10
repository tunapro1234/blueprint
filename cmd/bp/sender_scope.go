package main

import (
	"fmt"
	"os"

	"blueprint/internal/book"
	"blueprint/internal/identity"
)

// ownerEnv names the agent a sender without a verified agent identity (a
// systemd unit, a script, a --from declaration) works for. It only narrows
// where bp msg may deliver; it never changes the label or grants authority.
// It is deliberately not BP_AGENT, which names the session for bp mcp.
const ownerEnv = "BP_OWNER"

// scopedSources are the identity sources BP_OWNER applies to: none of them is
// a verified agent. Panes, threads, daemon children and CLI subagents
// (pane-process-context) are never narrowed; login names and other guesses
// keep today's behavior.
var scopedSources = map[string]bool{
	"systemd-unit": true,
	"process-tree": true,
	"--from":       true,
	"none":         true,
}

// ownerScope returns the BP_OWNER owner limiting who's targets, "" when no
// scope applies, or an error when BP_OWNER is set but does not name a known
// agent: a declared owner that cannot be checked refuses rather than lets the
// message go anywhere.
func (a *app) ownerScope(who identity.Identity) (string, error) {
	owner, _, err := a.ownerScopeFleet(who)
	return owner, err
}

func (a *app) ownerScopeFleet(who identity.Identity) (string, book.Fleet, error) {
	owner := os.Getenv(ownerEnv)
	if owner == "" || !scopedSources[who.Source] {
		return "", book.Fleet{}, nil
	}
	if !identity.ValidName(owner) {
		return "", book.Fleet{}, fmt.Errorf("%s=%q is not an agent name; message not sent or queued", ownerEnv, owner)
	}
	fleet, err := a.ownerFleet()
	if err != nil {
		return "", book.Fleet{}, fmt.Errorf("%s=%s cannot be checked against the agentbook (%v); message not sent or queued", ownerEnv, owner, err)
	}
	if _, ok := fleet.Agents[owner]; !ok {
		return "", book.Fleet{}, fmt.Errorf("%s=%s is not an agent in the agentbook; message not sent or queued", ownerEnv, owner)
	}
	return owner, fleet, nil
}

// ownerFleet reads the installation's configured books. AGENTBOOK is not
// honored here: a sender must not narrow-check itself against a tree it chose.
func (a *app) ownerFleet() (book.Fleet, error) {
	if a.loadFleet != nil {
		fleet, _, err := a.loadFleet()
		return fleet, err
	}
	return book.LoadFleet(a.config.Agentbooks)
}

// checkOwnerScope refuses a target outside the BP_OWNER owner's subtree.
func (a *app) checkOwnerScope(who identity.Identity, target string) error {
	owner, fleet, err := a.ownerScopeFleet(who)
	if err != nil || owner == "" {
		return err
	}
	if target == owner || fleet.IsDescendant(target, owner) {
		return nil
	}
	return fmt.Errorf("refused: %s is declared for %s (%s) and may only message %s or agents below it, not %s", who.Label, owner, ownerEnv, owner, target)
}

// noteIgnoredFrom says out loud when bp msg --from lost to a verified
// identity: inside a pane, a thread or the daemon the caller signs as itself.
func (a *app) noteIgnoredFrom(who identity.Identity) {
	if a.msgFrom == "" || who.Declared != "" || a.err == nil {
		return
	}
	fmt.Fprintf(a.err, "note: --from %q ignored: this command already runs as %s (%s)\n", a.msgFrom, who.Label, who.Source)
}
