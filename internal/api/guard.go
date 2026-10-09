package api

import (
	"fmt"
	"strings"

	"blueprint/internal/audit"
	"blueprint/internal/guard"
	"blueprint/internal/identity"
)

// The guard hooks for remote (gateway) callers. Local callers are the same
// trust domain as bp msg and pass through untouched.

// peerOf is the peer a remote caller is watched and rate-limited as.
func peerOf(caller Caller) string {
	if caller.PeerID != "" {
		return caller.PeerID
	}
	return caller.Transport + ":" + caller.Name
}

// authorOf is the local agent name behind a stored sender label: a bare
// name, or the claimed name of an unverified local caller (http:alice).
// External senders have no local author.
func authorOf(label string) string {
	if strings.HasPrefix(label, "external:") {
		return ""
	}
	if i := strings.IndexByte(label, ':'); i >= 0 {
		label = label[i+1:]
	}
	if !identity.ValidName(label) {
		return ""
	}
	return label
}

// denied reports a policy denial to the Watch, which turns repeated probes
// for hidden agents or rooms into alerts. Listing calls never report: only a
// request that names its target does.
func (c *Core) denied(caller Caller, target, code string) {
	if c.Watch != nil && caller.Remote {
		c.Watch.Observe(guard.Event{Kind: guard.EvDenied, Peer: peerOf(caller), Target: target, Detail: code})
	}
}

// looked reports a remote lookup of one agent name.
func (c *Core) looked(caller Caller, name string) {
	if c.Watch != nil && caller.Remote {
		c.Watch.Observe(guard.Event{Kind: guard.EvLookup, Peer: peerOf(caller), Target: name})
	}
}

// admitRemote applies the guard policy and rate to a remote send or room
// post. target is the agent (CapSend) or room (CapRooms). The error looks
// like not-found when the target is hidden.
func (c *Core) admitRemote(caller Caller, capability guard.Capability, target string, size int) error {
	if !caller.Remote || caller.Policy == nil {
		return nil
	}
	policy := caller.Policy.guard()
	decision := policy.CheckSend(target, size)
	if capability == guard.CapRooms {
		decision = policy.CheckRoom(capability, target, size)
	}
	if decision.Allow {
		decision = c.limits.Allow(peerOf(caller), policy)
	}
	if decision.Allow {
		return nil
	}
	c.denied(caller, target, decision.Code)
	c.auditRejected("guard:"+peerOf(caller), audit.Event{Kind: "api.guard.denied", Severity: audit.Warn, Actor: caller.Label(), Target: target,
		PeerID: peerOf(caller), Reason: decision.Reason, Fields: map[string]string{"transport": caller.Transport, "code": decision.Code}})
	switch decision.Code {
	case "not-exposed", "room-not-granted", "no-capability":
		if capability == guard.CapRooms {
			return fmt.Errorf("%w: room %s", ErrNotFound, target)
		}
		return fmt.Errorf("%w: agent %s", ErrNotFound, target)
	case "too-large":
		return invalid("%s", decision.Reason)
	}
	return fmt.Errorf("%w: rate limit: %s", ErrForbidden, decision.Reason)
}

// scanIntake flags remote text as it arrives. Flags never block; they are
// added to the accepted event's fields, and high flags also write a
// guard.finding alert, as P2P does.
func (c *Core) scanIntake(caller Caller, kind, target, id, text string) (map[string]string, guard.Severity) {
	if !caller.Remote {
		return nil, ""
	}
	findings := guard.Scan(text)
	if len(findings) == 0 {
		return nil, ""
	}
	worst := guard.MaxSeverity(findings)
	fields := map[string]string{"guard.flags": guard.Summary(findings), "guard.severity": string(worst)}
	if worst == guard.High {
		var high []guard.Finding
		for _, f := range findings {
			if f.Severity == guard.High {
				high = append(high, f)
			}
		}
		c.audit(audit.Event{Kind: "guard.finding", Severity: audit.Alert, Actor: caller.Label(), Target: target, ID: id,
			PeerID: peerOf(caller), Reason: "inbound " + kind + " raised high guard flags: " + guard.Summary(high),
			Fields: map[string]string{"transport": caller.Transport}})
	}
	return fields, worst
}

// inbound tells the Watch that remote text reached a local agent, which
// taints that agent for a while.
func (c *Core) inbound(caller Caller, agent, channel string, severity guard.Severity) {
	if c.Watch != nil && caller.Remote {
		c.Watch.Observe(guard.Event{Kind: guard.EvInbound, Peer: peerOf(caller), Agent: agent, Channel: channel, Severity: severity})
	}
}

// outbound redacts local text a remote caller is about to read and tells
// the Watch that author's text left the machine. author is the local agent
// that wrote it ("" when unknown). Text from remote authors is returned as
// it is: it never was this machine's.
func (c *Core) outbound(caller Caller, author string, untrusted bool, text string) string {
	if !caller.Remote || untrusted || text == "" {
		return text
	}
	var policy guard.RedactPolicy
	if caller.Policy != nil {
		policy = caller.Policy.Redact
	}
	redacted, findings := guard.Redact(text, policy)
	if c.Watch != nil && author != "" {
		detail := ""
		if len(findings) > 0 {
			detail = "redacted"
		}
		c.Watch.Observe(guard.Event{Kind: guard.EvOutbound, Peer: peerOf(caller), Agent: author, Detail: detail})
	}
	if len(findings) > 0 {
		c.audit(audit.Event{Kind: "api.guard.redacted", Severity: audit.Warn, Actor: author, Target: caller.Label(),
			PeerID: peerOf(caller), Reason: guard.Summary(findings), Fields: map[string]string{"transport": caller.Transport}})
	}
	return redacted
}
