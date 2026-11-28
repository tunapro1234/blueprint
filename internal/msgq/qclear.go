package msgq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"blueprint/internal/messagetext"
	bptmux "blueprint/internal/tmux"
)

// StatusLeftoverCleared closes a record whose own unsent paste an operator
// erased from the target's composer with bp qclear. It starts with
// "canceled", so every status reader treats it as not delivered.
const StatusLeftoverCleared = "canceled (leftover paste cleared by operator)"

// leftoverMaxAge bounds how long after a record closed qclear still acts on
// it. Older leftovers are a human's call.
const leftoverMaxAge = 24 * time.Hour

// leftoverClosedStatuses are the endings in which bp pasted the text and never
// saw it delivered, so a copy can still sit in the composer.
var leftoverClosedStatuses = map[string]bool{
	StatusUnconfirmed:                     true,
	StatusHangingComposer:                 true,
	"not delivered (verification failed)": true,
}

// LeftoverCheck is one precondition of ClearLeftover and what was found.
type LeftoverCheck struct {
	Name   string
	OK     bool
	Detail string
}

// LeftoverOutcome is how ClearLeftover ended.
type LeftoverOutcome int

const (
	// LeftoverRefused: a check failed and no key was pressed.
	LeftoverRefused LeftoverOutcome = iota
	// LeftoverDryRun: every check passed; nothing was pressed (--dry-run).
	LeftoverDryRun
	// LeftoverDelivered: the transcript has the message; nothing was touched.
	LeftoverDelivered
	// LeftoverCleared: the composer was erased, read back empty, and the
	// record now says so.
	LeftoverCleared
	// LeftoverNotCleared: the erase key went in and the composer did not read
	// empty afterwards. Nothing more was pressed and the record is unchanged.
	LeftoverNotCleared
)

// LeftoverOptions tunes one ClearLeftover call.
type LeftoverOptions struct {
	DryRun bool
	// Actor names the operator in the record and the log.
	Actor string
	// Authorize decides whether the caller may clear this record. Nil refuses.
	Authorize func(Message) error
}

// LeftoverResult reports every check ClearLeftover made and how it ended.
type LeftoverResult struct {
	ID      string
	To      string
	Pending bool
	Chip    bool
	Checks  []LeftoverCheck
	Outcome LeftoverOutcome
	Detail  string
}

// Failed returns the first failed check.
func (r LeftoverResult) Failed() (LeftoverCheck, bool) {
	for _, check := range r.Checks {
		if !check.OK {
			return check, true
		}
	}
	return LeftoverCheck{}, false
}

// leftoverEraser is what the target needs beyond Target for qclear: the agent
// pane check and the chip erase (tmux.Client provides both).
type leftoverEraser interface {
	RequireAgentPane(context.Context, string) error
	ClearStuckChip(context.Context, string, string) (bool, error)
}

