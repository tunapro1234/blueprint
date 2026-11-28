package main

import (
	"errors"
	"fmt"

	"blueprint/internal/audit"
	"blueprint/internal/identity"
	"blueprint/internal/msgq"
)

const qclearUsage = "usage: bp qclear [--dry-run] <channel-id>"

// qclear erases bp's own unsent paste of one message from its target's
// composer (msgq.Queue.ClearLeftover), for a message nobody wants delivered
// any more. Every check is printed; --dry-run presses nothing.
func (a *app) qclear(args []string) error {
	dryRun := false
	var ids []string
	for _, arg := range args {
		switch {
		case arg == "--dry-run":
			dryRun = true
		case len(arg) > 0 && arg[0] == '-':
			return fmt.Errorf("unknown flag %s; %s", arg, qclearUsage)
		default:
			ids = append(ids, arg)
		}
	}
	if len(ids) != 1 {
		return errors.New(qclearUsage)
	}
	if a.queue == nil || a.tmux == nil {
		return errors.New("qclear needs the local queue and tmux")
	}
	a.prepareDispatch()
	who := a.senderIdentity()
	result, err := a.queue.ClearLeftover(a.ctx, a.tmux, ids[0], msgq.LeftoverOptions{
		DryRun:    dryRun,
		Actor:     who.Label,
		Authorize: a.qclearAuthority(who),
	})
	for _, check := range result.Checks {
		mark := "ok  "
		if !check.OK {
			mark = "FAIL"
		}
		fmt.Fprintf(a.out, "  %s %s: %s\n", mark, check.Name, check.Detail)
	}
	if err != nil && result.Detail == "" {
		return err
	}
	fmt.Fprintf(a.out, "%s: %s\n", ids[0], result.Detail)
	switch result.Outcome {
	case msgq.LeftoverCleared, msgq.LeftoverNotCleared:
		// A key went into somebody's pane: that is always written down.
		event := audit.Event{Kind: "msgq.qclear", Actor: who.Label, Target: result.To, ID: ids[0], Reason: result.Detail,
			Fields: map[string]string{"chip": fmt.Sprint(result.Chip), "pending": fmt.Sprint(result.Pending)}}
		if result.Outcome == msgq.LeftoverNotCleared {
			event.Severity = audit.Warn
		}
		if auditErr := audit.Append(a.config.StateDir, event); auditErr != nil {
			fmt.Fprintf(a.err, "bp: could not record the qclear in the audit log: %v\n", auditErr)
		}
	}
	switch {
	case err != nil:
		return err
	case result.Outcome == msgq.LeftoverRefused:
		return errReported
	case result.Outcome == msgq.LeftoverNotCleared:
		return errReported
	}
	return nil
}

// qclearAuthority lets the message's own sender clear it, when the caller's
// identity is verified, and otherwise only the identities force-busy accepts.
func (a *app) qclearAuthority(who identity.Identity) func(msgq.Message) error {
	return func(m msgq.Message) error {
		if who.Authoritative() && who.Label != "" && who.Label == m.From {
			return nil
		}
		if a.allowForceBusy(who) == nil {
			return nil
		}
		return fmt.Errorf("only the sender (%s, verified) or root/bp/wa/whatsapp may clear it (identity: %s, source: %s)", m.From, who.Label, who.Source)
	}
}
