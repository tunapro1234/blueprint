package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

const PollInterval = 5 * time.Second

type AgentState string

const (
	Idle    AgentState = "idle"
	Working AgentState = "working"
	Blocked AgentState = "blocked"
	Unknown AgentState = "unknown"
	Dead    AgentState = "dead"
)

type Observation struct {
	State         AgentState `json:"state"`
	ContextKnown  bool       `json:"context_known,omitempty"`
	ContextTokens int        `json:"context_tokens,omitempty"`
	Model         string     `json:"model,omitempty"`
	ObservedAt    time.Time  `json:"observed_at,omitempty"`
	TurnAt        time.Time  `json:"turn_at,omitempty"`
	Detail        string     `json:"detail,omitempty"`
}

type DeliveryStatus string

const (
	DeliveryDelivered DeliveryStatus = "delivered"
	DeliveryQueued    DeliveryStatus = "queued"
	DeliveryFailed    DeliveryStatus = "failed"
)

type Delivery struct {
	Status      DeliveryStatus `json:"status"`
	ChannelID   string         `json:"channel_id,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	QueuedAt    time.Time      `json:"queued_at,omitempty"`
	DeliveredAt time.Time      `json:"delivered_at,omitempty"`
}

type Driver interface {
	Observe(ctx context.Context, agent string) (Observation, error)
	Send(ctx context.Context, agent, text string) (Delivery, error)
	Compact(ctx context.Context, agent string) error
}

// DeliveryWaiter observes an existing queue record without sending it again.
type DeliveryWaiter interface {
	WaitDelivery(ctx context.Context, agent string, delivery Delivery, timeout time.Duration) (Delivery, error)
}

type Engine struct {
	Store     *Store
	Driver    Driver
	Now       func() time.Time
	Sleep     func(context.Context, time.Duration) error
	Authority func(owner, agent string) error
	Notify    func(context.Context, string, string) error
	Validator func(context.Context, string, []string, time.Duration) (int, string, string, error)
	Poll      time.Duration
	mu        sync.Mutex
	active    *Run
}

var ErrCompactBusy = errors.New("agent became busy before compact")
var ErrCompactQueued = errors.New("compact delivery queued")
var ErrValidatorTimeout = errors.New("validator timed out")
var errStopBoundary = errors.New("workflow stop requested at a unit boundary")

type unitTask struct {
	unit        Unit
	previous    Event
	hasPrevious bool
}

type unitMetrics struct {
	started       time.Time
	startedAt     time.Time
	queuedAt      time.Time
	deliveredAt   time.Time
	model         string
	work          float64
	validate      float64
	wait          float64
	roundWork     []float64
	compact       float64
	compactResult string
	compactBefore *int
	compactAfter  *int
	ctxStart      *int
	controls      int
	channel       string
	last          Observation
}

func NewEngine(store *Store, driver Driver) *Engine {
	return &Engine{Store: store, Driver: driver, Now: time.Now, Sleep: sleepContext, Poll: PollInterval}
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func (e *Engine) sleep(ctx context.Context, delay time.Duration) error {
	if e.Sleep != nil {
		return e.Sleep(ctx, delay)
	}
	return sleepContext(ctx, delay)
}

func (e *Engine) interval() time.Duration {
	if e.Poll > 0 {
		return e.Poll
	}
	return PollInterval
}

func (e *Engine) Run(ctx context.Context, id string, retryFailed bool) error {
	if e.Store == nil || e.Driver == nil {
		return errors.New("workflow engine needs a store and driver")
	}
	run, err := e.Store.LoadRun(id)
	if err != nil {
		return err
	}
	retryFailed = retryFailed || run.RetryFailed
	if run.RetryFailed {
		updated, err := e.Store.UpdateRun(id, func(current *Run) error {
			current.RetryFailed = false
			return nil
		})
		if err != nil {
			return e.fail(id, err)
		}
		run = updated
	}
	d, err := ReadDefinition(e.Store.SnapshotDir(id))
	if err != nil {
		return e.fail(id, fmt.Errorf("read workflow snapshot: %w", err))
	}
	units, err := LoadUnits(d, run.Workdir)
	if err != nil {
		return e.fail(id, err)
	}
	if run.Limit > 0 && len(units) > run.Limit {
		units = units[:run.Limit]
	}
	keys := make([]string, len(units))
	for i, unit := range units {
		keys[i] = unit.Key
	}
	if !sameStrings(run.UnitKeys, keys) {
		updated, err := e.Store.UpdateRun(id, func(current *Run) error {
			current.UnitKeys = append([]string(nil), keys...)
			return nil
		})
		if err != nil {
			return e.fail(id, err)
		}
		run = updated
	}
	e.mu.Lock()
	e.active = &run
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.active = nil; e.mu.Unlock() }()
	if !run.StopRequested && run.Status != "stopping" {
		if err := e.setStatus("running", ""); err != nil {
			return e.fail(id, err)
		}
	}
	events, _, err := e.Store.Events(id)
	if err != nil {
		return e.fail(id, err)
	}
	latest := latestEvents(events)
	unitCounts := agentUnitCounts(events)
	run, err = e.Store.UpdateRun(id, func(current *Run) error {
		current.AgentUnits = unitCounts
		return nil
	})
	if err != nil {
		return e.fail(id, err)
	}
	recovery := make(map[string][]unitTask)
	var pending []unitTask
	for _, unit := range units {
		previous, ok := latest[unit.Key]
		if ok && terminalState(previous.State) {
			if previous.State != "failed" || !retryFailed {
				continue
			}
		}
		task := unitTask{unit: unit, previous: previous, hasPrevious: ok}
		if ok && !terminalState(previous.State) && previous.State != "compact_effective" && previous.State != "compact_ineffective" {
			if !contains(run.Agents, previous.Agent) {
				return e.fail(id, fmt.Errorf("unit %q is pinned to non-run agent %q", unit.Key, previous.Agent))
			}
			recovery[previous.Agent] = append(recovery[previous.Agent], task)
		} else {
			pending = append(pending, task)
		}
	}
	if len(run.Agents) == 0 {
		return e.fail(id, errors.New("workflow run has no agents"))
	}
	var completed atomic.Int64
	for _, event := range latest {
		if terminalState(event.State) {
			completed.Add(1)
		}
	}
	if err := e.runWorkers(ctx, run, d, pending, recovery, &completed); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return e.fail(id, err)
	}
	events, _, err = e.Store.Events(id)
	if err != nil {
		return e.fail(id, err)
	}
	latest = latestEvents(events)
	allFinished := true
	for _, unit := range units {
		if !terminalState(latest[unit.Key].State) {
			allFinished = false
			break
		}
	}
	run, err = e.Store.UpdateRun(id, func(current *Run) error {
		switch {
		case current.StopRequested || current.Status == "stopping":
			current.Status, current.LastError = "stopped", ""
		case allFinished:
			current.Status, current.LastError = "done", ""
		default:
			current.Status = "running"
		}
		return nil
	})
	if err != nil {
		return e.fail(id, err)
	}
	if (run.Status == "done" || run.Status == "stopped") && (d.Notify == "" || d.Notify == "owner") && e.Notify != nil {
		_ = e.Notify(ctx, run.Owner, fmt.Sprintf("workflow %s run %s %s", run.Workflow, run.ID, run.Status))
	}
	return nil
}

func (e *Engine) runWorkers(ctx context.Context, run Run, d Definition, pending []unitTask, recovery map[string][]unitTask, completed *atomic.Int64) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	next := 0
	var firstErr error
	var wg sync.WaitGroup
	for _, agent := range run.Agents {
		agent := agent
		wg.Add(1)
		go func() {
			defer wg.Done()
			lastFinished := time.Time{}
			for _, task := range recovery[agent] {
				if err := e.processTask(ctx, run, d, agent, task, &lastFinished, completed); err != nil {
					setFirstErr(&mu, &firstErr, cancel, err)
					return
				}
			}
			for {
				mu.Lock()
				if firstErr != nil || ctx.Err() != nil || next >= len(pending) {
					mu.Unlock()
					return
				}
				task := pending[next]
				next++
				mu.Unlock()
				if stop, err := e.stopRequested(run.ID); err != nil {
					setFirstErr(&mu, &firstErr, cancel, err)
					return
				} else if stop {
					return
				}
				if err := e.processTask(ctx, run, d, agent, task, &lastFinished, completed); err != nil {
					setFirstErr(&mu, &firstErr, cancel, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	return firstErr
}

func setFirstErr(mu *sync.Mutex, first *error, cancel context.CancelFunc, err error) {
	mu.Lock()
	defer mu.Unlock()
	if *first == nil {
		*first = err
		cancel()
	}
}

func (e *Engine) fail(id string, cause error) error {
	_, _ = e.Store.UpdateRun(id, func(run *Run) error {
		run.Status, run.LastError = "error", cause.Error()
		return nil
	})
	return cause
}

func (e *Engine) processTask(ctx context.Context, run Run, d Definition, agent string, task unitTask, lastFinished *time.Time, completed *atomic.Int64) error {
	if e.Authority != nil {
		if err := e.Authority(run.Owner, agent); err != nil {
			return fmt.Errorf("authority lost before unit %q on %s: %w", task.unit.Key, agent, err)
		}
	}
	if stop, err := e.stopRequested(run.ID); err != nil {
		return err
	} else if stop && (!task.hasPrevious || terminalState(task.previous.State)) {
		return nil
	}
	if err := e.setCurrent(run.ID, agent, task.unit.Key); err != nil {
		return err
	}
	defer func() { _ = e.setCurrent(run.ID, agent, "") }()
	if task.hasPrevious && !terminalState(task.previous.State) {
		return e.resumeTask(ctx, run, d, agent, task, lastFinished, completed)
	}
	idle, err := e.waitIdle(ctx, run.ID, agent, 0)
	if errors.Is(err, errStopBoundary) {
		return nil
	}
	if err != nil {
		return err
	}
	metrics := unitMetrics{model: idle.Model, last: idle}
	if idle.ContextKnown {
		value := idle.ContextTokens
		metrics.ctxStart = &value
	}
	updated, err := e.Store.LoadRun(run.ID)
	if err != nil {
		return err
	}
	if err := e.compactIfNeeded(ctx, run.ID, agent, d, updated.AgentUnits[agent], &metrics); errors.Is(err, errStopBoundary) {
		return nil
	} else if err != nil {
		return err
	}
	if stop, err := e.stopRequested(run.ID); err != nil {
		return err
	} else if stop {
		return nil
	}
	metrics.started = e.now()
	startedText := metrics.started.Format(time.RFC3339Nano)
	output, err := renderOutput(d, task.unit, run.Workdir, run.ID, startedText)
	if err != nil {
		return e.finishUnit(run, task.unit, agent, metrics, "failed", err.Error(), 1, nil, completed, lastFinished)
	}
	prompt, controls, err := renderPrompt(e.Store.SnapshotDir(run.ID), d, task.unit, run, 1, "", output, startedText)
	if err != nil {
		return e.finishUnit(run, task.unit, agent, metrics, "failed", "render prompt: "+err.Error(), 1, nil, completed, lastFinished)
	}
	metrics.controls = controls
	if err := e.appendEvent(run.ID, Event{Unit: task.unit.Key, Key: task.unit.Key, Agent: agent, State: "sending", Round: 1, ControlReplaced: controls, LastObservation: idle}); err != nil {
		return err
	}
	return e.sendAndWork(ctx, run, d, task.unit, agent, metrics, 1, prompt, output, completed, lastFinished)
}

func (e *Engine) resumeTask(ctx context.Context, run Run, d Definition, agent string, task unitTask, lastFinished *time.Time, completed *atomic.Int64) error {
	previous := task.previous
	metrics := unitMetrics{started: previous.At, channel: previous.DeliveryID, model: previous.LastObservation.Model, last: previous.LastObservation}
	if metrics.started.IsZero() {
		metrics.started = e.now()
	}
	if previous.LastObservation.ContextKnown {
		value := previous.LastObservation.ContextTokens
		metrics.ctxStart = &value
	}
	first, err := e.firstSend(task.unit.Key, run.ID)
	if err != nil {
		return err
	}
	if !first.IsZero() {
		metrics.started = first
	}
	if previous.State == "sending" {
		idle, waitErr := e.waitIdleInFlight(ctx, run.ID, agent, duration(d.Timeouts.Unit, 40*time.Minute))
		if waitErr != nil {
			return waitErr
		}
		metrics.last = idle
		return e.finishUnit(run, task.unit, agent, metrics, "failed", "delivery outcome unknown after restart; prompt was not resent", max(1, previous.Round), nil, completed, lastFinished)
	}
	if previous.State != "sent" && previous.State != "working" && previous.State != "validating" {
		return fmt.Errorf("cannot resume unit %q from state %q", task.unit.Key, previous.State)
	}
	startedAt := time.Time{}
	if previous.State == "sent" {
		deliveredAt := previous.At
		if previous.DeliveredAt != nil {
			deliveredAt = *previous.DeliveredAt
		}
		var observation Observation
		var startErr error
		startedAt, observation, startErr = e.waitForStart(ctx, agent, deliveredAt, d.Timeouts.Start)
		if startErr != nil {
			return startErr
		}
		metrics.last = observation
		if startedAt.IsZero() {
			return e.finishUnit(run, task.unit, agent, metrics, "failed", "never started after verified delivery (last observation: "+observationSummary(observation)+")", max(1, previous.Round), nil, completed, lastFinished)
		}
	} else {
		startedAt, err = e.firstWorking(task.unit.Key, run.ID)
		if err != nil {
			return err
		}
	}
	metrics.startedAt = startedAt
	idle, waitErr := e.waitIdleInFlight(ctx, run.ID, agent, duration(d.Timeouts.Unit, 40*time.Minute))
	if waitErr != nil {
		return waitErr
	}
	metrics.last = idle
	if !startedAt.IsZero() {
		seconds := max(0, e.now().Sub(startedAt).Seconds())
		metrics.work = seconds
		metrics.roundWork = append(metrics.roundWork, seconds)
	}
	if err := e.appendEvent(run.ID, Event{Unit: task.unit.Key, Key: task.unit.Key, Agent: agent, State: "validating", Round: max(1, previous.Round), DeliveryID: previous.DeliveryID, LastObservation: idle}); err != nil {
		return err
	}
	return e.validateLoop(ctx, run, d, task.unit, agent, metrics, max(1, previous.Round), previous.DeliveryID, completed, lastFinished)
}

func (e *Engine) sendAndWork(ctx context.Context, run Run, d Definition, unit Unit, agent string, metrics unitMetrics, round int, prompt, output string, completed *atomic.Int64, lastFinished *time.Time) error {
	queuedAt := e.now()
	delivery, err := e.Driver.Send(ctx, agent, prompt)
	if err != nil {
		return err
	}
	if delivery.QueuedAt.IsZero() {
		delivery.QueuedAt = queuedAt
	}
	if delivery.Status == DeliveryQueued {
		if waiter, ok := e.Driver.(DeliveryWaiter); ok {
			delivery, err = waiter.WaitDelivery(ctx, agent, delivery, duration(d.Timeouts.Start, 3*time.Minute))
			if err != nil && ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil && delivery.Reason == "" {
				delivery.Reason = err.Error()
			}
		}
	}
	if delivery.Status != DeliveryDelivered {
		reason := delivery.Reason
		if reason == "" {
			reason = "not delivered: " + string(delivery.Status)
		}
		if !strings.HasPrefix(strings.ToLower(reason), "not delivered") {
			reason = "not delivered: " + reason
		}
		return e.finishUnit(run, unit, agent, metrics, "failed", reason, round, &delivery, completed, lastFinished)
	}
	if delivery.DeliveredAt.IsZero() {
		delivery.DeliveredAt = e.now()
	}
	if metrics.queuedAt.IsZero() {
		metrics.queuedAt = delivery.QueuedAt
	}
	if metrics.deliveredAt.IsZero() {
		metrics.deliveredAt = delivery.DeliveredAt
	}
	if metrics.channel == "" {
		metrics.channel = delivery.ChannelID
	}
	metrics.wait += delivery.DeliveredAt.Sub(delivery.QueuedAt).Seconds()
	if err := e.appendEvent(run.ID, Event{Unit: unit.Key, Key: unit.Key, Agent: agent, State: "sent", Round: round, DeliveryID: delivery.ChannelID, DeliveryStatus: string(delivery.Status), QueuedAt: timePointer(delivery.QueuedAt), DeliveredAt: timePointer(delivery.DeliveredAt), LastObservation: metrics.last}); err != nil {
		return err
	}
	startedAt, observation, err := e.waitForStart(ctx, agent, delivery.DeliveredAt, d.Timeouts.Start)
	if err != nil {
		return err
	}
	metrics.last = observation
	if metrics.startedAt.IsZero() {
		metrics.startedAt = startedAt
	}
	if startedAt.IsZero() {
		reason := fmt.Sprintf("never started after verified delivery (delivery record %s; last observation: %s)", delivery.ChannelID, observationSummary(observation))
		return e.finishUnit(run, unit, agent, metrics, "failed", reason, round, &delivery, completed, lastFinished)
	}
	if err := e.appendEvent(run.ID, Event{Unit: unit.Key, Key: unit.Key, Agent: agent, State: "working", Round: round, DeliveryID: delivery.ChannelID, DeliveredAt: timePointer(delivery.DeliveredAt), LastObservation: observation}); err != nil {
		return err
	}
	done, seconds, idle, err := e.waitUnitIdle(ctx, run.ID, agent, d.Timeouts.Unit)
	if err != nil {
		return err
	}
	metrics.last, metrics.work = idle, metrics.work+seconds
	metrics.roundWork = append(metrics.roundWork, seconds)
	if !done {
		return e.finishUnit(run, unit, agent, metrics, "failed", "unit timed out before idle", round, &delivery, completed, lastFinished)
	}
	if err := e.appendEvent(run.ID, Event{Unit: unit.Key, Key: unit.Key, Agent: agent, State: "validating", Round: round, DeliveryID: delivery.ChannelID, LastObservation: idle}); err != nil {
		return err
	}
	return e.validateLoop(ctx, run, d, unit, agent, metrics, round, delivery.ChannelID, completed, lastFinished)
}

func (e *Engine) validateLoop(ctx context.Context, run Run, d Definition, unit Unit, agent string, metrics unitMetrics, round int, deliveryID string, completed *atomic.Int64, lastFinished *time.Time) error {
	started := metrics.started.Format(time.RFC3339Nano)
	output, err := renderOutput(d, unit, run.Workdir, run.ID, started)
	if err != nil {
		return e.finishUnit(run, unit, agent, metrics, "failed", err.Error(), round, nil, completed, lastFinished)
	}
	if err := validateOutput(d, run.Workdir, output, started); err != nil {
		if round >= configuredRounds(d) || d.Prompt.Followup == "" {
			return e.finishUnit(run, unit, agent, metrics, "failed", err.Error(), round, nil, completed, lastFinished)
		}
		return e.sendFollowup(ctx, run, d, unit, agent, metrics, round+1, err.Error(), output, completed, lastFinished)
	}
	begin := e.now()
	code, stdout, stderr, runErr := e.runValidator(ctx, run, d, unit, output, started, round)
	metrics.validate += e.now().Sub(begin).Seconds()
	if runErr != nil {
		reason := "validator failed: " + runErr.Error()
		if errors.Is(runErr, ErrValidatorTimeout) {
			reason = ErrValidatorTimeout.Error()
		}
		return e.finishUnit(run, unit, agent, metrics, "failed", reason, round, nil, completed, lastFinished)
	}
	if code == 0 {
		return e.finishUnit(run, unit, agent, metrics, "done", "", round, nil, completed, lastFinished)
	}
	if code != 1 && code != 3 {
		reason := strings.TrimSpace(stderr)
		if reason == "" {
			reason = fmt.Sprintf("validator exited with code %d", code)
		}
		return e.finishUnit(run, unit, agent, metrics, "failed", reason, round, nil, completed, lastFinished)
	}
	if code == 3 && round >= weakAfter(d) {
		return e.finishUnit(run, unit, agent, metrics, "weak", strings.TrimSpace(stdout), round, nil, completed, lastFinished)
	}
	if round >= configuredRounds(d) || d.Prompt.Followup == "" {
		result := "failed"
		if code == 1 && round >= weakAfter(d) {
			result = "weak"
		}
		return e.finishUnit(run, unit, agent, metrics, result, strings.TrimSpace(stdout), round, nil, completed, lastFinished)
	}
	return e.sendFollowup(ctx, run, d, unit, agent, metrics, round+1, truncateUTF8(stdout, 8*1024), output, completed, lastFinished)
}

func (e *Engine) sendFollowup(ctx context.Context, run Run, d Definition, unit Unit, agent string, metrics unitMetrics, round int, problems, output string, completed *atomic.Int64, lastFinished *time.Time) error {
	prompt, controls, err := renderPrompt(e.Store.SnapshotDir(run.ID), d, unit, run, round, problems, output, metrics.started.Format(time.RFC3339Nano))
	if err != nil {
		return e.finishUnit(run, unit, agent, metrics, "failed", "render follow-up: "+err.Error(), round-1, nil, completed, lastFinished)
	}
	metrics.controls += controls
	if err := e.appendEvent(run.ID, Event{Unit: unit.Key, Key: unit.Key, Agent: agent, State: "sending", Round: round, ControlReplaced: controls, LastObservation: metrics.last}); err != nil {
		return err
	}
	return e.sendAndWork(ctx, run, d, unit, agent, metrics, round, prompt, output, completed, lastFinished)
}

func (e *Engine) runValidator(ctx context.Context, run Run, d Definition, unit Unit, output, started string, round int) (int, string, string, error) {
	if e.Validator != nil {
		return e.Validator(ctx, run.Workdir, d.Validate.Command, duration(d.Validate.Timeout, 5*time.Minute))
	}
	if len(d.Validate.Command) == 0 {
		return 0, "", "", nil
	}
	data := TemplateData{Unit: unit.Data, Key: unit.Key, Round: round, Output: output, Started: started, Workdir: run.Workdir, Run: run.ID}
	args := make([]string, len(d.Validate.Command))
	for index, raw := range d.Validate.Command {
		value, err := RenderText(fmt.Sprintf("validate.command[%d]", index), raw, data)
		if err != nil {
			return -1, "", "", err
		}
		args[index] = value
	}
	for index := 1; index < len(args); index++ {
		if filepath.IsAbs(args[index]) || strings.HasPrefix(args[index], "-") || strings.Contains(args[index], "{{") {
			continue
		}
		if !strings.Contains(filepath.ToSlash(args[index]), "/") {
			continue
		}
		candidate, err := containedFile(e.Store.SnapshotDir(run.ID), args[index])
		if err == nil {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				args[index] = candidate
			}
		}
	}
	timeout := duration(d.Validate.Timeout, 5*time.Minute)
	validateCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stdout, stderr limitedBuffer
	cmd := exec.CommandContext(validateCtx, args[0], args[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = run.Workdir, nil, &stdout, &stderr
	err := cmd.Run()
	if errors.Is(validateCtx.Err(), context.DeadlineExceeded) {
		return -1, stdout.String(), stderr.String(), fmt.Errorf("%w after %s", ErrValidatorTimeout, timeout)
	}
	if err == nil {
		return 0, stdout.String(), stderr.String(), nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), stdout.String(), stderr.String(), nil
	}
	return -1, stdout.String(), stderr.String(), err
}

type limitedBuffer struct{ strings.Builder }

func (b *limitedBuffer) Write(value []byte) (int, error) {
	written := len(value)
	remaining := 8*1024 - b.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = b.Builder.Write(value)
	}
	return written, nil
}

func configuredRounds(d Definition) int {
	if d.Validate.Rounds > 0 {
		return d.Validate.Rounds
	}
	return 1
}
func weakAfter(d Definition) int {
	if d.Validate.WeakAfter > 0 {
		return d.Validate.WeakAfter
	}
	return configuredRounds(d) + 1
}
func duration(value string, fallback time.Duration) time.Duration {
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func validateOutput(d Definition, workdir, output, started string) error {
	if output == "" {
		return nil
	}
	path, err := workflowOutputPath(workdir, output)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("output missing: %w", err)
	}
	if d.Output.RequireFresh {
		start, parseErr := time.Parse(time.RFC3339Nano, started)
		if parseErr == nil && !info.ModTime().After(start) {
			return errors.New("output is not fresh (modified before unit start)")
		}
	}
	format := d.Output.Format
	if format == "" {
		format = "any"
	}
	if format != "json" {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("output is not valid JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("output JSON has trailing data")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("output JSON has trailing data: %w", err)
	}
	return nil
}

func workflowOutputPath(workdir, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", errors.New("output.path must resolve to a relative file path")
	}
	path, err := containedFile(workdir, filepath.FromSlash(relative))
	if err != nil {
		return "", fmt.Errorf("output.path escapes or leaves the workdir: %q: %w", relative, err)
	}
	return path, nil
}

func renderOutput(d Definition, unit Unit, workdir, runID, started string) (string, error) {
	if d.Output.Path == "" {
		return "", nil
	}
	data := TemplateData{Unit: unit.Data, Key: unit.Key, Round: 1, Workdir: workdir, Run: runID, Started: started}
	rendered, err := RenderText("output.path", d.Output.Path, data)
	if err != nil {
		return "", err
	}
	path, err := workflowOutputPath(workdir, rendered)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(workdir, path)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

func renderPrompt(snapshot string, d Definition, unit Unit, run Run, round int, problems, output, started string) (string, int, error) {
	path := d.Prompt.Template
	if round > 1 {
		path = d.Prompt.Followup
	}
	data := TemplateData{Unit: unit.Data, Key: unit.Key, Round: round, Problems: problems, Output: output, Started: started, Workdir: run.Workdir, Run: run.ID}
	text, err := RenderFile(snapshot, path, data)
	if err != nil {
		return "", 0, err
	}
	var result strings.Builder
	count := 0
	for _, char := range text {
		if unicode.IsControl(char) && char != '\n' && char != '\t' {
			result.WriteRune('\uFFFD')
			count++
		} else {
			result.WriteRune(char)
		}
	}
	return result.String(), count, nil
}

func (e *Engine) waitIdle(ctx context.Context, runID, agent string, timeout time.Duration) (Observation, error) {
	return e.waitIdleMode(ctx, runID, agent, timeout, true)
}

func (e *Engine) waitIdleInFlight(ctx context.Context, runID, agent string, timeout time.Duration) (Observation, error) {
	return e.waitIdleMode(ctx, runID, agent, timeout, false)
}

func (e *Engine) waitIdleMode(ctx context.Context, runID, agent string, timeout time.Duration, stopAtBoundary bool) (Observation, error) {
	var deadline time.Time
	if timeout > 0 {
		deadline = e.now().Add(timeout)
	}
	idleCount, blockedCount := 0, 0
	var last Observation
	for {
		if err := ctx.Err(); err != nil {
			return last, err
		}
		if stop, err := e.stopRequested(runID); err != nil {
			return last, err
		} else if stopAtBoundary && stop && idleCount == 0 {
			return last, errStopBoundary
		}
		observation, err := e.observe(ctx, agent)
		if err != nil {
			return last, err
		}
		last = observation
		switch observation.State {
		case Idle:
			idleCount++
			blockedCount = 0
			if idleCount >= 2 {
				if err := e.clearWaiting(runID, agent); err != nil {
					return last, err
				}
				return observation, nil
			}
		case Blocked:
			idleCount = 0
			blockedCount++
			if blockedCount > 2 {
				if err := e.statusWaiting(runID, agent); err != nil {
					return last, err
				}
			}
		case Working:
			idleCount, blockedCount = 0, 0
		case Dead:
			return last, fmt.Errorf("agent %s is dead", agent)
		case Unknown:
			idleCount, blockedCount = 0, 0
			if err := e.statusWaitingState(runID, agent, "unknown"); err != nil {
				return last, err
			}
		default:
			return last, fmt.Errorf("agent %s has unrecognized state %q", agent, observation.State)
		}
		if !deadline.IsZero() && !e.now().Before(deadline) {
			return last, fmt.Errorf("timed out waiting for agent %s to idle", agent)
		}
		if err := e.sleep(ctx, e.interval()); err != nil {
			return last, err
		}
	}
}

func (e *Engine) clearWaiting(runID, agent string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	run, err := e.Store.UpdateRun(runID, func(current *Run) error {
		if strings.HasPrefix(current.Status, "waiting: "+agent+" ") {
			current.Status, current.LastError = "running", ""
		}
		return nil
	})
	if err != nil {
		return err
	}
	if e.active != nil && e.active.ID == runID {
		e.active = &run
	}
	return nil
}

func (e *Engine) waitForStart(ctx context.Context, agent string, deliveredAt time.Time, timeout string) (time.Time, Observation, error) {
	deadline := e.now().Add(duration(timeout, 3*time.Minute))
	blockedCount := 0
	var last Observation
	for {
		if err := ctx.Err(); err != nil {
			return time.Time{}, last, err
		}
		observation, err := e.observe(ctx, agent)
		if err != nil {
			return time.Time{}, last, err
		}
		last = observation
		switch observation.State {
		case Dead:
			return time.Time{}, last, fmt.Errorf("agent %s is dead", agent)
		case Unknown:
			blockedCount = 0
			if err := e.statusWaitingForAgentState(agent, "unknown"); err != nil {
				return time.Time{}, last, err
			}
		case Idle:
			// A fast turn can begin and finish between polls. A newer decisive
			// transcript event proves that the delivered prompt ran even if no
			// Working observation was sampled.
			if observation.ObservedAt.After(deliveredAt) && observation.TurnAt.After(deliveredAt) {
				return observation.TurnAt, observation, nil
			}
		case Working:
			turnIsNew := observation.TurnAt.IsZero() || observation.TurnAt.After(deliveredAt)
			if observation.ObservedAt.After(deliveredAt) && turnIsNew {
				if observation.TurnAt.After(deliveredAt) {
					return observation.TurnAt, observation, nil
				}
				return observation.ObservedAt, observation, nil
			}
		case Blocked:
			blockedCount++
			if blockedCount > 2 {
				if err := e.statusWaitingForAgent(agent); err != nil {
					return time.Time{}, last, err
				}
			}
		default:
			blockedCount = 0
		}
		if !e.now().Before(deadline) {
			return time.Time{}, last, nil
		}
		if err := e.sleep(ctx, e.interval()); err != nil {
			return time.Time{}, last, err
		}
	}
}

func (e *Engine) waitUnitIdle(ctx context.Context, runID, agent string, timeout string) (bool, float64, Observation, error) {
	started := e.now()
	deadline := started.Add(duration(timeout, 40*time.Minute))
	idleCount, blockedCount := 0, 0
	var last Observation
	for {
		if err := ctx.Err(); err != nil {
			return false, e.now().Sub(started).Seconds(), last, err
		}
		observation, err := e.observe(ctx, agent)
		if err != nil {
			return false, e.now().Sub(started).Seconds(), last, err
		}
		last = observation
		switch observation.State {
		case Idle:
			idleCount++
			blockedCount = 0
			if idleCount >= 2 {
				if err := e.clearWaiting(runID, agent); err != nil {
					return false, 0, last, err
				}
				return true, e.now().Sub(started).Seconds(), observation, nil
			}
		case Blocked:
			idleCount = 0
			blockedCount++
			if blockedCount > 2 {
				if err := e.statusWaiting(runID, agent); err != nil {
					return false, 0, last, err
				}
			}
		case Working:
			idleCount, blockedCount = 0, 0
		case Dead:
			return false, e.now().Sub(started).Seconds(), last, fmt.Errorf("agent %s is dead", agent)
		case Unknown:
			idleCount, blockedCount = 0, 0
			if err := e.statusWaitingState(runID, agent, "unknown"); err != nil {
				return false, 0, last, err
			}
		}
		if !e.now().Before(deadline) {
			return false, e.now().Sub(started).Seconds(), last, nil
		}
		if err := e.sleep(ctx, e.interval()); err != nil {
			return false, e.now().Sub(started).Seconds(), last, err
		}
	}
}

func (e *Engine) observe(ctx context.Context, agent string) (Observation, error) {
	observation, err := e.Driver.Observe(ctx, agent)
	if err != nil {
		return observation, err
	}
	if observation.ObservedAt.IsZero() {
		observation.ObservedAt = e.now()
	}
	return observation, nil
}

func (e *Engine) compactIfNeeded(ctx context.Context, runID, agent string, d Definition, processed int, metrics *unitMetrics) error {
	contextTrigger := d.Compact.ContextAbove > 0 && metrics.last.ContextKnown && metrics.last.ContextTokens >= d.Compact.ContextAbove
	countTrigger := d.Compact.EveryUnits > 0 && processed > 0 && processed%d.Compact.EveryUnits == 0
	if !contextTrigger && !countTrigger {
		return nil
	}
	if metrics.last.ContextKnown {
		value := metrics.last.ContextTokens
		metrics.compactBefore = &value
	}
	started := e.now()
	queued := false
	for {
		err := e.Driver.Compact(ctx, agent)
		if errors.Is(err, ErrCompactBusy) {
			if _, waitErr := e.waitIdle(ctx, runID, agent, 0); waitErr != nil {
				return waitErr
			}
			continue
		}
		if errors.Is(err, ErrCompactQueued) {
			queued = true
			if appendErr := e.Store.AppendEvent(runID, Event{Agent: agent, State: "compact_pending", Reason: err.Error(), Compact: &CompactRecord{Result: "pending"}}); appendErr != nil {
				return appendErr
			}
			break
		}
		if err != nil {
			return err
		}
		break
	}
	if _, err := e.waitIdle(ctx, runID, agent, 0); err != nil {
		return err
	}
	after, err := e.observe(ctx, agent)
	if err != nil {
		return err
	}
	metrics.last, metrics.compact = after, e.now().Sub(started).Seconds()
	if after.ContextKnown {
		value := after.ContextTokens
		metrics.compactAfter = &value
	}
	result := "effective"
	if queued {
		result = "pending"
	} else if metrics.compactBefore != nil && after.ContextKnown && after.ContextTokens > int(float64(*metrics.compactBefore)*0.8) {
		result = "ineffective"
	}
	metrics.compactResult = result
	resetAgentUnits := func() error {
		_, err := e.Store.UpdateRun(runID, func(run *Run) error {
			if run.AgentUnits == nil {
				run.AgentUnits = make(map[string]int)
			}
			run.AgentUnits[agent] = 0
			return nil
		})
		return err
	}
	if queued {
		return resetAgentUnits()
	}
	if err := e.Store.AppendEvent(runID, Event{Agent: agent, State: "compact_" + result, Compact: &CompactRecord{Seconds: metrics.compact, Before: metrics.compactBefore, After: metrics.compactAfter, Result: result}}); err != nil {
		return err
	}
	return resetAgentUnits()
}

func (e *Engine) finishUnit(run Run, unit Unit, agent string, metrics unitMetrics, result, reason string, round int, delivery *Delivery, completed *atomic.Int64, lastFinished *time.Time) error {
	finished := e.now()
	channel, deliveryStatus := metrics.channel, ""
	if channel != "" {
		deliveryStatus = string(DeliveryDelivered)
	}
	queuedAt, deliveredAt := metrics.queuedAt, metrics.deliveredAt
	if delivery != nil {
		if delivery.ChannelID != "" {
			channel = delivery.ChannelID
		}
		if delivery.Status != "" {
			deliveryStatus = string(delivery.Status)
		}
		if queuedAt.IsZero() {
			queuedAt = delivery.QueuedAt
		}
		if deliveredAt.IsZero() {
			deliveredAt = delivery.DeliveredAt
		}
	}
	event := Event{At: finished, Unit: unit.Key, Key: unit.Key, Agent: agent, State: result, Round: max(1, round), Reason: reason, DeliveryID: channel, DeliveryStatus: deliveryStatus, ControlReplaced: metrics.controls, LastObservation: metrics.last}
	if !queuedAt.IsZero() {
		event.QueuedAt = timePointer(queuedAt)
	}
	if !deliveredAt.IsZero() {
		event.DeliveredAt = timePointer(deliveredAt)
	}
	if metrics.compact > 0 || metrics.compactBefore != nil || metrics.compactAfter != nil {
		event.Compact = &CompactRecord{Seconds: metrics.compact, Before: metrics.compactBefore, After: metrics.compactAfter, Result: metrics.compactResult}
	}
	if err := e.Store.AppendEvent(run.ID, event); err != nil {
		return err
	}
	row := TimeRow{Run: run.ID, Workflow: run.Workflow, Unit: unit.Key, Agent: agent, Model: metrics.model, Result: result, Reason: reason, Rounds: max(1, round), RoundSeconds: metrics.roundWork, WorkSeconds: metrics.work, ValidateSeconds: metrics.validate, WaitSeconds: metrics.wait, CompactSeconds: metrics.compact, CompactResult: metrics.compactResult, FinishedAt: finished, CtxStart: metrics.ctxStart}
	if !queuedAt.IsZero() {
		row.QueuedAt = timePointer(queuedAt)
	}
	if !deliveredAt.IsZero() {
		row.DeliveredAt = timePointer(deliveredAt)
	}
	if !metrics.startedAt.IsZero() {
		row.StartedAt = timePointer(metrics.startedAt)
	}
	if metrics.last.ContextKnown {
		value := metrics.last.ContextTokens
		row.CtxEnd = &value
	}
	if metrics.compactBefore != nil {
		row.CompactCtxBefore, row.CtxBefore = metrics.compactBefore, metrics.compactBefore
	}
	if metrics.compactAfter != nil {
		row.CompactCtxAfter, row.CtxAfter = metrics.compactAfter, metrics.compactAfter
	}
	if !lastFinished.IsZero() && !queuedAt.IsZero() {
		gap := queuedAt.Sub(*lastFinished).Seconds()
		if gap < 0 {
			gap = 0
		}
		row.IdleGapSeconds = &gap
	}
	if err := e.Store.AppendTime(run.ID, row); err != nil {
		return err
	}
	if _, err := e.Store.UpdateRun(run.ID, func(current *Run) error {
		if current.AgentUnits == nil {
			current.AgentUnits = make(map[string]int)
		}
		current.AgentUnits[agent]++
		return nil
	}); err != nil {
		return err
	}
	*lastFinished = finished
	completed.Add(1)
	return e.setCurrent(run.ID, agent, "")
}

func (e *Engine) appendEvent(id string, event Event) error { return e.Store.AppendEvent(id, event) }

func (e *Engine) firstSend(unitKey, runID string) (time.Time, error) {
	events, _, err := e.Store.Events(runID)
	if err != nil {
		return time.Time{}, err
	}
	for _, event := range events {
		if event.Key == unitKey && event.State == "sending" {
			return event.At, nil
		}
	}
	return time.Time{}, nil
}

func (e *Engine) firstWorking(unitKey, runID string) (time.Time, error) {
	events, _, err := e.Store.Events(runID)
	if err != nil {
		return time.Time{}, err
	}
	for _, event := range events {
		if event.Key == unitKey && event.State == "working" {
			return event.At, nil
		}
	}
	return time.Time{}, nil
}

func (e *Engine) setCurrent(runID, agent, unit string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, err := e.Store.UpdateRun(runID, func(run *Run) error {
		if run.Current == nil {
			run.Current = make(map[string]string)
		}
		if unit == "" {
			delete(run.Current, agent)
		} else {
			run.Current[agent] = unit
		}
		return nil
	})
	return err
}

func (e *Engine) setStatus(status, reason string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active == nil {
		return errors.New("workflow run is not active")
	}
	run, err := e.Store.UpdateRun(e.active.ID, func(current *Run) error {
		if status == "running" && (current.StopRequested || current.Status == "stopping") {
			return nil
		}
		current.Status, current.LastError = status, reason
		return nil
	})
	if err != nil {
		return err
	}
	e.active = &run
	return nil
}

func (e *Engine) stopRequested(runID string) (bool, error) {
	run, err := e.Store.LoadRun(runID)
	return run.StopRequested || run.Status == "stopping", err
}

func (e *Engine) statusWaiting(runID, agent string) error {
	return e.statusWaitingState(runID, agent, "blocked")
}

func (e *Engine) statusWaitingState(runID, agent, state string) error {
	return e.setRunStatus(runID, "waiting: "+agent+" "+state, "")
}

func (e *Engine) statusWaitingForAgent(agent string) error {
	return e.statusWaitingForAgentState(agent, "blocked")
}

func (e *Engine) statusWaitingForAgentState(agent, state string) error {
	e.mu.Lock()
	id := ""
	if e.active != nil {
		id = e.active.ID
	}
	e.mu.Unlock()
	if id == "" {
		return nil
	}
	return e.statusWaitingState(id, agent, state)
}

func (e *Engine) setRunStatus(id, status, reason string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	run, err := e.Store.UpdateRun(id, func(current *Run) error {
		if current.Status != status || current.LastError != reason {
			current.Status, current.LastError = status, reason
		}
		return nil
	})
	if err != nil {
		return err
	}
	if e.active != nil && e.active.ID == id {
		copyRun := run
		e.active = &copyRun
	}
	return nil
}

func agentUnitCounts(events []Event) map[string]int {
	counts := make(map[string]int)
	for _, event := range events {
		if strings.HasPrefix(event.State, "compact_") {
			counts[event.Agent] = 0
		} else if terminalState(event.State) && event.Agent != "" {
			counts[event.Agent]++
		}
	}
	return counts
}

func latestEvents(events []Event) map[string]Event {
	latest := make(map[string]Event)
	for _, event := range events {
		key := event.Key
		if key == "" {
			key = event.Unit
		}
		if key != "" {
			event.Key = key
			latest[key] = event
		}
	}
	return latest
}

func terminalState(state string) bool { return state == "done" || state == "weak" || state == "failed" }
func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
func observationSummary(value Observation) string {
	if value.Detail == "" {
		return string(value.State)
	}
	return string(value.State) + ": " + value.Detail
}
func timePointer(value time.Time) *time.Time { return &value }
