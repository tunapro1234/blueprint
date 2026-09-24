package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/cache"
	bpconfig "blueprint/internal/config"
	bptmux "blueprint/internal/tmux"
)

const fleetThread = "11111111-1111-4111-8111-111111111111"

type fakeFleetUpdater struct {
	panes      map[string]string
	captures   [][]string
	captureAt  int
	installed  map[string]bool
	updates    []string
	restarts   []fleetRestart
	verified   []string
	bars       []string
	restartErr error
	verifyErr  error
}

type fleetRestart struct {
	name string
	opts bptmux.OpenOptions
}

func (f *fakeFleetUpdater) Installed(name string) bool { return f.installed[name] }
func (f *fakeFleetUpdater) UpdateCLI(_ context.Context, name string, _ []string) error {
	f.updates = append(f.updates, name)
	return nil
}
func (f *fakeFleetUpdater) Capture(name string) (string, error) {
	if f.captureAt < len(f.captures) {
		value := f.captures[f.captureAt]
		f.captureAt++
		if len(value) == 2 {
			return value[0], errors.New(value[1])
		}
		if len(value) == 1 {
			return value[0], nil
		}
	}
	if pane, ok := f.panes[name]; ok {
		return pane, nil
	}
	return "❯ \n", nil
}
func (f *fakeFleetUpdater) Restart(name, _ string, opts bptmux.OpenOptions) error {
	f.restarts = append(f.restarts, fleetRestart{name: name, opts: opts})
	return f.restartErr
}
func (f *fakeFleetUpdater) Verify(name, _, _, _ string) error {
	f.verified = append(f.verified, name)
	return f.verifyErr
}
func (f *fakeFleetUpdater) ApplyBar(name string) { f.bars = append(f.bars, name) }

func liveFleetState(runtime, state, model, effort string) book.State {
	blocked := state != "idle"
	return book.State{Alive: true, Runtime: &cache.State{
		Runtime: runtime, Model: model, Effort: effort,
		Activity: &cache.Activity{State: state, ThreadID: fleetThread, DeliveryBlocked: blocked},
	}}
}

func testFleet(names ...string) book.Fleet {
	fleet := book.Fleet{Agents: map[string]book.Agent{}, Parents: map[string]string{}, Root: "root"}
	for _, name := range names {
		fleet.Order = append(fleet.Order, name)
		fleet.Agents[name] = book.Agent{Name: name, Folder: "/tmp", Launch: &bptmux.OpenOptions{Codex: true, Args: []string{"--search", "--yolo", "-m", "gpt-old", "-c", "model_reasoning_effort=medium"}}}
		if name != "root" {
			fleet.Parents[name] = "root"
		}
	}
	return fleet
}

func TestFleetUpdateDefersEveryUnsafeState(t *testing.T) {
	tests := []struct {
		name, pane, reason string
		state              book.State
	}{
		{"busy", "Working (1s · esc to interrupt)\n❯ \n", "agent busy", liveFleetState("codex", "idle", "gpt-old", "medium")},
		{"modal", "Confirm action\nEnter to confirm · Esc to cancel\n", "modal open", liveFleetState("codex", "idle", "gpt-old", "medium")},
		{"draft", "❯ do not erase this\n", "unsent user text in composer", liveFleetState("codex", "idle", "gpt-old", "medium")},
		{"unknown runtime", "❯ \n", "runtime unknown or unloaded", book.State{Alive: true}},
		{"unloaded runtime", "❯ \n", "runtime unknown or unloaded", liveFleetState("codex", "notLoaded", "gpt-old", "medium")},
		{"unknown model", "❯ \n", "current model unknown", liveFleetState("codex", "idle", "", "medium")},
		{"unknown effort", "❯ \n", "current effort unknown", liveFleetState("codex", "idle", "gpt-old", "")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fleet := testFleet("agent")
			backend := &fakeFleetUpdater{panes: map[string]string{"agent": tc.pane}}
			a := &app{}
			report, err := a.planFleetUpdate(fleet, map[string]book.State{"agent": tc.state}, fleetUpdateOptions{clis: true, models: map[string]string{}}, backend)
			if err != nil {
				t.Fatal(err)
			}
			if got := report.Agents[0]; got.Status != "deferred" || got.Reason != tc.reason {
				t.Fatalf("decision=%+v, want deferred(%s)", got, tc.reason)
			}
		})
	}
}

