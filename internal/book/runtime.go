package book

import (
	"blueprint/internal/cache"
	"blueprint/internal/codexrpc"
	bptmux "blueprint/internal/tmux"
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RuntimeState is the single observation used by status, bar and delivery.
// The bool reports a recognized harness, not certainty about its activity.
func RuntimeState(ctx context.Context, client *bptmux.Client, agent Agent) (cache.State, bool) {
	state := cache.State{LastHumanAge: -1, Busy: true}
	a := &cache.Activity{State: "unknown", Source: "none", ObservedAt: time.Now().UTC(), DeliveryBlocked: true}
	state.Activity = a
	process, err := client.PaneProcess(ctx, agent.Name)
	if err != nil {
		a.Reason = "pane process unavailable"
		return state, false
	}
	pane, err := client.CaptureAnsi(ctx, agent.Name)
	if err != nil {
		a.Reason = "pane unreadable"
		return state, bptmux.IsAgentCommand(process.Command)
	}
	screen := bptmux.Busy(pane)
	a.ScreenBusy = &screen
	if agent.Local == nil && !bptmux.IsAgentPane(process.Command, pane) {
		switch process.Command {
		case "sh", "bash", "zsh", "fish", "dash":
			a.State, a.Source = "dead", "pane-process"
		default:
			a.Reason = "unrecognized pane process"
		}
		return state, false
	}
	if agent.Local != nil {
		state = localRuntime(agent.Local, process.PID, a)
	} else if bptmux.IsCodexCommand(process.Command) && (process.Command != "node" || bptmux.CodexPane(pane)) {
		state = codexRuntime(ctx, process.PID, agent, a)
	} else if process.Command == "claude" {
		id, bindErr := bptmux.ClaudeProcessSession(process.PID, FirstPath(agent.Folder))
		if bindErr != nil {
			state.Runtime, a.Reason = "claude", bindErr.Error()
		} else {
			state = claudeRuntimeBound(agent, a, id)
		}
	} else {
		state.Runtime = process.Command
		a.Reason = "harness has no structured activity probe"
	}
	// Positive screen evidence must not disappear behind missing/stale metrics.
	// An empty composer is never affirmative idle evidence.
	if screen {
		if state.Runtime == "codex-remote" && a.State == "unknown" {
			// A disconnected client can retain an old Working frame. It cannot
			// repair an explicitly unknown shared-server observation.
		} else if a.State == "idle" {
			a.State, a.Reason = "unknown", a.Source+" idle conflicts with working pane"
		} else {
			a.State, a.Source = "working", "pane"
		}
	}
	if bptmux.Dialog(pane) {
		a.State, a.Source, a.Reason = "blocked", "pane", "dialog or navigation/search input"
	}
	a.DeliveryBlocked = a.State != "idle" || bptmux.Typing(pane)
	state.Activity, state.Busy, state.RuntimeError = a, a.DeliveryBlocked, a.Reason
	return state, true
}

func codexRuntime(ctx context.Context, pid int, agent Agent, a *cache.Activity) cache.State {
	info := bptmux.CodexProcessInfo(pid)
	if needsCodexWriterBinding(info, agent) {
		// Fresh embedded CLI launches have no resume UUID in argv. Use the
		// kernel-held writer lock, as for local bp run sessions, not cwd/mtime.
		observed := *a
		state := localRuntime(&cache.LocalBinding{PID: pid, Home: info.Home, Harness: "codex"}, pid, &observed)
		if observed.ThreadID != "" {
			*a = observed
			a.Binding = "pane-writer-lock"
			return state
		}
	}
	return readCodexRuntime(ctx, info, agent, a)
}

func needsCodexWriterBinding(info bptmux.CodexProcess, agent Agent) bool {
	return info.Remote == "" && (agent.Launch == nil || agent.Launch.Remote == "")
}

func readCodexRuntime(ctx context.Context, info bptmux.CodexProcess, agent Agent, a *cache.Activity) cache.State {
	state := cache.State{LastHumanAge: -1, Runtime: "codex"}
	id, binding := "", "pane-argv"
	// No cwd/mtime selection, inherited daemon environment, or conflicting pins.
	for _, candidate := range []struct{ id, source string }{{agent.IdentityThreadID, "agentbook-pin"}, {launchThread(agent, true), "launch-thread"}} {
		if candidate.id == "" {
			continue
		}
		if id != "" && id != candidate.id {
			a.Reason = "conflicting thread bindings"
			return state
		}
		id, binding = candidate.id, candidate.source
	}
	if agent.Launch != nil && info.Remote == "" {
		info.Remote = agent.Launch.Remote
	}
	explicitRemote := info.Remote != ""
	if explicitRemote {
		state.Runtime = "codex-remote"
	}
	retiredArgv := ""
	if id == "" {
		id = info.ThreadID
	} else if info.ThreadID != "" && info.ThreadID != id {
		// A remote client can switch conversations without changing its launch
		// argv. Only an explicit operator pin can supersede that hint, and only
		// after the server confirms the old thread is no longer loaded below.
		if !explicitRemote || agent.IdentityThreadID == "" {
			a.Reason = "conflicting thread bindings"
			return state
		}
		retiredArgv = info.ThreadID
	}
	if !threadUUID.MatchString(id) {
		a.Reason = "no valid explicit thread binding"
		return state
	}
	a.ThreadID, a.Binding = id, binding
	path, ok := cache.CodexPath(info.Home, FirstPath(agent.Folder), id)
	if ok {
		state = cache.ReadCodexPath(path)
		state.Runtime = "codex"
		a.TranscriptPath = path
		if state.TurnKnown && !state.TurnAt.IsZero() {
			a.LastEventAt = &state.TurnAt
			if time.Since(state.TurnAt) >= -time.Minute && (!state.Busy || time.Since(state.TurnAt) <= 15*time.Minute) {
				busy := state.Busy
				a.TurnBusy = &busy
				a.State, a.Source = "idle", "transcript"
				if busy {
					a.State = "working"
				}
			} else {
				a.Reason = "turn evidence stale"
			}
		} else {
			a.Reason = "no decisive turn event"
		}
		if !completeTranscriptTail(path) {
			a.State, a.Reason = "unknown", "incomplete transcript tail"
			a.TurnBusy = nil
		}
	} else {
		a.Reason = "bound transcript unavailable or cwd mismatch"
	}
	if !explicitRemote {
		info.Remote = "unix://"
	}
	socket := bptmux.CodexSocket(info.Home, info.Remote)
	if _, err := os.Stat(socket); err != nil && !explicitRemote {
		return state
	}
	if explicitRemote {
		state.Runtime = "codex-remote"
		a.State = "unknown"
		a.TurnBusy = nil
	}
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rpc, err := codexrpc.DialUnix(probe, socket)
	if err != nil {
		if explicitRemote {
			a.Reason = "app-server unavailable"
		}
		return state
	}
	defer rpc.Close()
	if retiredArgv != "" {
		old, err := rpc.ThreadRead(probe, retiredArgv)
		if err != nil || old.ID != retiredArgv || old.Status.Type != "notLoaded" {
			a.Reason, a.Binding, a.ThreadID, a.TranscriptPath = "conflicting thread bindings: launch thread still loaded or unverified", "", "", ""
			return cache.State{LastHumanAge: -1, Runtime: "codex-remote"}
		}
	}
	thread, err := rpc.ThreadRead(probe, id)
	if err != nil || thread.ID != id || thread.CWD != FirstPath(agent.Folder) {
		if explicitRemote {
			a.Reason = "remote thread could not be verified"
		}
		return state
	}
	if thread.Status.Type == "notLoaded" && !explicitRemote {
		return state
	}
	// Remote threads can change cwd after creation (e.g. iOS onboarding).
	// The server's verified current cwd and exact rollout path supersede the
	// session_meta birth cwd; never choose a different file by its mtime.
	if remoteTranscript(info.Home, id, thread.Path) {
		state = cache.ReadCodexPath(thread.Path)
		a.TranscriptPath = thread.Path
		if !state.TurnAt.IsZero() {
			a.LastEventAt = &state.TurnAt
		}
	}
	a.Source, a.Reason = "app-server", ""
	switch thread.Status.Type {
	case "idle":
		a.State = "idle"
	case "active":
		a.State = "working"
		if len(thread.Status.ActiveFlags) > 0 {
			a.State, a.Reason = "unknown", "app-server active flags: "+strings.Join(thread.Status.ActiveFlags, ",")
		}
	default:
		a.State, a.Reason = "unknown", "app-server state: "+thread.Status.Type
	}
	if a.State != "unknown" {
		busy := a.State == "working"
		a.TurnBusy = &busy
	} else {
		a.TurnBusy = nil
	}
	state.Runtime, state.ThreadID = "codex-remote", id
	if thread.Model != "" {
		state.Model = thread.Model
	}
	if thread.ReasoningEffort != "" {
		state.Effort = thread.ReasoningEffort
	}
	return state
}

func remoteTranscript(home, id, path string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	rel, err := filepath.Rel(filepath.Join(home, "sessions"), path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && cache.CodexID(path) == id
}

func launchThread(agent Agent, codex bool) string {
	if agent.Launch != nil && agent.Launch.Codex == codex && !agent.Launch.Hermes {
		return agent.Launch.ResumeID
	}
	return ""
}

// RuntimeFor also rejects a thread registered to multiple fleet agents.
func RuntimeFor(ctx context.Context, client *bptmux.Client, fleet Fleet, name string) cache.State {
	agent, exists := fleet.Agents[name]
	if !exists {
		return cache.State{LastHumanAge: -1, Activity: &cache.Activity{State: "unknown", Source: "agentbook", Reason: "agent not registered", ObservedAt: time.Now().UTC(), DeliveryBlocked: true}}
	}
	agent.Name = name
	state, _ := RuntimeState(ctx, client, agent)
	if a := state.Activity; a != nil && a.ThreadID != "" {
		for other, entry := range fleet.Agents {
			codex := strings.HasPrefix(state.Runtime, "codex")
			samePin := codex && entry.IdentityThreadID == a.ThreadID
			sameLaunch := launchThread(entry, codex) == a.ThreadID
			if other != name && (samePin || sameLaunch) {
				return cache.State{LastHumanAge: -1, Runtime: state.Runtime, Busy: true, Activity: &cache.Activity{State: "unknown", Source: "agentbook", Reason: "thread registered to multiple agents", ObservedAt: a.ObservedAt, DeliveryBlocked: true, ScreenBusy: a.ScreenBusy}}
			}
		}
	}
	return state
}

// RuntimeBlockProbe preserves unknown as unknown in queue reasons. Force can
// override known working, never an unknown runtime or a modal user input.
func RuntimeBlockProbe(paths []string) func(string, bool) string {
	return func(name string, force bool) string {
		fleet, err := LoadFleet(Paths(paths))
		if err != nil {
			return "runtime unknown: agentbook unavailable"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		state := RuntimeFor(ctx, bptmux.New(), fleet, name)
		a := state.Activity
		if a == nil {
			return "runtime unknown: no observation"
		}
		if a.Reason == "" && a.ThreadID != "" && (a.State == "idle" || (force && a.State == "working" && state.Runtime != "hermes")) {
			return ""
		}
		reason := "runtime " + a.State + ": " + a.Reason
		if a.ThreadID == "" && len(fleet.Sources[name]) > 0 {
			reason += "; agentbooks: " + strings.Join(fleet.Sources[name], ", ") + "; inspect bp status --json (force cannot repair binding)"
		}
		return reason
	}
}

func claudeRuntime(agent Agent, a *cache.Activity) cache.State {
	return claudeRuntimeBound(agent, a, "")
}

func claudeRuntimeBound(agent Agent, a *cache.Activity, liveID string) cache.State {
	state := cache.State{LastHumanAge: -1, Runtime: "claude"}
	id, binding := launchThread(agent, false), "claude-session-name"
	if liveID != "" {
		id, binding = liveID, "claude-process-session"
	}
	var path string
	var err error
	if liveID != "" {
		path, err = bptmux.ClaudeSessionPathForID(bptmux.ClaudeProjectsRoot(), FirstPath(agent.Folder), liveID)
	} else {
		path, err = bptmux.ResolveSessionPath(bptmux.ClaudeProjectsRoot(), FirstPath(agent.Folder), agent.Name, id)
	}
	if err != nil {
		a.Reason = err.Error()
		return state
	}
	state = cache.ReadClaudePath(path)
	state.Runtime = "claude"
	a.ThreadID, a.TranscriptPath, a.Binding = strings.TrimSuffix(filepath.Base(path), ".jsonl"), path, binding
	phase, stamp, err := readTurnPhase(path)
	if !stamp.IsZero() {
		a.LastEventAt = &stamp
	}
	if err != nil || phase == turnNone || phase == turnUncertainVerdict || stamp.IsZero() {
		a.Reason = "no readable decisive turn event"
		return state
	}
	age := a.ObservedAt.Sub(stamp)
	if age < -time.Minute || (phase == turnOpenVerdict && age > turnOpenCeiling) {
		a.Reason = "turn evidence stale"
		return state
	}
	busy := phase == turnOpenVerdict
	a.TurnBusy = &busy
	a.State, a.Source = "idle", "transcript"
	if busy {
		a.State = "working"
	}
	return state
}

func completeTranscriptTail(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	s, err := f.Stat()
	if err != nil || s.Size() == 0 {
		return false
	}
	last := make([]byte, 1)
	_, err = f.ReadAt(last, s.Size()-1)
	return err == nil && last[0] == '\n'
}
