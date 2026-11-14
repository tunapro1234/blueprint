package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"blueprint/internal/workflow"
)

const (
	workflowApprovalsUsage = "usage: bp workflow approvals <run-id> [--json]"
	workflowApproveUsage   = "usage: bp workflow approve <run-id> (<unit>... | --all) [--note <text>] | approve <run-id> <unit> --edit <text>"
	workflowRejectUsage    = "usage: bp workflow reject <run-id> (<unit>... | --all) [--reason <text>]"
)

func (a *app) workflowApprovals(store *workflow.Store, args []string) error {
	id, jsonOutput := "", false
	for _, arg := range args {
		switch {
		case arg == "--json":
			jsonOutput = true
		case strings.HasPrefix(arg, "-") || id != "":
			return errors.New(workflowApprovalsUsage)
		default:
			id = arg
		}
	}
	if id == "" {
		return errors.New(workflowApprovalsUsage)
	}
	approvals, err := store.Approvals(id)
	if err != nil {
		return err
	}
	if jsonOutput {
		if approvals == nil {
			approvals = []workflow.Approval{}
		}
		return json.NewEncoder(a.out).Encode(approvals)
	}
	if len(approvals) == 0 {
		fmt.Fprintf(a.out, "no unit of run %s awaits approval\n", id)
		return nil
	}
	for _, item := range approvals {
		fmt.Fprintln(a.out, approvalLine(item))
	}
	return nil
}

func approvalLine(item workflow.Approval) string {
	parts := []string{item.Unit, "agent=" + item.Agent}
	if item.Step != "" {
		parts = append(parts, "step="+item.Step)
	}
	parts = append(parts, "proposed="+item.Proposed, fmt.Sprintf("round=%d", item.Round), "parked_at="+item.ParkedAt.UTC().Format(time.RFC3339))
	if item.Output != "" {
		parts = append(parts, "output="+item.Output)
	}
	switch item.State {
	case workflow.StateRevise:
		parts = append(parts, fmt.Sprintf("revision pending on %s (requested by %s; approve or reject cancels it)", item.Agent, item.RevisionBy))
	case workflow.StateRevising:
		parts = append(parts, fmt.Sprintf("revision running on %s (requested by %s)", item.Agent, item.RevisionBy))
	}
	if reason := strings.Join(strings.Fields(item.Reason), " "); reason != "" {
		if len(reason) > 200 {
			reason = truncateRunes(reason, 200) + "..."
		}
		parts = append(parts, "reason="+reason)
	}
	return strings.Join(parts, " ")
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

type workflowDecideOptions struct {
	id string
	workflow.DecideOptions
}

// parseWorkflowDecide parses approve and reject. "--" ends the options, for
// unit keys that start with a dash.
func parseWorkflowDecide(command string, args []string) (workflowDecideOptions, error) {
	usage := workflowApproveUsage
	if command == "reject" {
		usage = workflowRejectUsage
	}
	options := workflowDecideOptions{DecideOptions: workflow.DecideOptions{Action: workflow.DecisionApprove}}
	if command == "reject" {
		options.Action = workflow.DecisionReject
	}
	textSet, editSet := false, false
	positional := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if positional || !strings.HasPrefix(arg, "-") {
			if options.id == "" {
				options.id = arg
			} else {
				options.Units = append(options.Units, arg)
			}
			continue
		}
		switch {
		case arg == "--":
			positional = true
		case arg == "--all":
			options.All = true
		case (arg == "--note" || arg == "--edit") && command == "approve", arg == "--reason" && command == "reject":
			if index+1 >= len(args) {
				return options, fmt.Errorf("%s needs a value", arg)
			}
			if textSet {
				return options, fmt.Errorf("%s: only one text option (--note, --edit or --reason) may be given", arg)
			}
			index++
			options.Text, textSet = args[index], true
			if arg == "--edit" {
				editSet = true
				options.Action = workflow.DecisionRevise
			}
		default:
			return options, fmt.Errorf("unknown workflow %s option %s; %s", command, arg, usage)
		}
	}
	if options.id == "" {
		return options, errors.New(usage)
	}
	if options.All && len(options.Units) > 0 {
		return options, errors.New("--all cannot be combined with unit names")
	}
	if !options.All && len(options.Units) == 0 {
		return options, fmt.Errorf("name the units to %s or use --all; %s", command, usage)
	}
	if editSet {
		if options.All || len(options.Units) != 1 {
			return options, errors.New("--edit applies to exactly one unit")
		}
		if strings.TrimSpace(options.Text) == "" {
			return options, errors.New("--edit needs the revision text")
		}
	}
	return options, nil
}

