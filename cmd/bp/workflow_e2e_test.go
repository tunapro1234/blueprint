package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/cache"
	bpconfig "blueprint/internal/config"
	"blueprint/internal/msgq"
	bptmux "blueprint/internal/tmux"
	"blueprint/internal/workflow"
)

func TestWorkflowRealDriverIsolatedTmuxThreeUnits(t *testing.T) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	root := t.TempDir()
	tmuxTmp := filepath.Join(root, "tmux-tmp")
	if err := os.MkdirAll(tmuxTmp, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "tmux.sock")
	cleanEnv := func() []string {
		env := withoutIssueTmux(os.Environ())
		return append(env, "TMUX_TMPDIR="+tmuxTmp)
	}
	baseTmux := func(args ...string) *exec.Cmd {
		cmd := exec.Command(tmuxPath, append([]string{"-f", "/dev/null", "-S", socket}, args...)...)
		cmd.Env = cleanEnv()
		return cmd
	}
	t.Cleanup(func() { _ = baseTmux("kill-server").Run() })
	shim := filepath.Join(root, "tmux")
	shimBody := `#!/bin/bash
real=` + quoteShell(tmuxPath) + `
socket=` + quoteShell(socket) + `
if [[ "$1" == "paste-buffer" ]]; then
  shift
  args=()
  for arg in "$@"; do
    [[ "$arg" == "-p" ]] || args+=("$arg")
  done
  exec "$real" -f /dev/null -S "$socket" paste-buffer "${args[@]}"
fi
exec "$real" -f /dev/null -S "$socket" "$@"
`
	if err := os.WriteFile(shim, []byte(shimBody), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("TMUX_TMPDIR", tmuxTmp)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))

	stateDir := filepath.Join(root, "state")
	workdir := filepath.Join(root, "workdir")
	source := filepath.Join(root, "workflow-source")
	for _, dir := range []string{stateDir, workdir, source} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(source, "prompts"), 0700); err != nil {
		t.Fatal(err)
	}
	workflowYAML := `name: e2e-sample
version: 1
units:
  file: units.json
  key: id
prompt:
  template: prompts/task.md
output:
  path: "results/{{.Key}}.json"
  format: json
  require_fresh: true
compact:
  every_units: 2
timeouts:
  start: 5s
  unit: 1m
notify: none
`
	if err := os.WriteFile(filepath.Join(source, "workflow.yaml"), []byte(workflowYAML), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "prompts", "task.md"), []byte("unit={{.Key}} output={{.Output}} please complete this sample scoring task"), 0600); err != nil {
		t.Fatal(err)
	}
	units := `[{"id":"one"},{"id":"two"},{"id":"three"}]`
	if err := os.WriteFile(filepath.Join(workdir, "units.json"), []byte(units), 0600); err != nil {
		t.Fatal(err)
	}
	threadID := "00000000-0000-4000-8000-000000000001"
	transcript := filepath.Join(root, threadID+".jsonl")
	observationPath := filepath.Join(root, "observation.json")
	compactLog := filepath.Join(root, "compacts.log")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	initial := fmt.Sprintf("{\"type\":\"system\",\"subtype\":\"turn_duration\",\"timestamp\":%q}\n", now)
	if err := os.WriteFile(transcript, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	fakeScript := filepath.Join(root, "claude")
	fakeBody := `#!/bin/bash
set -eu
OBS=` + quoteShell(observationPath) + `
TRANSCRIPT=` + quoteShell(transcript) + `
WORKDIR=` + quoteShell(workdir) + `
COMPACTS=` + quoteShell(compactLog) + `
THREAD=` + quoteShell(threadID) + `
write_observation() {
  context="$1"
  stamp=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)
  printf '{"session_id":"%s","transcript_path":"%s","cwd":"%s","model":"fake-model","context":%s,"observed_at":"%s"}\n' "$THREAD" "$TRANSCRIPT" "$WORKDIR" "$context" "$stamp" > "$OBS.tmp"
  mv "$OBS.tmp" "$OBS"
}

printf '\033[2J\033[H❯ '
while true; do
  IFS= read -r line || exit 0
  if [[ -z "$line" ]]; then
    printf '\033[2J\033[H❯ '
    continue
  fi
  if [[ "$line" == /compact ]]; then
    printf '\033[2J\033[H──────────\n❯ '
    printf 'compact\n' >> "$COMPACTS"
    stamp=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)
    printf '{"type":"system","subtype":"turn_duration","timestamp":"%s"}\n' "$stamp" >> "$TRANSCRIPT"
    write_observation 100
    continue
  fi
  printf '\033[2J\033[H✻ Working… (1s · esc to interrupt)\n❯ \n──────────\n'
  stamp=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)
  printf '{"type":"user","timestamp":"%s","message":{"content":"%s"}}\n' "$stamp" "$line" >> "$TRANSCRIPT"
  sleep 1.5
  key=${line#unit=}
  key=${key%% *}
  output=${line#*output=}
  output=${output%% *}
  mkdir -p "$WORKDIR/$(dirname "$output")"
  printf '{"id":"%s","score":80,"rationale":"fictional sample"}\n' "$key" > "$WORKDIR/$output"
  stamp=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)
  printf '{"type":"system","subtype":"turn_duration","timestamp":"%s"}\n' "$stamp" >> "$TRANSCRIPT"
  printf '\033[2J\033[H❯ '
done
`
	if err := os.WriteFile(fakeScript, []byte(fakeBody), 0700); err != nil {
		t.Fatal(err)
	}
	command := "/bin/bash -c " + quoteShell("exec -a claude /bin/bash "+quoteShell(fakeScript))
	if output, err := baseTmux("new-session", "-d", "-s", "worker", command).CombinedOutput(); err != nil {
		t.Fatalf("start fake CLI: %v: %s", err, output)
	}
	pidBytes, err := baseTmux("list-panes", "-t", "=worker:", "-F", "#{pane_pid}").Output()
	if err != nil {
		t.Fatal(err)
	}
	var panePID int
	if _, err := fmt.Sscan(strings.TrimSpace(string(pidBytes)), &panePID); err != nil || panePID <= 1 {
		panes, _ := baseTmux("list-panes", "-a", "-F", "#{session_name}:#{pane_pid}:#{pane_current_command}").CombinedOutput()
		capture, _ := baseTmux("capture-pane", "-p", "-t", "=worker:").CombinedOutput()
		t.Fatalf("invalid private pane pid %q: %v; panes=%q capture=%q", pidBytes, err, panes, capture)
	}
	var ctxTokens = 200
	obs := cache.LocalObservation{SessionID: threadID, TranscriptPath: transcript, CWD: workdir, Model: "fake-model", Context: &ctxTokens, ObservedAt: time.Now().UTC()}
	obsJSON, _ := json.Marshal(obs)
	if err := os.WriteFile(observationPath, append(obsJSON, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	bookPath := filepath.Join(root, "agentbook.json")
	bookJSON, _ := json.Marshal(map[string]any{"orchestrator": "root", "agents": []map[string]any{
		{"name": "root", "status": "open", "folder": root},
		{"name": "worker", "parent": "root", "status": "open", "folder": workdir,
			"localRuntime": map[string]any{"pid": panePID, "path": observationPath, "harness": "claude"}},
	}})
	if err := os.WriteFile(bookPath, bookJSON, 0600); err != nil {
		t.Fatal(err)
	}
	fleet, err := book.LoadFleet([]string{bookPath})
	if err != nil {
		t.Fatal(err)
	}
	store := workflow.NewStore(stateDir)
	definition, err := workflow.ReadDefinition(source)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(source, definition, workdir, []string{"worker"}, "root")
	if err != nil {
		t.Fatal(err)
	}
	tmuxClient := bptmux.New()
	tmuxClient.Bin = shim
	a := &app{
		ctx: context.Background(), config: bpconfig.Config{StateDir: stateDir, MsgqRoot: filepath.Join(stateDir, "msgq"), Agentbooks: []string{bookPath}},
		tmux: tmuxClient, queue: msgq.New(filepath.Join(stateDir, "msgq")), out: testOutput(t), err: testOutput(t),
	}
	a.loadFleet = func() (book.Fleet, map[string]book.State, error) { return fleet, map[string]book.State{}, nil }
	a.clearPane = func(string) error { return nil }
	a.turnOpenProbe = func(string) bool { return false }
	var observeMu sync.Mutex
	var observeCosts []time.Duration
	a.workflowObserveMeasured = func(cost time.Duration) {
		observeMu.Lock()
		observeCosts = append(observeCosts, cost)
		observeMu.Unlock()
	}
	driver, err := a.newWorkflowDriver("root")
	if err != nil {
		t.Fatal(err)
	}
	engine := workflow.NewEngine(store, driver)
	engine.Poll = 100 * time.Millisecond
	engine.Authority = func(owner, agent string) error { return a.workflowAuthorityFor(driver, owner, agent) }
	if err := engine.Run(context.Background(), run.ID, false); err != nil {
		panes, _ := baseTmux("list-panes", "-a", "-F", "#{session_name}:#{pane_pid}:#{pane_current_command}").CombinedOutput()
		capture, _ := baseTmux("capture-pane", "-e", "-p", "-t", "=worker:").CombinedOutput()
		transcriptData, _ := os.ReadFile(transcript)
		events, _, _ := store.Events(run.ID)
		t.Fatalf("workflow e2e: %v; panes=%q capture=%q transcript=%q events=%#v", err, panes, capture, transcriptData, events)
	}
	rows, torn, err := store.Times(run.ID)
	if err != nil || torn || len(rows) != 3 {
		t.Fatalf("expected three finished units, rows=%#v torn=%t err=%v", rows, torn, err)
	}
	for _, row := range rows {
		if row.Result != "done" {
			t.Fatalf("unit %s failed: %#v", row.Unit, row)
		}
	}
	compactData, err := os.ReadFile(compactLog)
	if err != nil || strings.Count(string(compactData), "compact") != 1 {
		t.Fatalf("expected one fake CLI /compact, got %q err=%v", compactData, err)
	}
	observeMu.Lock()
	costs := append([]time.Duration(nil), observeCosts...)
	observeMu.Unlock()
	var total time.Duration
	for _, cost := range costs {
		total += cost
	}
	if len(costs) == 0 {
		t.Fatal("real driver did not record any Observe calls")
	}
	average := total / time.Duration(len(costs))
	t.Logf("real RuntimeFor Observe: calls=%d average=%s; idle cycle estimate=%s (2 observes + %s poll)", len(costs), average, 2*average+workflow.PollInterval, workflow.PollInterval)
}

func isolatedIdleAgentTmux(t *testing.T, sessions ...string) (*bptmux.Client, string, map[string]string) {
	t.Helper()
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	root := t.TempDir()
	tmuxTmp := filepath.Join(root, "tmux-tmp")
	if err := os.MkdirAll(tmuxTmp, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "tmux.sock")
	cleanEnv := func() []string {
		return append(withoutIssueTmux(os.Environ()), "TMUX_TMPDIR="+tmuxTmp)
	}
	baseTmux := func(args ...string) *exec.Cmd {
		cmd := exec.Command(tmuxPath, append([]string{"-f", "/dev/null", "-S", socket}, args...)...)
		cmd.Env = cleanEnv()
		return cmd
	}
	t.Cleanup(func() { _ = baseTmux("kill-server").Run() })
	shim := filepath.Join(root, "tmux")
	shimBody := `#!/bin/bash
real=` + quoteShell(tmuxPath) + `
socket=` + quoteShell(socket) + `
if [[ "$1" == "paste-buffer" ]]; then
  shift
  args=()
  for arg in "$@"; do
    [[ "$arg" == "-p" ]] || args+=("$arg")
  done
  exec "$real" -f /dev/null -S "$socket" paste-buffer "${args[@]}"
fi
exec "$real" -f /dev/null -S "$socket" "$@"
`
	if err := os.WriteFile(shim, []byte(shimBody), 0700); err != nil {
		t.Fatal(err)
	}
	logPaths := make(map[string]string, len(sessions))
	fakeAgent := filepath.Join(root, "fake-agent")
	fakeBody := `#!/bin/bash
set -eu
log="$1"
printf '\033[2J\033[H❯ '
while IFS= read -r line; do
  if [[ "$line" == "/compact" ]]; then printf 'compact\n' >> "$log"; fi
  printf '\033[2J\033[H❯ '
done
`
	if err := os.WriteFile(fakeAgent, []byte(fakeBody), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range sessions {
		logPath := filepath.Join(root, name+".compacts")
		logPaths[name] = logPath
		command := "/bin/bash -c " + quoteShell("exec -a claude /bin/bash "+quoteShell(fakeAgent)+" "+quoteShell(logPath))
		if output, err := baseTmux("new-session", "-d", "-s", name, command).CombinedOutput(); err != nil {
			t.Fatalf("start isolated fake agent %s: %v: %s", name, err, output)
		}
	}
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("TMUX_TMPDIR", tmuxTmp)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	client := bptmux.New()
	client.Bin = shim
	return client, root, logPaths
}

func assertVerifiedCompactRecords(t *testing.T, queueRoot string, want map[string]bool) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(queueRoot, "done"))
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool)
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(queueRoot, "done", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var record msgq.Message
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		if record.Msg == "/compact" {
			if !msgq.IsVerifiedDelivery(record.Status) {
				t.Fatalf("compact record %s has non-verified status %q", record.ID, record.Status)
			}
			got[record.To] = true
		}
	}
	if len(got) != len(want) {
		t.Fatalf("verified compact records targets=%v, want=%v", got, want)
	}
	for target := range want {
		if !got[target] {
			t.Fatalf("no verified compact queue record for %s; got %v", target, got)
		}
	}
}

