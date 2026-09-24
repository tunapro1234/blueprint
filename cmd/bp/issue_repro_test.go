package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blueprint/internal/cache"
	bpconfig "blueprint/internal/config"
	"blueprint/internal/identity"
	"blueprint/internal/msgq"
	bptmux "blueprint/internal/tmux"
)

func issueWriteBook(t *testing.T, path string, agents []map[string]any) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"orchestrator": "server-main", "agents": agents})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// issueTmux is a private executable used only by these tests. It never consults
// the host tmux socket or a live session.
func issueTmux(t *testing.T, pane string, initiallyLive bool) (*bptmux.Client, string, string) {
	t.Helper()
	dir := t.TempDir()
	bin, marker := filepath.Join(dir, "tmux"), filepath.Join(dir, "session")
	log, paneFile := filepath.Join(dir, "calls"), filepath.Join(dir, "pane")
	if initiallyLive {
		if err := os.WriteFile(marker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(paneFile, []byte(pane), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"log=" + quoteShell(log) + "\nmarker=" + quoteShell(marker) + "\npane=" + quoteShell(paneFile) + "\n" +
		"printf '%s\\n' \"$*\" >> \"$log\"\n" +
		"case \"$1\" in\n" +
		"has-session) [ -f \"$marker\" ] ;;\n" +
		"new-session) touch \"$marker\" ;;\n" +
		"capture-pane) cat \"$pane\" ;;\n" +
		"display-message) echo claude ;;\n" +
		"list-panes) printf '1\\tzsh\\t123\\n' ;;\n" +
		"load-buffer) cat >/dev/null ;;\n" +
		"set-option|set-environment|send-keys|paste-buffer|list-clients|if-shell) exit 0 ;;\n" +
		"show-environment) printf 'BP_HOME=%s\\n' \"$BP_HOME\" ;;\n" +
		"*) exit 0 ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return &bptmux.Client{Bin: bin, Sleep: func(time.Duration) {}, Now: time.Now}, log, marker
}

func issueApp(t *testing.T, bookPath, stateDir string, tmux *bptmux.Client) *app {
	t.Helper()
	t.Setenv("AGENTBOOK", "")
	a := &app{
		ctx:    context.Background(),
		config: bpconfig.Config{Agentbooks: []string{bookPath}, StateDir: stateDir, Home: t.TempDir(), LocalObservation: true},
		tmux:   tmux,
		out:    testOutput(t),
		err:    testOutput(t),
	}
	a.resolveSender = func() identity.Identity {
		return identity.Identity{Label: "server-main", Certain: true, Source: "test"}
	}
	return a
}

func issueCalls(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestIssue03_StatusReconcilesAnOpenRecordWithoutATmuxSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agentbook.json")
	issueWriteBook(t, path, []map[string]any{
		{"name": "server-main", "folder": dir, "status": "open"},
		{"name": "ghost", "folder": dir, "parent": "server-main", "status": "open"},
	})
	tmux, _, _ := issueTmux(t, "", false)
	a := issueApp(t, path, filepath.Join(dir, "state"), tmux)
	a.loadCache = func(map[string]string) map[string]cache.State { return nil }
	if err := a.status(nil); err != nil {
		t.Fatal(err)
	}
	if got := bookStatus(t, path, "ghost"); got != "closed" {
		t.Fatalf("bp status left a vanished agent at %q; want automatic reconciliation to closed", got)
	}
}

func TestIssue03_CloseDoesNotClaimAStaleOpenRecordWasAlreadyClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agentbook.json")
	issueWriteBook(t, path, []map[string]any{{"name": "ghost", "folder": dir, "status": "open"}})
	tmux, _, _ := issueTmux(t, "", false)
	a := issueApp(t, path, filepath.Join(dir, "state"), tmux)
	if err := a.close([]string{"ghost"}); err != nil {
		t.Fatal(err)
	}
	if got := bookStatus(t, path, "ghost"); got != "closed" {
		t.Fatalf("status=%q, want closed", got)
	}
	if got := readTestOutput(t, a.out); strings.Contains(got, "already closed") {
		t.Fatalf("close misreported a real transition as a no-op: %q", got)
	}
}

func TestIssue09_UnresolvedSenderCannotPassAsACertainAgentName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agentbook.json")
	issueWriteBook(t, path, []map[string]any{{"name": "target", "folder": dir, "status": "open"}})
	tmux, _, _ := issueTmux(t, "", true)
	a := issueApp(t, path, filepath.Join(dir, "state"), tmux)
	a.sessionExists = func(string) bool { return true }
	a.resolveSender = func() identity.Identity {
		return identity.Identity{Label: "claude-e558-cwd", Certain: false, Source: "unresolved-pane"}
	}
	var delivered string
	a.deliverMessage = func(_, sender, _ string) (bool, string, error) {
		delivered = sender
		return false, "", nil
	}
	if err := a.message([]string{"target", "hello"}); err != nil {
		t.Fatal(err)
	}
	if identity.ValidName(delivered) {
		t.Fatalf("unverified source was rendered as a valid agent identity %q", delivered)
	}
}

func TestIssue10_OpenClaudeUsesManagedLaunchAndObserver(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agentbook.json")
	issueWriteBook(t, path, []map[string]any{
		{"name": "server-main", "folder": dir, "status": "open"},
		{"name": "ghost", "folder": dir, "parent": "server-main", "status": "closed"},
	})
	tmux, log, _ := issueTmux(t, "bypass permissions\n", false)
	a := issueApp(t, path, filepath.Join(dir, "state"), tmux)
	if err := a.open([]string{"ghost", dir, "--claude", "--no-prompt"}); err != nil {
		t.Fatal(err)
	}
	calls := issueCalls(t, log)
	// --settings (observer hooks) is added inside _open-session; at this layer
	// the managed launcher itself must wrap the Claude command.
	if !strings.Contains(calls, "_open-session 'ghost' claude") {
		t.Fatalf("Claude was launched without the managed launcher/observer: %s", calls)
	}
}

func TestIssue12_RenameMigratesPendingQueueTargets(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	path := filepath.Join(dir, "agentbook.json")
	issueWriteBook(t, path, []map[string]any{{"name": "old-name", "folder": dir, "status": "open"}})
	tmux, _, _ := issueTmux(t, "", false)
	a := issueApp(t, path, state, tmux)
	a.queue = msgq.New(state)
	id, err := a.queue.Enqueue("old-name", "server-main", "pending instruction")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.rename([]string{"old-name", "new-name", "--no-retitle"}); err != nil {
		t.Fatal(err)
	}
	status, err := a.queue.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(status, "old-name") || !strings.Contains(status, "new-name") {
		t.Fatalf("pending message was not migrated with rename: %s", status)
	}
}

func TestIssue13_OpenAppliesTheBpBarToItsSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agentbook.json")
	issueWriteBook(t, path, []map[string]any{{"name": "ghost", "folder": dir, "status": "closed"}})
	tmux, log, _ := issueTmux(t, "bypass permissions\n", false)
	a := issueApp(t, path, filepath.Join(dir, "state"), tmux)
	if err := a.open([]string{"ghost", dir, "--claude", "--no-prompt"}); err != nil {
		t.Fatal(err)
	}
	calls := issueCalls(t, log)
	for _, option := range []string{"status-left", "status-right", "status-style", "status-left-length", "status-right-length", "status-interval"} {
		if !strings.Contains(calls, option) {
			t.Fatalf("bp open did not configure session option %q: %s", option, calls)
		}
	}
}