// workflowDecider resolves who may decide a run's parked units: a verified
// caller who is the run's owner, the configured root, or an ancestor of the
// owner in the agentbook hierarchy (the same verified-identity and hierarchy
// rule bp workflow start applies to its agents).
func (a *app) workflowDecider(run workflow.Run) (string, error) {
	who := a.senderIdentity()
	if !who.Authoritative() {
		return "", fmt.Errorf("workflow decisions require verified authority; sender identity is %s", who.Source)
	}
	if who.Label == run.Owner {
		return who.Label, nil
	}
	fleet, err := a.loadWorkflowFleet()
	if err != nil {
		return "", err
	}
	if who.Label == fleet.Root || fleet.IsDescendant(run.Owner, who.Label) {
		return who.Label, nil
	}
	return "", fmt.Errorf("only the run owner %s or an agent above it may decide; %s is not", run.Owner, who.Label)
}

func (a *app) workflowDecide(store *workflow.Store, command string, args []string) error {
	options, err := parseWorkflowDecide(command, args)
	if err != nil {
		return err
	}
	run, err := store.LoadRun(options.id)
	if err != nil {
		return err
	}
	if options.By, err = a.workflowDecider(run); err != nil {
		return err
	}
	report, err := store.Decide(run.ID, options.DecideOptions)
	if err != nil {
		return err
	}
	for _, outcome := range report.Outcomes {
		wait := time.Duration(outcome.Wait * float64(time.Second)).Round(time.Second)
		if outcome.CancelledRevision {
			fmt.Fprintf(a.out, "cancelled the pending revision of %s on %s\n", outcome.Unit, outcome.Agent)
		}
		switch outcome.Result {
		case workflow.StateRevise:
			fmt.Fprintf(a.out, "revision of %s recorded for %s (waited %s)\n", outcome.Unit, outcome.Agent, wait)
		case "failed":
			fmt.Fprintf(a.out, "rejected %s (%s): proposed %s, result failed (waited %s)\n", outcome.Unit, outcome.Agent, outcome.Proposed, wait)
		default:
			if outcome.NextStep != "" {
				fmt.Fprintf(a.out, "approved %s (%s) at step %s as weak; continues with step %s (waited %s)\n", outcome.Unit, outcome.Agent, outcome.Step, outcome.NextStep, wait)
				continue
			}
			fmt.Fprintf(a.out, "approved %s (%s): proposed %s, result %s (waited %s)\n", outcome.Unit, outcome.Agent, outcome.Proposed, outcome.Result, wait)
		}
	}
	continued := false
	for _, outcome := range report.Outcomes {
		continued = continued || outcome.NextStep != ""
	}
	if continued && report.Run.Status != "running" && !strings.HasPrefix(report.Run.Status, "waiting:") {
		fmt.Fprintf(a.out, "run %s has no worker (status %s); continue it with: bp workflow resume %s\n", report.Run.ID, report.Run.Status, report.Run.ID)
	}
	if options.Action == workflow.DecisionRevise {
		a.printRevisionNext(store, report)
		return nil
	}
	switch {
	case report.Finished:
		fmt.Fprintf(a.out, "run %s is done\n", report.Run.ID)
	case report.Left.Parked > 0:
		fmt.Fprintf(a.out, "%d units of run %s still await approval\n", report.Left.Parked, report.Run.ID)
	}
	return nil
}

// printRevisionNext tells the decider how the revision will be delivered.
func (a *app) printRevisionNext(store *workflow.Store, report workflow.DecideReport) {
	run := report.Run
	agent := ""
	if len(report.Outcomes) > 0 {
		agent = report.Outcomes[0].Agent
	}
	if run.Status == "running" || strings.HasPrefix(run.Status, "waiting:") {
		fmt.Fprintf(a.out, "the running worker sends it to %s at that agent's next unit boundary, ahead of new units\n", agent)
	} else {
		fmt.Fprintf(a.out, "run %s has no worker (status %s); deliver the revision with: bp workflow resume %s\n", run.ID, run.Status, run.ID)
	}
	if status, err := store.Status(run.ID); err == nil {
		for _, breaker := range status.Breakers {
			if breaker.Agent == agent {
				fmt.Fprintf(a.out, "note: %s's breaker is %s; the revision waits for its trial unit (from %s)\n", agent, breaker.State, breaker.ReopensAt.UTC().Format(time.RFC3339))
			}
		}
	}
}

func printApprovalStats(out *os.File, stats *workflow.ApprovalStats) {
	if stats == nil {
		return
	}
	if stats.Approved+stats.Rejected+stats.Revised > 0 {
		fmt.Fprintf(out, "approvals: approved=%d rejected=%d revised=%d median_decision_wait=%s\n", stats.Approved, stats.Rejected, stats.Revised, time.Duration(stats.MedianWait*float64(time.Second)).Round(time.Second))
	}
	if stats.Waiting > 0 {
		fmt.Fprintf(out, "awaiting approval: %d units (not in times.jsonl until decided)\n", stats.Waiting)
	}
}