func TestCompactApplyUsesReentrantQueuePaneLock(t *testing.T) {
	previousWait := bptmux.PaneLockWait
	bptmux.PaneLockWait = 8 * time.Second
	t.Cleanup(func() { bptmux.PaneLockWait = previousWait })
	client, _, _ := isolatedIdleAgentTmux(t, "alpha-child")
	a := compactApp(t, nil, nil)
	trustedSenderFixture(a)
	a.ctx = context.Background()
	a.tmux = client
	a.config.MsgqRoot = filepath.Join(a.config.StateDir, "msgq")
	a.queue = msgq.New(a.config.MsgqRoot)
	a.deliverMessage = nil
	started := time.Now()
	if err := a.compact([]string{"--apply"}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed >= bptmux.PaneLockWait/2 {
		t.Fatalf("bp compact --apply took %s, close to pane lock wait %s", elapsed, bptmux.PaneLockWait)
	}
	assertVerifiedCompactRecords(t, a.queue.Root, map[string]bool{"alpha-child": true})
}

func TestWorkflowDriverConcurrentCompactsIsolatedTmux(t *testing.T) {
	previousWait := bptmux.PaneLockWait
	bptmux.PaneLockWait = 8 * time.Second
	t.Cleanup(func() { bptmux.PaneLockWait = previousWait })
	client, _, logs := isolatedIdleAgentTmux(t, "worker-a", "worker-b")
	queueRoot := filepath.Join(t.TempDir(), "msgq")
	a := &app{
		ctx: context.Background(), config: bpconfig.Config{MsgqRoot: queueRoot}, tmux: client,
		queue: msgq.New(queueRoot), out: testOutput(t), err: testOutput(t),
		capturePane: func(string) (string, error) { return idlePane, nil }, clearPane: func(string) error { return nil },
		turnOpenProbe: func(string) bool { return false },
	}
	driver := a.newWorkflowDriverBase("owner")
	started := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, agent := range []string{"worker-a", "worker-b"} {
		agent := agent
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := driver.Compact(context.Background(), agent); err != nil && !errors.Is(err, workflow.ErrCompactQueued) {
				errs <- fmt.Errorf("compact %s: %w", agent, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if elapsed := time.Since(started); elapsed >= bptmux.PaneLockWait/2 {
		t.Fatalf("concurrent compact calls took %s, close to pane lock wait %s", elapsed, bptmux.PaneLockWait)
	}
	if err := a.queue.Dispatch(context.Background(), client, nil); err != nil {
		t.Fatal(err)
	}
	assertVerifiedCompactRecords(t, queueRoot, map[string]bool{"worker-a": true, "worker-b": true})
	for agent, path := range logs {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "compact\n" {
			t.Errorf("agent %s received compact %q err=%v", agent, data, err)
		}
	}
}
