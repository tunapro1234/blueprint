package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blueprint/internal/book"
	"blueprint/internal/config"
	bptmux "blueprint/internal/tmux"
)

func TestResolveAttachRecordPrecedenceAndAmbiguity(t *testing.T) {
	records := []book.Record{
		{Agent: book.Agent{Name: "canonical", ArchivedAt: "2026-01-01", NativeTitle: &book.NativeTitle{Text: "old"}}, Path: "/archived.json"},
		{Agent: book.Agent{Name: "worker", NativeTitle: &book.NativeTitle{Text: "canonical"}}, Path: "/active.json"},
		{Agent: book.Agent{Name: "current", NativeTitle: &book.NativeTitle{Text: "shared"}}, Path: "/active.json"},
		{Agent: book.Agent{Name: "retired", ArchivedAt: "2026-01-01", NativeTitle: &book.NativeTitle{Text: "shared"}}, Path: "/archived.json"},
	}
	record, live, err := resolveAttachRecord(records, "canonical", func(string) bool { return false })
	if err != nil || record.Agent.Name != "canonical" || live {
		t.Fatalf("canonical precedence = %+v, %v, %v", record, live, err)
	}
	record, _, err = resolveAttachRecord(records, "shared", func(string) bool { return false })
	if err != nil || record.Agent.Name != "current" {
		t.Fatalf("active title precedence = %+v, %v", record, err)
	}

	records = append(records, book.Record{Agent: book.Agent{Name: "other", NativeTitle: &book.NativeTitle{Text: "shared"}}, Path: "/other.json"})
	if _, _, err := resolveAttachRecord(records, "shared", func(string) bool { return false }); err == nil || !strings.Contains(err.Error(), "current") || !strings.Contains(err.Error(), "other") {
		t.Fatalf("ambiguous title error = %v", err)
	}
	if _, _, err := resolveAttachRecord(records, "missing", func(string) bool { return false }); err == nil || err.Error() != `unknown agent: "missing"` {
		t.Fatalf("unknown error = %v", err)
	}
}

func TestResolveAttachRecordMatchesNativeTitleWithSpaces(t *testing.T) {
	records := []book.Record{{
		Agent: book.Agent{Name: "claude-project-hash", NativeTitle: &book.NativeTitle{Text: "hypr-main worker"}},
		Path:  "/agentbook.json",
	}}
	record, live, err := resolveAttachRecord(records, "hypr-main worker", func(string) bool { return false })
	if err != nil || record.Agent.Name != "claude-project-hash" || live {
		t.Fatalf("native title resolution = %+v, %v, %v", record, live, err)
	}
}

func TestLocalAttachQueryAllowsNativeTitleSpaces(t *testing.T) {
	if !validLocalAttachQuery("hypr-main worker") {
		t.Fatal("native title with spaces was rejected as a local attach query")
	}
	for _, query := range []string{"", " \t ", "line\nbreak", "line\rbreak", "nul\x00byte"} {
		if validLocalAttachQuery(query) {
			t.Errorf("invalid attach query accepted: %q", query)
		}
	}
}

func TestResolveAttachRecordPrefersLive(t *testing.T) {
	records := []book.Record{
		{Agent: book.Agent{Name: "closed", NativeTitle: &book.NativeTitle{Text: "title"}}, Path: "/one"},
		{Agent: book.Agent{Name: "running", NativeTitle: &book.NativeTitle{Text: "title"}}, Path: "/two"},
	}
	record, live, err := resolveAttachRecord(records, "title", func(name string) bool { return name == "running" })
	if err != nil || record.Agent.Name != "running" || !live {
		t.Fatalf("live precedence = %+v, %v, %v", record, live, err)
	}
}

func TestAttachCommandUsesExactTargetAndSwitchesInsideTmux(t *testing.T) {
	outer := attachCommand("/usr/bin/tmux", "lead", false)
	inside := attachCommand("/usr/bin/tmux", "lead", true)
	if got := strings.Join(outer.Args, " "); got != "/usr/bin/tmux attach-session -t =lead" {
		t.Fatalf("outer command = %q", got)
	}
	if got := strings.Join(inside.Args, " "); got != "/usr/bin/tmux switch-client -t =lead" {
		t.Fatalf("inside command = %q", got)
	}
}

func TestAttachRecordsLastLookedBeforeReplacingWithTmux(t *testing.T) {
	root := t.TempDir()
	bookPath := filepath.Join(root, "agentbook.json")
	data, err := json.Marshal(book.File{Agents: []book.Agent{{Name: "worker"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bookPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	tmuxBin := filepath.Join(root, "tmux")
	if err := os.WriteFile(tmuxBin, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX", "")
	var replaced commandSpec
	a := &app{
		ctx: context.Background(),
		config: config.Config{
			Agentbooks: []string{bookPath},
			StateDir:   filepath.Join(root, "state"),
		},
		tmux:           &bptmux.Client{Bin: tmuxBin},
		out:            testOutput(t),
		err:            testOutput(t),
		replaceProcess: func(spec commandSpec) error { replaced = spec; return nil },
	}
	if err := a.attach([]string{"worker"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(replaced.Args, " "); got != tmuxBin+" attach-session -t =worker" {
		t.Fatalf("replacement command = %q", got)
	}
	data, err = os.ReadFile(filepath.Join(a.config.StateDir, lastLookedFile))
	if err != nil {
		t.Fatal(err)
	}
	var seen seenState
	if err := json.Unmarshal(data, &seen); err != nil {
		t.Fatal(err)
	}
	if seen.Agents["worker"].IsZero() {
		t.Fatal("attach did not record the agent as last looked")
	}
}

func TestValidateRemoteAgentNameRejectsCommandText(t *testing.T) {
	for _, name := range []string{"", "worker;touch", "worker name", "worker@server"} {
		if err := validateRemoteAgentName(name); err == nil {
			t.Errorf("invalid remote agent name accepted: %q", name)
		}
	}
	if err := validateRemoteAgentName("worker-1"); err != nil {
		t.Fatalf("valid remote agent name rejected: %v", err)
	}
}
