package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/identity"
	"blueprint/internal/msgq"
	bptmux "blueprint/internal/tmux"
	"blueprint/internal/tokens"
	"blueprint/internal/workflow"
)

// tokenStoreStale is how old the token store's last collection may be before
// the workflow meter stops vouching for it. The daemon collects every five
// minutes; three missed collections mean the collector is not running.
const tokenStoreStale = 15 * time.Minute

type workflowDriver struct {
	app          *app
	fleetMu      sync.RWMutex
	fleet        book.Fleet
	fleetByAgent map[string]book.Fleet
	owner        string
	// typing serializes Send and Compact across agent goroutines. The app's
	// pane locks are reentrant per process, and the queue dispatch a delivery
	// runs walks every line, so without this one goroutine's pass could type
	// into a pane another goroutine is clearing. Waits stay concurrent.
	typing sync.Mutex
	// runtimes is the harness each agent was last observed running; the token
	// meter vouches only for harnesses whose usage the token store collects.
	runtimes map[string]string
	// transcripts is the Claude Code transcript each agent was last observed
	// bound to; the activity meter reads only these.
	transcripts map[string]string
	activity    *activityReader
	// tokenStore overrides the token store directory (tests).
	tokenStore string
	now        func() time.Time
}

func (a *app) workflowStore() *workflow.Store {
	store := workflow.NewStore(a.config.StateDir)
	store.Measure = settleMeasure(a.tokenStoreDir())
	return store
}

func (a *app) tokenStoreDir() string {
	return tokens.DefaultConfig(a.config.StateDir, nil).StoreDir
}

// settleMeasure re-measures recorded units straight from the token store. No
// runtime check is possible or needed: a row with tokens was metered.
func settleMeasure(storeDir string) workflow.TokenMeasure {
	return func(agent string, since, until time.Time) (workflow.TokenUse, bool) {
		usage, asOf, err := tokens.AgentUsage(storeDir, agent, since, until)
		if err != nil || !usage.Seen {
			return workflow.TokenUse{}, false
		}
		return workflow.TokenUse{Input: usage.In, Output: usage.Out, CacheWrite: usage.CacheWrite, CacheRead: usage.CacheRead, AsOf: asOf}, true
	}
}

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
	driver := a.newWorkflowDriverBase(owner)
	driver.fleet = fleet
	return driver, nil
}

func (a *app) newWorkflowDriverBase(owner string) *workflowDriver {
	if a.tmux == nil {
		a.tmux = bptmux.New()
	}
	if a.queue == nil {
		a.queue = msgq.New(a.config.MsgqRoot)
	}
	return &workflowDriver{app: a, fleetByAgent: make(map[string]book.Fleet), owner: owner, runtimes: make(map[string]string)}
}

func (d *workflowDriver) setFleet(agent string, fleet book.Fleet) {
	d.fleetMu.Lock()
	defer d.fleetMu.Unlock()
	d.fleetByAgent[agent] = fleet
	d.fleet = fleet
}

func (d *workflowDriver) fleetFor(agent string) book.Fleet {
	d.fleetMu.RLock()
	defer d.fleetMu.RUnlock()
	if fleet, ok := d.fleetByAgent[agent]; ok {
		return fleet
	}
	return d.fleet
}

