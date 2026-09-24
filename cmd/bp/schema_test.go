package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blueprint/internal/book"
	bpconfig "blueprint/internal/config"
	"blueprint/internal/projectschema"
	bptmux "blueprint/internal/tmux"
)

func writeSchemaBook(t *testing.T, path, root string, agents []book.Agent) {
	t.Helper()
	data, err := json.Marshal(book.File{Orchestrator: root, Agents: agents})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func schemaProjectFixture(t *testing.T) (string, string, []book.Agent) {
	t.Helper()
	t.Setenv("AGENTBOOK", "")
	root := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(filepath.Join(root, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, projectschema.Directory), 0o755); err != nil {
		t.Fatal(err)
	}
	schema := `version: 1
history: none
agents:
  - name: worker
    folder: child
    parent: lead
    runtime: codex
    role: researcher
    color: purple
    launch: no-sandbox
    model: gpt-example
    effort: high
  - name: lead
    folder: .
    runtime: claude
    role: lead
    color: blue
    model: claude-example
    effort: high
`
	if err := os.WriteFile(projectschema.Path(root), []byte(schema), 0o600); err != nil {
		t.Fatal(err)
	}
	agents := []book.Agent{
		{Name: "lead", Folder: root, Role: "old lead", Status: "closed"},
		{Name: "worker", Folder: filepath.Join(root, "child"), Parent: "lead", Role: "old worker", Status: "closed"},
	}
	return root, schema, agents
}

func schemaOutput(t *testing.T) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func readSchemaOutput(t *testing.T, file *os.File) string {
	t.Helper()
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestContinueRequiresTrustAndMaterializesParentsFirst(t *testing.T) {
	root, original, agents := schemaProjectFixture(t)
	bookPath := filepath.Join(t.TempDir(), "agentbook.json")
	writeSchemaBook(t, bookPath, "lead", agents)
	stateDir := filepath.Join(t.TempDir(), "state")
	var opened [][]string
	a := &app{
		ctx:         context.Background(),
		config:      bpconfig.Config{Agentbooks: []string{bookPath}, StateDir: stateDir},
		out:         schemaOutput(t),
		interactive: func() bool { return false },
		openForContinue: func(args []string) error {
			opened = append(opened, append([]string(nil), args...))
			return nil
		},
	}

	if err := a.continueProject([]string{root, "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 0 {
		t.Fatalf("dry run opened agents: %v", opened)
	}
	if _, err := os.Stat(a.schemaTrustPath(root)); !os.IsNotExist(err) {
		t.Fatalf("dry run recorded trust: %v", err)
	}
	if !strings.Contains(readSchemaOutput(t, a.out), "dry run: nothing was changed") {
		t.Fatal("dry run did not print its no-change result")
	}

	err := a.continueProject([]string{root})
	if err == nil || !strings.Contains(err.Error(), "rerun with --yes") {
		t.Fatalf("non-interactive first use error = %v", err)
	}
	if len(opened) != 0 {
		t.Fatalf("untrusted schema opened agents: %v", opened)
	}

	if err := a.continueProject([]string{root, "--yes"}); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 2 || opened[0][0] != "lead" || opened[1][0] != "worker" {
		t.Fatalf("open order = %v, want lead before worker", opened)
	}
	for _, args := range opened {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--fresh") || !strings.Contains(joined, "--rebind") {
			t.Errorf("history:none did not start a fresh, explicitly rebound thread: %s", joined)
		}
	}
	if got := strings.Join(opened[1], " "); !strings.Contains(got, "--parent lead") || !strings.Contains(got, "--no-sandbox") || !strings.Contains(got, "--model gpt-example") || !strings.Contains(got, "model_reasoning_effort=high") {
		t.Fatalf("worker launch args lost schema settings: %s", got)
	}
	if got := strings.Join(opened[0], " "); !strings.Contains(got, "--claude") || !strings.Contains(got, "--model claude-example") || !strings.Contains(got, "--effort high") {
		t.Fatalf("lead launch args lost schema settings: %s", got)
	}
	if _, err := os.Stat(a.schemaTrustPath(root)); err != nil {
		t.Fatalf("trusted hash was not recorded: %v", err)
	}

	changed := strings.Replace(original, "role: researcher", "role: updated researcher", 1)
	if err := os.WriteFile(projectschema.Path(root), []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	err = a.continueProject([]string{root})
	if err == nil || !strings.Contains(err.Error(), "rerun with --yes") {
		t.Fatalf("changed schema trust error = %v", err)
	}
	if len(opened) != 2 {
		t.Fatalf("changed untrusted schema opened agents: %v", opened)
	}
}

func TestContinueRefusesNameCollisionForDifferentFolder(t *testing.T) {
	root, _, _ := schemaProjectFixture(t)
	other := filepath.Join(t.TempDir(), "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	bookPath := filepath.Join(t.TempDir(), "agentbook.json")
	writeSchemaBook(t, bookPath, "lead", []book.Agent{{Name: "lead", Folder: other}})
	loaded, err := projectschema.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{config: bpconfig.Config{Agentbooks: []string{bookPath}}}
	_, err = a.buildContinuePlan(loaded)
	if err == nil || !strings.Contains(err.Error(), "agent name collision: lead") || !strings.Contains(err.Error(), other) {
		t.Fatalf("collision error = %v", err)
	}
}

func TestContinuePlanRecognizesLiveRegistrations(t *testing.T) {
	root, _, agents := schemaProjectFixture(t)
	bookPath := filepath.Join(t.TempDir(), "agentbook.json")
	writeSchemaBook(t, bookPath, "lead", agents)
	tmuxPath := filepath.Join(t.TempDir(), "tmux")
	if err := os.WriteFile(tmuxPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	loaded, err := projectschema.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{ctx: context.Background(), config: bpconfig.Config{Agentbooks: []string{bookPath}}, tmux: &bptmux.Client{Bin: tmuxPath}}
	plans, err := a.buildContinuePlan(loaded)
	if err != nil {
		t.Fatal(err)
	}
	for _, plan := range plans {
		if plan.State != "leave live agent open" {
			t.Errorf("plan for %s = %q", plan.Agent.Name, plan.State)
		}
	}
}

func TestSchemaExportKeepsInProjectTreeAndUpdatesGitignore(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	child := filepath.Join(root, "child")
	outside := filepath.Join(parent, "outside")
	for _, dir := range []string{child, outside, filepath.Join(root, projectschema.Directory)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, projectschema.Directory, ".gitignore"), []byte("logs/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := `version: 1
history: file
agents:
  - name: lead
    folder: .
    runtime: claude
    role: lead
    color: blue
`
	if err := os.WriteFile(projectschema.Path(root), []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	bookPath := filepath.Join(t.TempDir(), "agentbook.json")
	writeSchemaBook(t, bookPath, "lead", []book.Agent{
		{Name: "lead", Folder: root, Role: "Lead", Color: "blue"},
		{Name: "worker", Folder: child, Parent: "lead", Role: "Research", ColorOverride: "purple", Launch: &bptmux.OpenOptions{Codex: true, NoSandbox: true, Args: []string{"-m", "gpt-schema", "-c", "model_reasoning_effort=high"}}},
		{Name: "outside", Folder: outside, Role: "External", Color: "red"},
	})
	a := &app{config: bpconfig.Config{Agentbooks: []string{bookPath}}, out: schemaOutput(t)}
	if err := a.exportSchema(root, ""); err != nil {
		t.Fatal(err)
	}
	loaded, err := projectschema.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.History != "file" || len(loaded.Agents) != 2 {
		t.Fatalf("exported schema = %#v", loaded.Schema)
	}
	worker := loaded.Agents[1]
	if worker.Name != "worker" || worker.Folder != "child" || worker.Parent != "lead" || worker.Runtime != "codex" || worker.Role != "Research" || worker.Color != "purple" || worker.Launch != "no-sandbox" || worker.Model != "gpt-schema" || worker.Effort != "high" {
		t.Fatalf("exported worker = %#v", worker)
	}
	gitignore, err := os.ReadFile(filepath.Join(root, projectschema.Directory, ".gitignore"))
	if err != nil || !strings.Contains(string(gitignore), "logs/") || !strings.Contains(string(gitignore), "history/") {
		t.Fatalf("history ignore = %q, %v", gitignore, err)
	}
}

func TestRenameReportsSchemaReferenceWithoutEditingIt(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(filepath.Join(root, projectschema.Directory), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	schema := `version: 1
history: none
agents:
  - name: old-name
    folder: sub
    runtime: claude
    role: worker
    color: blue
`
	path := projectschema.Path(root)
	if err := os.WriteFile(path, []byte(schema), 0o600); err != nil {
		t.Fatal(err)
	}
	bookPath := filepath.Join(t.TempDir(), "agentbook.json")
	writeSchemaBook(t, bookPath, "", []book.Agent{{Name: "old-name", Folder: sub, ArchivedAt: "2026-09-24T00:00:00Z"}})
	output := schemaOutput(t)
	a := &app{config: bpconfig.Config{Agentbooks: []string{bookPath}}, out: output}
	if err := a.rename([]string{"old-name", "new-name", "--archived", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readSchemaOutput(t, output), "update the committed schema manually") {
		t.Fatal("rename did not print a manual schema update hint")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != schema {
		t.Fatalf("rename changed schema: %q, %v", data, err)
	}
}
