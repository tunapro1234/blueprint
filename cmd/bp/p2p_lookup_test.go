package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/config"
	"blueprint/internal/msgq"
	"blueprint/internal/p2p"
	bptmux "blueprint/internal/tmux"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
)

func startP2PLookupFixture(t *testing.T, resolver func(string) p2p.LookupResponse) (string, *p2p.Node) {
	t.Helper()
	root := t.TempDir()
	listen := []string{"/ip4/127.0.0.1/tcp/0"}
	local, err := p2p.New(context.Background(), filepath.Join(root, "local"), p2p.Config{Enabled: true, Listen: listen}, msgq.New(filepath.Join(root, "local-q")))
	if err != nil {
		t.Fatal(err)
	}
	remote, err := p2p.New(context.Background(), filepath.Join(root, "remote"), p2p.Config{Enabled: true, Listen: listen}, msgq.New(filepath.Join(root, "remote-q")))
	if err != nil {
		_ = local.Close()
		t.Fatal(err)
	}
	local.Config.Peers = map[string]p2p.Peer{"peer": {ID: remote.Host.ID().String()}}
	remote.Config.Peers = map[string]p2p.Peer{"local": {ID: local.Host.ID().String()}}
	remote.ResolveLookup = resolver
	local.Host.Peerstore().AddAddrs(remote.Host.ID(), remote.Host.Addrs(), peerstore.PermanentAddrTTL)
	connectCtx, connectCancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := local.Host.Connect(connectCtx, peer.AddrInfo{ID: remote.Host.ID(), Addrs: remote.Host.Addrs()}); err != nil {
		connectCancel()
		_ = local.Close()
		_ = remote.Close()
		t.Fatal(err)
	}
	connectCancel()
	local.Log = io.Discard
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- local.Serve(ctx, nil) }()
	deadline := time.Now().Add(3 * time.Second)
	var info p2p.Info
	for p2p.Control(context.Background(), local.Root, http.MethodGet, "/status", &info) != nil {
		if time.Now().After(deadline) {
			cancel()
			<-done
			_ = local.Close()
			_ = remote.Close()
			t.Fatal("local P2P control endpoint did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		<-done
		_ = local.Close()
		_ = remote.Close()
	})
	return local.Root, remote
}

func TestP2PLookupCLITextJSONAndExitCodes(t *testing.T) {
	root, _ := startP2PLookupFixture(t, func(query string) p2p.LookupResponse {
		switch query {
		case "worker":
			return p2p.LookupResponse{Found: true, Name: "worker", State: "live"}
		case "native title":
			return p2p.LookupResponse{Found: true, Name: "canonical-worker", State: "closed"}
		default:
			return p2p.LookupResponse{}
		}
	})
	a := &app{ctx: context.Background(), config: config.Config{StateDir: root}, out: testOutput(t), err: testOutput(t)}
	if err := a.p2pLookup([]string{"worker"}); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, a.out); got != "tip: 'worker' exists on peer peer (live) — attach it on that machine\n" {
		t.Fatalf("text lookup output = %q", got)
	}

	a.out = testOutput(t)
	if err := a.p2pLookup([]string{"native title", "--json"}); err != nil {
		t.Fatal(err)
	}
	var report p2p.LookupReport
	if err := json.Unmarshal([]byte(readTestOutput(t, a.out)), &report); err != nil {
		t.Fatal(err)
	}
	if report.Query != "native title" || len(report.Peers) != 1 || !report.Peers[0].Found || report.Peers[0].Name != "canonical-worker" || report.Peers[0].State != "closed" {
		t.Fatalf("JSON lookup report = %+v", report)
	}

	a.out = testOutput(t)
	err := a.p2pLookup([]string{"missing", "--json"})
	assertLookupExitCode(t, err, 1)
	if err := json.Unmarshal([]byte(readTestOutput(t, a.out)), &report); err != nil || len(report.Peers) != 1 || report.Peers[0].Found {
		t.Fatalf("not-found JSON report = %+v, err=%v", report, err)
	}

	if code := runP2PLookupCLIChild(t, root, "worker"); code != 0 {
		t.Fatalf("bp p2p lookup found exit code = %d, want 0", code)
	}
	if code := runP2PLookupCLIChild(t, root, "missing"); code != 1 {
		t.Fatalf("bp p2p lookup miss exit code = %d, want 1", code)
	}

	down := &app{ctx: context.Background(), config: config.Config{StateDir: t.TempDir()}, out: testOutput(t), err: testOutput(t)}
	assertLookupExitCode(t, down.p2pLookup([]string{"worker"}), 2)
	if code := runP2PLookupCLIChild(t, filepath.Join(t.TempDir(), "no-service"), "worker"); code != 2 {
		t.Fatalf("bp p2p lookup unavailable exit code = %d, want 2", code)
	}
}

