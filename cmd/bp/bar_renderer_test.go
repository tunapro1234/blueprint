package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"blueprint/internal/book"
	bpconfig "blueprint/internal/config"
	bptmux "blueprint/internal/tmux"
)

func TestBarRendererAttachedOnlyAndChangesOnly(t *testing.T) {
	sessions := "attached\t1\nunattached\t0\nclosed\t1\n"
	agents := map[string]book.Agent{
		"attached":   {Name: "attached", Status: "open"},
		"unattached": {Name: "unattached", Status: "open"},
		// Recorded closed while its session is alive and attached: the CLI
		// fallback renders it, so the daemon must too.
		"closed": {Name: "closed", Status: "closed"},
	}
	renderer, ops := newTestBarRenderer(t, sessions, book.Fleet{Agents: agents})
	loads := 0
	renderer.loadFleet = func() (book.Fleet, error) {
		loads++
		return book.Fleet{Agents: agents}, nil
	}
	var mu sync.Mutex
	var rendered []string
	renderer.render = func(_ context.Context, name string, _ bptmux.PaneProcess, _ book.Fleet, _ []string) (barRenderOutput, error) {
		mu.Lock()
		rendered = append(rendered, name)
		mu.Unlock()
		return barRenderOutput{name: "plate", bar: "line", style: "style"}, nil
	}
	if err := renderer.scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	sort.Strings(rendered)
	if strings.Join(rendered, ",") != "attached,closed" {
		t.Fatalf("rendered %v, want the attached sessions only", rendered)
	}
	if loads != 1 {
		t.Fatalf("agentbook loads=%d, want one per scan", loads)
	}
	first := readBarRendererOps(t, ops)
	if got := strings.Count(first, "list-panes -a -F"); got != 1 {
		t.Fatalf("session scans=%d, want one list-panes -a request", got)
	}
	if got := countTmuxSets(first); got != 6 {
		t.Fatalf("first scan set-option calls=%d, want 6 (name, bar, style for two agents): %s", got, first)
	}
	if strings.Contains(first, "=unattached:") {
		t.Fatalf("detached session was touched: %s", first)
	}
	if err := os.WriteFile(ops, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := renderer.scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countTmuxSets(readBarRendererOps(t, ops)); got != 0 {
		t.Fatalf("unchanged scan set-option calls=%d, want zero", got)
	}
	if loads != 2 {
		t.Fatalf("agentbook loads=%d after two scans, want 2", loads)
	}
	renderer.render = func(_ context.Context, name string, _ bptmux.PaneProcess, _ book.Fleet, _ []string) (barRenderOutput, error) {
		if name != "attached" {
			return barRenderOutput{name: "plate", bar: "line", style: "style"}, nil
		}
		return barRenderOutput{name: "plate", bar: "changed", style: "style"}, nil
	}
	if err := renderer.scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	changed := readBarRendererOps(t, ops)
	if got := countTmuxSets(changed); got != 1 || !strings.Contains(changed, "=attached: @bp-bar changed") {
		t.Fatalf("changed bar set-option calls=%d, want one bar update: %s", got, changed)
	}
	if loads != 3 {
		t.Fatalf("agentbook loads=%d after three scans, want 3", loads)
	}
}

// Unregistered and archived sessions are not rendered. One that still carries
// pushed text from an earlier render has it unset, so its #() fallback takes
// over instead of showing frozen text; one without leftovers and every
// detached session cost no tmux call beyond the scan listing.
func TestBarRendererClearsLeftoversOnSessionsItNoLongerRenders(t *testing.T) {
	sessions := "" +
		"agent\t1\t$1\t1\t1\tbash\t10\t1\t1\n" +
		"unregistered\t1\t$2\t1\t1\tbash\t20\t1\t1\n" +
		"archived\t2\t$3\t1\t1\tbash\t30\t1\t0\n" +
		"plain\t1\t$4\t1\t1\tbash\t40\t0\t0\n" +
		"detached-stale\t0\t$5\t1\t1\tbash\t50\t1\t1\n" +
		"detached-closed\t0\t$6\t1\t1\tbash\t60\t0\t0\n"
	renderer, ops := newTestBarRenderer(t, "", book.Fleet{Agents: map[string]book.Agent{
		"agent":           {Name: "agent", Status: "closed"},
		"archived":        {Name: "archived", Status: "closed", ArchivedAt: "2026-11-01T00:00:00Z"},
		"detached-closed": {Name: "detached-closed", Status: "closed"},
	}})
	sessionsFile := filepath.Join(filepath.Dir(ops), "sessions")
	if err := os.WriteFile(sessionsFile, []byte(sessions), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var rendered []string
	renderer.render = func(_ context.Context, name string, _ bptmux.PaneProcess, _ book.Fleet, _ []string) (barRenderOutput, error) {
		mu.Lock()
		rendered = append(rendered, name)
		mu.Unlock()
		return barRenderOutput{name: "plate", bar: "line", style: "style"}, nil
	}
	// The renderer once pushed text into "unregistered"; that memory must go
	// with the cleanup, or a later re-registration would be skipped as unchanged.
	renderer.last["unregistered"] = renderedBar{name: "plate", bar: "line", hasName: true, hasBar: true}
	renderer.sessionIDs["unregistered"] = "$2"
	if err := renderer.scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(rendered, ",") != "agent" {
		t.Fatalf("rendered %v, want the registered attached agent only", rendered)
	}
	got := readBarRendererOps(t, ops)
	var unsets []string
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		if strings.HasPrefix(line, "set-option -u ") {
			unsets = append(unsets, line)
		}
	}
	sort.Strings(unsets)
	want := []string{
		"set-option -u -t =archived: @bp-bar",
		"set-option -u -t =unregistered: @bp-bar",
		"set-option -u -t =unregistered: @bp-name",
	}
	if strings.Join(unsets, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unsets=%q, want %q; ops=%s", unsets, want, got)
	}
	for _, session := range []string{"plain", "detached-stale", "detached-closed"} {
		if strings.Contains(got, "="+session+":") {
			t.Fatalf("session %s was touched: %s", session, got)
		}
	}
	if _, kept := renderer.last["unregistered"]; kept {
		t.Fatal("cleared session kept its skip-unchanged memory")
	}

	// Once tmux reports the options gone, nothing is unset again.
	cleaned := strings.NewReplacer("\t20\t1\t1\n", "\t20\t0\t0\n", "\t30\t1\t0\n", "\t30\t0\t0\n").Replace(sessions)
	if err := os.WriteFile(sessionsFile, []byte(cleaned), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ops, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := renderer.scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readBarRendererOps(t, ops); strings.Contains(got, "set-option") || strings.Count(got, "\n") != 1 {
		t.Fatalf("steady scan ran more than the listing: %s", got)
	}
}

// The daemon must push exactly what `bp bar` / `bp name` print for the same
// session; a closed Status in the agentbook changes who renders, not the text.
func TestBarRendererMatchesCLIFallbackForClosedAgent(t *testing.T) {
	dir := t.TempDir()
	ops := filepath.Join(dir, "tmux-ops")
	bin := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + quoteShell(ops) + "\ncase \"$1 $2\" in\n" +
		"'list-panes -a') printf 'agent\\t1\\t$1\\t1\\t1\\tbash\\t41\\t0\\t0\\n' ;;\n" +
		"'list-panes -t') printf '1\\tbash\\t41\\n' ;;\n" +
		"capture-pane*) printf 'shell prompt\\n' ;;\n" +
		"*) exit 0 ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ops, nil, 0600); err != nil {
		t.Fatal(err)
	}
	bookPath := filepath.Join(dir, "agentbook.json")
	writeBarTestFile(t, bookPath, `{"agents":[{"name":"agent","status":"closed","folder":`+strconv.Quote(dir)+`,"colorOverride":"44"}]}`)
	a := &app{ctx: context.Background(), tmux: &bptmux.Client{Bin: bin}, out: testOutput(t), config: bpconfig.Config{
		Agentbooks: []string{bookPath},
		Bar:        bpconfig.BarConfig{Widgets: []string{"ctx", "temp", "talk"}},
	}}
	cliBar, cliName := a.barLine("agent"), a.barName("agent")
	if !strings.Contains(cliBar, "×") || !strings.Contains(cliName, " agent ") {
		t.Fatalf("CLI fallback did not render a real bar: bar=%q name=%q", cliBar, cliName)
	}

	renderer := newBarRenderer(a, nil)
	var logs bytes.Buffer
	renderer.logger = log.New(&logs, "", 0)
	if err := os.WriteFile(ops, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := renderer.scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := readBarRendererOps(t, ops)
	for _, want := range []string{
		"set-option -t =agent: @bp-bar " + escapeTmuxBarFormat(cliBar) + "\n",
		"set-option -t =agent: @bp-name " + escapeTmuxBarFormat(cliName) + "\n",
		"set-option -t =agent: status-style " + barStatusStyle("44") + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("renderer did not push the CLI text %q; ops=%s logs=%s", want, got, logs.String())
		}
	}
	if logs.Len() != 0 {
		t.Fatalf("renderer reported a failure for a closed-status agent: %s", logs.String())
	}
}

func TestBarRendererRewritesRecreatedSession(t *testing.T) {
	renderer, ops := newTestBarRenderer(t, "agent\t1\t$1\n", book.Fleet{Agents: map[string]book.Agent{
		"agent": {Name: "agent", Status: "open"},
	}})
	renderer.render = func(_ context.Context, _ string, _ bptmux.PaneProcess, _ book.Fleet, _ []string) (barRenderOutput, error) {
		return barRenderOutput{name: "plate", bar: "line", style: "style"}, nil
	}
	sessionsFile := filepath.Join(filepath.Dir(ops), "sessions")
	scan := func(sessions string) int {
		t.Helper()
		if err := os.WriteFile(sessionsFile, []byte(testSessionPanes(sessions)), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ops, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := renderer.scan(context.Background()); err != nil {
			t.Fatal(err)
		}
		return countTmuxSets(readBarRendererOps(t, ops))
	}
	if got := scan("agent\t1\t$1\n"); got != 3 {
		t.Fatalf("first scan sets=%d, want 3", got)
	}
	if got := scan("agent\t1\t$1\n"); got != 0 {
		t.Fatalf("unchanged scan sets=%d, want 0", got)
	}
	// Closed and reopened under the same name: tmux gives a new id and the new
	// session has none of the old options, so identical text must be set again.
	if got := scan("agent\t1\t$7\n"); got != 3 {
		t.Fatalf("recreated session sets=%d, want 3", got)
	}
	// Gone for one scan, then back with the same id: still rewritten.
	if got := scan(""); got != 0 {
		t.Fatalf("empty scan sets=%d, want 0", got)
	}
	if got := scan("agent\t1\t$7\n"); got != 3 {
		t.Fatalf("returning session sets=%d, want 3", got)
	}
}

func TestBarRendererSlowAgentDoesNotBlockFastAgent(t *testing.T) {
	renderer, ops := newTestBarRenderer(t, "slow\t1\nfast\t1\n", book.Fleet{Agents: map[string]book.Agent{
		"slow": {Name: "slow", Status: "open"}, "fast": {Name: "fast", Status: "open"},
	}})
	renderer.renderTimeout = 80 * time.Millisecond
	releaseSlow := make(chan struct{})
	renderer.render = func(ctx context.Context, name string, _ bptmux.PaneProcess, _ book.Fleet, _ []string) (barRenderOutput, error) {
		if name == "slow" {
			<-releaseSlow // Simulate a renderer dependency that ignores cancellation.
			return barRenderOutput{}, nil
		}
		return barRenderOutput{name: "fast-plate", bar: "fast-line", style: "fast-style"}, nil
	}
	start := time.Now()
	if err := renderer.scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(releaseSlow)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("slow agent delayed scan by %v", elapsed)
	}
	opsText := readBarRendererOps(t, ops)
	if !strings.Contains(opsText, "@bp-name fast-plate") {
		t.Fatalf("fast agent was not rendered while slow agent timed out: %s", opsText)
	}
	if strings.Contains(opsText, "@bp-name slow") {
		t.Fatalf("timed out agent overwrote its prior text: %s", opsText)
	}
}

func TestBarRendererProductionPathUsesOneFleetAndSessionScan(t *testing.T) {
	dir := t.TempDir()
	ops := filepath.Join(dir, "tmux-ops")
	bin := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + quoteShell(ops) + "\ncase \"$1\" in\n" +
		"list-panes) printf 'agent\\t1\\t$1\\t0\\t1\\tvim\\t41\\nagent\\t1\\t$1\\t1\\t0\\tbash\\t42\\n' ;;\n" +
		"capture-pane) printf 'shell prompt\\n' ;;\n" +
		"*) exit 0 ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ops, nil, 0600); err != nil {
		t.Fatal(err)
	}
	tmux := &bptmux.Client{Bin: bin}
	fleet := book.Fleet{Agents: map[string]book.Agent{
		"agent": {Name: "agent", Status: "open", Folder: dir, ColorOverride: "44"},
	}}
	loads := 0
	a := &app{ctx: context.Background(), tmux: tmux, config: bpconfig.Config{Bar: bpconfig.BarConfig{Widgets: []string{"clock"}}}}
	renderer := newBarRenderer(a, nil)
	renderer.loadFleet = func() (book.Fleet, error) {
		loads++
		return fleet, nil
	}
	if err := renderer.scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	commands := readBarRendererOps(t, ops)
	if loads != 1 || strings.Count(commands, "list-panes -a -F") != 1 {
		t.Fatalf("production scan repeated shared loads: fleet=%d sessions=%d; commands=%s", loads, strings.Count(commands, "list-panes -a -F"), commands)
	}
	// The scan listing already carries the pane process; a per-agent
	// list-panes would bring back one exec per attached agent per scan.
	if got := strings.Count(commands, "list-panes"); got != 1 {
		t.Fatalf("list-panes calls=%d, want only the scan listing; commands=%s", got, commands)
	}
}

