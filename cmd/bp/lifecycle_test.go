package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blueprint/internal/book"
	"blueprint/internal/cache"
	"blueprint/internal/config"
	"blueprint/internal/identity"
	"blueprint/internal/msgq"
	bptmux "blueprint/internal/tmux"
)

func writeLifecycleBook(t *testing.T, path string, agents ...book.Agent) {
	t.Helper()
	data, err := json.Marshal(book.File{Orchestrator: "root", Agents: append([]book.Agent{{Name: "root"}}, agents...)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func lifecycleTmux(t *testing.T, sessions []string, failPaneProcess bool) *bptmux.Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tmux")
	var sessionRows strings.Builder
	for _, session := range sessions {
		sessionRows.WriteString("printf '%s\\n' '")
		sessionRows.WriteString(session)
		sessionRows.WriteString("'\n")
	}
	var hasSession strings.Builder
	hasSession.WriteString("case \"$3\" in\n")
	for _, session := range sessions {
		hasSession.WriteString("  =")
		hasSession.WriteString(session)
		hasSession.WriteString(") exit 0 ;;\n")
	}
	hasSession.WriteString("  *) exit 1 ;;\nesac")
	paneProcess := "printf '1\\tzsh\\t42\\n'"
	if failPaneProcess {
		paneProcess = "exit 1"
	}
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"list-sessions)\n" + sessionRows.String() + "  exit 0 ;;\n" +
		"has-session)\n" + hasSession.String() + " ;;\n" +
		"list-panes) case \"$*\" in *' -a '*) exit 0 ;; *) " + paneProcess + " ;; esac ;;\n" +
		"capture-pane) exit 0 ;;\n" +
		"*) exit 0 ;;\n" +
		"esac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &bptmux.Client{Bin: path}
}

func lifecycleApp(t *testing.T, bookPath string, client *bptmux.Client) *app {
	t.Helper()
	stateDir := t.TempDir()
	a := &app{
		ctx: context.Background(),
		config: config.Config{
			Agentbooks: []string{bookPath}, StateDir: stateDir,
			UsageHistory: filepath.Join(stateDir, "history.json"),
			Lifecycle:    config.LifecycleConfig{EphemeralDefault: true, ArchiveOnClose: true},
		},
		tmux: client, out: testOutput(t), err: testOutput(t),
		resolveSender: func() identity.Identity { return identity.Identity{Label: "operator", Certain: true, Source: "test"} },
	}
	return a
}

func TestClosedEphemeralIsHiddenAndVisibleChildrenStayInTree(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	path := filepath.Join(t.TempDir(), "agentbook.json")
	writeLifecycleBook(t, path,
		book.Agent{Name: "throwaway-parent", Status: "closed", Lifetime: book.LifetimeEphemeral},
		book.Agent{Name: "kept-child", Parent: "throwaway-parent", Status: "closed", Lifetime: book.LifetimePersistent},
		book.Agent{Name: "archived-throwaway", Status: "closed", Lifetime: book.LifetimeEphemeral, ArchivedAt: "2026-09-01T00:00:00Z"},
	)
	a := lifecycleApp(t, path, lifecycleTmux(t, nil, false))
	a.config.Lifecycle.ArchiveOnClose = false

	if err := a.status(nil); err != nil {
		t.Fatal(err)
	}
	if output := readTestOutput(t, a.out); strings.Contains(output, "throwaway-parent") || strings.Contains(output, "archived-throwaway") || !strings.Contains(output, "kept-child") {
		t.Fatalf("default status did not hide only closed ephemeral rows: %s", output)
	}
	a.out = testOutput(t)
	if err := a.status([]string{"--json", "--all"}); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Agents []struct {
			Name string `json:"name"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(readTestOutput(t, a.out)), &report); err != nil {
		t.Fatal(err)
	}
	allNames := map[string]bool{}
	for _, agent := range report.Agents {
		allNames[agent.Name] = true
	}
	if !allNames["throwaway-parent"] || !allNames["archived-throwaway"] {
		t.Fatalf("status --all omitted ephemeral history: %v", allNames)
	}

	a.out = testOutput(t)
	if err := a.tree(nil); err != nil {
		t.Fatal(err)
	}
	if output := readTestOutput(t, a.out); strings.Contains(output, "throwaway-parent") || !strings.Contains(output, "kept-child") {
		t.Fatalf("default tree hid a visible child with its ephemeral parent: %s", output)
	}
	a.out = testOutput(t)
	if err := a.tree([]string{"--all"}); err != nil {
		t.Fatal(err)
	}
	if output := readTestOutput(t, a.out); !strings.Contains(output, "throwaway-parent") || !strings.Contains(output, "archived-throwaway") {
		t.Fatalf("tree --all omitted ephemeral history: %s", output)
	}
}

func TestCloseAutomaticallyArchivesEphemeralWhenEnabled(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	for _, archiveOnClose := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[archiveOnClose], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agentbook.json")
			writeLifecycleBook(t, path, book.Agent{Name: "temporary", Status: "open", Lifetime: book.LifetimeEphemeral})
			a := lifecycleApp(t, path, lifecycleTmux(t, nil, false))
			a.config.Lifecycle.ArchiveOnClose = archiveOnClose
			if err := a.close([]string{"temporary"}); err != nil {
				t.Fatal(err)
			}
			file, err := book.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			got := file.Agents[1]
			if got.Status != "closed" {
				t.Fatalf("status=%q, want closed", got.Status)
			}
			if (got.ArchivedAt != "") != archiveOnClose {
				t.Fatalf("archivedAt=%q, archiveOnClose=%t", got.ArchivedAt, archiveOnClose)
			}
		})
	}
}

func TestArchiveRefusesRecordedLiveNativeProcessWithoutChangingBook(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	path := filepath.Join(t.TempDir(), "agentbook.json")
	writeLifecycleBook(t, path, book.Agent{
		Name: "temporary", Status: "closed", Lifetime: book.LifetimeEphemeral,
		Local: &cache.LocalBinding{PID: os.Getpid(), Harness: "claude"},
	})
	a := lifecycleApp(t, path, lifecycleTmux(t, nil, false))
	if err := a.archive([]string{"temporary"}, true); err == nil || !strings.Contains(err.Error(), "recorded native process still alive") {
		t.Fatalf("archive error=%v, want live-process refusal", err)
	}
	file, err := book.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if file.Agents[1].ArchivedAt != "" || file.Agents[1].Status != "closed" {
		t.Fatalf("live-process archive changed the registration: %+v", file.Agents[1])
	}
}

func TestAutomaticArchivePreservesPendingMessages(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	path := filepath.Join(t.TempDir(), "agentbook.json")
	writeLifecycleBook(t, path, book.Agent{Name: "temporary", Status: "closed", Lifetime: book.LifetimeEphemeral})
	a := lifecycleApp(t, path, lifecycleTmux(t, nil, false))
	a.queue = msgq.New(filepath.Join(t.TempDir(), "queue"))
	if _, err := a.queue.Enqueue("temporary", "operator", "keep this message available"); err != nil {
		t.Fatal(err)
	}
	results := a.archiveClosedEphemerals()
	if len(results) != 1 || !strings.Contains(results[0], "pending channel") {
		t.Fatalf("archive results=%v", results)
	}
	file, err := book.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if file.Agents[1].ArchivedAt != "" || !strings.Contains(file.Agents[1].LifecycleNote, "pending channel") {
		t.Fatalf("record did not preserve pending message state: %+v", file.Agents[1])
	}
}

func TestKeepAndReleaseChangeActiveLifetime(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	path := filepath.Join(t.TempDir(), "agentbook.json")
	writeLifecycleBook(t, path, book.Agent{Name: "worker", Status: "open"})
	a := lifecycleApp(t, path, lifecycleTmux(t, nil, false))
	if err := a.run([]string{"release", "worker"}); err != nil {
		t.Fatal(err)
	}
	file, err := book.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if file.Agents[1].Lifetime != book.LifetimeEphemeral {
		t.Fatalf("released lifetime=%q", file.Agents[1].Lifetime)
	}
	if err := a.run([]string{"keep", "worker"}); err != nil {
		t.Fatal(err)
	}
	file, err = book.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if file.Agents[1].Lifetime != book.LifetimePersistent {
		t.Fatalf("kept lifetime=%q", file.Agents[1].Lifetime)
	}
}

func TestRenamePromotesEphemeralRegistration(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	path := filepath.Join(t.TempDir(), "agentbook.json")
	writeLifecycleBook(t, path, book.Agent{Name: "temporary", Status: "closed", Lifetime: book.LifetimeEphemeral})
	a := lifecycleApp(t, path, lifecycleTmux(t, nil, false))
	if err := a.rename([]string{"temporary", "named-worker"}); err != nil {
		t.Fatal(err)
	}
	file, err := book.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if file.Agents[1].Name != "named-worker" || file.Agents[1].Lifetime != book.LifetimePersistent {
		t.Fatalf("renamed agent=%+v", file.Agents[1])
	}
}

func TestNativeRetitlePromotesOnlyAcceptedAlias(t *testing.T) {
	for _, test := range []struct {
		name      string
		title     string
		withTaken bool
		wantAlias string
		wantLife  string
	}{
		{name: "valid alias", title: "named-worker", wantAlias: "named-worker", wantLife: book.LifetimePersistent},
		{name: "reserved alias", title: "already-used", withTaken: true, wantAlias: "auto-worker", wantLife: book.LifetimeEphemeral},
		{name: "invalid alias", title: "not a valid name", wantAlias: "auto-worker", wantLife: book.LifetimeEphemeral},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AGENTBOOK", "")
			root := t.TempDir()
			path := filepath.Join(root, "agentbook.json")
			transcript := filepath.Join(root, "session.jsonl")
			line, _ := json.Marshal(map[string]string{"type": "custom-title", "customTitle": test.title, "sessionId": "thread"})
			if err := os.WriteFile(transcript, append(line, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			agents := []book.Agent{{Name: "auto-worker", Lifetime: book.LifetimeEphemeral}}
			if test.withTaken {
				agents = append(agents, book.Agent{Name: "already-used", Lifetime: book.LifetimePersistent})
			}
			writeLifecycleBook(t, path, agents...)
			a := lifecycleApp(t, path, lifecycleTmux(t, nil, false))
			state := cache.State{Runtime: "claude", Activity: &cache.Activity{ThreadID: "thread", TranscriptPath: transcript}}
			got := a.nativeName(agents[0], &state)
			if got != test.wantAlias {
				t.Fatalf("native name=%q, want %q", got, test.wantAlias)
			}
			file, err := book.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if file.Agents[1].Lifetime != test.wantLife {
				t.Fatalf("native retitle lifetime=%q, want %q", file.Agents[1].Lifetime, test.wantLife)
			}
		})
	}
}

func TestArchiveStalePreviewsAndRespectsExplicitPersistenceAndChildren(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	root := t.TempDir()
	path := filepath.Join(root, "agentbook.json")
	missing := func(name string) *book.NativeTitle {
		return &book.NativeTitle{ThreadID: name + "-thread", Path: filepath.Join(root, name+".jsonl")}
	}
	existing := filepath.Join(root, "existing.jsonl")
	if err := os.WriteFile(existing, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeLifecycleBook(t, path,
		book.Agent{Name: "stale", Status: "closed", NativeTitle: missing("stale")},
		book.Agent{Name: "persistent", Status: "closed", Lifetime: book.LifetimePersistent, NativeTitle: missing("persistent")},
		book.Agent{Name: "unknown-binding", Status: "closed", Lifetime: book.LifetimeEphemeral},
		book.Agent{Name: "fresh", Status: "closed", Lifetime: book.LifetimeEphemeral, NativeTitle: &book.NativeTitle{ThreadID: "fresh", Path: existing}},
		book.Agent{Name: "blocked", Status: "closed", Lifetime: book.LifetimeEphemeral, NativeTitle: missing("blocked")},
		book.Agent{Name: "blocked-child", Parent: "blocked", Status: "open", Lifetime: book.LifetimePersistent},
	)
	a := lifecycleApp(t, path, lifecycleTmux(t, nil, false))
	before := readFixtureFile(t, path)
	if err := a.archive([]string{"--stale", "--dry-run"}, true); err != nil {
		t.Fatal(err)
	}
	preview := readTestOutput(t, a.out)
	if !strings.Contains(preview, "would archive stale") || !strings.Contains(preview, "still has child blocked-child") {
		t.Fatalf("stale preview did not show candidate and child refusal: %s", preview)
	}
	if after := readFixtureFile(t, path); after != before {
		t.Fatal("dry-run changed the agentbook")
	}
	a.out = testOutput(t)
	if err := a.archive([]string{"--stale"}, true); err != nil {
		t.Fatal(err)
	}
	file, err := book.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]book.Agent{}
	for _, agent := range file.Agents {
		byName[agent.Name] = agent
	}
	if byName["stale"].ArchivedAt == "" || byName["persistent"].ArchivedAt != "" || byName["unknown-binding"].ArchivedAt != "" || byName["fresh"].ArchivedAt != "" || byName["blocked"].ArchivedAt != "" {
		t.Fatalf("stale migration result: %+v", byName)
	}
	if _, err := os.Stat(existing); err != nil {
		t.Fatalf("existing native transcript was touched: %v", err)
	}
}

func TestRunLifetimeFlagsAreCarriedToManagedSession(t *testing.T) {
	for _, test := range []struct {
		name         string
		args         []string
		defaultLife  bool
		wantLifetime string
		wantUpdate   string
	}{
		{name: "default ephemeral", args: []string{"--name", "worker", "opencode"}, defaultLife: true, wantLifetime: "ephemeral"},
		{name: "config opt-out default", args: []string{"--name", "worker", "opencode"}, wantLifetime: "persistent"},
		{name: "persistent override", args: []string{"--name", "worker", "--persistent", "opencode"}, defaultLife: true, wantLifetime: "persistent", wantUpdate: "1"},
		{name: "ephemeral explicit", args: []string{"--name", "worker", "--ephemeral", "opencode"}, wantLifetime: "ephemeral", wantUpdate: "1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TMUX", "")
			t.Setenv("TMUX_PANE", "")
			t.Setenv("AGENTBOOK", "")
			root := t.TempDir()
			binDir := filepath.Join(root, "bin")
			if err := os.MkdirAll(binDir, 0o700); err != nil {
				t.Fatal(err)
			}
			opencode := filepath.Join(binDir, "opencode")
			if err := os.WriteFile(opencode, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir)
			bookPath := filepath.Join(root, "agentbook.json")
			if err := os.WriteFile(bookPath, []byte("{\"agents\":[]}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			calls := filepath.Join(root, "tmux-args")
			tmuxPath := filepath.Join(binDir, "tmux")
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > " + quoteShell(calls) + "\n"
			if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			stateDir := filepath.Join(root, "state")
			a := &app{
				ctx: context.Background(),
				config: config.Config{
					Home: root, Agentbooks: []string{bookPath}, StateDir: stateDir,
					UsageHistory: filepath.Join(stateDir, "history.json"), UpdateCheck: false,
					Lifecycle: config.LifecycleConfig{EphemeralDefault: test.defaultLife, ArchiveOnClose: true},
				},
				tmux: &bptmux.Client{Bin: tmuxPath}, out: testOutput(t), err: testOutput(t),
				interactive: func() bool { return true },
			}
			if err := a.localRun(test.args); err != nil {
				t.Fatal(err)
			}
			command := readFixtureFile(t, calls)
			if !strings.Contains(command, "BP_RUN_LIFETIME="+quoteShell(test.wantLifetime)) {
				t.Fatalf("managed launcher omitted lifetime: %s", command)
			}
			if !strings.Contains(command, "BP_RUN_LIFETIME_UPDATE="+quoteShell(test.wantUpdate)) {
				t.Fatalf("managed launcher update marker mismatch: %s", command)
			}
		})
	}
}

func TestOpenEphemeralFlagUpdatesAnExistingRegistration(t *testing.T) {
	for _, test := range []struct {
		name     string
		initial  string
		args     []string
		wantLife string
	}{
		{name: "explicit ephemeral", initial: book.LifetimePersistent, args: []string{"worker", "--ephemeral"}, wantLife: book.LifetimeEphemeral},
		{name: "open defaults existing registration to persistent", initial: book.LifetimeEphemeral, args: []string{"worker"}, wantLife: book.LifetimePersistent},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AGENTBOOK", "")
			root := t.TempDir()
			path := filepath.Join(root, "agentbook.json")
			writeLifecycleBook(t, path, book.Agent{Name: "worker", Status: "open", Lifetime: test.initial})
			a := lifecycleApp(t, path, lifecycleTmux(t, []string{"worker"}, true))
			a.config.Legacy = true
			dir := filepath.Join(root, "project")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := a.open(append([]string{test.args[0], dir}, test.args[1:]...)); err != nil {
				t.Fatal(err)
			}
			file, err := book.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if file.Agents[1].Lifetime != test.wantLife {
				t.Fatalf("open lifetime=%q, want %q", file.Agents[1].Lifetime, test.wantLife)
			}
		})
	}
}

func TestDoctorWarnsAboutNativeTitleMismatchAndSkipsEphemeralExitFailure(t *testing.T) {
	root := t.TempDir()
	transcript := filepath.Join(root, "claude.jsonl")
	line, _ := json.Marshal(map[string]string{"type": "custom-title", "customTitle": "native-alias", "sessionId": "thread"})
	if err := os.WriteFile(transcript, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	check, ok := doctorNativeTitleMismatch(book.Agent{
		Name: "canonical-name", Lifetime: book.LifetimeEphemeral,
		NativeTitle: &book.NativeTitle{ThreadID: "thread", Path: transcript},
	}, nil, nil)
	if !ok || !check.Warning || !check.OK || check.Next != `bp rename canonical-name canonical-name (retitle the native session to "canonical-name")` {
		t.Fatalf("native mismatch check=%+v, found=%t", check, ok)
	}

	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	tmuxDir := t.TempDir()
	tmuxPath := filepath.Join(tmuxDir, "tmux")
	if err := os.WriteFile(tmuxPath, []byte("#!/bin/sh\ncase \"$1\" in list-sessions) exit 0 ;; has-session) exit 1 ;; esac\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmuxDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	observationDir := filepath.Join(root, "run")
	if err := os.MkdirAll(observationDir, 0o700); err != nil {
		t.Fatal(err)
	}
	observation := filepath.Join(observationDir, "observation.json")
	if err := os.WriteFile(observation, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exitReport := localExitReport{Harness: "claude", Status: "1"}
	exitData, err := json.Marshal(exitReport)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(observationDir, "exit.json"), exitData, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name           string
		lifetime       string
		wantNativeExit bool
	}{
		{name: "ephemeral", lifetime: book.LifetimeEphemeral},
		{name: "persistent", lifetime: book.LifetimePersistent, wantNativeExit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fleet := book.Fleet{Agents: map[string]book.Agent{
				"agent": {Name: "agent", Lifetime: test.lifetime, Local: &cache.LocalBinding{Path: observation, PID: 42, Harness: "claude"}},
			}}
			checks := doctorRuntimeChecks(config.Config{StateDir: root}, fleet, "agent")
			foundNativeExit := false
			for _, row := range checks {
				foundNativeExit = foundNativeExit || strings.HasPrefix(row.Name, "native_exit/")
			}
			if foundNativeExit != test.wantNativeExit {
				t.Fatalf("native_exit present=%t, checks=%+v", foundNativeExit, checks)
			}
		})
	}
}
