package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"blueprint/internal/book"
	bptmux "blueprint/internal/tmux"
)

const (
	// barRenderInterval keeps pushed status text fresh without running #() jobs on redraw.
	barRenderInterval     = 2 * time.Second
	barAgentRenderTimeout = time.Second
)

type renderedBar struct {
	name, bar, style          string
	hasName, hasBar, hasStyle bool
}

type barRenderOutput struct {
	name, bar, style string
}

// barAgentRender renders one agent from the scan's shared snapshot. process is
// the session's pane process from the scan listing, zero when it had none.
type barAgentRender func(context.Context, string, bptmux.PaneProcess, book.Fleet, []string) (barRenderOutput, error)

type barRenderer struct {
	app           *app
	tmux          *bptmux.Client
	logger        *log.Logger
	loadFleet     func() (book.Fleet, error)
	render        barAgentRender
	last          map[string]renderedBar
	sessionIDs    map[string]string
	failures      map[string]string
	scanFailure   string
	models        *barModelCache
	renderTimeout time.Duration
	mu            sync.Mutex
}

func newBarRenderer(a *app, logger *log.Logger) *barRenderer {
	r := &barRenderer{
		app: a, tmux: a.tmux, logger: logger,
		loadFleet: func() (book.Fleet, error) { return book.LoadFleet(book.Paths(a.config.Agentbooks)) },
		last:      make(map[string]renderedBar), sessionIDs: make(map[string]string), failures: make(map[string]string),
		models: newBarModelCache(), renderTimeout: barAgentRenderTimeout,
	}
	r.render = r.renderAgent
	return r
}

