package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blueprint/internal/book"
	"blueprint/internal/config"
	bptmux "blueprint/internal/tmux"
)

// reconcileService runs reconcileExitedAgents against a private fake tmux
// whose list-sessions prints stdout and exits with code.
func reconcileService(t *testing.T, stdout, stderr string, code int, agents ...string) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\nfor arg; do case \"$arg\" in list-sessions) printf '%s' '" + stdout + "'; printf '%s' '" + stderr + "' >&2; exit " + string(rune('0'+code)) + ";; esac; done\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	path := writeBook(t, "server-main", agents...)
	return &Service{
		config: config.Config{StateDir: t.TempDir(), Agentbooks: []string{path}},
		tmux:   &bptmux.Client{Bin: bin},
		log:    log.New(io.Discard, "", 0),
		// No tmux server process: the reboot / crashed-server shape. Tests
		// that need a live server process replace it.
		serverProcessAlive: func() bool { return false },
	}, path
}

func daemonBookStatus(t *testing.T, path, name string) string {
	t.Helper()
	file, err := book.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range file.Agents {
		if agent.Name == name {
			return agent.Status
		}
	}
	t.Fatalf("%s missing from %s", name, path)
	return ""
}

// After a crashed or exited tmux server (its socket file left behind) the
// daemon is the one that records every agent gone: "no server running" must
// keep closing the fleet here.
func TestDaemonReconcileClosesFleetWhenNoServerRuns(t *testing.T) {
	service, path := reconcileService(t, "", "no server running on /tmp/tmux-0/default", 1, "alpha", "beta")
	if err := service.reconcileExitedAgents(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "beta"} {
		if got := daemonBookStatus(t, path, name); got != "closed" {
			t.Fatalf("%s=%q after reboot-shaped listing; want closed", name, got)
		}
	}
}

// tmux 3.4 after a reboot: /tmp was cleared, so there is no socket file and
// tmux reports a connection error rather than "no server running".
func TestDaemonReconcileClosesFleetAfterReboot(t *testing.T) {
	service, path := reconcileService(t, "", "error connecting to /tmp/tmux-0/default (No such file or directory)", 1, "alpha", "beta")
	if err := service.reconcileExitedAgents(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "beta"} {
		if got := daemonBookStatus(t, path, name); got != "closed" {
			t.Fatalf("%s=%q after a reboot-shaped listing; want closed", name, got)
		}
	}
}