func (d *workflowDriver) Observe(ctx context.Context, agent string) (workflow.Observation, error) {
	start := time.Now()
	defer func() {
		// Retain an optional measurement hook for focused diagnostics and tests.
		if d.app.workflowObserveMeasured != nil {
			d.app.workflowObserveMeasured(time.Since(start))
		}
	}()
	state := book.RuntimeFor(ctx, d.app.tmux, d.fleetFor(agent), agent)
	activity := state.Activity
	if activity == nil {
		return workflow.Observation{State: workflow.Unknown, Detail: "no runtime observation", ObservedAt: time.Now().UTC()}, nil
	}
	d.fleetMu.Lock()
	if d.runtimes == nil {
		d.runtimes = make(map[string]string)
	}
	d.runtimes[agent] = state.Runtime
	d.fleetMu.Unlock()
	d.noteTranscript(agent, state.Runtime, activity.TranscriptPath, activity.ThreadID, len(activity.BindingConflicts))
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

// Tokens implements workflow.TokenMeter by reading bp's token store, which
// the daemon's tokens-collect job fills from Claude and Codex transcripts
// (subagents included). It only reads the store and never collects itself.
func (d *workflowDriver) Tokens(_ context.Context, agent string, since, until time.Time) (workflow.TokenUse, bool, error) {
	d.fleetMu.RLock()
	runtime := d.runtimes[agent]
	d.fleetMu.RUnlock()
	if !meteredRuntime(runtime) {
		return workflow.TokenUse{}, false, nil
	}
	now := time.Now
	if d.now != nil {
		now = d.now
	}
	current := now()
	if until.IsZero() || until.After(current) {
		until = current
	}
	store := d.tokenStore
	if store == "" {
		store = d.app.tokenStoreDir()
	}
	usage, asOf, err := tokens.AgentUsage(store, agent, since, until)
	if errors.Is(err, tokens.ErrNotCollected) {
		return workflow.TokenUse{}, false, nil
	}
	if err != nil {
		return workflow.TokenUse{}, false, err
	}
	if current.Sub(asOf) > tokenStoreStale {
		return workflow.TokenUse{}, false, nil
	}
	if !usage.Seen {
		// The store has no record at all under this name around the
		// interval: the collector attributes the agent's transcript to
		// another label (no customTitle, a shared folder, an agentbook the
		// collector does not read). Zero would be a wrong answer.
		return workflow.TokenUse{}, false, nil
	}
	return workflow.TokenUse{Input: usage.In, Output: usage.Out, CacheWrite: usage.CacheWrite, CacheRead: usage.CacheRead, AsOf: asOf}, true, nil
}

// meteredRuntime lists harnesses whose transcripts the token collector reads:
// Claude Code transcripts under ~/.claude/projects and Codex rollouts under
// ~/.codex/sessions. A codex-remote agent's thread lives in a shared
// app-server whose sessions directory and cwd attribution are not proven to
// reach the collector, so it stays unknown.
func meteredRuntime(runtime string) bool {
	return runtime == "claude" || runtime == "codex"
}

func (d *workflowDriver) Send(ctx context.Context, agent, text string) (workflow.Delivery, error) {
	queuedAt := time.Now().UTC()
	d.typing.Lock()
	queued, channel, err := d.app.deliver(agent, d.owner, text)
	d.typing.Unlock()
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
			switch {
			case msgq.IsVerifiedDelivery(record.Status):
				delivery.Status = workflow.DeliveryDelivered
				delivery.DeliveredAt = time.Now().UTC()
				return delivery, nil
			case msgq.IsUnverifiedDelivery(record.Status):
				delivery.Status, delivery.Reason = workflow.DeliveryFailed, "not delivered: "+record.Status
				return delivery, nil
			case msgq.IsTerminalDelivery(record.Status):
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

func (d *workflowDriver) Compact(_ context.Context, agent string) error {
	d.typing.Lock()
	queued, channel, err := d.app.compactDeliver(agent, d.owner)
	d.typing.Unlock()
	if errors.Is(err, bptmux.ErrBusy) || errors.Is(err, bptmux.ErrTyping) || errors.Is(err, bptmux.ErrPaneLocked) {
		return workflow.ErrCompactBusy
	}
	if errors.Is(err, bptmux.ErrUnverified) {
		return fmt.Errorf("compact was not verified: %w", err)
	}
	if queued {
		return fmt.Errorf("%w: %s", workflow.ErrCompactQueued, channel)
	}
	return err
}

func (a *app) workflow(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: bp workflow add|list|show|check|start|status|stop|resume|times|approvals|approve|reject")
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
		if definition.MultiStep() {
			fmt.Fprintf(a.out, "name: %s\nversion: %d\ndescription: %s\nunits: %s (key %s)\n", definition.Name, definition.Version, definition.Description, definition.Units.File, definition.Units.Key)
			printWorkflowSteps(a.out, definition)
		} else {
			fmt.Fprintf(a.out, "name: %s\nversion: %d\ndescription: %s\nunits: %s (key %s)\nprompt: %s\n", definition.Name, definition.Version, definition.Description, definition.Units.File, definition.Units.Key, definition.Prompt.Template)
			if definition.Output.Path != "" {
				fmt.Fprintf(a.out, "output: %s (%s, fresh=%t)\n", definition.Output.Path, outputFormat(definition), definition.Output.RequireFresh)
			}
			if len(definition.Validate.Command) > 0 {
				fmt.Fprintf(a.out, "validator: %s\n", strings.Join(definition.Validate.Command, " "))
			}
		}
		printWorkflowSupervision(a.out, definition)
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
		if run.Status == workflow.StatusAwaitingApproval {
			waiting, err := store.AwaitingApproval(run.ID)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "run %s is already waiting for decisions: %d units await approval (bp workflow approvals %s)\n", run.ID, waiting, run.ID)
		} else if workflow.AtRest(run.Status) {
			fmt.Fprintf(a.out, "run %s is already %s\n", run.ID, run.Status)
		} else {
			fmt.Fprintf(a.out, "run %s will stop at the next unit boundary\n", run.ID)
		}
		return nil
	case "resume":
		return a.workflowResume(store, args[1:])
	case "times":
		return a.workflowTimes(store, args[1:])
	case "approvals":
		return a.workflowApprovals(store, args[1:])
	case "approve", "reject":
		return a.workflowDecide(store, args[0], args[1:])
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
	if definition.Budget != nil {
		fmt.Fprintln(a.out, "warning: budget needs token usage data. Counts come from bp's token collector, which lags by up to one collection (5m), so both ceilings are soft and can be overshot by about that much spend. Only Claude Code and Codex agents in a tmux pane whose usage the collector records under the agent's name are metered; any other agent runs with no budget enforcement.")
	}
	if definition.Guardrails != nil {
		fmt.Fprintln(a.out, guardrailsCheckWarning)
	}
	if definition.MultiStep() {
		parts := make([]string, 0, len(definition.Steps))
		for _, step := range definition.Steps {
			parts = append(parts, fmt.Sprintf("%s (role %s)", step.Name, step.Role()))
		}
		fmt.Fprintf(a.out, "steps: %s\n", strings.Join(parts, " -> "))
	}
	fmt.Fprintf(a.out, "OK: workflow %s\n", definition.Name)
	return nil
}

// printWorkflowSteps prints a pipeline's steps for bp workflow show.
func printWorkflowSteps(out *os.File, definition workflow.Definition) {
	fmt.Fprintf(out, "steps: %d (roles: %s)\n", len(definition.Steps), strings.Join(definition.Roles(), ", "))
	for index, step := range definition.Steps {
		line := fmt.Sprintf("step %d %s: role=%s prompt=%s", index+1, step.Name, step.Role(), step.Prompt.Template)
		if step.Prompt.Followup != "" {
			line += " followup=" + step.Prompt.Followup
		}
		fmt.Fprintln(out, line)
		if step.Output.Path != "" {
			format := step.Output.Format
			if format == "" {
				format = "any"
			}
			fmt.Fprintf(out, "  output: %s (%s, fresh=%t)\n", step.Output.Path, format, step.Output.RequireFresh)
		}
		if len(step.Validate.Command) > 0 {
			fmt.Fprintf(out, "  validator: %s\n", strings.Join(step.Validate.Command, " "))
		}
	}
}

func printWorkflowSupervision(out *os.File, definition workflow.Definition) {
	if spec := definition.Supervise; spec != nil {
		if spec.Breaker != nil {
			fmt.Fprintf(out, "breaker: %d consecutive failed units per agent, open for %s\n", spec.Breaker.Failures, spec.Breaker.OpenFor)
		}
		if spec.Meltdown != nil {
			fmt.Fprintf(out, "meltdown: pause after %d failed units within %s\n", spec.Meltdown.Failures, spec.Meltdown.Window)
		}
	}
	if budget := definition.Budget; budget != nil {
		parts := []string{}
		if budget.UnitTokens != nil {
			parts = append(parts, fmt.Sprintf("unit=%d", *budget.UnitTokens))
		}
		if budget.RunTokens != nil {
			parts = append(parts, fmt.Sprintf("run=%d", *budget.RunTokens))
		}
		parts = append(parts, fmt.Sprintf("cache_read=%d%%", definition.CacheReadWeight()))
		fmt.Fprintf(out, "budget: %s\n", strings.Join(parts, " "))
	}
	if definition.Approve != nil {
		fmt.Fprintf(out, "approve: %s wait for a human decision\n", strings.Join(definition.Approve.On, ", "))
	}
	printWorkflowGuardrails(out, definition.Guardrails)
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
	// roles maps an agent given as --agent <role>=<name> to its role;
	// agents given as a plain name have the default role.
	roles  map[string]string
	limit  int
	dryRun bool
}

const workflowStartUsage = "usage: bp workflow start <name> --workdir <dir> --agent [<role>=]<name> [--agent [<role>=]<name> ...] [--limit N] [--dry-run]"

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
				agent := value
				if role, name, ok := strings.Cut(value, "="); ok {
					if role == "" || name == "" {
						return options, fmt.Errorf("--agent %q needs both a role and an agent name: --agent <role>=<name>", value)
					}
					if !workflow.ValidRole(role) {
						return options, fmt.Errorf("--agent %q: invalid role %q; use lowercase letters, digits, '-' and '_', starting with a letter or digit", value, role)
					}
					if options.roles == nil {
						options.roles = map[string]string{}
					}
					if _, seen := options.roles[name]; !seen {
						options.roles[name] = role
					}
					agent = name
				}
				options.agents = append(options.agents, agent)
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
		return options, errors.New(workflowStartUsage)
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
	if definition.MultiStep() || len(options.roles) > 0 {
		if err := workflow.ValidateRoles(definition, options.agents, options.roles); err != nil {
			return err
		}
	}
	if options.dryRun {
		line := fmt.Sprintf("would run %s: units=%d agents=%s", definition.Name, len(units), strings.Join(options.agents, ","))
		if definition.MultiStep() {
			line += " roles=" + formatWorkflowRoles(definition, options.agents, options.roles)
		}
		fmt.Fprintln(a.out, line)
		return nil
	}
	checkCtx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
	defer cancel()
	if err := workflow.CheckDaemon(checkCtx, a.config.StateDir); err != nil {
		return fmt.Errorf("workflow start requires the bp daemon: %w", err)
	}
	run, err := store.CreateRunWithRoles(path, definition, workdir, options.agents, options.roles, who.Label)
	if err != nil {
		return err
	}
	if options.limit > 0 && options.limit < len(run.UnitKeys) {
		keys := append([]string(nil), run.UnitKeys[:options.limit]...)
		run, err = store.UpdateRun(run.ID, func(current *workflow.Run) error {
			current.Limit, current.UnitKeys = options.limit, keys
			return nil
		})
		if err != nil {
			return err
		}
	}
	if err := workflow.Start(a.ctx, a.config.StateDir, run.ID); err != nil {
		_, _ = store.UpdateRun(run.ID, func(current *workflow.Run) error {
			current.Status, current.LastError = "error", err.Error()
			return nil
		})
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
		name := agent.Agent
		if agent.Role != "" {
			name += "[" + agent.Role + "]"
		}
		entry := name + ":" + current
		if agent.CurrentStep != "" && agent.CurrentUnit != "" {
			entry += "@" + agent.CurrentStep
		}
		if agent.Elapsed > 0 {
			entry += " (" + agent.Elapsed.Round(time.Second).String() + ")"
		}
		entry += guardrailMarker(agent.Guardrails)
		agents = append(agents, entry)
	}
	line := fmt.Sprintf("%s %s owner=%s status=%s done=%d weak=%d failed=%d pending=%d", status.ID, status.Workflow, status.Owner, status.Status, status.Counts.Done, status.Counts.Weak, status.Counts.Failed, status.Counts.Pending)
	if status.Counts.AwaitingApproval > 0 {
		line += fmt.Sprintf(" awaiting_approval=%d", status.Counts.AwaitingApproval)
	}
	if len(status.Counts.Ready) > 0 {
		steps := make([]string, 0, len(status.Counts.Ready))
		for step := range status.Counts.Ready {
			steps = append(steps, step)
		}
		sort.Strings(steps)
		ready := make([]string, 0, len(steps))
		for _, step := range steps {
			ready = append(ready, fmt.Sprintf("%s=%d", step, status.Counts.Ready[step]))
		}
		line += " ready=[" + strings.Join(ready, ", ") + "]"
	}
	line += " agents=[" + strings.Join(agents, ", ") + "]"
	if status.LastError != "" {
		line += " last_error=" + status.LastError
	}
	if status.PauseReason != "" {
		line += " pause_reason=" + status.PauseReason
	}
	if len(status.Breakers) > 0 {
		breakers := make([]string, 0, len(status.Breakers))
		for _, breaker := range status.Breakers {
			if breaker.State == "half-open" {
				breakers = append(breakers, fmt.Sprintf("%s half-open (trial unit allowed; %d consecutive failures)", breaker.Agent, breaker.Failures))
			} else {
				breakers = append(breakers, fmt.Sprintf("%s open until %s (%d consecutive failures)", breaker.Agent, breaker.ReopensAt.UTC().Format(time.RFC3339), breaker.Failures))
			}
		}
		line += " breakers=[" + strings.Join(breakers, ", ") + "]"
	}
	if status.RunTokens > 0 {
		line += fmt.Sprintf(" run_tokens=%d/%d", status.RunTokensSpent, status.RunTokens)
	}
	if status.TornLog {
		line += " torn_log=true"
	}
	fmt.Fprintln(out, line)
}

const workflowResumeUsage = "usage: bp workflow resume <run-id> [--retry-failed] [--run-tokens N]"

type workflowResumeOptions struct {
	id string
	workflow.ResumeOptions
}

func parseWorkflowResume(args []string) (workflowResumeOptions, error) {
	var options workflowResumeOptions
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch arg {
		case "--retry-failed":
			options.RetryFailed = true
		case "--run-tokens":
			if index+1 >= len(args) {
				return options, errors.New("--run-tokens needs a value")
			}
			index++
			value, err := strconv.ParseInt(args[index], 10, 64)
			if err != nil || value <= 0 {
				return options, fmt.Errorf("--run-tokens must be a positive integer: %q", args[index])
			}
			options.RunTokens = value
		default:
			if strings.HasPrefix(arg, "-") || options.id != "" {
				return options, errors.New(workflowResumeUsage)
			}
			options.id = arg
		}
	}
	if options.id == "" {
		return options, errors.New(workflowResumeUsage)
	}
	return options, nil
}

