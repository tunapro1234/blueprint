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
)

func newRoomTestApp(t *testing.T, who string) *app {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	config := bpconfig.Config{StateDir: stateDir, MsgqRoot: filepath.Join(root, "msgq")}
	a := &app{
		ctx:    context.Background(),
		config: config,
		queue:  msgq.New(config.MsgqRoot),
		out:    testOutput(t),
		err:    testOutput(t),
		loadFleet: func() (book.Fleet, map[string]book.State, error) {
			return book.Fleet{
					Agents: map[string]book.Agent{"ada": {Name: "ada"}, "bob": {Name: "bob"}},
					Order:  []string{"ada", "bob"},
				}, map[string]book.State{
					"ada": {Alive: true}, "bob": {Alive: true},
				}, nil
		},
	}
	if who != "" {
		a.resolveSender = func() identity.Identity { return identity.Identity{Label: who, Certain: true, Source: "tmux"} }
	} else {
		a.resolveSender = func() identity.Identity { return identity.Identity{Label: "stranger", Certain: false, Source: "codex-unverified"} }
	}
	return a
}

// A terminal agent can make a room, post to it, and read the thread back over
// plain bp — the hive-mind surface the API/MCP already expose, now reachable
// without an MCP client.
func TestRoomJoinPostRead(t *testing.T) {
	a := newRoomTestApp(t, "ada")

	if err := a.run([]string{"room", "join", "lab", "--topic", "ship it", "--with", "bob"}); err != nil {
		t.Fatalf("room join: %v", err)
	}
	if err := a.run([]string{"room", "post", "lab", "tests", "are", "green"}); err != nil {
		t.Fatalf("room post: %v", err)
	}

	out := testOutput(t)
	a.out = out
	if err := a.run([]string{"room", "read", "lab"}); err != nil {
		t.Fatalf("room read: %v", err)
	}
	got := readTestOutput(t, out)
	if !strings.Contains(got, "tests are green") {
		t.Fatalf("room read did not show the post:\n%s", got)
	}
	if !strings.Contains(got, "ada") {
		t.Fatalf("room read did not attribute the author:\n%s", got)
	}

	// bob, a member, received the post in his queue (the pub/sub fan-out).
	pending, err := a.queue.PendingForTarget("bob")
	if err != nil {
		t.Fatalf("read bob's queue: %v", err)
	}
	if len(pending) == 0 {
		t.Fatalf("post was not delivered to member bob's queue")
	}
}

// A board is shared versioned state: put, read back, and see the change in
// history.
func TestBoardPutGetHistory(t *testing.T) {
	a := newRoomTestApp(t, "ada")

	if err := a.run([]string{"board", "put", "status", "deploy", "rolling"}); err != nil {
		t.Fatalf("board put: %v", err)
	}
	out := testOutput(t)
	a.out = out
	if err := a.run([]string{"board", "get", "status", "deploy"}); err != nil {
		t.Fatalf("board get: %v", err)
	}
	if got := readTestOutput(t, out); !strings.Contains(got, "rolling") {
		t.Fatalf("board get did not return the value:\n%s", got)
	}

	hist := testOutput(t)
	a.out = hist
	if err := a.run([]string{"board", "history", "status", "deploy"}); err != nil {
		t.Fatalf("board history: %v", err)
	}
	if got := readTestOutput(t, hist); !strings.Contains(got, "rolling") || !strings.Contains(got, "ada") {
		t.Fatalf("board history missing the change:\n%s", got)
	}
}

// Writes need a proven identity: a command from outside a known agent terminal
// cannot post as anyone, so no stray shell speaks for an agent.
func TestRoomWriteRefusedWithoutIdentity(t *testing.T) {
	a := newRoomTestApp(t, "") // unverified sender

	if err := a.run([]string{"room", "post", "lab", "hello"}); err == nil {
		t.Fatal("room post from an unverified caller should be refused")
	}
	if err := a.run([]string{"board", "put", "status", "k", "v"}); err == nil {
		t.Fatal("board put from an unverified caller should be refused")
	}
	// A read-only listing needs no identity.
	if err := a.run([]string{"room", "list"}); err != nil {
		t.Fatalf("room list should work without a proven identity: %v", err)
	}
}