func TestDaemonReconcileClosesOnlyAbsentSessions(t *testing.T) {
	service, path := reconcileService(t, "alpha\n", "", 0, "alpha", "beta")
	if err := service.reconcileExitedAgents(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := daemonBookStatus(t, path, "alpha"); got != "open" {
		t.Fatalf("alpha=%q; want open (session listed)", got)
	}
	if got := daemonBookStatus(t, path, "beta"); got != "closed" {
		t.Fatalf("beta=%q; want closed (session absent)", got)
	}
}

// A listing tmux refused for another reason proves nothing: no record moves.
func TestDaemonReconcileLeavesBookOnListingError(t *testing.T) {
	service, path := reconcileService(t, "", "error connecting to /tmp/tmux-0/default (Permission denied)", 1, "alpha", "beta")
	if err := service.reconcileExitedAgents(context.Background()); err == nil {
		t.Fatal("reconcile accepted a failed listing")
	}
	for _, name := range []string{"alpha", "beta"} {
		if got := daemonBookStatus(t, path, name); got != "open" {
			t.Fatalf("%s=%q after a failed listing; want open", name, got)
		}
	}
}

// A socket that does not answer while a tmux server process lives is a
// removed socket file under a running server: nothing may be closed, and the
// hold is logged once, not on every tick. When the process is gone too, the
// next pass closes the fleet and logs that the reconcile resumed.
func TestDaemonReconcileHoldsWhileServerProcessLives(t *testing.T) {
	service, path := reconcileService(t, "", "error connecting to /tmp/tmux-0/default (No such file or directory)", 1, "alpha", "beta")
	var logs bytes.Buffer
	service.log = log.New(&logs, "", 0)
	alive := true
	service.serverProcessAlive = func() bool { return alive }
	for pass := 0; pass < 3; pass++ {
		if err := service.reconcileExitedAgents(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"alpha", "beta"} {
		if got := daemonBookStatus(t, path, name); got != "open" {
			t.Fatalf("%s=%q while a tmux server process lives; want open", name, got)
		}
	}
	if got := strings.Count(logs.String(), "agentbook-reconcile: held"); got != 1 {
		t.Fatalf("held logged %d times over 3 passes, want once:\n%s", got, logs.String())
	}

	alive = false
	if err := service.reconcileExitedAgents(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "beta"} {
		if got := daemonBookStatus(t, path, name); got != "closed" {
			t.Fatalf("%s=%q once no server process is left; want closed", name, got)
		}
	}
	if got := strings.Count(logs.String(), "agentbook-reconcile: resumed"); got != 1 {
		t.Fatalf("resumed logged %d times, want once:\n%s", got, logs.String())
	}
}

// A reached server's listing is proof by itself: the process check is not
// consulted and absent sessions close as before.
func TestDaemonReconcileReachedIgnoresProcessCheck(t *testing.T) {
	service, path := reconcileService(t, "alpha\n", "", 0, "alpha", "beta")
	service.serverProcessAlive = func() bool {
		t.Fatal("process check consulted for a reached listing")
		return true
	}
	if err := service.reconcileExitedAgents(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := daemonBookStatus(t, path, "alpha"); got != "open" {
		t.Fatalf("alpha=%q; want open", got)
	}
	if got := daemonBookStatus(t, path, "beta"); got != "closed" {
		t.Fatalf("beta=%q; want closed", got)
	}
}

// While held, the automatic archive is skipped too: its session check would
// read the unanswered socket as "no session".
func TestDaemonReconcileHoldSkipsArchive(t *testing.T) {
	for _, alive := range []bool{true, false} {
		service, path := reconcileService(t, "", "error connecting to /tmp/tmux-0/default (No such file or directory)", 1)
		data, err := json.Marshal(map[string]any{"orchestrator": "server-main", "agents": []map[string]string{
			{"name": "eph", "folder": t.TempDir(), "status": "closed", "lifetime": "ephemeral"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		service.config.Lifecycle.ArchiveOnClose = true
		service.serverProcessAlive = func() bool { return alive }
		if err := service.reconcileExitedAgents(context.Background()); err != nil {
			t.Fatal(err)
		}
		file, err := book.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		archived := len(file.Agents) == 1 && file.Agents[0].ArchivedAt != ""
		if archived == alive {
			t.Fatalf("server process alive=%v: archived=%v, want %v", alive, archived, !alive)
		}
	}
}

func TestTmuxServerProcessAliveScansCommAndEffectiveUID(t *testing.T) {
	root := t.TempDir()
	proc := func(pid, comm, uids string) {
		dir := filepath.Join(root, pid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte("Name:\t"+comm+"\nUid:\t"+uids+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	proc("100", "tmux: server", "0\t0\t0\t0")
	proc("200", "tmux", "1000\t1000\t1000\t1000") // a client, not the server
	proc("300", "tmux: server", "0\t1000\t1000\t1000")
	proc("400", "bash", "7\t7\t7\t7")
	if err := os.MkdirAll(filepath.Join(root, "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		euid int
		want bool
	}{{0, true}, {1000, true}, {7, false}, {5, false}} {
		if got := tmuxServerProcessAlive(root, test.euid); got != test.want {
			t.Fatalf("euid %d: alive=%v, want %v", test.euid, got, test.want)
		}
	}
	if tmuxServerProcessAlive(filepath.Join(root, "missing"), 0) {
		t.Fatal("an unreadable proc root reported a live server")
	}
}
