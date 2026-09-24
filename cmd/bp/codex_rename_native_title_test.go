package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/cache"
	bpconfig "blueprint/internal/config"
	bptmux "blueprint/internal/tmux"
)

func TestRenameCodexUpdatesNativeTitleAndName(t *testing.T) {
	const (
		oldName  = "codex-original"
		newName  = "codex-renamed"
		threadID = "01a0d02a-a4b7-78a1-a382-59d926d5f6ba"
	)

	root := t.TempDir()
	codexHome := filepath.Join(root, "codex-home")
	t.Setenv("CODEX_HOME", codexHome)
	folder := filepath.Join(root, "project")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}

	transcript := filepath.Join(codexHome, "sessions", "2026", "09", "24", "rollout-"+threadID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o755); err != nil {
		t.Fatal(err)
	}
	meta, err := json.Marshal(map[string]any{
		"type": "session_meta",
		"payload": map[string]any{
			"id": threadID, "cwd": folder, "source": "cli",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, append(meta, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	index := filepath.Join(codexHome, "session_index.jsonl")
	firstRecord, err := json.Marshal(map[string]string{
		"id": "unrelated-thread", "thread_name": "unrelated-title", "updated_at": time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index, append(firstRecord, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	originalIndex, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	initialTitle, err := book.ReadCodexNativeTitle(index, threadID, nil)
	if err != nil || initialTitle.Text != "" {
		t.Fatalf("fixture unexpectedly has an existing row for %s: title=%q err=%v", threadID, initialTitle.Text, err)
	}
	nativeTitle := book.NativeTitle{ThreadID: threadID, Path: index, Text: oldName}

	bookPath := filepath.Join(root, "agentbook.json")
	entry := book.Agent{
		Name: oldName, Folder: folder, ColorOverride: "110",
		Launch:      &bptmux.OpenOptions{Codex: true, ResumeID: threadID, Remote: "unix://"},
		NativeTitle: &nativeTitle,
	}
	contents, err := json.Marshal(book.File{Agents: []book.Agent{entry}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bookPath, append(contents, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	tmuxBin := filepath.Join(root, "tmux")
	tmuxCalls := filepath.Join(root, "tmux-calls")
	sessionFile := filepath.Join(root, "session")
	if err := os.WriteFile(sessionFile, []byte(oldName), 0o600); err != nil {
		t.Fatal(err)
	}
	tmuxScript := "#!/bin/sh\n" +
		"echo \"$*\" >> " + tmuxCalls + "\n" +
		"case \"$1\" in\n" +
		"has-session) target=\"${3#=}\"; [ \"$target\" = \"$(cat " + sessionFile + ")\" ] ;;\n" +
		"rename-session) echo \"$4\" > " + sessionFile + " ;;\n" +
		"list-panes)\n" +
		"  case \"$*\" in\n" +
		"    *pane_pid*) printf '1\\tcodex\\t42\\n' ;;\n" +
		"    *pane_current_path*) printf '" + oldName + "\\t" + folder + "\\t" + folder + "\\n' ;;\n" +
		"    *) printf '" + oldName + "\\t1\\tcodex\\n' ;;\n" +
		"  esac ;;\n" +
		"capture-pane) printf '› \\n' ;;\n" +
		"*) : ;;\n" +
		"esac\n"
	if err := os.WriteFile(tmuxBin, []byte(tmuxScript), 0o755); err != nil {
		t.Fatal(err)
	}

	stateDir := filepath.Dir(bookPath)
	output := testOutput(t)
	app := &app{
		ctx: context.Background(),
		config: bpconfig.Config{
			Agentbooks: []string{bookPath}, UsageHistory: filepath.Join(stateDir, "history.json"),
		},
		tmux: &bptmux.Client{Bin: tmuxBin, Sleep: func(time.Duration) {}},
		out:  output, err: testOutput(t),
	}

	if err := app.rename([]string{oldName, newName}); err != nil {
		t.Fatalf("rename failed: %v\nstderr: %s", err, readTestOutput(t, app.err))
	}

	gotTitle, err := book.ReadCodexNativeTitle(index, threadID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotTitle.Text != newName {
		t.Errorf("Codex native title = %q, want %q", gotTitle.Text, newName)
	}
	updatedIndex, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(updatedIndex, originalIndex) || bytes.Count(updatedIndex, []byte{'\n'}) != 2 {
		t.Errorf("Codex index was not updated by preserving its original row and appending one row: %q", updatedIndex)
	}

	var renamedBook book.File
	if err := json.Unmarshal([]byte(readFixtureFile(t, bookPath)), &renamedBook); err != nil {
		t.Fatal(err)
	}
	if len(renamedBook.Agents) != 1 || renamedBook.Agents[0].NativeTitle == nil {
		t.Fatalf("renamed agentbook entry is missing nativeTitle: %+v", renamedBook.Agents)
	}
	if got := renamedBook.Agents[0].NativeTitle.Text; got != newName {
		t.Errorf("agentbook nativeTitle.text = %q, want %q", got, newName)
	}

	nameOutput := testOutput(t)
	app.out = nameOutput
	if err := app.run([]string{"name", newName}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, nameOutput); !strings.Contains(got, newName) || strings.Contains(got, oldName) {
		t.Errorf("bp name output = %q, want the canonical name %q", got, newName)
	}

	// An incomplete index tail still fails loudly before the next session or
	// agentbook rename, and the error points to the explicit escape hatch.
	bookBeforeFailure := readFixtureFile(t, bookPath)
	if err := os.WriteFile(index, []byte(`{"id":"unfinished-thread","thread_name":"unfinished"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	callsBeforeFailure, err := os.ReadFile(tmuxCalls)
	if err != nil {
		t.Fatal(err)
	}
	failureOutput, failureErr := testOutput(t), testOutput(t)
	app.out, app.err = failureOutput, failureErr
	if err := app.rename([]string{newName, "codex-final"}); err == nil {
		t.Fatal("rename succeeded with an incomplete final Codex index record")
	}
	if got := readTestOutput(t, failureErr); !strings.Contains(got, "WARNING: Codex native title") {
		t.Errorf("failed Codex retitle was not loudly reported: %q", got)
	}
	if got := readTestOutput(t, failureErr); !strings.Contains(got, "--no-retitle") {
		t.Errorf("failed Codex retitle did not suggest --no-retitle: %q", got)
	}
	if got := readTestOutput(t, failureOutput); strings.Contains(got, "renamed "+newName+" -> codex-final") {
		t.Errorf("failed Codex retitle reported success: %q", got)
	}
	if got := readFixtureFile(t, bookPath); got != bookBeforeFailure {
		t.Errorf("agentbook changed after failed Codex retitle:\n%s", got)
	}
	callsAfterFailure, err := os.ReadFile(tmuxCalls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(callsAfterFailure[len(callsBeforeFailure):]), "rename-session") {
		t.Errorf("tmux session was renamed after Codex retitle failed:\n%s", callsAfterFailure[len(callsBeforeFailure):])
	}
}

type simpleCodexRenameFixture struct {
	app      *app
	bookPath string
	index    string
	calls    string
}

func newSimpleCodexRenameFixture(t *testing.T, live, launchCodex bool, native *book.NativeTitle) simpleCodexRenameFixture {
	t.Helper()
	t.Setenv("AGENTBOOK", "")
	root := t.TempDir()
	index := filepath.Join(root, "codex-home", "session_index.jsonl")
	t.Setenv("CODEX_HOME", filepath.Dir(index))

	entry := book.Agent{Name: "codex-worker", Status: "open", NativeTitle: native}
	if launchCodex {
		entry.Launch = &bptmux.OpenOptions{Codex: true}
	}
	bookPath := filepath.Join(root, "agentbook.json")
	contents, err := json.Marshal(book.File{Agents: []book.Agent{entry}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bookPath, append(contents, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	sessionFile := filepath.Join(root, "session")
	if err := os.WriteFile(sessionFile, []byte("codex-worker"), 0o600); err != nil {
		t.Fatal(err)
	}
	tmuxBin := filepath.Join(root, "tmux")
	calls := filepath.Join(root, "tmux-calls")
	liveCheck := "exit 1"
	if live {
		liveCheck = `[ "$3" = "=codex-worker" ]`
	}
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + calls + "\n" +
		"case \"$1\" in\n" +
		"has-session) " + liveCheck + " ;;\n" +
		"display-message) echo codex ;;\n" +
		"list-panes) case \"$*\" in\n" +
		"  *pane_pid*) printf '1\\tcodex\\t42\\n' ;;\n" +
		"  *pane_current_path*) printf 'codex-worker\\t\\t\\n' ;;\n" +
		"  *) printf 'codex-worker\\t1\\tcodex\\n' ;;\n" +
		"esac ;;\n" +
		"capture-pane) printf '› \\n' ;;\n" +
		"rename-session) echo \"$4\" > " + sessionFile + " ;;\n" +
		"*) : ;;\n" +
		"esac\n"
	if err := os.WriteFile(tmuxBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Dir(bookPath)
	a := &app{
		ctx: context.Background(),
		config: bpconfig.Config{
			Agentbooks: []string{bookPath}, UsageHistory: filepath.Join(stateDir, "history.json"), StateDir: stateDir,
		},
		tmux: &bptmux.Client{Bin: tmuxBin, Sleep: func(time.Duration) {}}, out: testOutput(t), err: testOutput(t),
	}
	return simpleCodexRenameFixture{app: a, bookPath: bookPath, index: index, calls: calls}
}

func TestRenameCodexNoRetitleKeepsNativeTitleAndWarns(t *testing.T) {
	const oldName = "codex-worker"
	fix := newSimpleCodexRenameFixture(t, true, true, nil)
	if err := os.MkdirAll(filepath.Dir(fix.index), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fix.index, []byte(`{"id":"unknown-thread","thread_name":"codex-worker"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeIndex, err := os.ReadFile(fix.index)
	if err != nil {
		t.Fatal(err)
	}
	if err := fix.app.rename([]string{oldName, "codex-new", "--no-retitle"}); err != nil {
		t.Fatal(err)
	}
	if after, err := os.ReadFile(fix.index); err != nil || !bytes.Equal(after, beforeIndex) {
		t.Fatalf("--no-retitle changed Codex index: %q err=%v", after, err)
	}
	if calls, err := os.ReadFile(fix.calls); err != nil || strings.Contains(string(calls), "paste-buffer") || strings.Contains(string(calls), "send-keys") {
		t.Fatalf("--no-retitle typed into the Codex pane: %s err=%v", calls, err)
	}
	if warning := readTestOutput(t, fix.app.err); !strings.Contains(warning, "CODEX TITLE REMAINS STALE") {
		t.Fatalf("missing loud stale-title warning: %q", warning)
	}
	var updated book.File
	if err := json.Unmarshal([]byte(readFixtureFile(t, fix.bookPath)), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Agents[0].Name != "codex-new" || updated.Agents[0].NativeTitle != nil {
		t.Fatalf("canonical rename/native title state = %+v, want new name with no fabricated binding", updated.Agents[0])
	}
}

func TestRenameClosedCodexAppendsUsingStoredNativeTitleBinding(t *testing.T) {
	root := t.TempDir()
	index := filepath.Join(root, "codex-home", "session_index.jsonl")
	if err := os.MkdirAll(filepath.Dir(index), 0o700); err != nil {
		t.Fatal(err)
	}
	native := &book.NativeTitle{ThreadID: "closed-thread", Path: index, Text: "codex-worker"}
	fix := newSimpleCodexRenameFixture(t, false, false, native)
	if err := fix.app.rename([]string{"codex-worker", "codex-closed-new"}); err != nil {
		t.Fatal(err)
	}
	got, err := book.ReadCodexNativeTitle(index, "closed-thread", nil)
	if err != nil || got.Text != "codex-closed-new" {
		t.Fatalf("closed Codex title=%q err=%v", got.Text, err)
	}
	if info, err := os.Stat(index); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("closed Codex index stat=%v err=%v, want created 0600 index", info, err)
	}
	if calls, err := os.ReadFile(fix.calls); err != nil || strings.Contains(string(calls), "rename-session") {
		t.Fatalf("closed agent unexpectedly touched tmux: %s err=%v", calls, err)
	}
	if output := readTestOutput(t, fix.app.out); !strings.Contains(output, "append Codex thread_name codex-closed-new") {
		t.Fatalf("closed Codex report omitted the native title append: %q", output)
	}
}

func TestRenameClosedCodexWithoutStoredBindingWarnsAgentbookOnly(t *testing.T) {
	fix := newSimpleCodexRenameFixture(t, false, true, nil)
	if err := fix.app.rename([]string{"codex-worker", "codex-unbound-new"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fix.index); !os.IsNotExist(err) {
		t.Fatalf("unexpected index created without a stored binding: err=%v", err)
	}
	if output := readTestOutput(t, fix.app.out); !strings.Contains(output, "agentbook only") {
		t.Fatalf("closed Codex report omitted agentbook-only status: %q", output)
	}
	if warning := readTestOutput(t, fix.app.err); !strings.Contains(warning, "CODEX TITLE REMAINS STALE") {
		t.Fatalf("missing closed Codex stale-title warning: %q", warning)
	}
}

func TestRenameSameNameRepairsStaleCodexTitle(t *testing.T) {
	const oldTitle = "old-codex-title"
	index := filepath.Join(t.TempDir(), "session_index.jsonl")
	if err := os.WriteFile(index, []byte(`{"id":"repair-thread","thread_name":"`+oldTitle+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	native := &book.NativeTitle{ThreadID: "repair-thread", Path: index, Text: oldTitle}
	fix := newSimpleCodexRenameFixture(t, false, false, native)
	file, err := book.Load(fix.bookPath)
	if err != nil {
		t.Fatal(err)
	}
	file.Agents[0].Local = &cache.LocalBinding{
		Path: filepath.Join(filepath.Dir(fix.bookPath), "local", "run", "observation.json"),
		PID:  42, Harness: "codex", Home: filepath.Dir(index),
	}
	bookData, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fix.bookPath, append(bookData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := fix.app.rename([]string{"codex-worker", "codex-worker"}); err != nil {
		t.Fatal(err)
	}
	value, err := book.ReadCodexNativeTitle(index, "repair-thread", nil)
	if err != nil || value.Text != "codex-worker" {
		t.Fatalf("repaired Codex title=%q err=%v", value.Text, err)
	}
	if after, err := os.ReadFile(index); err != nil || bytes.HasPrefix(after, before) == false || bytes.Count(after, []byte{'\n'}) != 2 {
		t.Fatalf("same-name Codex repair did not append once: %q err=%v", after, err)
	}
	if output := readTestOutput(t, fix.app.out); !strings.Contains(output, "reapplied codex native title") {
		t.Fatalf("repair output=%q", output)
	}
}

func TestRenameSameNameReportsCodexTitleAlreadyMatching(t *testing.T) {
	index := filepath.Join(t.TempDir(), "session_index.jsonl")
	original := []byte(`{"id":"repair-thread","thread_name":"codex-worker"}` + "\n")
	if err := os.WriteFile(index, original, 0o600); err != nil {
		t.Fatal(err)
	}
	native := &book.NativeTitle{ThreadID: "repair-thread", Path: index, Text: "codex-worker"}
	fix := newSimpleCodexRenameFixture(t, false, false, native)
	if err := fix.app.rename([]string{"codex-worker", "codex-worker"}); err != nil {
		t.Fatal(err)
	}
	if after, err := os.ReadFile(index); err != nil || !bytes.Equal(after, original) {
		t.Fatalf("already-matching title was changed: %q err=%v", after, err)
	}
	if output := readTestOutput(t, fix.app.out); !strings.Contains(output, "already matches; no repair needed") {
		t.Fatalf("repair output=%q", output)
	}
}
