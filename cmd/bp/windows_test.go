package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/cache"
	"blueprint/internal/compositor"
	"blueprint/internal/config"
	bptmux "blueprint/internal/tmux"
)

func TestWindowsAndStatusJSONExposeAttention(t *testing.T) {
	adapter := &testWindowAdapter{lists: [][]compositor.Window{{testTerminalWindow()}}}
	a, fleet, states, replyAt := newWindowTestApp(t, adapter)

	if err := a.windows([]string{"--json"}); err != nil {
		t.Fatal(err)
	}
	var windows []windowReport
	if err := json.Unmarshal([]byte(readTestOutput(t, a.out)), &windows); err != nil {
		t.Fatal(err)
	}
	if len(windows) != 1 || windows[0].Name != "worker" || windows[0].Activity != "waiting" || !windows[0].AwaitingUser || !windows[0].Unread {
		t.Fatalf("window report=%+v", windows)
	}
	if windows[0].Accent != xtermColor(44) || windows[0].Workspace != "3" || windows[0].PID != 100 {
		t.Fatalf("window mapping fields=%+v", windows[0])
	}

	a.out = testOutput(t)
	if err := a.statusJSON(fleet, states, nil); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Schema int `json:"schema_version"`
		Agents []struct {
			Name         string `json:"name"`
			AwaitingUser bool   `json:"awaiting_user"`
			Unread       bool   `json:"unread"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(readTestOutput(t, a.out)), &report); err != nil {
		t.Fatal(err)
	}
	if report.Schema != 3 {
		t.Fatalf("schema=%d, want 3", report.Schema)
	}
	for _, row := range report.Agents {
		if row.Name == "worker" && row.AwaitingUser && row.Unread {
			if replyAt.IsZero() {
				t.Fatal("test reply timestamp was not recorded")
			}
			return
		}
	}
	t.Fatalf("status attention fields missing: %+v", report.Agents)
}

func TestFocusRecordsLastLookedAndClearsUnread(t *testing.T) {
	adapter := &testWindowAdapter{lists: [][]compositor.Window{{testTerminalWindow()}}}
	a, fleet, states, replyAt := newWindowTestApp(t, adapter)
	if err := a.focus([]string{"worker"}); err != nil {
		t.Fatal(err)
	}
	if len(adapter.focused) != 1 || adapter.focused[0] != "0x1234" {
		t.Fatalf("focused windows=%v", adapter.focused)
	}
	data, err := os.ReadFile(filepath.Join(a.config.StateDir, lastLookedFile))
	if err != nil {
		t.Fatal(err)
	}
	var seen seenState
	if err := json.Unmarshal(data, &seen); err != nil {
		t.Fatal(err)
	}
	if !seen.Agents["worker"].After(replyAt) {
		t.Fatalf("last-looked timestamp %v is not after reply %v", seen.Agents["worker"], replyAt)
	}

	a.out = testOutput(t)
	if err := a.statusJSON(fleet, states, nil); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Agents []struct {
			Name   string `json:"name"`
			Unread bool   `json:"unread"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(readTestOutput(t, a.out)), &report); err != nil {
		t.Fatal(err)
	}
	for _, row := range report.Agents {
		if row.Name == "worker" {
			if row.Unread {
				t.Fatal("focused agent remained unread")
			}
			return
		}
	}
	t.Fatal("worker was omitted from status")
}

