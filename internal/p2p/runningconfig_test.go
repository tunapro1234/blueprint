package p2p

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blueprint/internal/buildinfo"
)

func TestConfigFingerprint(t *testing.T) {
	base := func() *Config {
		return &Config{Enabled: true, Listen: []string{"/ip4/127.0.0.1/tcp/4001"}, Peers: map[string]Peer{
			"alpha": {ID: "peer-a", Expose: []string{"main", "worker"}, Rate: 10},
			"beta":  {ID: "peer-b", Expose: []string{"main"}},
		}}
	}
	reference := ConfigFingerprint(base())
	cases := []struct {
		name   string
		edit   func(*Config)
		stable bool
	}{
		{"same values", func(*Config) {}, true},
		{"peers inserted in another order", func(c *Config) {
			c.Peers = map[string]Peer{}
			c.Peers["beta"] = Peer{ID: "peer-b", Expose: []string{"main"}}
			c.Peers["alpha"] = Peer{ID: "peer-a", Expose: []string{"main", "worker"}, Rate: 10}
		}, true},
		{"expose reordered", func(c *Config) {
			p := c.Peers["alpha"]
			p.Expose = []string{"worker", "main"}
			c.Peers["alpha"] = p
		}, true},
		{"expose duplicate", func(c *Config) {
			p := c.Peers["alpha"]
			p.Expose = []string{"main", "worker", "main"}
			c.Peers["alpha"] = p
		}, true},
		{"empty instead of nil lists", func(c *Config) {
			c.Advertise, c.Rendezvous = []string{}, []string{}
		}, true},
		{"expose gains an agent", func(c *Config) {
			p := c.Peers["beta"]
			p.Expose = append(p.Expose, "worker")
			c.Peers["beta"] = p
		}, false},
		{"expose emptied", func(c *Config) {
			p := c.Peers["beta"]
			p.Expose = nil
			c.Peers["beta"] = p
		}, false},
		{"peer added", func(c *Config) { c.Peers["gamma"] = Peer{ID: "peer-c"} }, false},
		{"rate changed", func(c *Config) {
			p := c.Peers["alpha"]
			p.Rate = 11
			c.Peers["alpha"] = p
		}, false},
		{"disabled", func(c *Config) { c.Enabled = false }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.edit(c)
			if got := ConfigFingerprint(c) == reference; got != tc.stable {
				t.Fatalf("fingerprint equal=%v, want %v", got, tc.stable)
			}
		})
	}
	if ConfigFingerprint(nil) == reference || ConfigFingerprint(nil) != ConfigFingerprint(nil) {
		t.Fatal("a removed p2p section must have its own stable fingerprint")
	}
	// The input must not be modified by canonicalisation.
	c := base()
	p := c.Peers["alpha"]
	p.Expose = []string{"worker", "main"}
	c.Peers["alpha"] = p
	ConfigFingerprint(c)
	if got := c.Peers["alpha"].Expose; got[0] != "worker" {
		t.Fatalf("caller's expose list was reordered: %v", got)
	}
}

// Pins the canonical serialization: if this changes, a node started by the
// previous release would warn falsely. Bump runningConfigScheme instead.
func TestConfigFingerprintGolden(t *testing.T) {
	c := &Config{Enabled: true, Peers: map[string]Peer{"alpha": {ID: "peer-a", Expose: []string{"main"}}}}
	// sha256("bp-p2p-config-v1\n" + `{"enabled":true,"peers":{"alpha":{"id":"peer-a","expose":["main"]}}}`)
	const want = "757dca388276b1b315c35a17050701457f00dbbee3ee571e2f44b61ea6436ea7"
	if got := ConfigFingerprint(c); got != want {
		t.Fatalf("fingerprint scheme changed: got %s", got)
	}
}

func TestRunningConfigRecordRoundTrip(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Enabled: true, Peers: map[string]Peer{"alpha": {ID: "peer-a", Expose: []string{"main"}, LoopCap: 3}}}
	before := time.Now().Add(-time.Second)
	if err := WriteRunningConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	r, err := ReadRunningConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if r.PID != os.Getpid() || r.Fingerprint != ConfigFingerprint(&cfg) || r.Scheme != runningConfigScheme || r.Started.Before(before) {
		t.Fatalf("record = %+v", r)
	}
	if r.StartTicks != buildinfo.ProcessStartTicks(os.Getpid()) {
		t.Fatalf("start ticks = %q", r.StartTicks)
	}
	info, err := os.Stat(RunningConfigPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	if filepath.Dir(RunningConfigPath(root)) != filepath.Dir(statePath(root, "service.lock")) {
		t.Fatal("record must live next to the service lock")
	}
	entries, _ := os.ReadDir(filepath.Dir(RunningConfigPath(root)))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".write-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
	data, _ := os.ReadFile(RunningConfigPath(root))
	for _, secret := range []string{"peer-a", "alpha", "main"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("record contains config text %q: %s", secret, data)
		}
	}
}