// ClearLeftover erases bp's OWN unsent paste of one message from its target's
// composer, for an operator who no longer wants it delivered. It acts only
// when every check holds: the caller is authorized; bp pasted this record and
// never saw it delivered (pending and never to be pasted again, or closed with
// an unverified ending within leftoverMaxAge); the target is still the session
// bp pasted into; the transcript witness does not have the message; no turn is
// open; the pane is an idle agent pane without a dialog; and its complete
// composer holds exactly this message, verbatim or as the one Claude chip
// ClaudeChipFor accepts for it. The erase is C-u (never Escape), followed by a
// read that must show an empty box. On success a pending record is closed and
// a closed one rewritten as StatusLeftoverCleared.
//
// Every check is evaluated and reported, so a dry run shows them all. A
// message the witness finds ends at once as LeftoverDelivered.
func (q *Queue) ClearLeftover(ctx context.Context, target Target, id string, opts LeftoverOptions) (LeftoverResult, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	result := LeftoverResult{ID: id}
	if !opts.DryRun {
		release, err := q.lockDispatchForOperator(cancelLockWait)
		if err != nil {
			return result, fmt.Errorf("%w; nothing cleared, check bp qstat and retry", err)
		}
		defer release()
	}
	check := func(name string, ok bool, detail string) {
		result.Checks = append(result.Checks, LeftoverCheck{Name: name, OK: ok, Detail: detail})
	}
	refuse := func() (LeftoverResult, error) {
		failed, _ := result.Failed()
		result.Outcome, result.Detail = LeftoverRefused, "refused: "+failed.Name+": "+failed.Detail
		return result, nil
	}

	message, path, pending, err := q.leftoverRecord(id)
	if err != nil {
		check("record", false, err.Error())
		return refuse()
	}
	result.To, result.Pending = message.To, pending
	where := "closed as " + message.Status
	if pending {
		where = "pending"
	}
	check("record", true, fmt.Sprintf("%s, to %s, from %s", where, message.To, message.From))

	if opts.Authorize == nil {
		check("authority", false, "no authority check is available")
	} else if err := opts.Authorize(message); err != nil {
		check("authority", false, err.Error())
	} else {
		check("authority", true, "caller may clear this record")
	}

	pasted, pastedDetail := q.leftoverPasted(message, pending)
	check("pasted by bp", pasted, pastedDetail)

	rec := record{path: path, Message: message}
	if reason := q.recoveryBlock(rec); reason != "" {
		check("same session", false, reason)
	} else {
		check("same session", true, "binding "+message.AttemptBinding)
	}

	wire, renderErr := q.leftoverWire(message)
	if renderErr != nil {
		check("message text", false, renderErr.Error())
		return refuse()
	}
	switch {
	case q.Witness == nil || (q.CanWitness != nil && !q.CanWitness(wire)):
		check("transcript witness", false, "no transcript witness can look for this message; bp cannot show it was not delivered")
	case q.Witness(message.To, wire, time.Unix(0, int64(message.TS*1e9))):
		check("transcript witness", true, "the message is in the transcript")
		result.Outcome, result.Detail = LeftoverDelivered, "already delivered, nothing to clear"
		return result, nil
	default:
		check("transcript witness", true, "not in the transcript")
	}

	if q.TurnOpen == nil {
		check("no open turn", false, "the turn probe is unavailable")
	} else if q.TurnOpen(message.To) {
		check("no open turn", false, "a turn is open in the target")
	} else {
		check("no open turn", true, "no turn is open")
	}

	if !opts.DryRun {
		release, err := q.lockPane(message.To)
		if err != nil {
			check("pane lock", false, err.Error())
			return refuse()
		}
		defer release()
	}

	eraser, canErase := target.(leftoverEraser)
	switch {
	case !canErase:
		check("agent pane", false, "this target cannot check or erase a pane")
	default:
		if err := eraser.RequireAgentPane(ctx, message.To); err != nil {
			check("agent pane", false, err.Error())
		} else {
			check("agent pane", true, "agent CLI")
		}
	}
	pane, captureErr := target.CaptureAnsi(ctx, message.To)
	if captureErr != nil {
		check("screen", false, captureErr.Error())
	} else {
		dialog, busy, complete := bptmux.Dialog(pane), bptmux.Busy(pane), bptmux.ComposerComplete(pane)
		check("no dialog", !dialog, pick(dialog, "a dialog is open", "no dialog"))
		check("idle", !busy, pick(busy, "the pane is busy", "the pane is idle"))
		check("composer complete", complete, pick(complete, "the whole composer box is visible", "the composer box is unreadable or scrolled"))
		switch {
		case bptmux.ExactPaste(pane, []string{wire}):
			check("composer holds this message", true, "verbatim, nothing else")
		case bptmux.ClaudeChipFor(pane, wire):
			result.Chip = true
			check("composer holds this message", true, "one paste chip with this message's line count, nothing else")
		case bptmux.ComposerEmpty(pane):
			check("composer holds this message", false, "the composer is empty")
		default:
			check("composer holds this message", false, "the composer holds something other than exactly this message")
		}
	}

	if _, failed := result.Failed(); failed {
		return refuse()
	}
	key := "C-u on the composer (verbatim text)"
	if result.Chip {
		key = "C-u once on the paste chip"
	}
	if opts.DryRun {
		result.Outcome, result.Detail = LeftoverDryRun, "dry run: all checks pass; would press "+key
		return result, nil
	}

	var cleared bool
	var eraseErr error
	if result.Chip {
		cleared, eraseErr = eraser.ClearStuckChip(ctx, message.To, wire)
	} else {
		cleared, eraseErr = target.ClearDelivered(ctx, message.To, []string{wire})
	}
	after, afterErr := target.CaptureAnsi(ctx, message.To)
	if eraseErr != nil || !cleared || afterErr != nil || !bptmux.ComposerEmpty(after) {
		detail := "the composer is NOT empty after the erase; nothing more was pressed"
		switch {
		case eraseErr != nil:
			detail += " (" + eraseErr.Error() + ")"
		case afterErr != nil:
			detail = "the composer could not be read after the erase; nothing more was pressed (" + afterErr.Error() + ")"
		case !cleared:
			detail = "the erase was refused at the last read, the composer changed; nothing was pressed"
		}
		result.Outcome, result.Detail = LeftoverNotCleared, detail
		return result, nil
	}

	previous := "pending"
	if !pending {
		previous = message.Status
	}
	message.Reason = fmt.Sprintf("bp's unsent paste was erased from the composer by %s (record was %s)", leftoverActor(opts.Actor), previous)
	if pending {
		err = q.finish(path, message, StatusLeftoverCleared)
	} else {
		err = q.rewriteDone(path, message, StatusLeftoverCleared)
	}
	if err != nil {
		result.Outcome, result.Detail = LeftoverCleared, "composer cleared, but the record could not be updated: "+err.Error()
		return result, err
	}
	result.Outcome, result.Detail = LeftoverCleared, "composer cleared; record closed as "+StatusLeftoverCleared
	return result, nil
}

