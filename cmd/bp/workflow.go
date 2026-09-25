package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/identity"
	"blueprint/internal/msgq"
	bptmux "blueprint/internal/tmux"
	"blueprint/internal/workflow"
)

type workflowDriver struct {
	app   *app
	fleet book.Fleet
	owner string
}

func (a *app) workflowStore() *workflow.Store { return workflow.NewStore(a.config.StateDir) }

func (a *app) loadWorkflowFleet() (book.Fleet, error) {
	if a.loadFleet != nil {
		fleet, _, err := a.loadFleet()
		return fleet, err
	}
	return book.LoadFleet(book.Paths(a.config.Agentbooks))
}

func (a *app) newWorkflowDriver(owner string) (*workflowDriver, error) {
	fleet, err := a.loadWorkflowFleet()
	if err != nil {
		return nil, err
	}
	if a.tmux == nil {
		a.tmux = bptmux.New()
	}
	if a.queue == nil {
		a.queue = msgq.New(a.config.MsgqRoot)
	}
	return &workflowDriver{app: a, fleet: fleet, owner: owner}, nil
}

func (d *workflowDriver) Observe(ctx context.Context, agent string) (workflow.Observation, error) {
	start := time.Now()
	defer func() {
		// Retain an optional measurement hook for focused diagnostics and tests.
		if d.app.workflowObserveMeasured != nil {
			d.app.workflowObserveMeasured(time.Since(start))
		}
	}()
	state := book.RuntimeFor(ctx, d.app.tmux, d.fleet, agent)
	activity := state.Activity
	if activity == nil {
		return workflow.Observation{State: workflow.Unknown, Detail: "no runtime observation", ObservedAt: time.Now().UTC()}, nil
	}
	stateName := workflow.AgentState(activity.State)
	if stateName == workflow.Idle && activity.DeliveryBlocked {
		stateName = workflow.Blocked
	}
	observation := workflow.Observation{
		State: stateName, ContextKnown: state.Known || state.CtxTokens > 0,
		ContextTokens: state.CtxTokens, Model: state.Model, ObservedAt: activity.ObservedAt,
		Detail: activity.Reason,
	}
	if state.TurnKnown {
		observation.TurnAt = state.TurnAt
	} else if activity.LastEventAt != nil {
		observation.TurnAt = *activity.LastEventAt
	}
	return observation, nil
}

func (d *workflowDriver) Send(ctx context.Context, agent, text string) (workflow.Delivery, error) {
	queuedAt := time.Now().UTC()
	queued, channel, err := d.app.deliver(agent, d.owner, text)
	delivery := workflow.Delivery{ChannelID: channel, QueuedAt: queuedAt}
	if errors.Is(err, bptmux.ErrUnverified) {
		delivery.Status = workflow.DeliveryFailed
		delivery.Reason = "not delivered: queue could not verify delivery"
		return delivery, nil
	}
	if err != nil && !queued {
		delivery.Status, delivery.Reason = workflow.DeliveryFailed, "not delivered: "+err.Error()
		return delivery, nil
	}
	if queued {
		delivery.Status = workflow.DeliveryQueued
		if err != nil {
			delivery.Reason = err.Error()
		}
		return delivery, nil
	}
	delivery.Status, delivery.DeliveredAt = workflow.DeliveryDelivered, time.Now().UTC()
	return delivery, nil
}

