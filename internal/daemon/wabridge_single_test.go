package daemon

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeBridgeProcs is a process table for the leftover check: no real bridge
// and no /proc. ignoresTerm survives SIGTERM, unkillable survives both.
type fakeBridgeProcs struct {
	mu          sync.Mutex
	live        map[int]bool
	ignoresTerm map[int]bool
	unkillable  map[int]bool
	events      []string
}

func (f *fakeBridgeProcs) record(event string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
}

func (f *fakeBridgeProcs) Events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

// ops wires the fake into a supervisor whose start records "start" and then
// cancels the supervisor, so one test pass ends right after the first start.
// The started command is a no-op child, not a bridge.
func (f *fakeBridgeProcs) ops(t *testing.T, cancel context.CancelFunc) *bridgeProcessOps {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("true is not installed")
	}
	return &bridgeProcessOps{
		find: func(string) ([]int, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			var pids []int
			for pid, alive := range f.live {
				if alive {
					pids = append(pids, pid)
				}
			}
			f.events = append(f.events, "find")
			return pids, nil
		},
		signal: func(pid int, sig syscall.Signal) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			name := map[syscall.Signal]string{syscall.SIGTERM: "TERM", syscall.SIGKILL: "KILL"}[sig]
			f.events = append(f.events, fmt.Sprintf("%s %d", name, pid))
			switch {
			case sig == syscall.SIGTERM && !f.ignoresTerm[pid] && !f.unkillable[pid]:
				f.live[pid] = false
			case sig == syscall.SIGKILL && !f.unkillable[pid]:
				f.live[pid] = false
			}
			return nil
		},
		alive: func(pid int) bool {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.live[pid]
		},
		command: func(string) *exec.Cmd {
			f.record("start")
			cancel()
			return exec.Command(truePath)
		},
		termWait: 30 * time.Millisecond,
		killWait: 30 * time.Millisecond,
		poll:     5 * time.Millisecond,
		retry:    10 * time.Millisecond,
	}
}

func runSupervisor(t *testing.T, procs *fakeBridgeProcs, timeout time.Duration) (*Service, *bytes.Buffer) {
	t.Helper()
	service, _, _ := blockedService(t)
	service.state = NewState(filepath.Join(t.TempDir(), "jobs.json"))
	var logs bytes.Buffer
	service.log = log.New(&logs, "", 0)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	service.waOps = procs.ops(t, cancel)
	service.superviseWA(ctx, "/srv/wa/bridge.js")
	return service, &logs
}

func TestSuperviseWAStartsWithoutLeftover(t *testing.T) {
	procs := &fakeBridgeProcs{live: map[int]bool{}}
	runSupervisor(t, procs, 5*time.Second)
	if got := procs.Events(); !reflect.DeepEqual(got, []string{"find", "start"}) {
		t.Fatalf("events=%v; want find then start", got)
	}
}

func TestSuperviseWATerminatesLeftoverBeforeStart(t *testing.T) {
	procs := &fakeBridgeProcs{live: map[int]bool{4242: true}}
	_, logs := runSupervisor(t, procs, 5*time.Second)
	if got := procs.Events(); !reflect.DeepEqual(got, []string{"find", "TERM 4242", "start"}) {
		t.Fatalf("events=%v; want the leftover terminated before the start", got)
	}
	if !strings.Contains(logs.String(), "leftover bridge pid 4242") {
		t.Fatalf("log=%q; want the leftover's pid named", logs.String())
	}
}

func TestSuperviseWAKillsLeftoverThatIgnoresTerm(t *testing.T) {
	procs := &fakeBridgeProcs{live: map[int]bool{4242: true}, ignoresTerm: map[int]bool{4242: true}}
	runSupervisor(t, procs, 5*time.Second)
	if got := procs.Events(); !reflect.DeepEqual(got, []string{"find", "TERM 4242", "KILL 4242", "start"}) {
		t.Fatalf("events=%v; want TERM, then KILL, then start", got)
	}
}

// A leftover that survives SIGKILL (another user's, uninterruptible sleep)
// blocks every start: the job is failed, the coordinator is alarmed once
// however many passes retry, and no second bridge is ever started.
func TestSuperviseWADoesNotStartBesideUnremovableLeftover(t *testing.T) {
	procs := &fakeBridgeProcs{live: map[int]bool{4242: true}, unkillable: map[int]bool{4242: true}}
	service, _ := runSupervisor(t, procs, 400*time.Millisecond)
	events := procs.Events()
	finds := 0
	for _, event := range events {
		if event == "start" {
			t.Fatalf("a second bridge was started beside a live leftover: %v", events)
		}
		if event == "find" {
			finds++
		}
	}
	if finds < 2 {
		t.Fatalf("supervisor did not retry: %v", events)
	}
	jobs, err := LoadState(service.state.path)
	if err != nil {
		t.Fatal(err)
	}
	if job := jobs["wa-bridge"]; job.Status != "failed" || !strings.Contains(job.Error, "still running after SIGTERM and SIGKILL") {
		t.Fatalf("job state=%+v; want failed with the reason", job)
	}
	alarms := 0
	for _, notice := range serverMainNotices(t, service.queue) {
		if strings.Contains(notice.Msg, "wa-bridge not started") {
			alarms++
		}
	}
	if alarms != 1 {
		t.Fatalf("%d alarms over %d passes; want exactly one", alarms, finds)
	}
}

// Only an exact "node <bridgePath>" of this euid is the bridge: a different
// script, extra arguments, node in another role, a wrapper shell, another
// user's process or the daemon itself are never touched.
func TestFindBridgeProcessesMatchesExactArgvAndUID(t *testing.T) {
	root := t.TempDir()
	proc := func(pid int, uid int, argv ...string) {
		dir := filepath.Join(root, fmt.Sprint(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		cmdline := strings.Join(argv, "\x00") + "\x00"
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644); err != nil {
			t.Fatal(err)
		}
		status := fmt.Sprintf("Name:\tnode\nUid:\t%d\t%d\t%d\t%d\n", uid, uid, uid, uid)
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	proc(10, 0, "node", "/srv/wa/bridge.js")
	proc(11, 0, "/usr/bin/node", "/srv/wa/../wa/bridge.js")
	proc(12, 0, "node", "/srv/wa/bridge.js", "--inspect")
	proc(13, 0, "node", "/srv/other/bridge.js")
	proc(14, 0, "nodemon", "/srv/wa/bridge.js")
	proc(15, 0, "/bin/sh", "-c", "node /srv/wa/bridge.js")
	proc(16, 0, "node", "--require", "/srv/wa/bridge.js")
	proc(17, 1000, "node", "/srv/wa/bridge.js")
	proc(18, 0, "node", "bridge.js")
	proc(19, 0, "node", "/srv/wa/bridge.js", "")
	proc(20, 0, "node", "/srv/wa/bridge.js.bak")
	proc(99, 0, "node", "/srv/wa/bridge.js") // the caller itself
	if err := os.MkdirAll(filepath.Join(root, "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	pids, err := findBridgeProcesses(root, "/srv/wa/bridge.js", 0, 99)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pids, []int{10, 11}) {
		t.Fatalf("pids=%v; want only the exact bridge argv of uid 0 (10, 11)", pids)
	}
	if _, err := findBridgeProcesses(filepath.Join(root, "missing"), "/srv/wa/bridge.js", 0, 99); err == nil {
		t.Fatal("an unreadable proc root must be an error, so the bridge is not started blind")
	}
}