func pick(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}

func leftoverActor(actor string) string {
	if actor == "" {
		return "an operator"
	}
	return actor
}

// leftoverRecord finds id among pending and closed records.
func (q *Queue) leftoverRecord(id string) (Message, string, bool, error) {
	if id == "" || filepath.Base(id) != id || id == "." || id == ".." {
		return Message{}, "", false, fmt.Errorf("invalid channel ID")
	}
	donePath := filepath.Join(q.done(), id+".json")
	if m, err := read(donePath); err == nil {
		return m, donePath, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Message{}, "", false, err
	}
	pendingPath := filepath.Join(q.pending(), id+".json")
	m, err := read(pendingPath)
	if errors.Is(err, os.ErrNotExist) {
		return Message{}, "", false, fmt.Errorf("no record %s (pending or closed)", id)
	}
	if err != nil {
		return Message{}, "", false, err
	}
	return m, pendingPath, true, nil
}

// leftoverPasted is the "bp pasted this and never saw it delivered" check.
func (q *Queue) leftoverPasted(m Message, pending bool) (bool, string) {
	if pending {
		switch {
		case m.Cleanup:
			return false, "the record is already confirmed delivered"
		case !m.NoRepaste:
			return false, "bp has not pasted this record unverified; use bp qcancel"
		}
		if m.UnverifiedAt > 0 {
			// Right after a paste or an Enter the screen may still show text the
			// agent has already taken; only the witness may judge it then.
			if left := screenSettleGrace - q.Now().Sub(time.Unix(0, int64(m.UnverifiedAt*1e9))); left > 0 {
				return false, fmt.Sprintf("pasted or submitted moments ago; the screen may still be catching up, retry in %s", left.Round(time.Second))
			}
		}
		return true, "pasted, unverified, never to be pasted again"
	}
	if m.Status == StatusLeftoverCleared {
		return false, "its leftover was already cleared"
	}
	if !leftoverClosedStatuses[m.Status] || !m.NoRepaste {
		return false, "closed as " + m.Status + ", not an unverified paste"
	}
	if age := q.Now().Sub(time.Unix(0, int64(m.Finished*1e9))); age > leftoverMaxAge {
		return false, fmt.Sprintf("closed %s ago, more than %s", age.Round(time.Minute), leftoverMaxAge)
	}
	return true, "pasted, closed unverified as " + m.Status
}

// leftoverWire is the text bp pasted for m: rendered the way dispatch renders
// it, so a framed external message is matched in its framed form.
func (q *Queue) leftoverWire(m Message) (string, error) {
	wire := m.Msg
	if q.Render != nil {
		rendered, err := q.Render(m)
		if err != nil {
			return "", fmt.Errorf("render delivery body: %w", err)
		}
		wire = rendered
	}
	if neutral, changed := messagetext.NeutralizeTrailingMention(wire); changed {
		wire = neutral
	}
	return wire, nil
}

// rewriteDone replaces a closed record with a new final status, atomically,
// and appends the new final state to the message log. The log reader that
// looks a message up (api Status) takes the LAST line for an id, Reindex
// skips ids it has seen, and the guard's taint reader ignores "canceled"
// lines, so the second line changes no earlier reading.
func (q *Queue) rewriteDone(path string, message Message, status string) error {
	message.Status = status
	message.clearDeliveredWait()
	message.Finished = float64(q.Now().UnixNano()) / 1e9
	tmp, err := os.CreateTemp(q.done(), ".done-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	err = json.NewEncoder(tmp).Encode(message)
	if syncErr := tmp.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	if err := syncQueueDir(q.done()); err != nil {
		return err
	}
	_ = q.appendMessageLog(message)
	return nil
}