func (d *workflowDriver) WaitDelivery(ctx context.Context, agent string, delivery workflow.Delivery, timeout time.Duration) (workflow.Delivery, error) {
	if d.app.queue == nil || delivery.ChannelID == "" {
		delivery.Status, delivery.Reason = workflow.DeliveryFailed, "not delivered: no queue record is available"
		return delivery, nil
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		record, err := d.app.queue.Record(delivery.ChannelID)
		if err == nil {
			status := strings.ToLower(record.Status)
			switch {
			case strings.HasPrefix(status, "delivered") && status != "delivered (unverified)":
				delivery.Status = workflow.DeliveryDelivered
				delivery.DeliveredAt = time.Now().UTC()
				return delivery, nil
			case strings.HasPrefix(status, "failed"), strings.HasPrefix(status, "canceled"), strings.HasPrefix(status, "cancelled"), strings.HasPrefix(status, "expired"):
				delivery.Status, delivery.Reason = workflow.DeliveryFailed, "not delivered: "+record.Status
				return delivery, nil
			}
		}
		if timeout > 0 && !time.Now().Before(deadline) {
			delivery.Status, delivery.Reason = workflow.DeliveryQueued, "not delivered before start timeout"
			return delivery, nil
		}
		select {
		case <-ctx.Done():
			return delivery, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (d *workflowDriver) Compact(ctx context.Context, agent string) error {
	if d.app.paneBusy(agent) {
		return workflow.ErrCompactBusy
	}
	release, err := d.app.lockPane(agent)
	if err != nil {
		return workflow.ErrCompactBusy
	}
	defer release()
	if d.app.paneBusy(agent) {
		return workflow.ErrCompactBusy
	}
	if err := d.app.clearComposer(agent); err != nil {
		if errors.Is(err, bptmux.ErrBusy) || errors.Is(err, bptmux.ErrTyping) {
			return workflow.ErrCompactBusy
		}
		return err
	}
	// This operation already owns the pane lock. Sending through app.deliver
	// would ask the queue to acquire that non-reentrant lock again and turn a
	// safe compact into an endlessly retried queued message. Client.Send keeps
	// the same pane, composer, and at-most-once verification gates under our lock.
	if err := d.app.tmux.Send(ctx, agent, "/compact"); err != nil {
		if errors.Is(err, bptmux.ErrBusy) || errors.Is(err, bptmux.ErrTyping) || errors.Is(err, bptmux.ErrPaneLocked) {
			return workflow.ErrCompactBusy
		}
		if errors.Is(err, bptmux.ErrUnverified) {
			return fmt.Errorf("compact was not verified: %w", err)
		}
		return err
	}
	return nil
}

func (a *app) workflow(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: bp workflow add|list|show|check|start|status|stop|resume|times")
	}
	store := a.workflowStore()
	switch args[0] {
	case "add":
		replace := false
		var source string
		for _, arg := range args[1:] {
			if arg == "--replace" {
				replace = true
			} else if strings.HasPrefix(arg, "-") {
				return fmt.Errorf("unknown workflow add option %s", arg)
			} else if source == "" {
				source = arg
			} else {
				return errors.New("usage: bp workflow add <dir> [--replace]")
			}
		}
		if source == "" {
			return errors.New("usage: bp workflow add <dir> [--replace]")
		}
		definition, hash, err := store.Add(source, replace)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "added workflow %s (%s)\n", definition.Name, hash)
		return nil
	case "list":
		if len(args) != 1 {
			return errors.New("usage: bp workflow list")
		}
		definitions, err := store.List()
		if err != nil {
			return err
		}
		for _, definition := range definitions {
			fmt.Fprintf(a.out, "%s\tv%d\t%s\n", definition.Name, definition.Version, definition.Description)
		}
		return nil
	case "show":
		if len(args) != 2 {
			return errors.New("usage: bp workflow show <name>")
		}
		_, definition, err := store.ResolveWorkflow(args[1])
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "name: %s\nversion: %d\ndescription: %s\nunits: %s (key %s)\nprompt: %s\n", definition.Name, definition.Version, definition.Description, definition.Units.File, definition.Units.Key, definition.Prompt.Template)
		if definition.Output.Path != "" {
			fmt.Fprintf(a.out, "output: %s (%s, fresh=%t)\n", definition.Output.Path, outputFormat(definition), definition.Output.RequireFresh)
		}
		if len(definition.Validate.Command) > 0 {
			fmt.Fprintf(a.out, "validator: %s\n", strings.Join(definition.Validate.Command, " "))
		}
		return nil
	case "check":
		return a.workflowCheck(store, args[1:])
	case "start":
		return a.workflowStart(store, args[1:])
	case "status":
		return a.workflowStatus(store, args[1:])
	case "stop":
		if len(args) != 2 {
			return errors.New("usage: bp workflow stop <run-id>")
		}
		run, err := store.RequestStop(args[1])
		if err != nil {
			return err
		}
		if run.Status == "done" || run.Status == "stopped" || run.Status == "error" {
			fmt.Fprintf(a.out, "run %s is already %s\n", run.ID, run.Status)
		} else {
			fmt.Fprintf(a.out, "run %s will stop at the next unit boundary\n", run.ID)
		}
		return nil
	case "resume":
		return a.workflowResume(store, args[1:])
	case "times":
		return a.workflowTimes(store, args[1:])
	default:
		return fmt.Errorf("unknown workflow command %q", args[0])
	}
}

