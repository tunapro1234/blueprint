package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/cache"
	bptmux "blueprint/internal/tmux"
)

const fleetUpdateUsage = "usage: bp update [--clis] [--models <from=to...>] [--all] [--agent <name>...] [--set-defaults] [--dry-run] [--yes] [--json]"

type fleetUpdateOptions struct {
	clis, all, dryRun, yes, json, setDefaults bool
	models                                    map[string]string
	agents                                    []string
}

type fleetUpdateRow struct {
	Agent       string `json:"agent"`
	OldModel    string `json:"old_model,omitempty"`
	OldEffort   string `json:"old_effort,omitempty"`
	NewModel    string `json:"new_model,omitempty"`
	NewEffort   string `json:"new_effort,omitempty"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	ThreadID    string `json:"thread_id,omitempty"`
	Harness     string `json:"harness,omitempty"`
	Live        bool   `json:"live"`
	restart     bool
	storeModels bool
	launch      bptmux.OpenOptions
	folder      string
}

type fleetUpdateReport struct {
	CLIs            []string         `json:"clis,omitempty"`
	DefaultsUpdated []string         `json:"defaults_updated,omitempty"`
	Agents          []fleetUpdateRow `json:"agents"`
}

// fleetUpdateBackend is the mutation boundary for fleet updates. Tests replace
// it completely, so no test can run a native updater or touch a real tmux pane.
type fleetUpdateBackend interface {
	Installed(string) bool
	UpdateCLI(context.Context, string, []string) error
	Capture(string) (string, error)
	Restart(string, string, bptmux.OpenOptions) error
	Verify(string, string, string, string) error
	ApplyBar(string)
}

func parseFleetUpdate(args []string) (fleetUpdateOptions, error) {
	opts := fleetUpdateOptions{models: map[string]string{}}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--clis":
			opts.clis = true
		case "--all":
			opts.all = true
		case "--dry-run":
			opts.dryRun = true
		case "--yes":
			opts.yes = true
		case "--json":
			opts.json = true
		case "--set-defaults":
			opts.setDefaults = true
		case "--agent":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return opts, fmt.Errorf("--agent requires a name")
			}
			i++
			opts.agents = append(opts.agents, args[i])
		case "--models":
			start := i + 1
			for i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
				from, to, ok := strings.Cut(args[i], "=")
				if !ok || strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" {
					return opts, fmt.Errorf("invalid model mapping %q; expected from=to", args[i])
				}
				opts.models[from] = to
			}
			if i+1 == start {
				return opts, fmt.Errorf("--models requires at least one from=to mapping")
			}
		default:
			return opts, fmt.Errorf("%s", fleetUpdateUsage)
		}
	}
	if !opts.clis && !opts.all && len(opts.models) == 0 {
		return opts, fmt.Errorf("%s", fleetUpdateUsage)
	}
	if opts.setDefaults && len(opts.models) == 0 {
		return opts, fmt.Errorf("--set-defaults requires --models")
	}
	return opts, nil
}

func (a *app) fleetUpdate(args []string) error {
	opts, err := parseFleetUpdate(args)
	if err != nil {
		return err
	}
	fleet, states, err := a.fleet()
	if err != nil {
		return err
	}
	backend := a.fleetUpdater
	if backend == nil {
		backend = &liveFleetUpdate{app: a}
	}
	report, err := a.planFleetUpdate(fleet, states, opts, backend)
	if err != nil {
		return err
	}
	if opts.dryRun {
		return a.printFleetUpdate(report, opts.json)
	}
	if !opts.yes {
		if !a.interactiveTerminal() {
			return fmt.Errorf("refusing a non-interactive fleet update without --yes")
		}
		if err := a.printFleetUpdate(report, opts.json); err != nil {
			return err
		}
		confirmed, err := a.confirmFleetUpdate()
		if err != nil {
			return err
		}
		if !confirmed {
			return fmt.Errorf("fleet update cancelled")
		}
	}

	if opts.clis || opts.all {
		for _, harness := range report.CLIs {
			command := a.config.CLIUpdates[harness]
			if err := backend.UpdateCLI(a.ctx, harness, command); err != nil {
				return fmt.Errorf("update %s CLI: %w", harness, err)
			}
		}
	}

	now := time.Now().UTC()
	for index := range report.Agents {
		row := &report.Agents[index]
		registered := fleet.Agents[row.Agent]
		migration := nextFleetMigration(registered, *row, fleetMigrationModels(*row, opts.models), now)
		if row.Status == "deferred" {
			if err := book.SetFleetUpdate(a.config.Agentbooks, row.Agent, migration); err != nil {
				return err
			}
			continue
		}
		if !row.restart {
			if len(opts.models) > 0 || registered.FleetUpdate != nil {
				if err := book.SetFleetUpdate(a.config.Agentbooks, row.Agent, migration); err != nil {
					return err
				}
			}
			continue
		}
		release, err := a.lockPane(row.Agent)
		if err != nil {
			if errors.Is(err, bptmux.ErrBusy) {
				row.Status, row.Reason = "deferred", "pane busy in another bp operation"
				migration = nextFleetMigration(registered, *row, fleetMigrationModels(*row, opts.models), now)
				if recordErr := book.SetFleetUpdate(a.config.Agentbooks, row.Agent, migration); recordErr != nil {
					row.Status, row.Reason = "failed", "record deferral: "+recordErr.Error()
				}
			} else {
				row.Status, row.Reason = "failed", err.Error()
			}
			continue
		}
		currentFleet, currentStates, err := a.fleet()
		if err != nil {
			release()
			row.Status, row.Reason = "failed", "refresh fleet state: "+err.Error()
			continue
		}
		currentOpts := opts
		currentOpts.agents = []string{row.Agent}
		current, err := a.planFleetUpdate(currentFleet, currentStates, currentOpts, backend)
		if err != nil {
			release()
			row.Status, row.Reason = "failed", "refresh update plan: "+err.Error()
			continue
		}
		if len(current.Agents) != 1 {
			release()
			row.Status, row.Reason = "failed", "refresh update plan returned no agent"
			continue
		}
		*row = current.Agents[0]
		migration = nextFleetMigration(currentFleet.Agents[row.Agent], *row, fleetMigrationModels(*row, opts.models), now)
		if row.Status == "deferred" || !row.restart {
			release()
			if row.Status == "deferred" || len(opts.models) > 0 || currentFleet.Agents[row.Agent].FleetUpdate != nil {
				if err := book.SetFleetUpdate(a.config.Agentbooks, row.Agent, migration); err != nil {
					row.Status, row.Reason = "failed", "record migration: "+err.Error()
				}
			}
			continue
		}
		if state := currentStates[row.Agent]; state.ScreenBusy || state.TurnBusy {
			release()
			row.Status, row.Reason = "deferred", "agent busy"
			migration = nextFleetMigration(currentFleet.Agents[row.Agent], *row, fleetMigrationModels(*row, opts.models), now)
			if recordErr := book.SetFleetUpdate(a.config.Agentbooks, row.Agent, migration); recordErr != nil {
				row.Status, row.Reason = "failed", "record deferral: "+recordErr.Error()
			}
			continue
		}
		pane, err := backend.Capture(row.Agent)
		if err != nil {
			release()
			row.Status, row.Reason = "deferred", "pane unreadable"
			migration := nextFleetMigration(currentFleet.Agents[row.Agent], *row, fleetMigrationModels(*row, opts.models), now)
			migration.DeferredReason = row.Reason
			if recordErr := book.SetFleetUpdate(a.config.Agentbooks, row.Agent, migration); recordErr != nil {
				row.Status, row.Reason = "failed", "record deferral: "+recordErr.Error()
			}
			continue
		}
		switch {
		case strings.TrimSpace(pane) == "":
			row.Status, row.Reason = "deferred", "pane unreadable"
		case bptmux.Dialog(pane):
			row.Status, row.Reason = "deferred", "modal open"
		case bptmux.Typing(pane):
			row.Status, row.Reason = "deferred", "unsent user text in composer"
		case bptmux.Busy(pane):
			row.Status, row.Reason = "deferred", "agent busy"
		}
		if row.Status == "deferred" {
			release()
			migration := nextFleetMigration(currentFleet.Agents[row.Agent], *row, fleetMigrationModels(*row, opts.models), now)
			migration.DeferredReason = row.Reason
			if recordErr := book.SetFleetUpdate(a.config.Agentbooks, row.Agent, migration); recordErr != nil {
				row.Status, row.Reason = "failed", "record deferral: "+recordErr.Error()
			}
			continue
		}
		err = backend.Restart(row.Agent, row.folder, row.launch)
		release()
		if err == nil {
			err = backend.Verify(row.Agent, row.ThreadID, row.NewModel, row.NewEffort)
		}
		if err != nil {
			switch {
			case errors.Is(err, bptmux.ErrTyping):
				row.Status, row.Reason = "deferred", "unsent user text in composer"
			case errors.Is(err, bptmux.ErrBusy):
				row.Status, row.Reason = "deferred", "agent busy"
			default:
				row.Status, row.Reason = "failed", err.Error()
			}
			migration = nextFleetMigration(currentFleet.Agents[row.Agent], *row, fleetMigrationModels(*row, opts.models), now)
			migration.DeferredReason = row.Reason
			if recordErr := book.SetFleetUpdate(a.config.Agentbooks, row.Agent, migration); recordErr != nil {
				row.Reason += "; failed to record result: " + recordErr.Error()
			}
			continue
		}
		backend.ApplyBar(row.Agent)
		row.Status, row.Reason = "ok", ""
		migration = nextFleetMigration(currentFleet.Agents[row.Agent], *row, fleetMigrationModels(*row, opts.models), now)
		if err := book.SetFleetUpdate(a.config.Agentbooks, row.Agent, migration); err != nil {
			row.Status, row.Reason = "failed", "record migration: "+err.Error()
			continue
		}
		if err := book.SetStatus(a.config.Agentbooks, row.Agent, "open", row.folder, book.Registration{Launch: &row.launch}); err != nil {
			row.Status, row.Reason = "failed", "record launch: "+err.Error()
		}
	}
	if opts.setDefaults {
		changed, err := updateFleetDefaults(opts.models, time.Now().UTC())
		if err != nil {
			return err
		}
		report.DefaultsUpdated = changed
	}
	return a.printFleetUpdate(report, opts.json)
}

func (a *app) confirmFleetUpdate() (bool, error) {
	if a.confirmUpdate != nil {
		return a.confirmUpdate()
	}
	fmt.Fprint(a.out, "Apply this fleet update? [y/N] ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && len(line) == 0 {
		return false, err
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes", nil
}

func (a *app) planFleetUpdate(fleet book.Fleet, states map[string]book.State, opts fleetUpdateOptions, backend fleetUpdateBackend) (fleetUpdateReport, error) {
	names := append([]string(nil), fleet.Order...)
	if len(opts.agents) > 0 {
		names = nil
		seen := map[string]bool{}
		for _, name := range opts.agents {
			if _, ok := fleet.Agents[name]; !ok {
				return fleetUpdateReport{}, fmt.Errorf("unknown agent: %s", name)
			}
			if !seen[name] {
				names, seen[name] = append(names, name), true
			}
		}
	}
	sort.SliceStable(names, func(i, j int) bool {
		if names[i] == fleet.Root {
			return false
		}
		if names[j] == fleet.Root {
			return true
		}
		di, dj := fleetDepth(fleet, names[i]), fleetDepth(fleet, names[j])
		if di != dj {
			return di > dj
		}
		return names[i] < names[j]
	})
	report := fleetUpdateReport{Agents: make([]fleetUpdateRow, 0, len(names))}
	if opts.clis || opts.all {
		harnesses := []string{"claude", "codex", "opencode", "hermes"}
		if len(opts.agents) > 0 {
			selected := make(map[string]bool, len(opts.agents))
			for _, name := range names {
				if harness := fleetCLIName(fleet.Agents[name], states[name]); harness != "" {
					selected[harness] = true
				}
			}
			harnesses = harnesses[:0]
			for _, harness := range []string{"claude", "codex", "opencode", "hermes"} {
				if selected[harness] {
					harnesses = append(harnesses, harness)
				}
			}
		}
		for _, harness := range harnesses {
			if len(a.config.CLIUpdates[harness]) > 0 && backend.Installed(harness) {
				report.CLIs = append(report.CLIs, harness)
			}
		}
	}
	for _, name := range names {
		agent := fleet.Agents[name]
		state := states[name]
		row := fleetUpdateRow{Agent: name, Live: state.Alive, Status: "planned", folder: book.FirstPath(agent.Folder)}
		if !state.Alive {
			row.Status, row.Reason = "deferred", "closed; model mapping will apply on next resume"
			row.OldModel, row.OldEffort = recordedModelEffort(agent)
			if state.Runtime != nil {
				if state.Runtime.Model != "" {
					row.OldModel = state.Runtime.Model
				}
				if state.Runtime.Effort != "" {
					row.OldEffort = state.Runtime.Effort
				}
			}
			row.NewModel, row.NewEffort = row.OldModel, row.OldEffort
			if len(opts.models) > 0 {
				switch {
				case !supportsFleetModelLaunch(agent.Launch):
					row.Reason = "closed; unsupported or unknown launch mode; model mapping not stored"
				case row.OldModel == "" || row.OldEffort == "":
					row.Reason = "closed; current model or effort unknown; model mapping not stored"
				default:
					row.storeModels = true
					row.NewModel = mappedModel(row.OldModel, opts.models)
				}
			}
			report.Agents = append(report.Agents, row)
			continue
		}
		runtime := state.Runtime
		if runtime == nil || runtime.Runtime == "" || runtime.Runtime == "unknown" || runtime.Activity == nil || runtime.Activity.ThreadID == "" || runtime.Activity.State == "unknown" || runtime.Activity.State == "notLoaded" {
			row.Status, row.Reason = "deferred", "runtime unknown or unloaded"
			report.Agents = append(report.Agents, row)
			continue
		}
		row.ThreadID, row.Harness = runtime.Activity.ThreadID, runtime.Runtime
		if row.Harness != "claude" && row.Harness != "codex" && row.Harness != "codex-remote" {
			row.Status, row.Reason = "deferred", "unsupported runtime "+row.Harness
			report.Agents = append(report.Agents, row)
			continue
		}
		pane, captureErr := backend.Capture(name)
		switch {
		case captureErr != nil:
			row.Status, row.Reason = "deferred", "pane unreadable"
		case strings.TrimSpace(pane) == "":
			row.Status, row.Reason = "deferred", "pane unreadable"
		case bptmux.Dialog(pane):
			row.Status, row.Reason = "deferred", "modal open"
		case bptmux.Typing(pane):
			row.Status, row.Reason = "deferred", "unsent user text in composer"
		case state.ScreenBusy || state.TurnBusy || runtime.Activity.State != "idle" || bptmux.Busy(pane):
			row.Status, row.Reason = "deferred", "agent busy"
		case runtime.Model == "":
			row.Status, row.Reason = "deferred", "current model unknown"
		case runtime.Effort == "":
			row.Status, row.Reason = "deferred", "current effort unknown"
		}
		row.OldModel, row.OldEffort = runtime.Model, runtime.Effort
		row.NewModel, row.NewEffort = mappedModel(row.OldModel, opts.models), row.OldEffort
		if len(opts.models) > 0 && row.OldModel != "" && row.OldEffort != "" && fleetModelMigrationMatches(agent, row.Harness) {
			row.storeModels = true
		}
		if row.Status == "deferred" {
			report.Agents = append(report.Agents, row)
			continue
		}
		if !fleetModelMigrationMatches(agent, row.Harness) {
			row.Status, row.Reason = "deferred", "launch mode is unavailable or does not match runtime"
			report.Agents = append(report.Agents, row)
			continue
		}
		row.launch = fleetLaunch(agent, row.Harness, row.ThreadID, row.NewModel, row.NewEffort)
		row.restart = opts.clis || opts.all || row.NewModel != row.OldModel
		if !row.restart {
			row.Status = "ok"
		}
		report.Agents = append(report.Agents, row)
	}
	return report, nil
}

func fleetCLIName(agent book.Agent, state book.State) string {
	if state.Runtime != nil {
		switch state.Runtime.Runtime {
		case "claude":
			return "claude"
		case "codex", "codex-remote":
			return "codex"
		case "opencode", "hermes":
			return state.Runtime.Runtime
		}
	}
	if agent.Launch == nil {
		return ""
	}
	if agent.Launch.Hermes {
		return "hermes"
	}
	if agent.Launch.Codex || agent.Launch.Remote != "" {
		return "codex"
	}
	return "claude"
}

func recordedModelEffort(agent book.Agent) (string, string) {
	if agent.FleetUpdate != nil && agent.FleetUpdate.Model != "" && agent.FleetUpdate.Effort != "" {
		return agent.FleetUpdate.Model, agent.FleetUpdate.Effort
	}
	if agent.Launch == nil {
		return "", ""
	}
	if agent.Launch.Codex {
		return codexArgsModel(agent.Launch.Args)
	}
	var model, effort string
	for i := 0; i < len(agent.Launch.Args); i++ {
		name, inlineValue, inline := strings.Cut(agent.Launch.Args[i], "=")
		switch name {
		case "--model", "--effort":
			value := inlineValue
			if !inline && i+1 < len(agent.Launch.Args) {
				i++
				value = agent.Launch.Args[i]
			}
			if name == "--model" {
				model = value
			} else {
				effort = value
			}
		}
	}
	return model, effort
}

func supportsFleetModelLaunch(launch *bptmux.OpenOptions) bool {
	return launch != nil && !launch.Hermes && !launch.OpenCode && (launch.Remote == "" || launch.Codex)
}

func fleetModelMigrationMatches(agent book.Agent, runtime string) bool {
	if !supportsFleetModelLaunch(agent.Launch) {
		return false
	}
	if runtime != "claude" && runtime != "codex" && runtime != "codex-remote" {
		return false
	}
	remoteRuntime := runtime == "codex-remote"
	remoteLaunch := agent.Launch.Remote != ""
	return agent.Launch.Codex == strings.HasPrefix(runtime, "codex") && remoteRuntime == remoteLaunch
}

func fleetMigrationModels(row fleetUpdateRow, requested map[string]string) map[string]string {
	if !row.storeModels {
		return nil
	}
	return requested
}

func applyStoredFleetUpdate(agent book.Agent, opts *bptmux.OpenOptions) error {
	if opts == nil || agent.FleetUpdate == nil || len(agent.FleetUpdate.Models) == 0 || !opts.Resume || !supportsFleetModelLaunch(opts) {
		return nil
	}
	model, effort := recordedModelEffort(book.Agent{Launch: opts, FleetUpdate: agent.FleetUpdate})
	if model == "" {
		model = agent.FleetUpdate.Model
	}
	if effort == "" {
		effort = agent.FleetUpdate.Effort
	}
	if model == "" || effort == "" {
		return fmt.Errorf("stored fleet model migration for %s cannot establish the previous model and effort; inspect the record before resuming", agent.Name)
	}
	model = mappedModel(model, agent.FleetUpdate.Models)
	opts.Args = fleetModelArgs(opts.Codex, opts.Args, model, effort)
	return nil
}

func fleetDepth(fleet book.Fleet, name string) int {
	depth, seen := 0, map[string]bool{}
	for name != "" && !seen[name] {
		seen[name], name, depth = true, fleet.Parents[name], depth+1
	}
	return depth
}

func mappedModel(model string, mappings map[string]string) string {
	if mapped, ok := mappings[model]; ok {
		return mapped
	}
	return model
}

func cloneModelMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

func nextFleetMigration(agent book.Agent, row fleetUpdateRow, models map[string]string, now time.Time) book.FleetUpdate {
	if len(models) == 0 && agent.FleetUpdate != nil {
		models = agent.FleetUpdate.Models
	}
	model, effort := row.NewModel, row.NewEffort
	if !row.storeModels && row.NewModel != row.OldModel {
		model = row.OldModel
	}
	reason := row.Reason
	if agent.FleetUpdate != nil {
		if model == "" {
			model = agent.FleetUpdate.Model
		}
		if effort == "" {
			effort = agent.FleetUpdate.Effort
		}
		if reason == "" {
			reason = agent.FleetUpdate.DeferredReason
		}
	}
	return book.FleetUpdate{Models: cloneModelMap(models), Model: model, Effort: effort, DeferredReason: reason, RecordedAt: now}
}

func fleetLaunch(agent book.Agent, runtime, thread, model, effort string) bptmux.OpenOptions {
	opts := bptmux.OpenOptions{Codex: strings.HasPrefix(runtime, "codex")}
	if agent.Launch != nil {
		opts = *agent.Launch
		opts.Args = append([]string(nil), agent.Launch.Args...)
	}
	opts.Codex, opts.Hermes = strings.HasPrefix(runtime, "codex"), false
	opts.Resume, opts.ResumeID, opts.NoPrompt = true, thread, true
	opts.Args = fleetModelArgs(opts.Codex, opts.Args, model, effort)
	return opts
}

func fleetModelArgs(codex bool, args []string, model, effort string) []string {
	filtered := make([]string, 0, len(args)+4)
	for i := 0; i < len(args); i++ {
		name, _, inline := strings.Cut(args[i], "=")
		if name == "-m" || name == "--model" || !codex && name == "--effort" {
			if !inline && i+1 < len(args) {
				i++
			}
			continue
		}
		if codex && (name == "-c" || name == "--config") && !inline && i+1 < len(args) {
			key, _, ok := strings.Cut(args[i+1], "=")
			if ok && (strings.TrimSpace(key) == "model" || strings.TrimSpace(key) == "model_reasoning_effort") {
				i++
				continue
			}
		}
		filtered = append(filtered, args[i])
	}
	if codex {
		return append(filtered, "-m", model, "-c", "model_reasoning_effort="+effort)
	}
	return append(filtered, "--model", model, "--effort", effort)
}

func (b *liveFleetUpdate) Verify(name, threadID, model, effort string) error {
	var last cache.State
	for attempt := 0; attempt < 20; attempt++ {
		state, err := b.observe(name)
		if err == nil {
			last = state
			if state.Activity != nil && state.Activity.ThreadID == threadID && state.Model == model && state.Effort == effort {
				return nil
			}
		}
		if attempt < 19 {
			time.Sleep(250 * time.Millisecond)
		}
	}
	thread := ""
	if last.Activity != nil {
		thread = last.Activity.ThreadID
	}
	return fmt.Errorf("verification mismatch: thread/model/effort got %s/%s/%s, want %s/%s/%s", thread, last.Model, last.Effort, threadID, model, effort)
}

func (a *app) printFleetUpdate(report fleetUpdateReport, jsonOutput bool) error {
	if jsonOutput {
		return json.NewEncoder(a.out).Encode(report)
	}
	if len(report.CLIs) > 0 {
		fmt.Fprintf(a.out, "CLI updates: %s\n", strings.Join(report.CLIs, ", "))
	}
	if len(report.DefaultsUpdated) > 0 {
		fmt.Fprintf(a.out, "Defaults updated: %s\n", strings.Join(report.DefaultsUpdated, ", "))
	}
	fmt.Fprintf(a.out, "%-24s %-28s %-28s %s\n", "AGENT", "OLD MODEL/EFFORT", "NEW MODEL/EFFORT", "RESULT")
	for _, row := range report.Agents {
		result := row.Status
		if row.Reason != "" {
			result += " (" + row.Reason + ")"
		}
		fmt.Fprintf(a.out, "%-24s %-28s %-28s %s\n", row.Agent, modelEffort(row.OldModel, row.OldEffort), modelEffort(row.NewModel, row.NewEffort), result)
	}
	return nil
}

func modelEffort(model, effort string) string {
	if model == "" && effort == "" {
		return "-"
	}
	return model + "/" + effort
}

type liveFleetUpdate struct{ app *app }

func (b *liveFleetUpdate) Installed(harness string) bool {
	_, err := exec.LookPath(harness)
	return err == nil
}

func (b *liveFleetUpdate) UpdateCLI(ctx context.Context, _ string, command []string) error {
	if len(command) == 0 {
		return nil
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, b.app.out, b.app.err
	return cmd.Run()
}

func (b *liveFleetUpdate) Capture(name string) (string, error) { return b.app.capture(name) }

func (b *liveFleetUpdate) Restart(name, folder string, opts bptmux.OpenOptions) error {
	if err := opts.Validate(); err != nil {
		return fmt.Errorf("invalid restart options: %w", err)
	}
	if folder == "" {
		return fmt.Errorf("working directory is unavailable; refusing to exit the native CLI")
	}
	if info, err := os.Stat(folder); err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("not a directory")
		}
		return fmt.Errorf("working directory %s is unavailable; refusing to exit the native CLI: %w", folder, err)
	}
	launch, err := b.app.managedFleetLaunch(name, opts)
	if err != nil {
		return fmt.Errorf("prepare restart: %w", err)
	}
	process, err := b.app.tmux.PaneProcess(b.app.ctx, name)
	if err != nil {
		return fmt.Errorf("verify pane before restart: %w", err)
	}
	pane, err := b.app.tmux.Capture(b.app.ctx, name)
	if err != nil {
		return fmt.Errorf("verify pane before restart: %w", err)
	}
	if !fleetRestartPaneMatches(launch, process.Command, pane) {
		return fmt.Errorf("pane no longer matches the planned harness; refusing to exit it")
	}
	if err := b.app.tmux.Send(b.app.ctx, name, "/exit"); err != nil {
		return fmt.Errorf("exit native CLI: %w", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	dead := false
	for time.Now().Before(deadline) {
		var err error
		dead, err = b.app.tmux.PaneDead(b.app.ctx, name)
		if err != nil {
			return fmt.Errorf("verify native CLI exit: %w", err)
		}
		if dead {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !dead {
		return fmt.Errorf("native CLI did not leave an exited pane; refusing to terminate a live pane")
	}
	return b.app.tmux.Open(b.app.ctx, name, folder, launch, func(message string) { fmt.Fprintln(b.app.out, message) })
}

func fleetRestartPaneMatches(opts bptmux.OpenOptions, command, pane string) bool {
	if opts.Hermes || opts.OpenCode {
		return false
	}
	if opts.Codex {
		if opts.Remote == "" && !bptmux.IsCodexCommand(command) {
			return false
		}
		return bptmux.CodexPane(pane)
	}
	return command == "claude" && bptmux.IsAgentPane(command, pane)
}

func (b *liveFleetUpdate) observe(name string) (cache.State, error) {
	fleet, err := book.LoadFleet(book.Paths(b.app.config.Agentbooks))
	if err != nil {
		return cache.State{}, err
	}
	return book.RuntimeFor(b.app.ctx, b.app.tmux, fleet, name), nil
}

func (b *liveFleetUpdate) ApplyBar(name string) { b.app.applyOpenBar(name) }

func (a *app) managedFleetLaunch(name string, opts bptmux.OpenOptions) (bptmux.OpenOptions, error) {
	if a.config.Legacy || opts.Hermes || opts.Remote != "" {
		return opts, nil
	}
	self, err := os.Executable()
	if err != nil {
		return opts, err
	}
	harness := "claude"
	if opts.Codex {
		harness = "codex"
	}
	opts.Launcher = "env BP_HOME=" + quoteShell(a.config.Home)
	for _, key := range []string{"HOME", "PATH", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "AGENTBOOK"} {
		if value, ok := os.LookupEnv(key); ok {
			opts.Launcher += " " + key + "=" + quoteShell(value)
		}
	}
	opts.Launcher += " " + quoteShell(self) + " _open-session " + quoteShell(name) + " " + harness
	return opts, nil
}

// updateFleetDefaults is intentionally file-only: native CLIs are never invoked.
// Each changed file is copied beside itself before an atomic rewrite.
func updateFleetDefaults(models map[string]string, now time.Time) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	claudeConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if claudeConfigDir == "" {
		claudeConfigDir = filepath.Join(home, ".claude")
	}
	var changed []string
	paths := []struct {
		path string
		kind string
	}{
		{filepath.Join(codexHome, "config.toml"), "toml"},
		{filepath.Join(claudeConfigDir, "settings.json"), "json"},
	}
	for _, item := range paths {
		data, err := os.ReadFile(item.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return changed, err
		}
		updated, ok, err := mapDefaultModel(data, item.kind, models)
		if err != nil {
			return changed, fmt.Errorf("update %s: %w", item.path, err)
		}
		if !ok {
			continue
		}
		info, err := os.Stat(item.path)
		if err != nil {
			return changed, err
		}
		backup := item.path + ".before-fleet-update-" + now.Format("20060102T150405.000000000Z")
		backupFile, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
		if err != nil {
			return changed, err
		}
		if _, err = backupFile.Write(data); err == nil {
			err = backupFile.Sync()
		}
		if closeErr := backupFile.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return changed, err
		}
		if err := writeFleetDefault(item.path, updated, info.Mode().Perm()); err != nil {
			return changed, err
		}
		changed = append(changed, item.path)
	}
	return changed, nil
}

func writeFleetDefault(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".fleet-default-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(mode); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func mapDefaultModel(data []byte, kind string, models map[string]string) ([]byte, bool, error) {
	if kind == "json" {
		var value map[string]any
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, false, err
		}
		current, _ := value["model"].(string)
		next, ok := models[current]
		if !ok {
			return data, false, nil
		}
		value["model"] = next
		updated, err := json.MarshalIndent(value, "", "  ")
		return append(updated, '\n'), true, err
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			break
		}
		key, raw, ok := strings.Cut(trimmed, "=")
		if !ok || strings.TrimSpace(key) != "model" {
			continue
		}
		current := strings.Trim(strings.TrimSpace(raw), `"'`)
		next, mapped := models[current]
		if !mapped {
			return data, false, nil
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + "model = " + fmt.Sprintf("%q", next)
		return []byte(strings.Join(lines, "\n")), true, nil
	}
	return data, false, nil
}