func TestCompareRunningConfig(t *testing.T) {
	loaded := Config{Enabled: true, Peers: map[string]Peer{"alpha": {ID: "peer-a", Expose: []string{"main"}}}}
	edited := Config{Enabled: true, Peers: map[string]Peer{"alpha": {ID: "peer-a", Expose: []string{"main", "worker"}}}}
	self := os.Getpid()
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Skip("cannot start a short-lived process:", err)
	}
	deadPID := dead.ProcessState.Pid()
	write := func(t *testing.T, root string, r RunningConfig) {
		t.Helper()
		data, _ := json.Marshal(r)
		if err := os.MkdirAll(statePath(root), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(RunningConfigPath(root), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	record := func(pid int, ticks string, cfg Config) RunningConfig {
		return RunningConfig{Scheme: runningConfigScheme, Fingerprint: ConfigFingerprint(&cfg), PID: pid, StartTicks: ticks, Started: time.Now().UTC()}
	}
	selfTicks := buildinfo.ProcessStartTicks(self)
	cases := []struct {
		name    string
		setup   func(t *testing.T, root string)
		livePID int
		current *Config
		want    DriftState
	}{
		{"stale", func(t *testing.T, root string) { write(t, root, record(self, selfTicks, loaded)) }, self, &edited, DriftStale},
		{"fresh", func(t *testing.T, root string) { write(t, root, record(self, selfTicks, loaded)) }, self, &loaded, DriftCurrent},
		{"p2p section removed", func(t *testing.T, root string) { write(t, root, record(self, selfTicks, loaded)) }, self, nil, DriftStale},
		{"no record (older binary)", func(*testing.T, string) {}, self, &edited, DriftUnknown},
		{"record from another process", func(t *testing.T, root string) { write(t, root, record(self+1, selfTicks, loaded)) }, self, &edited, DriftUnknown},
		{"dead pid", func(t *testing.T, root string) { write(t, root, record(deadPID, "1", loaded)) }, deadPID, &edited, DriftUnknown},
		{"pid reused by another process", func(t *testing.T, root string) { write(t, root, record(self, "1", loaded)) }, self, &edited, DriftUnknown},
		{"unreadable record", func(t *testing.T, root string) {
			_ = os.MkdirAll(statePath(root), 0o700)
			if err := os.WriteFile(RunningConfigPath(root), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, self, &edited, DriftUnknown},
		{"other scheme", func(t *testing.T, root string) {
			r := record(self, selfTicks, loaded)
			r.Scheme = "bp-p2p-config-v0"
			write(t, root, r)
		}, self, &edited, DriftUnknown},
		{"no live node", func(t *testing.T, root string) { write(t, root, record(self, selfTicks, loaded)) }, 0, &edited, DriftUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.setup(t, root)
			d := CompareRunningConfig(root, tc.livePID, tc.current)
			if d.State != tc.want {
				t.Fatalf("state = %v, want %v", d.State, tc.want)
			}
			if w := d.Warning(); (w != "") != (tc.want == DriftStale) {
				t.Fatalf("warning = %q", w)
			}
		})
	}
}

// fakeControl answers /status on the node's control socket like a live node.
// It is a unix socket in the test's temp dir, not a network listener.
func fakeControl(t *testing.T, root string, pid int) func() {
	t.Helper()
	if err := os.MkdirAll(statePath(root), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", SocketPath(root))
	if err != nil {
		t.Skip("unix socket unavailable:", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Info{ID: "test", PID: pid})
	})}
	go func() { _ = server.Serve(listener) }()
	stop := func() { _ = server.Close() }
	t.Cleanup(stop)
	return stop
}

func TestCheckRunningConfigProvesLiveness(t *testing.T) {
	loaded := Config{Enabled: true, Peers: map[string]Peer{"alpha": {ID: "peer-a", Expose: []string{"main"}}}}
	edited := Config{Enabled: true, Peers: map[string]Peer{"alpha": {ID: "peer-a", Expose: []string{"main", "worker"}}}}
	root := t.TempDir()
	if d := CheckRunningConfig(context.Background(), root, &edited); d.State != DriftUnknown {
		t.Fatalf("no record: %v", d.State)
	}
	if err := WriteRunningConfig(root, loaded); err != nil {
		t.Fatal(err)
	}
	// A record without a reachable node is a node that has exited.
	if d := CheckRunningConfig(context.Background(), root, &edited); d.State != DriftUnknown {
		t.Fatalf("no node: %v", d.State)
	}
	stop := fakeControl(t, root, os.Getpid())
	d := CheckRunningConfig(context.Background(), root, &edited)
	if d.State != DriftStale || d.PID != os.Getpid() {
		t.Fatalf("live stale node: %+v", d)
	}
	if !strings.Contains(d.Warning(), "bp p2p stop") || !strings.Contains(d.Warning(), "peers.<name>.expose") {
		t.Fatalf("warning = %q", d.Warning())
	}
	if d := CheckRunningConfig(context.Background(), root, &loaded); d.State != DriftCurrent {
		t.Fatalf("live fresh node: %v", d.State)
	}
	stop()
	// A node from an older binary answers with a pid that wrote no record.
	root2 := t.TempDir()
	if err := WriteRunningConfig(root2, loaded); err != nil {
		t.Fatal(err)
	}
	fakeControl(t, root2, os.Getpid()+1)
	if d := CheckRunningConfig(context.Background(), root2, &edited); d.State != DriftUnknown {
		t.Fatalf("live node that did not write the record: %v", d.State)
	}
}