func outputFormat(definition workflow.Definition) string {
	if definition.Output.Format == "" {
		return "any"
	}
	return definition.Output.Format
}

func (a *app) workflowCheck(store *workflow.Store, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: bp workflow check <dir|name> [--workdir <dir>]")
	}
	selector, workdir := "", ""
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch arg {
		case "--workdir":
			if index+1 >= len(args) {
				return errors.New("workflow check --workdir needs a directory")
			}
			index++
			workdir = args[index]
		default:
			if strings.HasPrefix(arg, "-") || selector != "" {
				return fmt.Errorf("unexpected workflow check argument %s", arg)
			}
			selector = arg
		}
	}
	path, definition, err := store.ResolveWorkflow(selector)
	if err != nil {
		return err
	}
	if workdir == "" {
		workdir = path
	}
	if err := checkSampleIfPresent(path, workdir, definition); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "OK: workflow %s\n", definition.Name)
	return nil
}

func checkSampleIfPresent(workflowDir, workdir string, definition workflow.Definition) error {
	path := filepath.Join(workdir, filepath.FromSlash(definition.Units.File))
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return workflow.CheckSampleTemplates(workflowDir, workdir, definition)
}

type workflowStartOptions struct {
	name    string
	workdir string
	agents  []string
	limit   int
	dryRun  bool
}

func parseWorkflowStart(args []string) (workflowStartOptions, error) {
	var options workflowStartOptions
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch arg {
		case "--workdir", "--agent", "--limit":
			if index+1 >= len(args) {
				return options, fmt.Errorf("%s needs a value", arg)
			}
			index++
			value := args[index]
			switch arg {
			case "--workdir":
				options.workdir = value
			case "--agent":
				options.agents = append(options.agents, value)
			case "--limit":
				limit, err := strconv.Atoi(value)
				if err != nil || limit <= 0 {
					return options, fmt.Errorf("--limit must be a positive integer: %q", value)
				}
				options.limit = limit
			}
		case "--dry-run":
			options.dryRun = true
		default:
			if strings.HasPrefix(arg, "-") || options.name != "" {
				return options, fmt.Errorf("unexpected workflow start argument %s", arg)
			}
			options.name = arg
		}
	}
	if options.name == "" || options.workdir == "" || len(options.agents) == 0 {
		return options, errors.New("usage: bp workflow start <name> --workdir <dir> --agent <name> [--agent <name> ...] [--limit N] [--dry-run]")
	}
	seen := map[string]bool{}
	for _, agent := range options.agents {
		if !identity.ValidName(agent) || seen[agent] {
			return options, fmt.Errorf("invalid or duplicate agent %q", agent)
		}
		seen[agent] = true
	}
	return options, nil
}

