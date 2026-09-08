package identity

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Origin is execution context, separate from a caller's proposed display name.
// It prevents inherited app-server tmux state from identifying another thread.
// It is not a boundary against processes with the same OS credentials.
type Origin struct {
	ThreadID string
	Verified bool
}

// CodexOrigin cross-checks CODEX_THREAD_ID against the original environment of
// the per-execution Codex sandbox helper. An env assignment on the bp command
// alone cannot establish a different thread. Inaccessible evidence fails closed.
func CodexOrigin(context.Context) Origin {
	return codexOrigin(os.Getenv("CODEX_THREAD_ID"), os.Getppid(), "/proc")
}

func codexOrigin(hint string, pid int, procRoot string) Origin {
	if hint == "" {
		return Origin{}
	}
	result := Origin{ThreadID: hint}
	seen := map[int]bool{}
	for hops := 0; pid > 0 && hops < 64 && !seen[pid]; hops++ {
		seen[pid] = true
		base := filepath.Join(procRoot, strconv.Itoa(pid))
		command, err := os.ReadFile(filepath.Join(base, "cmdline"))
		if err != nil {
			return result
		}
		args := strings.Split(string(command), "\x00")
		// The per-execution sandbox helper receives the thread's context.
		// Shared app-server/exec-server processes do not: their startup env may
		// belong to another thread, so never authenticate from those processes.
		if filepath.Base(args[0]) == "codex-linux-sandbox" {
			executable, err := os.Readlink(filepath.Join(base, "exe"))
			if err != nil || filepath.Base(executable) != "codex" {
				return result
			}
			env, err := os.ReadFile(filepath.Join(base, "environ"))
			if err != nil {
				return result
			}
			for _, entry := range strings.Split(string(env), "\x00") {
				if value, ok := strings.CutPrefix(entry, "CODEX_THREAD_ID="); ok {
					result.Verified = value == hint
					return result
				}
			}
			return result
		}
		for _, arg := range args[1:] {
			if arg == "app-server" || arg == "exec-server" {
				return result
			}
		}
		status, err := os.ReadFile(filepath.Join(base, "status"))
		if err != nil {
			return result
		}
		parent := 0
		for _, line := range strings.Split(string(status), "\n") {
			if value, ok := strings.CutPrefix(line, "PPid:"); ok {
				parent, _ = strconv.Atoi(strings.TrimSpace(value))
				break
			}
		}
		pid = parent
	}
	return result
}

// CallingPane proves that the reported tmux pane process is in this caller's
// process chain. A standalone app-server is never a pane identity, even when
// its launcher was once attached to that pane. ps supplies ancestry on macOS.
func CallingPane(ctx context.Context, panePID int) bool {
	if panePID <= 0 {
		return false
	}
	seen := map[int]bool{}
	for pid, hops := os.Getpid(), 0; pid > 0 && hops < 64 && !seen[pid]; hops++ {
		seen[pid] = true
		args := procCmdline(pid)
		parent, ok := procParent(pid)
		if !ok {
			output, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "ppid=", "-o", "args=").Output()
			fields := strings.Fields(string(output))
			if err != nil || len(fields) < 2 {
				return false
			}
			parent, err = strconv.Atoi(fields[0])
			if err != nil {
				return false
			}
			args = fields[1:]
		}
		if len(args) > 1 {
			for _, arg := range args[1:] {
				if arg == "app-server" || arg == "exec-server" {
					return false
				}
			}
		}
		if pid == panePID {
			return true
		}
		pid = parent
	}
	return false
}