func TestFleetUpdateDefersWhenLaunchModeIsUnknown(t *testing.T) {
	fleet := testFleet("agent")
	fleet.Agents["agent"] = book.Agent{Name: "agent", Folder: "/tmp"}
	state := liveFleetState("codex", "idle", "gpt-old", "medium")
	report, err := (&app{}).planFleetUpdate(fleet, map[string]book.State{"agent": state}, fleetUpdateOptions{clis: true}, &fakeFleetUpdater{})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Agents[0]; got.Status != "deferred" || !strings.Contains(got.Reason, "launch mode") {
		t.Fatalf("decision=%+v, want unknown launch mode deferral", got)
	}

	fleet = testFleet("agent")
	entry := fleet.Agents["agent"]
	entry.Launch.OpenCode = true
	fleet.Agents["agent"] = entry
	state = liveFleetState("claude", "idle", "claude-old", "high")
	report, err = (&app{}).planFleetUpdate(fleet, map[string]book.State{"agent": state}, fleetUpdateOptions{clis: true, models: map[string]string{"claude-old": "claude-new"}}, &fakeFleetUpdater{panes: map[string]string{"agent": "❯ \n"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Agents[0]; got.Status != "deferred" || !strings.Contains(got.Reason, "launch mode") || got.storeModels {
		t.Fatalf("decision=%+v, want mismatched OpenCode launch deferral without migration", got)
	}
}

func TestFleetUpdateOrdersChildrenBeforeCoordinator(t *testing.T) {
	fleet := testFleet("root", "child", "grandchild", "zzz-orphan")
	fleet.Parents["grandchild"] = "child"
	states := map[string]book.State{}
	for _, name := range fleet.Order {
		states[name] = liveFleetState("codex", "idle", "gpt-old", "medium")
	}
	report, err := (&app{}).planFleetUpdate(fleet, states, fleetUpdateOptions{clis: true, models: map[string]string{}}, &fakeFleetUpdater{})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{report.Agents[0].Agent, report.Agents[1].Agent, report.Agents[2].Agent, report.Agents[3].Agent}
	if want := []string{"grandchild", "child", "zzz-orphan", "root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order=%v, want %v", got, want)
	}
}

func fleetUpdateApp(t *testing.T, backend *fakeFleetUpdater, fleet book.Fleet, states map[string]book.State) (*app, string) {
	t.Helper()
	dir := t.TempDir()
	bookPath := filepath.Join(dir, "agentbook.json")
	file := book.File{Orchestrator: fleet.Root}
	for _, name := range fleet.Order {
		file.Agents = append(file.Agents, fleet.Agents[name])
	}
	data, err := json.Marshal(file)
	if err != nil || os.WriteFile(bookPath, data, 0600) != nil {
		t.Fatal("write fleet fixture")
	}
	a := &app{
		ctx: context.Background(), out: testOutput(t), err: testOutput(t), fleetUpdater: backend,
		config:    bpconfig.Config{Agentbooks: []string{bookPath}, StateDir: filepath.Join(dir, "state"), CLIUpdates: map[string][]string{"codex": {"fake-codex-update"}}},
		loadFleet: func() (book.Fleet, map[string]book.State, error) { return fleet, states, nil },
	}
	return a, bookPath
}

func TestFleetUpdatePreservesEffortAndLaunchModeAcrossModelChange(t *testing.T) {
	fleet := testFleet("root")
	states := map[string]book.State{"root": liveFleetState("codex", "idle", "gpt-old", "medium")}
	backend := &fakeFleetUpdater{}
	a, _ := fleetUpdateApp(t, backend, fleet, states)
	if err := a.fleetUpdate([]string{"--models", "gpt-old=gpt-new", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if len(backend.restarts) != 1 {
		t.Fatalf("restarts=%d, want 1", len(backend.restarts))
	}
	got := backend.restarts[0].opts
	joined := strings.Join(got.Args, " ")
	for _, want := range []string{"--search", "--yolo", "-m gpt-new", "-c model_reasoning_effort=medium"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("launch args %q lost %q", joined, want)
		}
	}
	if strings.Contains(joined, "gpt-old") || !got.Resume || got.ResumeID != fleetThread {
		t.Fatalf("restart options=%+v", got)
	}
}

func TestFleetUpdateReportsVerificationFailure(t *testing.T) {
	fleet := testFleet("root")
	states := map[string]book.State{"root": liveFleetState("codex", "idle", "gpt-old", "medium")}
	backend := &fakeFleetUpdater{verifyErr: errors.New("thread did not bind")}
	a, _ := fleetUpdateApp(t, backend, fleet, states)
	if err := a.fleetUpdate([]string{"--clis", "--yes"}); err != nil {
		t.Fatal(err)
	}
	output := readTestOutput(t, a.out)
	if !strings.Contains(output, "failed (thread did not bind)") || len(backend.bars) != 0 {
		t.Fatalf("output=%q bars=%v", output, backend.bars)
	}
}

func TestFleetUpdateDryRunMakesNoChanges(t *testing.T) {
	fleet := testFleet("root")
	states := map[string]book.State{"root": liveFleetState("codex", "idle", "gpt-old", "medium")}
	backend := &fakeFleetUpdater{installed: map[string]bool{"codex": true}}
	a, bookPath := fleetUpdateApp(t, backend, fleet, states)
	before, err := os.ReadFile(bookPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.fleetUpdate([]string{"--all", "--models", "gpt-old=gpt-new", "--set-defaults", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(bookPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || len(backend.updates) != 0 || len(backend.restarts) != 0 || len(backend.verified) != 0 || len(backend.bars) != 0 {
		t.Fatalf("dry-run mutated state: updates=%v restarts=%v verified=%v bars=%v", backend.updates, backend.restarts, backend.verified, backend.bars)
	}
}

func TestFleetUpdateRechecksPaneBeforeRestart(t *testing.T) {
	fleet := testFleet("root")
	states := map[string]book.State{"root": liveFleetState("codex", "idle", "gpt-old", "medium")}
	backend := &fakeFleetUpdater{captures: [][]string{{"❯ \n"}, {"Working (1s · esc to interrupt)\n❯ \n"}}}
	a, bookPath := fleetUpdateApp(t, backend, fleet, states)
	if err := a.fleetUpdate([]string{"--clis", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if len(backend.restarts) != 0 {
		t.Fatalf("restarted a pane that became busy after confirmation: %+v", backend.restarts)
	}
	loaded, err := book.LoadFleet([]string{bookPath})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Agents["root"].FleetUpdate == nil || loaded.Agents["root"].FleetUpdate.DeferredReason != "agent busy" {
		t.Fatalf("late deferral was not recorded: %+v", loaded.Agents["root"].FleetUpdate)
	}
}

func TestFleetUpdateDefersDraftDetectedByExitGuard(t *testing.T) {
	fleet := testFleet("root")
	states := map[string]book.State{"root": liveFleetState("codex", "idle", "gpt-old", "medium")}
	backend := &fakeFleetUpdater{restartErr: bptmux.ErrTyping}
	a, bookPath := fleetUpdateApp(t, backend, fleet, states)
	if err := a.fleetUpdate([]string{"--clis", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if len(backend.verified) != 0 {
		t.Fatal("verified a restart that was refused by the composer guard")
	}
	if output := readTestOutput(t, a.out); !strings.Contains(output, "deferred (unsent user text in composer)") {
		t.Fatalf("draft result=%q", output)
	}
	loaded, err := book.LoadFleet([]string{bookPath})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Agents["root"].FleetUpdate == nil || loaded.Agents["root"].FleetUpdate.DeferredReason != "unsent user text in composer" {
		t.Fatalf("draft deferral was not recorded: %+v", loaded.Agents["root"].FleetUpdate)
	}
}

func TestFleetUpdateSkipsUninstalledCLIsThenRestarts(t *testing.T) {
	fleet := testFleet("root", "child")
	states := map[string]book.State{
		"root":  liveFleetState("codex", "idle", "gpt-old", "medium"),
		"child": liveFleetState("codex", "idle", "gpt-old", "medium"),
	}
	backend := &fakeFleetUpdater{installed: map[string]bool{"codex": true}}
	a, _ := fleetUpdateApp(t, backend, fleet, states)
	if err := a.fleetUpdate([]string{"--clis", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backend.updates, []string{"codex"}) {
		t.Fatalf("updated CLIs=%v", backend.updates)
	}
	if got := []string{backend.restarts[0].name, backend.restarts[1].name}; !reflect.DeepEqual(got, []string{"child", "root"}) {
		t.Fatalf("restart order=%v", got)
	}
}

func TestFleetUpdateAgentSelectionLimitsCLIUpdates(t *testing.T) {
	fleet := testFleet("root", "child")
	child := fleet.Agents["child"]
	child.Launch = &bptmux.OpenOptions{Args: []string{"--model", "claude-old"}}
	fleet.Agents["child"] = child
	states := map[string]book.State{
		"root":  liveFleetState("codex", "idle", "gpt-old", "medium"),
		"child": liveFleetState("claude", "idle", "claude-old", "high"),
	}
	backend := &fakeFleetUpdater{installed: map[string]bool{"claude": true, "codex": true}}
	a, _ := fleetUpdateApp(t, backend, fleet, states)
	a.config.CLIUpdates = map[string][]string{
		"claude": {"fake-claude-update"},
		"codex":  {"fake-codex-update"},
	}
	if err := a.fleetUpdate([]string{"--clis", "--agent", "root", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backend.updates, []string{"codex"}) {
		t.Fatalf("updated CLIs=%v, want only selected agent's Codex harness", backend.updates)
	}
	if len(backend.restarts) != 1 || backend.restarts[0].name != "root" {
		t.Fatalf("restarts=%+v, want only selected root agent", backend.restarts)
	}
}

func TestFleetUpdateRequiresYesInNonInteractiveMode(t *testing.T) {
	fleet := testFleet("root")
	states := map[string]book.State{"root": liveFleetState("codex", "idle", "gpt-old", "medium")}
	backend := &fakeFleetUpdater{installed: map[string]bool{"codex": true}}
	a, _ := fleetUpdateApp(t, backend, fleet, states)
	a.interactive = func() bool { return false }
	if err := a.fleetUpdate([]string{"--clis"}); err == nil || !strings.Contains(err.Error(), "without --yes") {
		t.Fatalf("noninteractive update error=%v", err)
	}
	if len(backend.updates) != 0 || len(backend.restarts) != 0 {
		t.Fatalf("noninteractive refusal mutated fleet: updates=%v restarts=%v", backend.updates, backend.restarts)
	}
}

func TestClosedFleetMigrationSurvivesCLISOnlyUpdate(t *testing.T) {
	fleet := testFleet("closed")
	fleet.Root = "closed"
	states := map[string]book.State{"closed": {Alive: false}}
	backend := &fakeFleetUpdater{}
	a, bookPath := fleetUpdateApp(t, backend, fleet, states)
	if err := a.fleetUpdate([]string{"--models", "gpt-old=gpt-new", "--yes"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := book.LoadFleet([]string{bookPath})
	if err != nil {
		t.Fatal(err)
	}
	fleet.Agents["closed"] = loaded.Agents["closed"]
	if err := a.fleetUpdate([]string{"--clis", "--yes"}); err != nil {
		t.Fatal(err)
	}
	loaded, err = book.LoadFleet([]string{bookPath})
	if err != nil {
		t.Fatal(err)
	}
	update := loaded.Agents["closed"].FleetUpdate
	if update == nil || update.Models["gpt-old"] != "gpt-new" || update.DeferredReason == "" {
		t.Fatalf("closed migration was lost: %+v", update)
	}
	launch := *loaded.Agents["closed"].Launch
	launch.Resume = true
	if err := applyStoredFleetUpdate(loaded.Agents["closed"], &launch); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(launch.Args, " "); !strings.Contains(got, "-m gpt-new -c model_reasoning_effort=medium") {
		t.Fatalf("closed resume args=%q", got)
	}
}

func TestClosedUnsupportedLaunchDoesNotStoreModelMigration(t *testing.T) {
	fleet := testFleet("closed")
	fleet.Root = "closed"
	entry := fleet.Agents["closed"]
	entry.Launch = &bptmux.OpenOptions{OpenCode: true, Args: []string{"--model", "gpt-old", "--effort", "medium"}}
	fleet.Agents["closed"] = entry
	backend := &fakeFleetUpdater{}
	a, bookPath := fleetUpdateApp(t, backend, fleet, map[string]book.State{"closed": {Alive: false}})
	if err := a.fleetUpdate([]string{"--models", "gpt-old=gpt-new", "--yes"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := book.LoadFleet([]string{bookPath})
	if err != nil {
		t.Fatal(err)
	}
	update := loaded.Agents["closed"].FleetUpdate
	if update == nil || len(update.Models) != 0 || !strings.Contains(update.DeferredReason, "unsupported") {
		t.Fatalf("unsupported launch migration=%+v", update)
	}

	agent := loaded.Agents["closed"]
	agent.FleetUpdate.Models = map[string]string{"gpt-old": "gpt-new"}
	launch := *agent.Launch
	launch.Resume = true
	before := append([]string(nil), launch.Args...)
	if err := applyStoredFleetUpdate(agent, &launch); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(launch.Args, before) {
		t.Fatalf("unsupported resume args changed: got %v, want %v", launch.Args, before)
	}
}

func TestLiveFleetUpdateRefusesPaneThatNoLongerMatchesLaunch(t *testing.T) {
	root := t.TempDir()
	sentPath := filepath.Join(root, "sent-exit")
	tmuxBin := filepath.Join(root, "tmux")
	script := "#!/bin/sh\ncase \"$1\" in\nlist-panes) printf '1\\tzsh\\t4242\\n' ;;\nsend-keys) printf sent > " + quoteShell(sentPath) + " ;;\nesac\n"
	if err := os.WriteFile(tmuxBin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	a := &app{
		ctx:    context.Background(),
		config: bpconfig.Config{Home: root, Legacy: true},
		tmux:   &bptmux.Client{Bin: tmuxBin},
	}
	backend := &liveFleetUpdate{app: a}
	err := backend.Restart("worker", root, bptmux.OpenOptions{Codex: true, Resume: true, ResumeID: fleetThread})
	if err == nil || !strings.Contains(err.Error(), "refusing to exit") {
		t.Fatalf("restart error=%v, want safe refusal", err)
	}
	if _, err := os.Stat(sentPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exit command was sent to a mismatched pane: stat err=%v", err)
	}
}

func TestLiveFleetUpdateValidatesWorkingDirectoryBeforeTmux(t *testing.T) {
	root := t.TempDir()
	a := &app{
		ctx:    context.Background(),
		config: bpconfig.Config{Home: root, Legacy: true},
		tmux:   &bptmux.Client{Bin: filepath.Join(root, "missing-tmux")},
	}
	backend := &liveFleetUpdate{app: a}
	err := backend.Restart("worker", filepath.Join(root, "missing-folder"), bptmux.OpenOptions{Codex: true, Resume: true, ResumeID: fleetThread})
	if err == nil || !strings.Contains(err.Error(), "working directory") {
		t.Fatalf("restart error=%v, want working-directory refusal before tmux access", err)
	}
}

func TestUpdateFleetDefaultsUsesCustomHomesAndBackups(t *testing.T) {
	home, codexHome, claudeHome := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
	codexPath := filepath.Join(codexHome, "config.toml")
	claudePath := filepath.Join(claudeHome, "settings.json")
	codexBefore := "model = \"gpt-old\"\n[profiles.work]\nmodel = \"gpt-old\"\n"
	claudeBefore := "{\"model\":\"claude-old\",\"other\":true}\n"
	for path, data := range map[string]string{codexPath: codexBefore, claudePath: claudeBefore} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	stamp := time.Date(2026, 9, 24, 12, 30, 0, 0, time.UTC)
	changed, err := updateFleetDefaults(map[string]string{"gpt-old": "gpt-new", "claude-old": "claude-new"}, stamp)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(changed, []string{codexPath, claudePath}) {
		t.Fatalf("changed paths=%v", changed)
	}
	codexAfter, err := os.ReadFile(codexPath)
	if err != nil || !strings.Contains(string(codexAfter), `model = "gpt-new"`) || !strings.Contains(string(codexAfter), `model = "gpt-old"`) {
		t.Fatalf("Codex defaults=%q err=%v", codexAfter, err)
	}
	claudeAfter, err := os.ReadFile(claudePath)
	if err != nil || !strings.Contains(string(claudeAfter), `"model": "claude-new"`) {
		t.Fatalf("Claude defaults=%q err=%v", claudeAfter, err)
	}
	for path, want := range map[string]string{codexPath: codexBefore, claudePath: claudeBefore} {
		backup := path + ".before-fleet-update-" + stamp.Format("20060102T150405.000000000Z")
		data, err := os.ReadFile(backup)
		if err != nil || string(data) != want {
			t.Fatalf("backup %s=%q err=%v", backup, data, err)
		}
	}
}

func TestFleetUpdateSetDefaultsWritesOnlyWithFlag(t *testing.T) {
	home, codexHome := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	path := filepath.Join(codexHome, "config.toml")
	before := []byte("model = \"gpt-old\"\n")
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	fleet := testFleet("root")
	states := map[string]book.State{"root": liveFleetState("codex", "idle", "gpt-old", "medium")}
	a, _ := fleetUpdateApp(t, &fakeFleetUpdater{}, fleet, states)
	if err := a.fleetUpdate([]string{"--models", "gpt-old=gpt-new", "--yes"}); err != nil {
		t.Fatal(err)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != string(before) {
		t.Fatalf("defaults changed without --set-defaults: %q err=%v", unchanged, err)
	}
	if err := a.fleetUpdate([]string{"--models", "gpt-old=gpt-new", "--set-defaults", "--yes"}); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(updated), `model = "gpt-new"`) {
		t.Fatalf("default model=%q err=%v", updated, err)
	}
	if output := readTestOutput(t, a.out); !strings.Contains(output, "Defaults updated: "+path) {
		t.Fatalf("set-defaults result=%q", output)
	}
}

func TestStoredFleetMigrationAppliesOnClosedResume(t *testing.T) {
	agent := book.Agent{Name: "closed", FleetUpdate: &book.FleetUpdate{
		Models: map[string]string{"gpt-old": "gpt-new"}, Model: "gpt-old", Effort: "low", RecordedAt: time.Now(),
	}}
	opts := bptmux.OpenOptions{Codex: true, Resume: true, Args: []string{"--search"}}
	if err := applyStoredFleetUpdate(agent, &opts); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(opts.Args, " "); !strings.Contains(got, "-m gpt-new -c model_reasoning_effort=low") || !strings.Contains(got, "--search") {
		t.Fatalf("resume args=%q", got)
	}
}