func TestUnreadRemainsSetWhileAgentWorksOnNextTurn(t *testing.T) {
	adapter := &testWindowAdapter{lists: [][]compositor.Window{{testTerminalWindow()}}}
	a, _, states, replyAt := newWindowTestApp(t, adapter)
	path := states["worker"].Runtime.Activity.TranscriptPath
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(map[string]any{
		"type": "user", "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"message": map[string]any{"content": "another prompt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	states["worker"].Runtime.Activity.State = "working"
	row := a.attention(states)["worker"]
	if row.AwaitingUser || !row.Unread || !row.RepliedAt.Equal(replyAt) {
		t.Fatalf("attention=%+v, want not awaiting, unread, with prior reply time", row)
	}
}

func TestFocusWithoutWindowMentionsAttach(t *testing.T) {
	adapter := &testWindowAdapter{lists: [][]compositor.Window{{testTerminalWindow()}}}
	a, _, _, _ := newWindowTestApp(t, adapter)
	a.listTmuxClients = func(context.Context) ([]bptmux.AttachedClient, error) { return nil, nil }
	if err := a.focus([]string{"worker"}); err == nil || !strings.Contains(err.Error(), "bp attach worker") {
		t.Fatalf("focus error=%v", err)
	}
}

func TestWindowsWatchResetsDepartedWindowToExplicitColor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	adapter := &testWindowAdapter{lists: [][]compositor.Window{{testTerminalWindow()}, {}}, failReset: 1}
	adapter.onBorder = func(call borderCall) {
		if call.Active == xtermColor(255) && call.Inactive == xtermColor(255) {
			cancel()
		}
	}
	a, _, _, _ := newWindowTestApp(t, adapter)
	a.ctx = ctx
	a.windowPoll = time.Millisecond
	if err := a.watchWindows(); err != nil {
		t.Fatal(err)
	}
	if len(adapter.borders) < 3 {
		t.Fatalf("border calls=%+v; want paint, failed reset and retry", adapter.borders)
	}
	last := adapter.borders[len(adapter.borders)-1]
	if last.ID != "0x1234" || last.Active != xtermColor(255) || last.Inactive != xtermColor(255) {
		t.Fatalf("last border call=%+v, want explicit white reset", last)
	}
	if adapter.resetCalls != 2 {
		t.Fatalf("reset attempts=%d, want retry after first error", adapter.resetCalls)
	}
}

func newWindowTestApp(t *testing.T, adapter *testWindowAdapter) (*app, book.Fleet, map[string]book.State, time.Time) {
	t.Helper()
	t.Setenv("AGENTBOOK", "")
	root := t.TempDir()
	bookPath := filepath.Join(root, "agentbook.json")
	writeLifecycleBook(t, bookPath, book.Agent{Name: "worker", Status: "open", ColorOverride: "44"})
	fleet, err := book.LoadFleet([]string{bookPath})
	if err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(root, "claude.jsonl")
	replyAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	line, err := json.Marshal(map[string]any{
		"type": "assistant", "timestamp": replyAt.Format(time.RFC3339Nano),
		"message": map[string]any{"stop_reason": "end_turn"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	state := book.State{Alive: true, Runtime: &cache.State{
		Runtime:  "claude",
		Activity: &cache.Activity{State: "idle", ThreadID: "thread", TranscriptPath: transcript},
	}}
	states := map[string]book.State{"worker": state}
	stateDir := filepath.Join(root, "state")
	a := &app{
		ctx: context.Background(),
		config: config.Config{
			Agentbooks: []string{bookPath}, StateDir: stateDir,
			Windows: config.WindowsConfig{ResetColor: "white"},
		},
		out: testOutput(t), err: testOutput(t),
		loadFleet:        func() (book.Fleet, map[string]book.State, error) { return fleet, states, nil },
		detectCompositor: func() (compositor.Adapter, error) { return adapter, nil },
		listTmuxClients: func(context.Context) ([]bptmux.AttachedClient, error) {
			return []bptmux.AttachedClient{{PID: 101, Session: "worker"}}, nil
		},
		processLister: testWindowProcesses{100: {100: {}, 101: {}}},
	}
	return a, fleet, states, replyAt
}

func testTerminalWindow() compositor.Window {
	return compositor.Window{ID: "0x1234", PID: 100, Class: "kitty", Workspace: "3", Focused: false}
}

type testWindowProcesses map[int]map[int]struct{}

func (p testWindowProcesses) Descendants(_ context.Context, pid int) (map[int]struct{}, error) {
	processes, ok := p[pid]
	if !ok {
		return nil, os.ErrNotExist
	}
	return processes, nil
}

type borderCall struct {
	ID       string
	Active   string
	Inactive string
}

type testWindowAdapter struct {
	lists      [][]compositor.Window
	listAt     int
	focused    []string
	borders    []borderCall
	onBorder   func(borderCall)
	failReset  int
	resetCalls int
}

func (a *testWindowAdapter) ListWindows(context.Context) ([]compositor.Window, error) {
	if a.listAt >= len(a.lists) {
		return nil, nil
	}
	windows := a.lists[a.listAt]
	a.listAt++
	return windows, nil
}

func (a *testWindowAdapter) FocusWindow(_ context.Context, id string) error {
	a.focused = append(a.focused, id)
	return nil
}

func (a *testWindowAdapter) SetBorderColors(_ context.Context, id, active, inactive string) error {
	call := borderCall{ID: id, Active: active, Inactive: inactive}
	a.borders = append(a.borders, call)
	if active == xtermColor(255) && inactive == xtermColor(255) {
		a.resetCalls++
		if a.failReset > 0 {
			a.failReset--
			return errors.New("temporary compositor failure")
		}
	}
	if a.onBorder != nil {
		a.onBorder(call)
	}
	return nil
}
