package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"blueprint/internal/api"
	"blueprint/internal/book"
	bpcache "blueprint/internal/cache"
	bpconfig "blueprint/internal/config"
	"blueprint/internal/identity"
	"blueprint/internal/pending"
)

// inboxTestApp is an isolated bp with the given agentbook agents (none live)
// and the inbox agents registered through the API.
func inboxTestApp(t *testing.T, bookAgents []string, inboxAgents ...string) (*app, string) {
	t.Helper()
	t.Setenv("AGENT", "ada")
	t.Setenv("TMUX", "")
	home := t.TempDir()
	stateDir := filepath.Join(home, "state")
	fleet := book.Fleet{Agents: map[string]book.Agent{}, Parents: map[string]string{}}
	for _, name := range bookAgents {
		fleet.Agents[name] = book.Agent{Name: name}
	}
	a := &app{
		ctx:           context.Background(),
		out:           testOutput(t),
		err:           testOutput(t),
		config:        bpconfig.Config{StateDir: stateDir, MsgqRoot: filepath.Join(home, "msgq")},
		sessionExists: func(string) bool { return false },
		loadFleet:     func() (book.Fleet, map[string]book.State, error) { return fleet, map[string]book.State{}, nil },
		loadCache:     func(map[string]string) map[string]bpcache.State { return nil },
	}
	trustedSenderFixture(a)
	core := api.NewCore(stateDir, nil, func(context.Context) ([]api.AgentInfo, error) { return nil, nil })
	for _, name := range inboxAgents {
		if _, err := core.Register(context.Background(), api.Caller{Name: name, Transport: "mcp"}, name, "phone app"); err != nil {
			t.Fatal(err)
		}
	}
	return a, stateDir
}

func TestMessageToInboxAgentLandsInItsInbox(t *testing.T) {
	a, stateDir := inboxTestApp(t, nil, "phone")
	if err := a.message([]string{"phone", "hello", "there"}); err != nil {
		t.Fatal(err)
	}
	out := readTestOutput(t, a.out)
	match := regexp.MustCompile(`(?m)^RESULT=queued CHANNEL=(ib[0-9a-f]+) ROUTE=inbox$`).FindStringSubmatch(out)
	if match == nil || strings.Contains(out, "offline") || strings.Contains(out, "delivered when it opens") {
		t.Fatalf("receipt:\n%s", out)
	}
	id := match[1]
	if snapshot, err := pending.Load(stateDir, "phone"); err != nil || len(snapshot.Entries) != 0 {
		t.Fatalf("inbox message went to the pending spool: %+v %v", snapshot.Entries, err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "api", "inbox", "phone.jsonl")); err != nil {
		t.Fatalf("no inbox file: %v", err)
	}

	// Not delivered until the agent reads it.
	a.out = testOutput(t)
	if err := a.queueStatus([]string{id}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); !strings.HasPrefix(got, "accepted: waiting in the inbox of phone") {
		t.Fatalf("qstat before read = %q", got)
	}

	// bp_inbox (the same Core.Inbox) returns it, from the verified sender.
	core := api.NewCore(stateDir, nil, func(context.Context) ([]api.AgentInfo, error) { return nil, nil })
	read, err := core.Inbox(api.Caller{Name: "phone", Transport: "mcp"}, "", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Messages) != 1 || read.Messages[0].Text != "hello there" || read.Messages[0].From != "ada" || read.Messages[0].ID != id {
		t.Fatalf("inbox = %+v", read.Messages)
	}
	a.out = testOutput(t)
	if err := a.queueStatus([]string{id, "--json"}); err != nil {
		t.Fatal(err)
	}
	var status api.SendResult
	if err := json.Unmarshal([]byte(readTestOutput(t, a.out)), &status); err != nil || status.State != api.StateDelivered || status.Route != "inbox" {
		t.Fatalf("qstat --json after read = %+v %v", status, err)
	}
}

func TestMessageToUnverifiedSenderIsMarked(t *testing.T) {
	// Every shape of unverified label the resolver produces is recorded as
	// cli:<name>, never as the bare (verified-looking) name.
	for _, label := range []string{"ada", "ada?", "zsh?:ada", "agent?:ada", "scope?:ada?", "scope?:codex?:ada"} {
		a, stateDir := inboxTestApp(t, nil, "phone")
		a.resolveSender = func() identity.Identity { return identity.Identity{Label: label, Source: "test"} }
		if err := a.message([]string{"phone", "hi"}); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		core := api.NewCore(stateDir, nil, func(context.Context) ([]api.AgentInfo, error) { return nil, nil })
		read, err := core.Inbox(api.Caller{Name: "phone", Transport: "mcp"}, "", 0, false)
		if err != nil || len(read.Messages) != 1 || read.Messages[0].From != "cli:ada" {
			t.Fatalf("%s: inbox = %+v %v", label, read.Messages, err)
		}
	}
}

func TestMessageKeepsTerminalPathsBeforeInbox(t *testing.T) {
	// A closed agentbook agent keeps the pending spool even if an inbox
	// registration with its name exists: the terminal path wins.
	a, stateDir := inboxTestApp(t, []string{"phone"}, "phone")
	if err := a.message([]string{"phone", "hello"}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); got != "queued for phone (offline; delivered when it opens)\n" {
		t.Fatalf("output=%q", got)
	}
	if snapshot, err := pending.Load(stateDir, "phone"); err != nil || len(snapshot.Entries) != 1 {
		t.Fatalf("pending = %+v %v", snapshot.Entries, err)
	}

	// A name that is neither keeps the spool too.
	b, stateDir := inboxTestApp(t, nil)
	if err := b.message([]string{"nobody", "hello"}); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := pending.Load(stateDir, "nobody"); err != nil || len(snapshot.Entries) != 1 {
		t.Fatalf("pending = %+v %v", snapshot.Entries, err)
	}
}

func TestMessageToInboxAgentRefusesForceBusy(t *testing.T) {
	a, stateDir := inboxTestApp(t, nil, "phone")
	err := a.inboxMessage(a.senderIdentity(), "phone", "hello", true)
	if err == nil || !strings.Contains(err.Error(), "inbox agent") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "api", "inbox", "phone.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("a refused message was stored: %v", err)
	}
}

func TestStatusListsInboxAgents(t *testing.T) {
	a, _ := inboxTestApp(t, nil, "phone")
	if err := a.message([]string{"phone", "hello"}); err != nil {
		t.Fatal(err)
	}
	a.out = testOutput(t)
	if err := a.status(nil); err != nil {
		t.Fatal(err)
	}
	text := readTestOutput(t, a.out)
	if !regexp.MustCompile(`(?m)^INBOX AGENT +UNREAD +DESCRIPTION$`).MatchString(text) ||
		!regexp.MustCompile(`(?m)^phone +1 +phone app$`).MatchString(text) {
		t.Fatalf("status:\n%s", text)
	}
	a.out = testOutput(t)
	if err := a.status([]string{"--json"}); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Inbox []api.InboxSummary `json:"inbox_agents"`
	}
	if err := json.Unmarshal([]byte(readTestOutput(t, a.out)), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Inbox) != 1 || report.Inbox[0].Name != "phone" || report.Inbox[0].Unread != 1 {
		t.Fatalf("inbox_agents = %+v", report.Inbox)
	}
}