func TestIssue14_ArchivedClaudeResumeDoesNotBecomeTheNextRunName(t *testing.T) {
	dir, home, state := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	const thread = "22222222-2222-2222-2222-222222222222"
	obsDir := filepath.Join(state, "local", "old-run")
	if err := os.MkdirAll(obsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	obsPath := filepath.Join(obsDir, "observation.json")
	obs, _ := json.Marshal(map[string]any{"session_id": thread, "cwd": dir, "transcript_path": filepath.Join(home, "projects", "thread.jsonl"), "observed_at": time.Now().UTC()})
	if err := os.WriteFile(obsPath, obs, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agentbook.json")
	archivedName := "claude-" + thread
	issueWriteBook(t, path, []map[string]any{
		{"name": archivedName, "folder": dir, "status": "closed", "archivedAt": "2026-09-01T00:00:00Z", "localRuntime": map[string]any{"path": obsPath, "pid": 77, "harness": "claude", "cwd": dir}},
	})
	tmux, _, _ := issueTmux(t, "", false)
	a := issueApp(t, path, state, tmux)
	guard, existing, err := a.guardClaudeResume(thread, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	if guard != nil {
		defer guard.close()
	}
	if existing == archivedName || guard.name == archivedName {
		t.Fatalf("archived registration blocked or captured a new run: existing=%q guard=%+v", existing, guard)
	}
}

func TestIssue17_OpenCodeGetsARuntimeObservationPlan(t *testing.T) {
	a := &app{config: bpconfig.Config{LocalObservation: true, StateDir: t.TempDir()}}
	args, path, err := a.prepareLocalObservation("opencode", []string{"--model", "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	if path == "" || len(args) != 2 || args[0] != "--model" {
		t.Fatalf("opencode launch has no runtime observation plan: args=%v path=%q", args, path)
	}
}

// #18: the worker used to read the pane once and gave up (no reap) when tmux
// had not marked it dead yet, or reported "status , signal " as a failure.
func TestIssue18_OpenCodeExitReapsItsDeadPane(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	path := filepath.Join(dir, "agentbook.json")
	issueWriteBook(t, path, []map[string]any{{"name": "opencode-agent", "folder": dir, "status": "open"}})
	bin, log, stage := filepath.Join(dir, "tmux"), filepath.Join(dir, "calls"), filepath.Join(dir, "stage")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + quoteShell(log) + "\n" +
		"case \"$1\" in\n" +
		// tmux sees the exit in stages: pane still alive when the worker notices
		// its parent is gone, then dead without a reaped status, then status 0.
		"display-message) case \"$*\" in *pane_id*) n=$(cat " + quoteShell(stage) + " 2>/dev/null || echo 0); echo $((n+1)) > " + quoteShell(stage) + "\n" +
		"  case $n in 0) printf '%%1\\t99999999\\t0\\t\\t\\n' ;; 1) printf '%%1\\t99999999\\t1\\t\\t\\n' ;; *) printf '%%1\\t99999999\\t1\\t0\\t\\n' ;; esac ;;\n" +
		"  *) printf '99999999\\topencode-agent\\n' ;; esac ;;\n" +
		"if-shell|kill-pane|set-option) exit 0 ;;\n*) exit 0 ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_PANE", "%1")
	report := filepath.Join(state, "exit.json")
	t.Setenv("BP_EXIT_REPORT", report)
	a := issueApp(t, path, state, &bptmux.Client{Bin: bin, Sleep: func(time.Duration) {}, Now: time.Now})
	if err := a.localWorker([]string{"opencode-agent", "99999999", "opencode"}); err != nil {
		t.Fatal(err)
	}
	if calls := issueCalls(t, log); !strings.Contains(calls, "if-shell") || !strings.Contains(calls, "kill-pane") {
		t.Fatalf("clean opencode exit did not reap its dead pane: %s", calls)
	}
	if data, err := os.ReadFile(report); err != nil || !strings.Contains(string(data), `"status":"0"`) {
		t.Fatalf("exit report=%s err=%v, want the reaped status 0, not a status-less false failure", data, err)
	}
	if got := bookStatus(t, path, "opencode-agent"); got != "closed" {
		t.Fatalf("exit did not close the agentbook record: %s", got)
	}
}

func TestIssue23_RunHelpDocumentsParentAndRole(t *testing.T) {
	// Other commands document --parent too; only the bp run line counts.
	for _, line := range strings.Split(usage, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "bp run ") {
			if !strings.Contains(line, "[--parent <name>]") || !strings.Contains(line, "[--role <text>]") {
				t.Fatalf("bp run help does not expose parent/role registration: %s", line)
			}
			return
		}
	}
	t.Fatal("usage has no bp run line")
}

func TestIssue24_OpenRefusesToRebindAnExistingAgentFolder(t *testing.T) {
	oldDir, newDir := t.TempDir(), t.TempDir()
	path := filepath.Join(t.TempDir(), "agentbook.json")
	issueWriteBook(t, path, []map[string]any{{"name": "ghost", "folder": oldDir, "status": "closed"}})
	tmux, log, marker := issueTmux(t, "› Ask Codex to do anything\n", false)
	a := issueApp(t, path, filepath.Join(t.TempDir(), "state"), tmux)
	err := a.open([]string{"ghost", newDir, "--codex"})
	if err == nil || !strings.Contains(err.Error(), "bound") {
		t.Fatalf("open should refuse a folder rebind with a conflict explanation, got %v", err)
	}
	calls, _ := os.ReadFile(log) // no tmux call at all is the ideal refusal
	if _, err := os.Stat(marker); err == nil || strings.Contains(string(calls), "new-session") {
		t.Fatal("open started a new pane before refusing the rebind")
	}
}

func TestIssue25_OpenPreservesNativeLaunchModeFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agentbook.json")
	issueWriteBook(t, path, []map[string]any{
		{"name": "server-main", "folder": dir, "status": "open"},
		{"name": "ghost", "folder": dir, "status": "closed", "launch": map[string]any{
			"codex": true, "noSandbox": true, "args": []string{"--search", "-m", "gpt-test", "-c", "model_reasoning_effort=high"},
		}},
	})
	tmux, log, _ := issueTmux(t, "› Ask Codex to do anything\n", false)
	a := issueApp(t, path, filepath.Join(dir, "state"), tmux)
	if err := a.open([]string{"ghost", dir}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Agents []struct {
			Name   string         `json:"name"`
			Launch map[string]any `json:"launch"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, agent := range raw.Agents {
		if agent.Name == "ghost" {
			if agent.Launch["noSandbox"] != true || !strings.Contains(strings.Join(anyStrings(agent.Launch["args"]), " "), "-m gpt-test -c model_reasoning_effort=high") {
				t.Fatalf("launch mode fields were lost on revive: %#v", agent.Launch)
			}
			if calls := issueCalls(t, log); !strings.Contains(calls, "--search") || !strings.Contains(calls, "gpt-test") {
				t.Fatalf("saved native flags were not reused by the launch: %s", calls)
			}
			return
		}
	}
	t.Fatal("revived agent missing from book")
}

func anyStrings(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}
