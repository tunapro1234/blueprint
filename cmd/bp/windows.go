package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"text/tabwriter"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/compositor"
	"blueprint/internal/config"
	bptmux "blueprint/internal/tmux"
	"blueprint/internal/windowmap"
)

const lastLookedFile = "windows-seen.json"

type attentionState struct {
	AwaitingUser bool
	Unread       bool
	RepliedAt    time.Time
}

type seenState struct {
	Agents map[string]time.Time `json:"agents"`
}

func (a *app) attention(states map[string]book.State) map[string]attentionState {
	seen := a.readLastLooked()
	result := make(map[string]attentionState, len(states))
	for name, state := range states {
		if state.Runtime == nil || state.Runtime.Activity == nil {
			continue
		}
		activity := state.Runtime.Activity
		awaiting, repliedAt := book.AwaitingUser(activity.TranscriptPath, state.Runtime.Runtime)
		row := attentionState{AwaitingUser: awaiting, RepliedAt: repliedAt}
		row.Unread = !repliedAt.IsZero() && repliedAt.After(seen[name])
		result[name] = row
	}
	return result
}

func (a *app) seenPath() string {
	if a.config.StateDir == "" {
		return ""
	}
	return filepath.Join(a.config.StateDir, lastLookedFile)
}

func (a *app) readLastLooked() map[string]time.Time {
	result := map[string]time.Time{}
	path := a.seenPath()
	if path == "" {
		return result
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return result
	}
	var state seenState
	if json.Unmarshal(data, &state) == nil && state.Agents != nil {
		return state.Agents
	}
	return result
}

func (a *app) markLooked(name string, at time.Time) error {
	path := a.seenPath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	agents := a.readLastLooked()
	agents[name] = at.UTC()
	// Bound this operational cache independently of the lifetime of old books.
	if len(agents) > 512 {
		type entry struct {
			name string
			at   time.Time
		}
		entries := make([]entry, 0, len(agents))
		for agent, stamp := range agents {
			entries = append(entries, entry{agent, stamp})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].at.After(entries[j].at) })
		agents = map[string]time.Time{}
		for _, item := range entries[:512] {
			agents[item.name] = item.at
		}
	}
	data, err := json.Marshal(seenState{Agents: agents})
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".windows-seen-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(append(data, '\n'))
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

