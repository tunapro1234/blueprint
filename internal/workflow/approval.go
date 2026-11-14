package workflow

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Unit event states and the run status used by approval gates.
const (
	// StateAwaitingApproval parks a validated unit until a human decides.
	// The agent is released; the unit is neither a success nor a failure.
	StateAwaitingApproval = "awaiting_approval"
	// StateRevise records a human request for one more round on the same
	// agent (bp workflow approve --edit). A worker claims it as StateRevising.
	StateRevise   = "revise"
	StateRevising = "revising"
	// StatusAwaitingApproval is the run status when every unit is finished
	// or parked and at least one is parked. No worker runs; the supervisor
	// never restarts it.
	StatusAwaitingApproval = "awaiting-approval"

	// eventApprovalNotice records an owner notification about parked units so
	// a restarted worker keeps the notification throttle.
	eventApprovalNotice = "approval_notice"
	// approvalNoticeEvery is the minimum gap between parked-unit notices.
	approvalNoticeEvery = 10 * time.Minute
)

// Decision actions.
const (
	DecisionApprove = "approve"
	DecisionReject  = "reject"
	DecisionRevise  = "revise"
)

// ParkRecord is everything needed to finish a parked unit later: the proposed
// result and the times.jsonl row it would have written (result = proposed,
// finished_at = when it parked).
type ParkRecord struct {
	Proposed string    `json:"proposed"`
	Output   string    `json:"output,omitempty"`
	Started  time.Time `json:"started"`
	Row      TimeRow   `json:"row"`
	// StepRecord is the parked step's own record in a workflow with steps;
	// Row then describes the whole unit.
	StepRecord *StepRecord `json:"step_record,omitempty"`
}

// DecisionRecord is a human decision on a parked unit.
type DecisionRecord struct {
	Action      string    `json:"action"`
	By          string    `json:"by"`
	Text        string    `json:"text,omitempty"`
	ParkedAt    time.Time `json:"parked_at"`
	WaitSeconds float64   `json:"wait_s"`
	// CancelledRevision marks an approve or reject that cancelled a
	// pending revision request.
	CancelledRevision bool `json:"cancelled_revision,omitempty"`
}

// RowApproval is the approval history of a finished unit in times.jsonl.
// WaitSeconds sums every wait for a decision, revisions included.
type RowApproval struct {
	Proposed    string     `json:"proposed,omitempty"`
	Decision    string     `json:"decision,omitempty"`
	By          string     `json:"by,omitempty"`
	Note        string     `json:"note,omitempty"`
	DecidedAt   *time.Time `json:"decided_at,omitempty"`
	WaitSeconds float64    `json:"wait_s,omitempty"`
	Revisions   int        `json:"revisions,omitempty"`
}

func parkedState(state string) bool   { return state == StateAwaitingApproval }
func revisionState(state string) bool { return state == StateRevise || state == StateRevising }

// Approval is a parked unit or a pending revision as bp workflow approvals
// lists it.
type Approval struct {
	Unit     string    `json:"unit"`
	Agent    string    `json:"agent"`
	State    string    `json:"state"`
	Proposed string    `json:"proposed"`
	Round    int       `json:"round"`
	Reason   string    `json:"reason,omitempty"`
	Output   string    `json:"output,omitempty"`
	ParkedAt time.Time `json:"parked_at"`
	// Step is the step the unit parked at (workflows with steps).
	Step string `json:"step,omitempty"`
	// RevisionBy and RevisionText describe a pending revision.
	RevisionBy   string `json:"revision_by,omitempty"`
	RevisionText string `json:"revision_text,omitempty"`
}

