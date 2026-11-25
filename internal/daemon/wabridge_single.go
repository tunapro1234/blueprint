package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// bridgeProcessOps finds and signals WhatsApp bridge processes the daemon did
// not start itself (or no longer supervises). Two bridges on one WhatsApp
// session must never run: the bridge has no single-instance guard of its own,
// and a daemon that was SIGKILLed or crashed (the unit uses KillMode=process)
// leaves its bridge running as an orphan. Every field is injectable so tests
// need no real processes and no /proc.
type bridgeProcessOps struct {
	// find returns the pids of live processes that are this bridge.
	find func(bridgePath string) ([]int, error)
	// signal delivers sig to pid.
	signal func(pid int, sig syscall.Signal) error
	// alive reports whether pid still exists (a zombie counts as gone).
	alive func(pid int) bool
	// command builds the bridge process; the daemon starts it.
	command func(bridgePath string) *exec.Cmd
	// termWait and killWait bound the waits after SIGTERM and SIGKILL; poll
	// is how often alive is asked meanwhile; retry is the supervisor's pause
	// before it tries again after a failed start or an exit.
	termWait, killWait, poll, retry time.Duration
}

func defaultBridgeOps() bridgeProcessOps {
	euid := os.Geteuid()
	return bridgeProcessOps{
		find: func(bridgePath string) ([]int, error) {
			return findBridgeProcesses("/proc", bridgePath, euid, os.Getpid())
		},
		signal:   func(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) },
		alive:    procAlive,
		command:  bridgeCommand,
		termWait: 10 * time.Second,
		killWait: 5 * time.Second,
		poll:     100 * time.Millisecond,
		retry:    5 * time.Second,
	}
}

func (s *Service) bridgeOps() bridgeProcessOps {
	if s.waOps != nil {
		return *s.waOps
	}
	return defaultBridgeOps()
}

// bridgeCommand is the bridge as the daemon has always started it.
func bridgeCommand(bridgePath string) *exec.Cmd {
	cmd := exec.Command("node", bridgePath)
	cmd.Dir = filepath.Dir(bridgePath)
	cmd.Env = append(os.Environ(), "TMUX_TMPDIR=/tmp")
	return cmd
}

// removeLeftoverBridges makes sure no other process is this bridge before a
// new one starts: each one found gets SIGTERM, then SIGKILL if it outlives
// termWait, and the call returns only once it is gone. An error means a
// bridge is (or may be) still running, and the caller must not start another.
func (s *Service) removeLeftoverBridges(ctx context.Context, bridgePath string) error {
	ops := s.bridgeOps()
	pids, err := ops.find(bridgePath)
	if err != nil {
		return fmt.Errorf("cannot check for a running bridge (%v); not starting a second one", err)
	}
	for _, pid := range pids {
		s.log.Printf("wa-bridge: leftover bridge pid %d (node %s) is running; stopping it before starting a new one", pid, bridgePath)
		_ = ops.signal(pid, syscall.SIGTERM)
		if gone, err := waitProcessGone(ctx, ops, pid, ops.termWait); err != nil {
			return err
		} else if gone {
			continue
		}
		s.log.Printf("wa-bridge: leftover bridge pid %d outlived SIGTERM by %s; sending SIGKILL", pid, ops.termWait)
		_ = ops.signal(pid, syscall.SIGKILL)
		if gone, err := waitProcessGone(ctx, ops, pid, ops.killWait); err != nil {
			return err
		} else if !gone {
			return fmt.Errorf("leftover bridge pid %d (node %s) is still running after SIGTERM and SIGKILL; not starting a second bridge", pid, bridgePath)
		}
	}
	return nil
}

func waitProcessGone(ctx context.Context, ops bridgeProcessOps, pid int, limit time.Duration) (bool, error) {
	deadline := time.Now().Add(limit)
	for {
		if !ops.alive(pid) {
			return true, nil
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		if !wait(ctx, ops.poll) {
			return false, ctx.Err()
		}
	}
}

// findBridgeProcesses lists processes under procRoot whose argv is exactly
// "node <bridgePath>" (argv[0] may be a path to node; argv[1] is compared after
// cleaning, and any further argument rules the process out) and whose
// effective uid is euid. self is never returned.
func findBridgeProcesses(procRoot, bridgePath string, euid, self int) ([]int, error) {
	target, err := filepath.Abs(bridgePath)
	if err != nil {
		return nil, err
	}
	target = filepath.Clean(target)
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == self {
			continue
		}
		dir := filepath.Join(procRoot, entry.Name())
		data, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if err != nil || len(data) == 0 {
			continue
		}
		argv := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
		if len(argv) != 2 || filepath.Base(argv[0]) != "node" || !filepath.IsAbs(argv[1]) || filepath.Clean(argv[1]) != target {
			continue
		}
		if uid, ok := procEffectiveUID(dir); !ok || uid != euid {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

func procEffectiveUID(dir string) (int, bool) {
	status, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "Uid:" {
			uid, err := strconv.Atoi(fields[2])
			return uid, err == nil
		}
	}
	return 0, false
}

// procAlive reports whether pid exists and is not a zombie.
func procAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	status, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return !errors.Is(err, os.ErrNotExist)
	}
	for _, line := range strings.Split(string(status), "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "State:" {
			return fields[1] != "Z"
		}
	}
	return true
}