func (a *app) workflowStart(store *workflow.Store, args []string) error {
	options, err := parseWorkflowStart(args)
	if err != nil {
		return err
	}
	path, definition, err := store.ResolveWorkflow(options.name)
	if err != nil {
		return err
	}
	workdir, err := filepath.Abs(options.workdir)
	if err != nil {
		return err
	}
	units, err := workflow.LoadUnits(definition, workdir)
	if err != nil {
		return err
	}
	if len(units) == 0 {
		return errors.New("units file contains no units")
	}
	if err := workflow.CheckSampleTemplates(path, workdir, definition); err != nil {
		return err
	}
	if options.limit > 0 && len(units) > options.limit {
		units = units[:options.limit]
	}
	fleet, err := a.loadWorkflowFleet()
	if err != nil {
		return err
	}
	who := a.senderIdentity()
	if !who.Authoritative() {
		return fmt.Errorf("workflow start requires verified authority; sender identity is %s", who.Source)
	}
	if _, ok := fleet.Agents[who.Label]; !ok && who.Label != fleet.Root {
		return fmt.Errorf("workflow owner %s is not in the configured agentbook hierarchy", who.Label)
	}
	for _, agent := range options.agents {
		if _, ok := fleet.Agents[agent]; !ok {
			return fmt.Errorf("workflow agent %s is not registered", agent)
		}
		if who.Label != fleet.Root && !fleet.IsDescendant(agent, who.Label) {
			return fmt.Errorf("workflow agent %s is outside owner %s's hierarchy", agent, who.Label)
		}
	}
	if options.dryRun {
		fmt.Fprintf(a.out, "would run %s: units=%d agents=%s\n", definition.Name, len(units), strings.Join(options.agents, ","))
		return nil
	}
	checkCtx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
	defer cancel()
	if err := workflow.CheckDaemon(checkCtx, a.config.StateDir); err != nil {
		return fmt.Errorf("workflow start requires the bp daemon: %w", err)
	}
	run, err := store.CreateRun(path, definition, workdir, options.agents, who.Label)
	if err != nil {
		return err
	}
	if options.limit > 0 && options.limit < len(run.UnitKeys) {
		run.Limit = options.limit
		run.UnitKeys = run.UnitKeys[:options.limit]
		if err := store.SaveRun(run); err != nil {
			return err
		}
	}
	if err := workflow.Start(a.ctx, a.config.StateDir, run.ID); err != nil {
		run.Status, run.LastError = "error", err.Error()
		_ = store.SaveRun(run)
		return err
	}
	fmt.Fprintf(a.out, "started workflow %s as run %s\n", run.Workflow, run.ID)
	return nil
}

func (a *app) workflowStatus(store *workflow.Store, args []string) error {
	id, jsonOutput := "", false
	for _, arg := range args {
		if arg == "--json" {
			jsonOutput = true
		} else if strings.HasPrefix(arg, "-") || id != "" {
			return errors.New("usage: bp workflow status [<run-id>] [--json]")
		} else {
			id = arg
		}
	}
	if id != "" {
		status, err := store.Status(id)
		if err != nil {
			return err
		}
		if jsonOutput {
			return json.NewEncoder(a.out).Encode(status)
		}
		printWorkflowStatus(a.out, status)
		return nil
	}
	runs, err := store.Runs()
	if err != nil {
		return err
	}
	statuses := make([]workflow.RunStatus, 0, len(runs))
	for _, run := range runs {
		status, err := store.Status(run.ID)
		if err != nil {
			return err
		}
		statuses = append(statuses, status)
	}
	if jsonOutput {
		return json.NewEncoder(a.out).Encode(statuses)
	}
	for _, status := range statuses {
		printWorkflowStatus(a.out, status)
	}
	return nil
}

