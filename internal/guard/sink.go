package guard

import (
	"fmt"
	"io"

	"blueprint/internal/audit"
)

// AuditSink writes Watch alerts to <StateDir>/audit.jsonl. Events are not
// written by default because the transports already audit their own
// decisions; set Events to log every observation as "guard.<kind>".
type AuditSink struct {
	StateDir string
	Events   bool
	// Errors receives audit write failures; nil discards them.
	Errors io.Writer
}

func (s AuditSink) Event(ev Event) {
	if !s.Events {
		return
	}
	s.write(audit.Event{Time: ev.Time.UTC(), Kind: "guard." + string(ev.Kind), Severity: auditSeverity(ev.Severity, false),
		Actor: ev.Agent, Target: ev.Target, Peer: ev.Peer, ID: ev.Channel, Reason: ev.Detail})
}

func (s AuditSink) Alert(a Alert) {
	s.write(audit.Event{Time: a.Time.UTC(), Kind: "guard.alert." + a.Rule, Severity: auditSeverity(a.Severity, true),
		Actor: a.Agent, Peer: a.Peer, ID: a.Channel, Reason: a.Summary})
}

func (s AuditSink) write(ev audit.Event) {
	if err := audit.Append(s.StateDir, ev); err != nil && s.Errors != nil {
		fmt.Fprintf(s.Errors, "guard: audit: %v\n", err)
	}
}

// auditSeverity maps guard severities onto the audit scale. Only alerts with
// High severity become audit "alert", the level the owner is notified about.
func auditSeverity(s Severity, alert bool) string {
	switch {
	case s == High && alert:
		return audit.Alert
	case s == High || s == Warn:
		return audit.Warn
	}
	return audit.Info
}