type windowReport struct {
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`
	WindowID     string `json:"window_id"`
	Workspace    string `json:"workspace,omitempty"`
	PID          int    `json:"pid"`
	Class        string `json:"class,omitempty"`
	Focused      bool   `json:"focused"`
	Accent       string `json:"accent"`
	Activity     string `json:"activity"`
	AwaitingUser bool   `json:"awaiting_user"`
	Unread       bool   `json:"unread"`
}

func (a *app) compositor() (compositor.Adapter, error) {
	if a.detectCompositor != nil {
		return a.detectCompositor()
	}
	return compositor.Detect()
}

func (a *app) tmuxClients(ctx context.Context) ([]bptmux.AttachedClient, error) {
	if a.listTmuxClients != nil {
		return a.listTmuxClients(ctx)
	}
	if a.tmux == nil {
		return nil, errors.New("tmux client is unavailable")
	}
	return a.tmux.Clients(ctx)
}

func (a *app) discoverWindows(ctx context.Context, adapter compositor.Adapter) ([]windowReport, error) {
	fleet, states, err := a.fleet()
	if err != nil {
		return nil, err
	}
	windows, err := adapter.ListWindows(ctx)
	if err != nil {
		return nil, err
	}
	clients, err := a.tmuxClients(ctx)
	if err != nil {
		return nil, err
	}
	processes := a.processLister
	if processes == nil {
		processes = windowmap.DefaultProcessLister()
	}
	agents := make(map[string]bool, len(fleet.Agents))
	for name := range fleet.Agents {
		agents[name] = true
	}
	mappings, err := windowmap.Map(ctx, windows, processes, clients, agents)
	if err != nil {
		return nil, err
	}
	attention := a.attention(states)
	reports := make([]windowReport, 0, len(mappings))
	for _, mapping := range mappings {
		agent := fleet.Agents[mapping.Agent]
		state := states[mapping.Agent]
		display := mapping.Agent
		activity := "unknown"
		if state.Runtime != nil {
			display = a.nativeName(agent, state.Runtime)
			if state.Runtime.Activity != nil {
				switch state.Runtime.Activity.State {
				case "working", "idle", "blocked", "unknown":
					activity = state.Runtime.Activity.State
				}
			}
		}
		attentionRow := attention[mapping.Agent]
		if attentionRow.AwaitingUser && activity == "idle" {
			activity = "waiting"
		}
		index, err := strconv.Atoi(a.barAccent(mapping.Agent))
		if err != nil || index < 0 || index > 255 {
			return nil, fmt.Errorf("invalid accent for %s", mapping.Agent)
		}
		reports = append(reports, windowReport{
			Name: mapping.Agent, DisplayName: display, WindowID: mapping.Window.ID,
			Workspace: mapping.Window.Workspace, PID: mapping.Window.PID, Class: mapping.Window.Class,
			Focused: mapping.Window.Focused, Accent: xtermColor(index), Activity: activity,
			AwaitingUser: attentionRow.AwaitingUser, Unread: attentionRow.Unread,
		})
	}
	return reports, nil
}

func (a *app) windows(args []string) error {
	if len(args) == 1 && args[0] == "watch" {
		return a.watchWindows()
	}
	asJSON := false
	for _, arg := range args {
		if arg != "--json" {
			return fmt.Errorf("usage: bp windows [--json] | bp windows watch")
		}
		if asJSON {
			return fmt.Errorf("--json may only be specified once")
		}
		asJSON = true
	}
	adapter, err := a.compositor()
	if err != nil {
		return err
	}
	reports, err := a.discoverWindows(a.ctx, adapter)
	if err != nil {
		return err
	}
	if asJSON {
		encoder := json.NewEncoder(a.out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(reports)
	}
	w := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "AGENT\tDISPLAY\tWINDOW\tWORKSPACE\tPID\tACCENT\tACTIVITY\tFLAGS")
	for _, row := range reports {
		flags := ""
		if row.Unread {
			flags = "unread"
		} else if row.AwaitingUser {
			flags = "awaiting-user"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n", row.Name, row.DisplayName, row.WindowID, row.Workspace, row.PID, row.Accent, row.Activity, flags)
	}
	return w.Flush()
}

func (a *app) focus(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: bp focus <agent>")
	}
	if err := rejectFlag("focus", args[0]); err != nil {
		return err
	}
	target, err := a.resolveNativeTarget(args[0])
	if err != nil {
		return err
	}
	adapter, err := a.compositor()
	if err != nil {
		return err
	}
	reports, err := a.discoverWindows(a.ctx, adapter)
	if err != nil {
		return err
	}
	var matches []windowReport
	for _, row := range reports {
		if row.Name == target {
			matches = append(matches, row)
		}
	}
	if len(matches) == 0 {
		return fmt.Errorf("agent %s has no OS window; use bp attach %s", target, target)
	}
	chosen := matches[0]
	for _, row := range matches {
		if row.Focused {
			chosen = row
			break
		}
	}
	if err := adapter.FocusWindow(a.ctx, chosen.WindowID); err != nil {
		return err
	}
	if err := a.markLooked(target, time.Now().UTC()); err != nil {
		return fmt.Errorf("record last looked: %w", err)
	}
	return nil
}

func (a *app) watchWindows() error {
	adapter, err := a.compositor()
	if err != nil {
		return err
	}
	interval := a.windowPoll
	if interval <= 0 {
		interval = time.Second
	}
	ctx, stop := signal.NotifyContext(a.ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	painted := map[string][2]string{}
	resetPending := map[string]bool{}
	paint := func() error {
		reports, err := a.discoverWindows(ctx, adapter)
		if err != nil {
			return err
		}
		next := make(map[string][2]string, len(reports))
		present := make(map[string]bool, len(reports))
		for _, row := range reports {
			colors := [2]string{row.Accent, dimColor(row.Accent)}
			present[row.WindowID] = true
			previous, wasPainted := painted[row.WindowID]
			if wasPainted && previous == colors && !resetPending[row.WindowID] {
				next[row.WindowID] = colors
				continue
			}
			if err := adapter.SetBorderColors(ctx, row.WindowID, colors[0], colors[1]); err != nil {
				if errors.Is(err, compositor.ErrBorderColorsUnsupported) {
					return err
				}
				fmt.Fprintf(a.err, "warning: paint window %s: %v\n", row.WindowID, err)
				if wasPainted {
					next[row.WindowID] = previous
				}
				continue
			}
			delete(resetPending, row.WindowID)
			next[row.WindowID] = colors
		}
		reset, err := config.ColorIndex(a.config.Windows.ResetColor)
		if err != nil {
			return err
		}
		index, _ := strconv.Atoi(reset)
		resetHex := xtermColor(index)
		for id, colors := range painted {
			if present[id] {
				continue
			}
			if err := adapter.SetBorderColors(ctx, id, resetHex, resetHex); err != nil && !errors.Is(err, context.Canceled) {
				fmt.Fprintf(a.err, "warning: reset window %s border: %v\n", id, err)
				next[id] = colors
				resetPending[id] = true
			} else {
				delete(resetPending, id)
			}
		}
		painted = next
		return nil
	}
	if err := paint(); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := paint(); err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				fmt.Fprintf(a.err, "warning: refresh agent windows: %v\n", err)
			}
		}
	}
}

func dimColor(value string) string {
	if len(value) != 7 || value[0] != '#' {
		return value
	}
	components := make([]int64, 3)
	for i := range components {
		component, err := strconv.ParseInt(value[1+i*2:3+i*2], 16, 64)
		if err != nil {
			return value
		}
		components[i] = component / 2
	}
	return fmt.Sprintf("#%02x%02x%02x", components[0], components[1], components[2])
}