func printWorkflowStatus(out *os.File, status workflow.RunStatus) {
	agents := make([]string, 0, len(status.Agents))
	for _, agent := range status.Agents {
		current := agent.CurrentUnit
		if current == "" {
			current = "-"
		}
		entry := agent.Agent + ":" + current
		if agent.Elapsed > 0 {
			entry += " (" + agent.Elapsed.Round(time.Second).String() + ")"
		}
		agents = append(agents, entry)
	}
	line := fmt.Sprintf("%s %s owner=%s status=%s done=%d weak=%d failed=%d pending=%d agents=[%s]", status.ID, status.Workflow, status.Owner, status.Status, status.Counts.Done, status.Counts.Weak, status.Counts.Failed, status.Counts.Pending, strings.Join(agents, ", "))
	if status.LastError != "" {
		line += " last_error=" + status.LastError
	}
	if status.TornLog {
		line += " torn_log=true"
	}
	fmt.Fprintln(out, line)
}

func (a *app) workflowResume(store *workflow.Store, args []string) error {
	id, retryFailed := "", false
	for _, arg := range args {
		switch arg {
		case "--retry-failed":
			retryFailed = true
		default:
			if strings.HasPrefix(arg, "-") || id != "" {
				return errors.New("usage: bp workflow resume <run-id> [--retry-failed]")
			}
			id = arg
		}
	}
	if id == "" {
		return errors.New("usage: bp workflow resume <run-id> [--retry-failed]")
	}
	run, err := store.LoadRun(id)
	if err != nil {
		return err
	}
	if run.Status == "done" {
		return errors.New("completed workflow runs cannot be resumed")
	}
	run.Status, run.LastError, run.StopRequested, run.RetryFailed = "running", "", false, retryFailed
	if err := store.SaveRun(run); err != nil {
		return err
	}
	if err := workflow.Start(a.ctx, a.config.StateDir, id); err != nil {
		run.Status, run.LastError = "error", err.Error()
		_ = store.SaveRun(run)
		return err
	}
	fmt.Fprintf(a.out, "resumed workflow run %s\n", id)
	return nil
}

func (a *app) workflowTimes(store *workflow.Store, args []string) error {
	selector, jsonOutput := "", false
	for _, arg := range args {
		if arg == "--json" {
			jsonOutput = true
		} else if strings.HasPrefix(arg, "-") || selector != "" {
			return errors.New("usage: bp workflow times <run-id|name> [--json]")
		} else {
			selector = arg
		}
	}
	if selector == "" {
		return errors.New("usage: bp workflow times <run-id|name> [--json]")
	}
	rows, err := store.TimeRows(selector)
	if err != nil {
		return err
	}
	stats := workflow.SummarizeTimes(rows)
	if jsonOutput {
		return json.NewEncoder(a.out).Encode(stats)
	}
	fmt.Fprintf(a.out, "%s\n", stats.String())
	for name, group := range stats.ByAgent {
		fmt.Fprintf(a.out, "agent %s: count=%d done=%d weak=%d failed=%d median_work=%.1fs p90_work=%.1fs\n", name, group.Count, group.Done, group.Weak, group.Failed, group.MedianWork, group.P90Work)
	}
	for model, group := range stats.ByModel {
		fmt.Fprintf(a.out, "model %s: count=%d done=%d weak=%d failed=%d median_work=%.1fs p90_work=%.1fs\n", model, group.Count, group.Done, group.Weak, group.Failed, group.MedianWork, group.P90Work)
	}
	for round, count := range stats.Rounds {
		fmt.Fprintf(a.out, "rounds %d: %d\n", round, count)
	}
	for _, slow := range stats.Slowest {
		fmt.Fprintf(a.out, "slowest: %s agent=%s model=%s result=%s work=%.1fs\n", slow.Unit, slow.Agent, slow.Model, slow.Result, slow.WorkSec)
	}
	return nil
}