func (r *barRenderer) run(ctx context.Context) {
	defer r.clear()
	ticker := time.NewTicker(barRenderInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := r.scan(ctx); err != nil {
			r.reportScan(err)
		} else {
			r.reportScan(nil)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *barRenderer) scan(ctx context.Context) error {
	sessions, err := r.tmux.SessionsWithAttachments(ctx)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	fleet, err := r.loadFleet()
	if err != nil {
		return fmt.Errorf("load agentbook: %w", err)
	}
	r.forgetReplacedSessions(sessions)
	allSessions := make([]string, 0, len(sessions))
	var targets []string
	processes := make(map[string]bptmux.PaneProcess)
	for _, session := range sessions {
		allSessions = append(allSessions, session.Name)
		agent, ok := fleet.Agents[session.Name]
		if ok && agent.Status == "open" && agent.ArchivedAt == "" && session.Attached > 0 {
			targets = append(targets, session.Name)
			processes[session.Name] = session.Process
		}
	}

	type result struct {
		name   string
		output barRenderOutput
		err    error
	}
	results := make(chan result, len(targets))
	cancels := make(map[string]context.CancelFunc, len(targets))
	for _, name := range targets {
		name, process := name, processes[name]
		renderCtx, cancel := context.WithTimeout(ctx, r.renderTimeout)
		cancels[name] = cancel
		go func() {
			output, err := r.render(renderCtx, name, process, fleet, allSessions)
			results <- result{name: name, output: output, err: err}
		}()
	}
	if len(targets) == 0 {
		return nil
	}
	timer := time.NewTimer(r.renderTimeout)
	defer timer.Stop()
	for len(cancels) > 0 {
		var result result
		select {
		case result = <-results:
			cancel, pending := cancels[result.name]
			if !pending {
				continue
			}
			cancel()
			delete(cancels, result.name)
		case <-timer.C:
			for name, cancel := range cancels {
				cancel()
				r.reportAgent(name, context.DeadlineExceeded)
				delete(cancels, name)
			}
			continue
		case <-ctx.Done():
			for name, cancel := range cancels {
				cancel()
				delete(cancels, name)
			}
			return nil
		}
		if result.err != nil {
			r.reportAgent(result.name, result.err)
			continue
		}
		var errs []error
		if err := r.set(ctx, result.name, "@bp-name", escapeTmuxBarFormat(result.output.name), "name"); err != nil {
			errs = append(errs, err)
		}
		if err := r.set(ctx, result.name, "@bp-bar", escapeTmuxBarFormat(result.output.bar), "bar"); err != nil {
			errs = append(errs, err)
		}
		if err := r.set(ctx, result.name, "status-style", result.output.style, "style"); err != nil {
			errs = append(errs, err)
		}
		if len(errs) > 0 {
			r.reportAgent(result.name, errorsJoin(errs))
		} else {
			r.reportAgent(result.name, nil)
		}
	}
	return nil
}

// forgetReplacedSessions drops what the renderer remembers about sessions that
// are gone or were recreated under the same name. The skip-unchanged check
// compares against that memory, so a stale entry would leave a new session
// without @bp-bar and silently back on the #() fallback.
func (r *barRenderer) forgetReplacedSessions(sessions []bptmux.SessionAttachment) {
	current := make(map[string]string, len(sessions))
	for _, session := range sessions {
		current[session.Name] = session.ID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range r.last {
		id, present := current[name]
		if !present || id != r.sessionIDs[name] {
			delete(r.last, name)
		}
	}
	for name := range r.sessionIDs {
		if _, present := current[name]; !present {
			delete(r.sessionIDs, name)
		}
	}
	for name, id := range current {
		r.sessionIDs[name] = id
	}
}

func errorsJoin(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		parts = append(parts, err.Error())
	}
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}

func (r *barRenderer) set(ctx context.Context, session, option, value, field string) error {
	r.mu.Lock()
	last := r.last[session]
	var previous string
	var hasPrevious bool
	switch field {
	case "name":
		previous, hasPrevious = last.name, last.hasName
	case "bar":
		previous, hasPrevious = last.bar, last.hasBar
	case "style":
		previous, hasPrevious = last.style, last.hasStyle
	}
	r.mu.Unlock()
	if hasPrevious && previous == value {
		return nil
	}
	if err := r.tmux.SetOption(ctx, "="+session+":", option, value); err != nil {
		return fmt.Errorf("set %s for %s: %w", option, session, err)
	}
	r.mu.Lock()
	last = r.last[session]
	switch field {
	case "name":
		last.name, last.hasName = value, true
	case "bar":
		last.bar, last.hasBar = value, true
	case "style":
		last.style, last.hasStyle = value, true
	}
	r.last[session] = last
	r.mu.Unlock()
	return nil
}

func (r *barRenderer) renderAgent(ctx context.Context, name string, process bptmux.PaneProcess, fleet book.Fleet, sessions []string) (barRenderOutput, error) {
	agent, ok := fleet.Agents[name]
	if !ok || agent.Status != "open" || agent.ArchivedAt != "" {
		return barRenderOutput{}, fmt.Errorf("agent registration is no longer open")
	}
	// The scan listing supplies the process and one capture serves the
	// runtime state, the title and the accent; each extra tmux call is paid
	// for every attached agent every scan.
	if process.PID == 0 {
		var err error
		if process, err = r.tmux.PaneProcess(ctx, name); err != nil {
			return barRenderOutput{}, fmt.Errorf("read pane process: %w", err)
		}
	}
	pane, paneErr := r.tmux.CaptureAnsi(ctx, name)
	state := book.RuntimeForObserved(ctx, r.tmux, fleet, name, sessions, process, pane, paneErr)
	workerApp := *r.app
	workerApp.ctx = ctx
	folder := workerApp.barFolderWithFleet(name, fleet)
	if folder == "" {
		return barRenderOutput{}, fmt.Errorf("agent folder unavailable")
	}
	label := workerApp.liveNameWithState(agent, &state, fleet, process)
	accent := workerApp.barAccentFrom(name, label, &fleet, func() (string, error) { return pane, paneErr })
	line := workerApp.barLineForScan(name, folder, process, state, r.models)
	return barRenderOutput{name: barNamePlate(label, accent), bar: line, style: barStatusStyle(accent)}, nil
}

func (r *barRenderer) clear() {
	r.mu.Lock()
	last := make(map[string]renderedBar, len(r.last))
	for name, state := range r.last {
		last[name] = state
	}
	r.mu.Unlock()
	for session, state := range last {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if state.hasName {
			if err := r.tmux.UnsetOption(ctx, "="+session+":", "@bp-name"); err != nil && r.logger != nil {
				r.logger.Printf("bar render cleanup %s @bp-name: %v", session, err)
			}
		}
		if state.hasBar {
			if err := r.tmux.UnsetOption(ctx, "="+session+":", "@bp-bar"); err != nil && r.logger != nil {
				r.logger.Printf("bar render cleanup %s @bp-bar: %v", session, err)
			}
		}
		cancel()
	}
}

func (r *barRenderer) reportAgent(name string, err error) {
	state := ""
	if err != nil {
		state = err.Error()
	}
	r.mu.Lock()
	previous, hadPrevious := r.failures[name]
	if state == previous {
		r.mu.Unlock()
		return
	}
	if state == "" {
		delete(r.failures, name)
	} else {
		r.failures[name] = state
	}
	r.mu.Unlock()
	if r.logger == nil {
		return
	}
	if state == "" && hadPrevious {
		r.logger.Printf("bar render %s recovered", name)
	} else if state != "" {
		r.logger.Printf("bar render %s: %s", name, state)
	}
}

func (r *barRenderer) reportScan(err error) {
	state := ""
	if err != nil {
		state = err.Error()
	}
	r.mu.Lock()
	previous := r.scanFailure
	if state != previous {
		r.scanFailure = state
	}
	r.mu.Unlock()
	if r.logger == nil || state == previous {
		return
	}
	if state == "" {
		r.logger.Printf("bar render scan recovered")
	} else {
		r.logger.Printf("bar render scan: %s", state)
	}
}

func escapeTmuxBarFormat(line string) string {
	var escaped strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] != '#' {
			escaped.WriteByte(line[i])
			continue
		}
		if i+1 < len(line) && line[i+1] == '[' {
			if end := strings.IndexByte(line[i+2:], ']'); end >= 0 {
				end += i + 2
				directive := line[i+2 : end]
				if validBarStyleDirective(directive) {
					escaped.WriteString(line[i : end+1])
					i = end
					continue
				}
			}
		}
		escaped.WriteString("##")
	}
	return escaped.String()
}

func validBarStyleDirective(value string) bool {
	if value == "default" {
		return true
	}
	if !strings.HasPrefix(value, "bg=") && !strings.HasPrefix(value, "fg=") {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("=,_-", char)) {
			return false
		}
	}
	return true
}