func BenchmarkBarRendererScan15Sessions(b *testing.B) {
	dir := b.TempDir()
	ops := filepath.Join(dir, "tmux-ops")
	bin := filepath.Join(dir, "tmux")
	var sessionOutput strings.Builder
	agents := make(map[string]book.Agent, 15)
	for i := 0; i < 15; i++ {
		name := fmt.Sprintf("agent-%02d", i)
		fmt.Fprintf(&sessionOutput, "%s\t1\t$%d\t1\t1\tbash\t%d\n", name, i, 100+i)
		agents[name] = book.Agent{Name: name, Status: "open"}
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + quoteShell(ops) + "\ncase \"$1\" in\n" +
		"list-panes) cat <<'SESSIONS'\n" + sessionOutput.String() + "SESSIONS\n;;\n" +
		"*) exit 0 ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		b.Fatal(err)
	}
	tmux := &bptmux.Client{Bin: bin}
	renderer := &barRenderer{
		app: &app{tmux: tmux}, tmux: tmux,
		loadFleet: func() (book.Fleet, error) { return book.Fleet{Agents: agents}, nil },
		last:      make(map[string]renderedBar), failures: make(map[string]string),
		models: newBarModelCache(), renderTimeout: time.Second,
	}
	renderer.render = func(_ context.Context, name string, _ bptmux.PaneProcess, _ book.Fleet, _ []string) (barRenderOutput, error) {
		return barRenderOutput{name: name, bar: "line", style: "style"}, nil
	}
	firstBefore := rendererCPUTime()
	if err := renderer.scan(context.Background()); err != nil {
		b.Fatal(err)
	}
	firstCPU := rendererCPUTime() - firstBefore
	b.ResetTimer()
	steadyBefore := rendererCPUTime()
	for i := 0; i < b.N; i++ {
		if err := renderer.scan(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
	steadyCPU := rendererCPUTime() - steadyBefore
	b.ReportMetric(float64(firstCPU.Nanoseconds()), "first-scan-cpu-ns")
	b.ReportMetric(float64(steadyCPU.Nanoseconds())/float64(b.N), "steady-scan-cpu-ns")
}

func rendererCPUTime() time.Duration {
	var self, children syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &self)
	_ = syscall.Getrusage(syscall.RUSAGE_CHILDREN, &children)
	toDuration := func(value syscall.Timeval) time.Duration {
		return time.Duration(value.Sec)*time.Second + time.Duration(value.Usec)*time.Microsecond
	}
	return toDuration(self.Utime) + toDuration(self.Stime) + toDuration(children.Utime) + toDuration(children.Stime)
}

func TestBarRendererClearUnsetsOnlyOptionsItSet(t *testing.T) {
	renderer, ops := newTestBarRenderer(t, "one\t1\n", book.Fleet{Agents: map[string]book.Agent{
		"one": {Name: "one", Status: "open"},
	}})
	renderer.render = func(_ context.Context, _ string, _ bptmux.PaneProcess, _ book.Fleet, _ []string) (barRenderOutput, error) {
		return barRenderOutput{name: "plate", bar: "line", style: "style"}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		renderer.run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && countTmuxSets(readBarRendererOps(t, ops)) < 3 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := countTmuxSets(readBarRendererOps(t, ops)); got != 3 {
		cancel()
		t.Fatal("renderer did not set its options before shutdown")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("renderer did not finish cleanup after cancellation")
	}
	got := readBarRendererOps(t, ops)
	if strings.Count(got, "set-option -u") != 2 || strings.Contains(got, "-u -t =one: status-style") {
		t.Fatalf("cleanup should unset name and bar only: %s", got)
	}
}

func TestBarRendererLogsOnlyFailureStateChanges(t *testing.T) {
	renderer, _ := newTestBarRenderer(t, "one\t1\n", book.Fleet{Agents: map[string]book.Agent{
		"one": {Name: "one", Status: "open"},
	}})
	var logs bytes.Buffer
	renderer.logger = log.New(&logs, "", 0)
	fail := true
	renderer.render = func(_ context.Context, _ string, _ bptmux.PaneProcess, _ book.Fleet, _ []string) (barRenderOutput, error) {
		if fail {
			return barRenderOutput{}, fmt.Errorf("pane unavailable")
		}
		return barRenderOutput{name: "plate", bar: "line", style: "style"}, nil
	}
	for i := 0; i < 2; i++ {
		if err := renderer.scan(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Count(logs.String(), "bar render one: pane unavailable"); got != 1 {
		t.Fatalf("repeated failure logged %d times: %q", got, logs.String())
	}
	fail = false
	if err := renderer.scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(logs.String(), "bar render one recovered"); got != 1 {
		t.Fatalf("recovery transition logged %d times: %q", got, logs.String())
	}
}

func TestEscapeTmuxBarFormat(t *testing.T) {
	got := escapeTmuxBarFormat("#[bg=colour236] hello#world #[] #[default]")
	want := "#[bg=colour236] hello##world ##[] #[default]"
	if got != want {
		t.Fatalf("escaped format=%q, want %q", got, want)
	}
}

func TestBarModelCacheUsesPathAndFileMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"model":"first","effortLevel":"low"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cache := newBarModelCache()
	readCount := 0
	reader := func(path string) (string, string) {
		readCount++
		return readCodexModel(path)
	}
	firstModel, firstEffort := cache.read(path, reader)
	secondModel, secondEffort := cache.read(path, reader)
	if firstModel != "" || firstEffort != "" || secondModel != firstModel || secondEffort != firstEffort {
		t.Fatalf("unexpected cached values: %q/%q then %q/%q", firstModel, firstEffort, secondModel, secondEffort)
	}
	if readCount != 1 {
		t.Fatalf("same metadata caused %d reads, want one", readCount)
	}
	if err := os.WriteFile(path, []byte("model = \"gpt-6-sol\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Force a metadata change even on filesystems with coarse mtime resolution.
	if err := os.Chtimes(path, time.Now().Add(time.Second), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	changedModel, changedEffort := cache.read(path, reader)
	if changedModel != "gpt-6-sol" || changedEffort != "" || readCount != 2 {
		t.Fatalf("changed file did not invalidate cache: value=%q/%q reads=%d", changedModel, changedEffort, readCount)
	}
}

func TestBarModelCacheRetainsRolloutAndClaudeResultsUntilFileChanges(t *testing.T) {
	root := t.TempDir()
	rollout := filepath.Join(root, "rollout.jsonl")
	if err := os.WriteFile(rollout, []byte("first rollout"), 0600); err != nil {
		t.Fatal(err)
	}
	cache := newBarModelCache()
	if model, effort := cache.remember(rollout, "gpt-6-sol", "high"); model != "gpt-6-sol" || effort != "high" {
		t.Fatalf("first rollout values=%q/%q", model, effort)
	}
	if model, effort := cache.remember(rollout, "gpt-6-luna", "max"); model != "gpt-6-sol" || effort != "high" {
		t.Fatalf("unchanged rollout did not reuse cache: %q/%q", model, effort)
	}
	if err := os.WriteFile(rollout, []byte("updated rollout with changed size"), 0600); err != nil {
		t.Fatal(err)
	}
	changed := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(rollout, changed, changed); err != nil {
		t.Fatal(err)
	}
	if model, effort := cache.remember(rollout, "gpt-6-astra", "medium"); model != "gpt-6-astra" || effort != "medium" {
		t.Fatalf("changed rollout retained stale cache: %q/%q", model, effort)
	}

	local := filepath.Join(root, "agent", ".claude", "settings.local.json")
	writeBarTestFile(t, local, `{"model":"claude-sonnet-4","effortLevel":"low"}`)
	if model, effort, _ := cache.readClaudeFile(local); model != "claude-sonnet-4" || effort != "low" {
		t.Fatalf("first Claude file values=%q/%q", model, effort)
	}
	if err := os.WriteFile(local, []byte(`{"model":"claude-opus-5","effortLevel":"high"}`), 0600); err != nil {
		t.Fatal(err)
	}
	changed = time.Now().Add(2 * time.Second)
	if err := os.Chtimes(local, changed, changed); err != nil {
		t.Fatal(err)
	}
	if model, effort, _ := cache.readClaudeFile(local); model != "claude-opus-5" || effort != "high" {
		t.Fatalf("changed Claude file retained stale cache: %q/%q", model, effort)
	}
}

func newTestBarRenderer(t *testing.T, sessions string, fleet book.Fleet) (*barRenderer, string) {
	t.Helper()
	dir := t.TempDir()
	ops := filepath.Join(dir, "tmux-ops")
	sessionsFile := filepath.Join(dir, "sessions")
	if err := os.WriteFile(sessionsFile, []byte(testSessionPanes(sessions)), 0600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + quoteShell(ops) + "\ncase \"$1\" in\n" +
		"list-panes) cat " + quoteShell(sessionsFile) + "\n;;\n" +
		"*) exit 0 ;;\nesac\n"
	bin := filepath.Join(dir, "tmux")
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ops, nil, 0600); err != nil {
		t.Fatal(err)
	}
	tmux := &bptmux.Client{Bin: bin}
	renderer := &barRenderer{
		app: &app{tmux: tmux}, tmux: tmux,
		loadFleet: func() (book.Fleet, error) { return fleet, nil },
		last:      make(map[string]renderedBar), sessionIDs: make(map[string]string), failures: make(map[string]string),
		models: newBarModelCache(), renderTimeout: time.Second,
	}
	return renderer, ops
}

// testSessionPanes turns "name\tattached[\tid]" lines into the list-panes -a
// rows the scan reads, one active pane per session.
func testSessionPanes(sessions string) string {
	var out strings.Builder
	for i, line := range strings.Split(strings.TrimSpace(sessions), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		id := fmt.Sprintf("$%d", 100+i)
		if len(fields) > 2 {
			id = fields[2]
		}
		fmt.Fprintf(&out, "%s\t%s\t%s\t1\t1\tbash\t%d\n", fields[0], fields[1], id, 1000+i)
	}
	return out.String()
}

func readBarRendererOps(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func countTmuxSets(ops string) int {
	count := 0
	for _, line := range strings.Split(ops, "\n") {
		if strings.HasPrefix(line, "set-option -t ") {
			count++
		}
	}
	return count
}