func (a *app) workflowWait(args []string) error {
	agent, untilValue, jsonOutput := "", "", false
	confirm := 0
	var timeout time.Duration
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch arg {
		case "--until", "--confirm", "--timeout":
			if index+1 >= len(args) {
				return fmt.Errorf("%s needs a value", arg)
			}
			index++
			value := args[index]
			switch arg {
			case "--until":
				untilValue = value
			case "--confirm":
				parsed, err := strconv.Atoi(value)
				if err != nil || parsed < 1 {
					return fmt.Errorf("--confirm must be a positive integer: %q", value)
				}
				confirm = parsed
			case "--timeout":
				parsed, err := time.ParseDuration(value)
				if err != nil || parsed <= 0 {
					return fmt.Errorf("--timeout must be a positive duration: %q", value)
				}
				timeout = parsed
			}
		case "--json":
			jsonOutput = true
		default:
			if strings.HasPrefix(arg, "-") || agent != "" {
				return errors.New("usage: bp wait <agent> --until idle|working [--confirm N] [--timeout D] [--json]")
			}
			agent = arg
		}
	}
	if agent == "" || (untilValue != "idle" && untilValue != "working") {
		return errors.New("usage: bp wait <agent> --until idle|working [--confirm N] [--timeout D] [--json]")
	}
	driver, err := a.newWorkflowDriver("")
	if err != nil {
		return err
	}
	if _, ok := driver.fleet.Agents[agent]; !ok {
		return &commandExitError{code: 2, message: fmt.Sprintf("agent %s is closed or not registered", agent)}
	}
	if confirm == 0 {
		if untilValue == "idle" {
			confirm = 2
		} else {
			confirm = 1
		}
	}
	result, waitErr := workflow.Wait(a.ctx, driver, agent, workflow.AgentState(untilValue), confirm, timeout, workflow.PollInterval, time.Now, nil)
	if jsonOutput {
		_ = json.NewEncoder(a.out).Encode(result)
	} else if waitErr == nil {
		fmt.Fprintf(a.out, "%s reached %s (%d observations)\n", agent, untilValue, result.Confirmed)
	}
	if waitErr == nil {
		return nil
	}
	if errors.Is(waitErr, workflow.ErrWaitTimeout) {
		return &commandExitError{code: 1, message: waitErr.Error()}
	}
	if errors.Is(waitErr, workflow.ErrAgentUnavailable) {
		return &commandExitError{code: 2, message: waitErr.Error()}
	}
	return waitErr
}

func (a *app) workflowRun(args []string) error {
	if len(args) == 0 || len(args) > 2 {
		return errors.New("usage: bp _workflow-run <run-id> [--retry-failed]")
	}
	retryFailed := false
	for _, arg := range args[1:] {
		if arg != "--retry-failed" {
			return fmt.Errorf("unknown workflow runner option %s", arg)
		}
		retryFailed = true
	}
	store := a.workflowStore()
	run, err := store.LoadRun(args[0])
	if err != nil {
		return err
	}
	driver, err := a.newWorkflowDriver(run.Owner)
	if err != nil {
		run.Status, run.LastError = "error", err.Error()
		_ = store.SaveRun(run)
		return err
	}
	engine := workflow.NewEngine(store, driver)
	engine.Authority = a.workflowAuthority
	engine.Notify = func(ctx context.Context, owner, message string) error {
		_, _, err := a.deliver(owner, owner, message)
		return err
	}
	return engine.Run(a.ctx, run.ID, retryFailed || run.RetryFailed)
}

func (a *app) workflowAuthority(owner, agent string) error {
	fleet, err := a.loadWorkflowFleet()
	if err != nil {
		return err
	}
	if _, ok := fleet.Agents[agent]; !ok {
		return fmt.Errorf("agent %s is no longer registered", agent)
	}
	if _, ok := fleet.Agents[owner]; !ok && owner != fleet.Root {
		return fmt.Errorf("workflow owner %s is no longer registered", owner)
	}
	if owner != fleet.Root && !fleet.IsDescendant(agent, owner) {
		return fmt.Errorf("agent %s is no longer below workflow owner %s", agent, owner)
	}
	return nil
}
