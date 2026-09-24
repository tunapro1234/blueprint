package main

import (
	"strings"
	"testing"

	"blueprint/internal/book"
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