func (a *app) workflowResume(store *workflow.Store, args []string) error {
	options, err := parseWorkflowResume(args)
	if err != nil {
		return err
	}
	id := options.id
	run, err := store.LoadRun(id)
	if err != nil {
		return err
	}
	if run.Status == "done" {
		return errors.New("completed workflow runs cannot be resumed")
	}
	if run.Status == workflow.StatusAwaitingApproval {
		// Without a pending revision or a unit to run, a resume would start a
		// worker that ends at once; keep the run waiting instead.
		work, err := store.ResumeWork(id, options.RetryFailed)
		if err == nil && work.Pending == 0 && work.Revisions == 0 {
			fmt.Fprintf(a.out, "nothing to run: run %s waits for decisions on %d units (bp workflow approvals %s)\n", id, work.Parked, id)
			return nil
		}
	}
	if _, err := store.Resume(id, options.ResumeOptions); err != nil {
		return err
	}
	if spent, ceiling, err := store.RunSpend(id); err == nil && ceiling > 0 && spent >= ceiling {
		fmt.Fprintf(a.out, "note: %s; the run will pause again before its next unit. Raise the ceiling with --run-tokens N.\n", workflow.RunBudgetReason(spent, ceiling))
	}
	if err := workflow.Start(a.ctx, a.config.StateDir, id); err != nil {
		_, _ = store.UpdateRun(id, func(current *workflow.Run) error {
			current.Status, current.LastError = "error", err.Error()
			return nil
		})
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
	rows, unsettled, err := store.SettledTimeRows(selector)
	if err != nil {
		return err
	}
	stats := workflow.SummarizeTimes(rows)
	if stats.Tokens != nil {
		stats.Tokens.Unsettled = unsettled
	}
	waiting, err := store.AwaitingApproval(selector)
	if err != nil {
		return err
	}
	if waiting > 0 {
		if stats.Approvals == nil {
			stats.Approvals = &workflow.ApprovalStats{}
		}
		stats.Approvals.Waiting = waiting
	}
	if jsonOutput {
		return json.NewEncoder(a.out).Encode(stats)
	}
	fmt.Fprintf(a.out, "%s\n", stats.String())
	for _, name := range sortedGroupKeys(stats.ByAgent) {
		group := stats.ByAgent[name]
		fmt.Fprintf(a.out, "agent %s: count=%d done=%d weak=%d failed=%d median_work=%.1fs p90_work=%.1fs\n", name, group.Count, group.Done, group.Weak, group.Failed, group.MedianWork, group.P90Work)
	}
	for _, model := range sortedGroupKeys(stats.ByModel) {
		group := stats.ByModel[model]
		fmt.Fprintf(a.out, "model %s: count=%d done=%d weak=%d failed=%d median_work=%.1fs p90_work=%.1fs\n", model, group.Count, group.Done, group.Weak, group.Failed, group.MedianWork, group.P90Work)
	}
	rounds := make([]int, 0, len(stats.Rounds))
	for round := range stats.Rounds {
		rounds = append(rounds, round)
	}
	sort.Ints(rounds)
	for _, round := range rounds {
		count := stats.Rounds[round]
		fmt.Fprintf(a.out, "rounds %d: %d\n", round, count)
	}
	for _, slow := range stats.Slowest {
		fmt.Fprintf(a.out, "slowest: %s agent=%s model=%s result=%s work=%.1fs\n", slow.Unit, slow.Agent, slow.Model, slow.Result, slow.WorkSec)
	}
	if t := stats.Tokens; t != nil {
		fmt.Fprintf(a.out, "tokens: units=%d/%d total=%d weighted=%d in=%d out=%d cache_write=%d cache_read=%d\n", t.Units, stats.Count, t.Total, t.Weighted, t.Input, t.Output, t.CacheWrite, t.CacheRead)
		fmt.Fprintf(a.out, "tokens per unit: median=%.0f p90=%.0f median_weighted=%.0f p90_weighted=%.0f\n", t.MedianTotal, t.P90Total, t.MedianWeighted, t.P90Weighted)
		for _, name := range sortedTokenKeys(t.ByAgent) {
			group := t.ByAgent[name]
			fmt.Fprintf(a.out, "tokens agent %s: units=%d total=%d weighted=%d\n", name, group.Units, group.Total, group.Weighted)
		}
		for _, model := range sortedTokenKeys(t.ByModel) {
			group := t.ByModel[model]
			fmt.Fprintf(a.out, "tokens model %s: units=%d total=%d weighted=%d\n", model, group.Units, group.Total, group.Weighted)
		}
		for _, unit := range t.Costliest {
			fmt.Fprintf(a.out, "costliest: %s agent=%s model=%s result=%s total=%d weighted=%d\n", unit.Unit, unit.Agent, unit.Model, unit.Result, unit.Total, unit.Weighted)
		}
		if t.Unsettled > 0 {
			fmt.Fprintf(a.out, "tokens not final yet: %d units (the token collector has not caught up with their finish; run again in a few minutes)\n", t.Unsettled)
		}
	}
	for _, step := range stats.Steps {
		fmt.Fprintf(a.out, "step %s: units=%d failed=%d median_work=%.1fs p90_work=%.1fs median_queue=%.1fs\n", step.Name, step.Units, step.Failed, step.MedianWork, step.P90Work, step.MedianQueue)
	}
	printApprovalStats(a.out, stats.Approvals)
	printActivityStats(a.out, stats.Count, stats.Activity)
	return nil
}

// formatWorkflowRoles lists each role of a pipeline with its agents, in step
// order: "coder=agent-a,agent-c reviewer=agent-b".
func formatWorkflowRoles(definition workflow.Definition, agents []string, roles map[string]string) string {
	parts := make([]string, 0, len(definition.Roles()))
	for _, role := range definition.Roles() {
		var names []string
		for _, agent := range agents {
			own := roles[agent]
			if own == "" {
				own = workflow.DefaultRole
			}
			if own == role {
				names = append(names, agent)
			}
		}
		parts = append(parts, role+"="+strings.Join(names, ","))
	}
	return strings.Join(parts, " ")
}

func sortedTokenKeys(values map[string]workflow.TokenGroup) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedGroupKeys(values map[string]workflow.GroupStats) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
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
	driver := a.newWorkflowDriverBase(run.Owner)
	engine := workflow.NewEngine(store, driver)
	engine.Authority = func(owner, agent string) error { return a.workflowAuthorityFor(driver, owner, agent) }
	engine.Notify = a.workflowNotifier(run.ID)
	return engine.Run(a.ctx, run.ID, retryFailed || run.RetryFailed)
}

func (a *app) workflowNotifier(runID string) func(context.Context, string, string) error {
	return func(_ context.Context, owner, message string) error {
		_, _, err := a.deliver(owner, "workflow:"+runID, message)
		return err
	}
}

func (a *app) workflowAuthority(owner, agent string) error {
	fleet, err := a.loadWorkflowFleet()
	if err != nil {
		return err
	}
	return checkWorkflowAuthority(fleet, owner, agent)
}

func (a *app) workflowAuthorityFor(driver *workflowDriver, owner, agent string) error {
	fleet, err := a.loadWorkflowFleet()
	if err != nil {
		return err
	}
	driver.setFleet(agent, fleet)
	return checkWorkflowAuthority(fleet, owner, agent)
}

func checkWorkflowAuthority(fleet book.Fleet, owner, agent string) error {
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
