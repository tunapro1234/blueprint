package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blueprint/internal/book"
	bpconfig "blueprint/internal/config"
	"blueprint/internal/identity"
	"blueprint/internal/msgq"
	bptmux "blueprint/internal/tmux"
)

func TestRenameArchivedRegistrationResolvesMultipleRegistrationCLIPath(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	path := filepath.Join(t.TempDir(), "book.json")
	if err := os.WriteFile(path, []byte(`{"agents":[{"name":"duplicate","status":"open"},{"name":"duplicate","status":"closed","archivedAt":"2026-09-01"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &bptmux.Client{Bin: filepath.Join(t.TempDir(), "missing-tmux")}
	a := &app{ctx: context.Background(), config: bpconfig.Config{Agentbooks: []string{path}}, tmux: client, out: testOutput(t), err: testOutput(t)}
	if err := a.rename([]string{"duplicate", "duplicate-old", "--archived"}); err != nil {
		t.Fatal(err)
	}
	file, err := book.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if file.Agents[0].Name != "duplicate" || file.Agents[1].Name != "duplicate-old" || file.Agents[1].ArchivedAt == "" {
		t.Fatalf("registrations after archived rename: %+v", file.Agents)
	}
	if err := book.SetArchived([]string{path}, "duplicate", true, func(book.Agent) error { return nil }); err != nil {
		t.Fatalf("recovery command did not unblock archive: %v", err)
	}
}

func TestRenameDryRunShowsArchivedDestinationConflict(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	path := filepath.Join(t.TempDir(), "book.json")
	if err := os.WriteFile(path, []byte(`{"agents":[{"name":"source","status":"closed"},{"name":"target","status":"closed","archivedAt":"2026-09-01"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &app{ctx: context.Background(), config: bpconfig.Config{Agentbooks: []string{path}}, tmux: &bptmux.Client{Bin: filepath.Join(t.TempDir(), "missing-tmux")}, out: testOutput(t), err: testOutput(t)}
	err := a.rename([]string{"source", "target", "--dry-run"})
	if err == nil || !strings.Contains(err.Error(), "archived record") {
		t.Fatalf("error=%v, want archived-name conflict", err)
	}
	if output := readTestOutput(t, a.out); !strings.Contains(output, "dry run collision") || !strings.Contains(output, "bp rename target <free-name> --archived") {
		t.Fatalf("dry-run output=%q, want collision and recovery command", output)
	}
}

func TestQstatNamesPendingTargetMissingFromEveryBook(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	path := filepath.Join(t.TempDir(), "book.json")
	if err := os.WriteFile(path, []byte(`{"agents":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	queue := msgq.New(t.TempDir())
	id, err := queue.Enqueue("gone", "sender", "message waiting")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{config: bpconfig.Config{Agentbooks: []string{path}}, queue: queue, out: testOutput(t)}
	if err := a.queueStatus([]string{id}); err != nil {
		t.Fatal(err)
	}
	if output := readTestOutput(t, a.out); !strings.Contains(output, "no agentbook registration") || !strings.Contains(output, "cannot reopen") {
		t.Fatalf("qstat=%q, want missing-registration explanation", output)
	}
}

func TestRenameMigratesPendingMessagesAndListsChannels(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	path := filepath.Join(t.TempDir(), "book.json")
	if err := os.WriteFile(path, []byte(`{"agents":[{"name":"old","folder":"/repo","status":"closed"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	queue := msgq.New(t.TempDir())
	id, err := queue.Enqueue("old", "sender", "message waiting")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{
		ctx: context.Background(), config: bpconfig.Config{Agentbooks: []string{path}, StateDir: t.TempDir()},
		tmux: &bptmux.Client{Bin: filepath.Join(t.TempDir(), "missing-tmux")}, queue: queue,
		out: testOutput(t), err: testOutput(t),
	}
	if err := a.rename([]string{"old", "new", "--no-retitle"}); err != nil {
		t.Fatal(err)
	}
	message, err := queue.Record(id)
	if err != nil || message.To != "new" {
		t.Fatalf("message target=%q err=%v, want new", message.To, err)
	}
	if output := readTestOutput(t, a.out); !strings.Contains(output, "msgq channel "+id) || !strings.Contains(output, "target old -> new") {
		t.Fatalf("rename output=%q, want pending channel listed", output)
	}
}

func TestCloseReportsStateTransitionAndNoopTruthfully(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	path := filepath.Join(t.TempDir(), "book.json")
	if err := os.WriteFile(path, []byte(`{"agents":[{"name":"stale","status":"open"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "tmux")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncase \"$1\" in has-session) exit 1;; *) exit 0;; esac\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &app{
		ctx: context.Background(), config: bpconfig.Config{Agentbooks: []string{path}}, tmux: &bptmux.Client{Bin: bin}, out: testOutput(t),
		resolveSender: func() identity.Identity { return identity.Identity{Label: "caller"} },
	}
	if err := a.close([]string{"stale"}); err != nil {
		t.Fatal(err)
	}
	if output := readTestOutput(t, a.out); !strings.Contains(output, "stale: open -> closed") {
		t.Fatalf("close output=%q", output)
	}
	if got := bookStatus(t, path, "stale"); got != "closed" {
		t.Fatalf("book status=%q, want closed", got)
	}
	a.out = testOutput(t)
	if err := a.close([]string{"stale"}); err != nil {
		t.Fatal(err)
	}
	if output := readTestOutput(t, a.out); !strings.Contains(output, "already closed, no change") {
		t.Fatalf("second close output=%q", output)
	}
}