func TestP2PLookupCLIProcessHelper(t *testing.T) {
	if os.Getenv("BP_TEST_P2P_LOOKUP_CHILD") != "1" {
		return
	}
	os.Args = []string{"bp", "p2p", "lookup", os.Getenv("BP_TEST_P2P_LOOKUP_QUERY")}
	main()
	os.Exit(0)
}

func runP2PLookupCLIChild(t *testing.T, stateDir, query string) int {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"stateDir":`+mustJSON(t, stateDir)+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestP2PLookupCLIProcessHelper$")
	command.Env = make([]string, 0, len(os.Environ())+3)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "BP_HOME=") || strings.HasPrefix(value, "BP_TEST_P2P_LOOKUP_CHILD=") || strings.HasPrefix(value, "BP_TEST_P2P_LOOKUP_QUERY=") {
			continue
		}
		command.Env = append(command.Env, value)
	}
	command.Env = append(command.Env, "BP_HOME="+home, "BP_TEST_P2P_LOOKUP_CHILD=1", "BP_TEST_P2P_LOOKUP_QUERY="+query)
	if err := command.Run(); err == nil {
		return 0
	} else if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode()
	} else {
		t.Fatalf("run bp lookup child: %v", err)
		return -1
	}
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestP2PLookupCLIHonorsBudgetAndMarksOldPeer(t *testing.T) {
	root, remote := startP2PLookupFixture(t, func(string) p2p.LookupResponse {
		return p2p.LookupResponse{}
	})
	blocked := make(chan struct{})
	defer close(blocked)
	remote.Host.RemoveStreamHandler(p2p.LookupProtocol)
	remote.Host.SetStreamHandler(p2p.LookupProtocol, func(stream network.Stream) {
		defer stream.Close()
		<-blocked
	})
	a := &app{ctx: context.Background(), config: config.Config{StateDir: root}, out: testOutput(t), err: testOutput(t)}
	started := time.Now()
	err := a.p2pLookup([]string{"worker", "--timeout", "300ms", "--json"})
	assertLookupExitCode(t, err, 1)
	if elapsed := time.Since(started); elapsed > 800*time.Millisecond {
		t.Fatalf("lookup exceeded budget: %s", elapsed)
	}
	var report p2p.LookupReport
	if err := json.Unmarshal([]byte(readTestOutput(t, a.out)), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Peers) != 1 || report.Peers[0].Error != "error" || report.Peers[0].Found {
		t.Fatalf("old/non-answering peer report = %+v", report)
	}
}

func TestP2PLookupCLIQuietlySkipsUnsupportedPeerInText(t *testing.T) {
	root, remote := startP2PLookupFixture(t, func(string) p2p.LookupResponse {
		return p2p.LookupResponse{}
	})
	remote.Host.RemoveStreamHandler(p2p.LookupProtocol)
	a := &app{ctx: context.Background(), config: config.Config{StateDir: root}, out: testOutput(t), err: testOutput(t)}
	assertLookupExitCode(t, a.p2pLookup([]string{"worker"}), 1)
	if got := readTestOutput(t, a.out); got != "" {
		t.Fatalf("unsupported peer printed text output: %q", got)
	}

	a.out = testOutput(t)
	assertLookupExitCode(t, a.p2pLookup([]string{"worker", "--json"}), 1)
	var report p2p.LookupReport
	if err := json.Unmarshal([]byte(readTestOutput(t, a.out)), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Peers) != 1 || report.Peers[0].Error != "error" {
		t.Fatalf("unsupported peer JSON result = %+v", report)
	}
}

func TestAttachUnknownAgentAddsOnlyAvailablePeerTip(t *testing.T) {
	root, _ := startP2PLookupFixture(t, func(string) p2p.LookupResponse {
		return p2p.LookupResponse{Found: true, Name: "worker", State: "archived"}
	})
	a := &app{ctx: context.Background(), config: config.Config{StateDir: root}, tmux: &bptmux.Client{Bin: filepath.Join(t.TempDir(), "unused-tmux")}, out: testOutput(t), err: testOutput(t)}
	err := a.attach([]string{"worker"})
	if err == nil || err.Error() != `unknown agent: "worker"` {
		t.Fatalf("attach error = %v", err)
	}
	if got := readTestOutput(t, a.err); got != "tip: 'worker' exists on peer peer (archived) — attach it on that machine\n" {
		t.Fatalf("attach tip = %q", got)
	}

	down := &app{ctx: context.Background(), config: config.Config{StateDir: t.TempDir()}, out: testOutput(t), err: testOutput(t)}
	err = down.attach([]string{"worker"})
	if err == nil || err.Error() != `unknown agent: "worker"` {
		t.Fatalf("p2p-down attach error = %v", err)
	}
	if got := readTestOutput(t, down.err); got != "" {
		t.Fatalf("p2p-down attach changed output: %q", got)
	}
}

func TestResolvePeerLookupUsesAttachMatchingRules(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	root := t.TempDir()
	path := filepath.Join(root, "agentbook.json")
	file := book.File{Agents: []book.Agent{
		{Name: "canonical", NativeTitle: &book.NativeTitle{Text: "other title"}},
		{Name: "title-live", NativeTitle: &book.NativeTitle{Text: "native title"}},
		{Name: "closed"},
		{Name: "archived", ArchivedAt: "2026-01-01"},
		{Name: "ambiguous-one", NativeTitle: &book.NativeTitle{Text: "shared"}},
		{Name: "ambiguous-two", NativeTitle: &book.NativeTitle{Text: "shared"}},
		{Name: "title-collision", NativeTitle: &book.NativeTitle{Text: "canonical"}},
	}}
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	tmux := filepath.Join(root, "tmux")
	if err := os.WriteFile(tmux, []byte("#!/bin/sh\n[ \"$1\" = has-session ] && [ \"$3\" = =title-live ]\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := &app{ctx: context.Background(), config: config.Config{Agentbooks: []string{path}}, tmux: &bptmux.Client{Bin: tmux}}
	for _, test := range []struct {
		query string
		name  string
		state string
	}{
		{query: "canonical", name: "canonical", state: "closed"},
		{query: "native title", name: "title-live", state: "live"},
		{query: "closed", name: "closed", state: "closed"},
		{query: "archived", name: "archived", state: "archived"},
	} {
		got := a.resolvePeerLookup(test.query)
		if !got.Found || got.Name != test.name || got.State != test.state {
			t.Errorf("lookup %q = %+v", test.query, got)
		}
	}
	for _, query := range []string{"shared", "missing"} {
		if got := a.resolvePeerLookup(query); got.Found {
			t.Errorf("lookup %q unexpectedly found %+v", query, got)
		}
	}
}

func assertLookupExitCode(t *testing.T, err error, want int) {
	t.Helper()
	var exitErr *commandExitError
	if !errors.As(err, &exitErr) || exitErr.code != want {
		t.Fatalf("exit error = %#v, want code %d", err, want)
	}
	if exitErr.message != "" && strings.TrimSpace(exitErr.message) == "" {
		t.Fatal("exit error message is whitespace")
	}
}