// withDecisionLock serializes decisions, revision claims and the worker's
// final status across processes. Lock order: decisions.lock before run.lock
// (UpdateRun); never the other way around.
func (s *Store) withDecisionLock(id string, fn func() error) error {
	if !validRunID(id) {
		return fmt.Errorf("invalid workflow run id %q", id)
	}
	lock, err := os.OpenFile(filepath.Join(s.RunPath(id), "decisions.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
}

// Approvals lists the run's parked units and pending revisions, oldest first.
func (s *Store) Approvals(id string) ([]Approval, error) {
	if _, err := s.LoadRun(id); err != nil {
		return nil, err
	}
	events, _, err := s.Events(id)
	if err != nil {
		return nil, err
	}
	return approvalsFrom(events), nil
}

func approvalsFrom(events []Event) []Approval {
	var result []Approval
	for key, event := range latestEvents(events) {
		if event.Park == nil || (!parkedState(event.State) && !revisionState(event.State)) {
			continue
		}
		item := Approval{Unit: key, Agent: event.Agent, State: event.State, Proposed: event.Park.Proposed, Round: event.Round, Reason: event.Reason, Output: event.Park.Output, ParkedAt: event.At, Step: event.Step}
		if event.Decision != nil {
			item.ParkedAt = event.Decision.ParkedAt
			item.RevisionBy, item.RevisionText = event.Decision.By, event.Decision.Text
			item.Reason = event.Park.Row.Reason
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].ParkedAt.Equal(result[j].ParkedAt) {
			return result[i].ParkedAt.Before(result[j].ParkedAt)
		}
		return result[i].Unit < result[j].Unit
	})
	return result
}

// AwaitingApproval counts parked units of a run, or of every run of a
// workflow when selector is a workflow name.
func (s *Store) AwaitingApproval(selector string) (int, error) {
	ids := []string{}
	if run, err := s.LoadRun(selector); err == nil {
		ids = append(ids, run.ID)
	} else {
		runs, err := s.Runs()
		if err != nil {
			return 0, err
		}
		for _, run := range runs {
			if run.Workflow == selector {
				ids = append(ids, run.ID)
			}
		}
	}
	count := 0
	for _, id := range ids {
		events, _, err := s.Events(id)
		if err != nil {
			return 0, err
		}
		for _, event := range latestEvents(events) {
			if parkedState(event.State) {
				count++
			}
		}
	}
	return count, nil
}

// parkedRows returns the proposed times rows of units that are parked or
// waiting for a revision: their tokens are spent although no row is written.
func parkedRows(events []Event) []TimeRow {
	var rows []TimeRow
	for _, event := range latestEvents(events) {
		if event.Park != nil && (parkedState(event.State) || revisionState(event.State)) {
			rows = append(rows, event.Park.Row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].FinishedAt.Before(rows[j].FinishedAt) })
	return rows
}

// RunWork is what is left of a run: units without any result, parked units
// and pending revisions.
type RunWork struct {
	Pending   int
	Parked    int
	Revisions int
}

func workLeft(keys []string, latest map[string]Event, retryFailed bool) RunWork {
	var work RunWork
	for _, key := range keys {
		event, ok := latest[key]
		switch {
		case ok && parkedState(event.State):
			work.Parked++
		case ok && revisionState(event.State):
			work.Revisions++
		case ok && terminalState(event.State):
			if event.State == "failed" && retryFailed {
				work.Pending++
			}
		default:
			work.Pending++
		}
	}
	return work
}

// ResumeWork reports what a resume would run, reading the units file the way
// the worker does so new unit keys count as pending.
func (s *Store) ResumeWork(id string, retryFailed bool) (RunWork, error) {
	run, err := s.LoadRun(id)
	if err != nil {
		return RunWork{}, err
	}
	d, err := ReadDefinition(s.SnapshotDir(id))
	if err != nil {
		return RunWork{}, err
	}
	units, err := LoadUnits(d, run.Workdir)
	if err != nil {
		return RunWork{}, err
	}
	if run.Limit > 0 && len(units) > run.Limit {
		units = units[:run.Limit]
	}
	keys := make([]string, len(units))
	for i, unit := range units {
		keys[i] = unit.Key
	}
	events, _, err := s.Events(id)
	if err != nil {
		return RunWork{}, err
	}
	return workLeft(keys, latestEvents(events), retryFailed), nil
}

// DecideOptions is one bp workflow approve or reject call.
type DecideOptions struct {
	Action string
	Units  []string
	All    bool
	// Text is the approval note, the rejection reason or the revision text.
	Text string
	By   string
}

// DecideOutcome is the decision on one unit.
type DecideOutcome struct {
	Unit     string `json:"unit"`
	Agent    string `json:"agent"`
	Proposed string `json:"proposed"`
	Result   string `json:"result"`
	// Wait is the unit's whole time awaiting a decision, summed over its
	// parks and revision requests.
	Wait float64 `json:"wait_s"`
	// CancelledRevision is set when the decision cancelled a pending
	// revision request.
	CancelledRevision bool `json:"cancelled_revision,omitempty"`
	// Step is the step the unit was parked at (workflows with steps).
	Step string `json:"step,omitempty"`
	// NextStep is set when an approval accepted a failed step before the
	// last one as weak: the unit continues with this step.
	NextStep string `json:"next_step,omitempty"`
}

// DecideReport is the result of Decide.
type DecideReport struct {
	Outcomes []DecideOutcome
	Run      Run
	Left     RunWork
	// Finished is true when this decision ended an awaiting-approval run.
	Finished bool
}

// Decide applies a human decision to parked units. It holds the decision lock
// for the whole read-check-write, so two concurrent decisions on one unit
// write exactly one terminal event; the second sees the unit's new state and
// fails. Every named unit is checked before anything is written.
func (s *Store) Decide(id string, options DecideOptions) (DecideReport, error) {
	var report DecideReport
	switch options.Action {
	case DecisionApprove, DecisionReject, DecisionRevise:
	default:
		return report, fmt.Errorf("unknown decision %q", options.Action)
	}
	if options.All == (len(options.Units) > 0) {
		return report, errors.New("name the units to decide or use --all, not both")
	}
	if options.Action == DecisionRevise && (options.All || len(options.Units) != 1) {
		return report, errors.New("--edit applies to exactly one unit")
	}
	if options.Action == DecisionRevise && strings.TrimSpace(options.Text) == "" {
		return report, errors.New("--edit needs the revision text")
	}
	if options.Action == DecisionRevise {
		// The text reaches the agent as .Problems, capped like validator output.
		options.Text = truncateUTF8(options.Text, 8*1024)
	}
	err := s.withDecisionLock(id, func() error {
		run, err := s.LoadRun(id)
		if err != nil {
			return err
		}
		var definition *Definition
		if options.Action == DecisionRevise {
			d, err := ReadDefinition(s.SnapshotDir(id))
			if err != nil {
				return fmt.Errorf("read workflow snapshot: %w", err)
			}
			if !d.MultiStep() && d.Prompt.Followup == "" {
				return errors.New("this workflow has no prompt.followup; --edit sends its text through the follow-up template")
			}
			definition = &d
		}
		events, _, err := s.Events(id)
		if err != nil {
			return err
		}
		latest := latestEvents(events)
		var targets []Event
		if options.All {
			for _, item := range approvalsFrom(events) {
				if parkedState(item.State) {
					targets = append(targets, latest[item.Unit])
				}
			}
			if len(targets) == 0 {
				pending := 0
				for _, event := range latest {
					if event.State == StateRevise {
						pending++
					}
				}
				if pending > 0 && options.Action != DecisionRevise {
					return fmt.Errorf("no unit of run %s awaits approval; --all does not cover the %d units with a pending revision: name them to cancel the revision", id, pending)
				}
				return fmt.Errorf("no unit of run %s awaits approval", id)
			}
		} else {
			seen := map[string]bool{}
			for _, key := range options.Units {
				if seen[key] {
					continue
				}
				seen[key] = true
				event, ok := latest[key]
				switch {
				case !ok:
					return fmt.Errorf("unit %q has no state in run %s", key, id)
				case parkedState(event.State) && event.Park != nil:
					targets = append(targets, event)
				case event.State == StateRevise && event.Park != nil && options.Action != DecisionRevise:
					// Approve or reject cancels a revision no worker has
					// claimed yet and decides the earlier proposal.
					targets = append(targets, event)
				case event.State == StateRevise:
					return fmt.Errorf("unit %q already has a revision pending on %s; approve or reject it to cancel that revision", key, event.Agent)
				case event.State == StateRevising:
					return fmt.Errorf("unit %q cannot be decided now: its revision round is running on %s; decide after the round, when the unit is parked again or finished", key, event.Agent)
				default:
					return fmt.Errorf("unit %q is not awaiting approval (state %s)", key, event.State)
				}
			}
		}
		for _, parked := range targets {
			if parked.Step == "" {
				continue
			}
			if definition == nil {
				d, err := ReadDefinition(s.SnapshotDir(id))
				if err != nil {
					return fmt.Errorf("read workflow snapshot: %w", err)
				}
				definition = &d
			}
			if options.Action == DecisionRevise {
				// The revision goes through the parked step's follow-up.
				index := definition.StepIndex(parked.Step)
				if index < 0 {
					return fmt.Errorf("unit %q was parked at unknown step %q", parked.Key, parked.Step)
				}
				if definition.Steps[index].Prompt.Followup == "" {
					return fmt.Errorf("step %s has no prompt.followup; --edit sends its text through that step's follow-up template", parked.Step)
				}
			}
		}
		now := s.now()
		for _, parked := range targets {
			outcome, err := s.writeDecision(run, definition, parked, options, now)
			if err != nil {
				return err
			}
			report.Outcomes = append(report.Outcomes, outcome)
		}
		events, _, err = s.Events(id)
		if err != nil {
			return err
		}
		report.Left = workLeft(run.UnitKeys, latestEvents(events), false)
		report.Run = run
		if run.Status == StatusAwaitingApproval && report.Left == (RunWork{}) {
			updated, err := s.UpdateRun(id, func(current *Run) error {
				if current.Status == StatusAwaitingApproval {
					current.Status, current.LastError = "done", ""
				}
				return nil
			})
			if err != nil {
				return err
			}
			report.Run, report.Finished = updated, updated.Status == "done"
		}
		return nil
	})
	return report, err
}

func (s *Store) writeDecision(run Run, d *Definition, parked Event, options DecideOptions, now time.Time) (DecideOutcome, error) {
	park := *parked.Park
	// parked is an awaiting_approval event or, when a decision cancels a
	// pending revision, its revise event. Either way the wait since that
	// event adds to the waits recorded in the park row, so the row's
	// approval.wait_s covers the original park and the cancelled request.
	wait := max(0, now.Sub(parked.At).Seconds())
	cancelled := parked.State == StateRevise
	decision := &DecisionRecord{Action: options.Action, By: options.By, Text: options.Text, ParkedAt: parked.At, WaitSeconds: wait, CancelledRevision: cancelled}
	row := park.Row
	approval := RowApproval{Proposed: park.Proposed}
	if row.Approval != nil {
		approval = *row.Approval
		approval.Proposed = park.Proposed
	}
	approval.WaitSeconds += wait
	outcome := DecideOutcome{Unit: parked.Key, Agent: parked.Agent, Proposed: park.Proposed, Wait: approval.WaitSeconds, CancelledRevision: cancelled, Step: parked.Step}
	// In a workflow with steps the parked step's own record carries the
	// step's approval history, which the unit row sums over its steps.
	var stepApproval RowApproval
	if park.StepRecord != nil {
		record := *park.StepRecord
		stepApproval = RowApproval{Proposed: park.Proposed}
		if record.Row.Approval != nil {
			stepApproval = *record.Row.Approval
			stepApproval.Proposed = park.Proposed
		}
		stepApproval.WaitSeconds += wait
		park.StepRecord = &record
	}
	if options.Action == DecisionRevise {
		approval.Revisions++
		row.Approval = &approval
		park.Row = row
		if park.StepRecord != nil {
			stepApproval.Revisions++
			park.StepRecord.Row.Approval = &stepApproval
		}
		event := Event{At: now, Unit: parked.Key, Key: parked.Key, Agent: parked.Agent, State: StateRevise, Round: parked.Round, Reason: "revision requested by " + options.By, Park: &park, Decision: decision,
			DeliveryID: parked.DeliveryID, DeliveryStatus: parked.DeliveryStatus, QueuedAt: parked.QueuedAt, DeliveredAt: parked.DeliveredAt, Step: parked.Step, Attempt: parked.Attempt}
		outcome.Result = StateRevise
		return outcome, s.AppendEvent(run.ID, event)
	}
	if index := stepIndexOf(d, parked.Step); options.Action == DecisionApprove && park.StepRecord != nil && index >= 0 && index < len(d.Steps)-1 {
		// A failed step before the last one, approved: its output is
		// accepted as weak and the unit continues with the next step.
		record := park.StepRecord
		stepApproval.Decision, stepApproval.By, stepApproval.Note, stepApproval.DecidedAt = "approved", options.By, options.Text, timePointer(now)
		record.Row.Approval = &stepApproval
		if record.Row.Result == "failed" {
			record.Row.Result = "weak"
		}
		next := d.Steps[index+1].Name
		event := Event{At: now, Unit: parked.Key, Key: parked.Key, Agent: parked.Agent, State: StateStepDone, Round: parked.Round, Reason: fmt.Sprintf("approved by %s as weak; continues with step %s", options.By, next),
			DeliveryID: parked.DeliveryID, DeliveryStatus: parked.DeliveryStatus, QueuedAt: parked.QueuedAt, DeliveredAt: parked.DeliveredAt, Decision: decision, Step: parked.Step, Attempt: parked.Attempt, StepRecord: record}
		outcome.Result, outcome.NextStep = record.Row.Result, next
		return outcome, s.AppendEvent(run.ID, event)
	}
	result, reason := "weak", row.Reason
	approval.Decision = "approved"
	if options.Action == DecisionReject {
		result, approval.Decision = "failed", "rejected"
		reason = "rejected"
		if text := strings.TrimSpace(options.Text); text != "" {
			reason += ": " + text
		}
	} else {
		if park.Proposed == "done" {
			result = "done"
		}
		approval.Note = options.Text
	}
	approval.By, approval.DecidedAt = options.By, timePointer(now)
	row.Result, row.Reason, row.Approval = result, reason, &approval
	if count := len(row.Steps); count > 0 {
		// The decided step's entry follows the decision: a rejected step
		// failed, an approved failed step is weak.
		row.Steps = append([]StepRow(nil), row.Steps...)
		switch {
		case result == "failed":
			row.Steps[count-1].Result = "failed"
		case row.Steps[count-1].Result == "failed":
			row.Steps[count-1].Result = "weak"
		}
	}
	event := Event{At: now, Unit: parked.Key, Key: parked.Key, Agent: parked.Agent, State: result, Round: parked.Round, Reason: reason,
		DeliveryID: parked.DeliveryID, DeliveryStatus: parked.DeliveryStatus, QueuedAt: parked.QueuedAt, DeliveredAt: parked.DeliveredAt, Decision: decision, Step: parked.Step, Attempt: parked.Attempt}
	// The terminal event is the source of truth; it is written before the
	// times row, as the engine does.
	if err := s.AppendEvent(run.ID, event); err != nil {
		return outcome, err
	}
	if err := s.AppendTime(run.ID, row); err != nil {
		return outcome, err
	}
	outcome.Result = result
	return outcome, nil
}

func stepIndexOf(d *Definition, step string) int {
	if d == nil || step == "" {
		return -1
	}
	return d.StepIndex(step)
}

// claimRevision marks a pending revision as taken by agent's worker. It
// returns the claim and true when the unit's latest state is a revision for
// that agent (revising is re-claimed after a worker restart).
func (s *Store) claimRevision(id, key, agent string) (Event, bool, error) {
	var claim Event
	found := false
	err := s.withDecisionLock(id, func() error {
		events, _, err := s.Events(id)
		if err != nil {
			return err
		}
		event, ok := latestEvents(events)[key]
		if !ok || event.Agent != agent || event.Park == nil || !revisionState(event.State) {
			return nil
		}
		claim, found = event, true
		if event.State == StateRevising {
			return nil
		}
		claim.At, claim.State = s.now(), StateRevising
		claim.Reason = "revision claimed by the worker"
		return s.AppendEvent(id, claim)
	})
	return claim, found, err
}

// pendingRevisions returns the revisions waiting for agent among the given
// unit keys, oldest first.
func pendingRevisions(events []Event, agent string, units map[string]Unit) []Event {
	var result []Event
	for key, event := range latestEvents(events) {
		if _, ok := units[key]; ok && event.Agent == agent && event.Park != nil && revisionState(event.State) {
			result = append(result, event)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].At.Before(result[j].At) })
	return result
}
