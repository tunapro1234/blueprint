package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bpconfig "blueprint/internal/config"
	"blueprint/internal/p2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func testPeerID(t *testing.T) string {
	t.Helper()
	key, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return id.String()
}

// loadTestHome writes one config file into a fresh temporary bp home and loads
// it the way a CLI would, never touching the live installation.
func loadTestHome(t *testing.T, name, content string) bpconfig.Config {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := bpconfig.LoadHome(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.InvalidConfig != "" || cfg.P2P == nil {
		t.Fatalf("config not loaded: invalid=%q p2p=%v", cfg.InvalidConfig, cfg.P2P)
	}
	return cfg
}

func TestP2PConfigFingerprintIgnoresFileFormatting(t *testing.T) {
	a, b := testPeerID(t), testPeerID(t)
	compact := `{"p2p":{"enabled":true,"peers":{"alpha":{"id":"` + a + `","expose":["main","worker"]},"beta":{"id":"` + b + `","expose":["main"]}}}}`
	reordered := `{
  "p2p": {
    "peers": {
      "beta":  { "expose": [ "main" ], "id": "` + b + `" },
      "alpha": { "expose": [ "worker", "main" ], "id": "` + a + `" }
    },
    "enabled": true
  }
}
`
	yaml := "p2p:\n  enabled: true\n  peers:\n    beta:\n      id: " + b + "\n      expose: [main]\n    alpha:\n      id: " + a + "\n      expose:\n        - main\n        - worker\n"
	changed := `{"p2p":{"enabled":true,"peers":{"alpha":{"id":"` + a + `","expose":["main","worker"]},"beta":{"id":"` + b + `","expose":["main","worker"]}}}}`
	reference := p2p.ConfigFingerprint(loadTestHome(t, "config.json", compact).P2P)
	cases := []struct {
		name, file, content string
		same                bool
	}{
		{"same file", "config.json", compact, true},
		{"keys reordered and reindented", "config.json", reordered, true},
		{"yaml", "config.yaml", yaml, true},
		{"expose list changed", "config.json", changed, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := p2p.ConfigFingerprint(loadTestHome(t, tc.file, tc.content).P2P)
			if (got == reference) != tc.same {
				t.Fatalf("fingerprint equal=%v, want %v", got == reference, tc.same)
			}
		})
	}
}

// fakeP2PControl answers /status on the node's unix control socket as a live
// node with this pid would; no network listener and no libp2p host.
func fakeP2PControl(t *testing.T, stateDir string, pid int) func() {
	t.Helper()
	socket := p2p.SocketPath(stateDir)
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skip("unix socket unavailable:", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(p2p.Info{ID: "test-node", PID: pid, Peers: []p2p.PeerStatus{}})
	})}
	go func() { _ = server.Serve(listener) }()
	stop := func() { _ = server.Close() }
	t.Cleanup(stop)
	return stop
}

func TestConfigCheckWarnsAboutStaleP2PNode(t *testing.T) {
	id := testPeerID(t)
	onDisk := loadTestHome(t, "config.json", `{"p2p":{"enabled":true,"peers":{"alpha":{"id":"`+id+`","expose":["main","worker"]}}}}`)
	loaded := p2p.Config{Enabled: true, Peers: map[string]p2p.Peer{"alpha": {ID: id, Expose: []string{"main"}}}}

	run := func(t *testing.T, args ...string) (string, string) {
		t.Helper()
		out, errOut := testOutput(t), testOutput(t)
		a := &app{ctx: context.Background(), config: onDisk, out: out, err: errOut}
		if err := a.run(args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return readTestOutput(t, out), readTestOutput(t, errOut)
	}
	const warning = "warning: the running P2P node (pid "

	// No node: nothing beyond the normal result.
	out, errOut := run(t, "config", "check")
	if !strings.HasPrefix(out, "OK: ") || errOut != "" {
		t.Fatalf("no node: out=%q err=%q", out, errOut)
	}

	// The test process plays a node that loaded the older expose list.
	if err := p2p.WriteRunningConfig(onDisk.StateDir, loaded); err != nil {
		t.Fatal(err)
	}
	// Record present but no node answering: it has exited, stay silent.
	if _, errOut = run(t, "config", "check"); errOut != "" {
		t.Fatalf("exited node: err=%q", errOut)
	}
	stop := fakeP2PControl(t, onDisk.StateDir, os.Getpid())
	out, errOut = run(t, "config", "check")
	if !strings.HasPrefix(out, "OK: ") || strings.Contains(out, warning) {
		t.Fatalf("stale node: out=%q", out)
	}
	if !strings.HasPrefix(errOut, warning) || strings.Count(errOut, "\n") != 1 ||
		!strings.Contains(errOut, "peers.<name>.expose") || !strings.Contains(errOut, "bp p2p stop") {
		t.Fatalf("stale node: err=%q", errOut)
	}
	out, errOut = run(t, "p2p", "status")
	if !strings.HasPrefix(out, "test-node (pid ") || !strings.HasPrefix(errOut, warning) {
		t.Fatalf("p2p status: out=%q err=%q", out, errOut)
	}
	out, errOut = run(t, "p2p", "status", "--json")
	var info p2p.Info
	if json.Unmarshal([]byte(out), &info) != nil || !strings.HasPrefix(errOut, warning) {
		t.Fatalf("p2p status --json must stay one JSON document: out=%q err=%q", out, errOut)
	}

	// The node runs exactly the config on disk: silent.
	if err := p2p.WriteRunningConfig(onDisk.StateDir, *onDisk.P2P); err != nil {
		t.Fatal(err)
	}
	if _, errOut = run(t, "config", "check"); errOut != "" {
		t.Fatalf("fresh node: err=%q", errOut)
	}
	if _, errOut = run(t, "p2p", "status"); errOut != "" {
		t.Fatalf("fresh node status: err=%q", errOut)
	}

	// A node that wrote no record (an older bp) is never reported.
	stop()
	fakeP2PControl(t, onDisk.StateDir, os.Getpid()+1)
	if err := p2p.WriteRunningConfig(onDisk.StateDir, loaded); err != nil {
		t.Fatal(err)
	}
	if _, errOut = run(t, "config", "check"); errOut != "" {
		t.Fatalf("node without its own record: err=%q", errOut)
	}
}
