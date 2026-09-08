package book

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"blueprint/internal/cache"
	bptmux "blueprint/internal/tmux"
	"context"
	"fmt"
)

func localRuntime(binding *cache.LocalBinding, pid int, a *cache.Activity) cache.State {
	state := cache.State{LastHumanAge: -1, Runtime: binding.Harness}
	var o cache.LocalObservation
	var err error
	if binding.Harness == "codex" {
		o, err = localCodexObservation(binding, pid)
	} else {
		o, err = cache.ReadLocalObservation(binding, pid)
	}
	if err != nil {
		a.Reason = err.Error()
		return state
	}
	state.Model = o.Model
	a.ThreadID, a.TranscriptPath, a.Binding = o.SessionID, o.TranscriptPath, "local-launch-observer"
	base := filepath.Base(o.TranscriptPath)
	if binding.Harness == "claude" {
		if base != o.SessionID+".jsonl" {
			a.Reason = "Claude observation transcript/session mismatch"
			return state
		}
		state = cache.ReadClaudePath(o.TranscriptPath)
		state.Runtime = "claude"
		phase, stamp, err := readTurnPhase(o.TranscriptPath)
		if err == nil && !stamp.IsZero() && (phase == turnOpenVerdict || phase == turnClosedVerdict) {
			localPhase(a, phase == turnOpenVerdict, stamp)
		} else {
			a.Reason = "waiting for decisive Claude turn event"
		}
		// The status-line input supplies the selected model even before the
		// first response and the actual context window (no model-size table).
		if o.Model != "" && (state.Model == "" || state.UsageAt.IsZero() || !o.ObservedAt.Before(state.UsageAt)) {
			if state.Model != o.Model {
				state.Effort = ""
			}
			state.Model = o.Model
		}
		if o.Window > 0 {
			state.Window = o.Window
		}
		if o.Context != nil && *o.Context >= 0 && (state.UsageAt.IsZero() || o.ObservedAt.After(state.UsageAt)) {
			state.CtxTokens, state.Known = *o.Context, true
			state.UsageAt, state.Age = o.ObservedAt, time.Since(o.ObservedAt)
		}
	} else if binding.Harness == "codex" {
		if !strings.HasSuffix(base, "-"+o.SessionID+".jsonl") || !localCodexMeta(o) {
			a.Reason = "Codex observation transcript/session/cwd mismatch or subagent"
			return state
		}
		state = cache.ReadCodexPath(o.TranscriptPath)
		state.Runtime = "codex"
		if o.Model != "" && state.Model == "" {
			state.Model = o.Model
		}
		if state.TurnKnown && !state.TurnAt.IsZero() && completeTranscriptTail(o.TranscriptPath) {
			localPhase(a, state.Busy, state.TurnAt)
		} else {
			a.Reason = "waiting for decisive Codex turn event"
		}
	} else {
		a.Reason = "unsupported local observation harness"
	}
	return state
}

func localPhase(a *cache.Activity, busy bool, stamp time.Time) {
	a.LastEventAt = &stamp
	age := a.ObservedAt.Sub(stamp)
	if age < -time.Minute || (busy && age > turnOpenCeiling) {
		a.Reason = "local turn evidence stale"
		return
	}
	a.TurnBusy = &busy
	a.State, a.Source = "idle", "transcript"
	if busy {
		a.State = "working"
	}
}

func localCodexMeta(o cache.LocalObservation) bool {
	id, cwd, ok := localCodexHeader(o.TranscriptPath)
	return ok && id == o.SessionID && filepath.Clean(cwd) == filepath.Clean(o.CWD)
}

func localCodexHeader(path string) (string, string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	if !scanner.Scan() {
		return "", "", false
	}
	var r struct {
		Type    string `json:"type"`
		Payload struct {
			ID     string          `json:"id"`
			CWD    string          `json:"cwd"`
			Source json.RawMessage `json:"source"`
		} `json:"payload"`
	}
	if json.Unmarshal(scanner.Bytes(), &r) != nil {
		return "", "", false
	}
	var source string
	ok := r.Type == "session_meta" && filepath.IsAbs(r.Payload.CWD) && json.Unmarshal(r.Payload.Source, &source) == nil && source == "cli"
	return r.Payload.ID, r.Payload.CWD, ok
}

func localCodexObservation(binding *cache.LocalBinding, pid int) (cache.LocalObservation, error) {
	var found cache.LocalObservation
	if binding.PID != pid || pid <= 0 {
		return found, fmt.Errorf("local launch PID no longer matches pane")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ids, err := bptmux.CodexWriterIDs(ctx, pid, binding.Home)
	if err != nil {
		return found, err
	}
	for _, id := range ids {
		paths, err := filepath.Glob(filepath.Join(binding.Home, "sessions", "*", "*", "*", "*-"+id+".jsonl"))
		if err != nil || len(paths) != 1 {
			continue
		}
		actualID, cwd, ok := localCodexHeader(paths[0])
		if !ok || actualID != id {
			continue
		}
		// The held writer lock selects the thread. Its own metadata supplies
		// cwd, including --cd and a resume picker switching directories.
		o := cache.LocalObservation{SessionID: id, TranscriptPath: paths[0], CWD: cwd, ObservedAt: time.Now().UTC()}
		if found.SessionID != "" {
			return cache.LocalObservation{}, fmt.Errorf("multiple root Codex writers under this pane")
		}
		found = o
	}
	if found.SessionID == "" {
		return found, fmt.Errorf("waiting for local Codex writer lock and transcript")
	}
	return found, nil
}
