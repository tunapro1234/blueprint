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
	ThreadID      string
	Verified      bool
	CodexDetected bool
}

// CodexOrigin cross-checks CODEX_THREAD_ID against the original environment of
// the per-execution Codex sandbox helper. An env assignment on the bp command
// alone cannot establish a different thread. Inaccessible evidence fails closed.
func CodexOrigin(ctx context.Context) Origin {
	result := codexOrigin(os.Getenv("CODEX_THREAD_ID"), os.Getppid(), "/proc")
	if !result.CodexDetected {
		result.CodexDetected = codexAncestryPS(ctx, os.Getppid(), inspectPSProcess)
	}
	return result
}

func codexOrigin(hint string, pid int, procRoot string) Origin {
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
		if len(args) == 0 || args[0] == "" {
			return result
		}
		executable, err := os.Readlink(filepath.Join(base, "exe"))
		isCodex := err == nil && filepath.Base(strings.TrimSuffix(executable, " (deleted)")) == "codex"
		// The per-execution sandbox helper receives the thread's context.
		// Shared app-server/exec-server processes do not: their startup env may
		// belong to another thread, so never authenticate from those processes.
		if isCodex {
			result.CodexDetected = true
		}
		if isCodex && filepath.Base(args[0]) == "codex-linux-sandbox" {
			if hint == "" {
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
		if isCodex {
			for _, arg := range args[1:] {
				if arg == "app-server" || arg == "exec-server" {
					return result
				}
			}
			// A naked Codex CLI is shared by its root thread and CLI subagents.
			// Its environment and writer locks cannot select the caller, so merely
			// detecting it must never turn a thread hint into authority.
			return result
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

type psProcess func(context.Context, int) (parent int, command string, ok bool)

// codexAncestryPS is the fail-safe path for hosts without readable /proc (most
// notably macOS). ps can establish only that a Codex process is an ancestor;
// it can never authenticate a thread or upgrade authority.
func codexAncestryPS(ctx context.Context, pid int, inspect psProcess) bool {
	seen := map[int]bool{}
	for hops := 0; pid > 0 && hops < 64 && !seen[pid]; hops++ {
		seen[pid] = true
		parent, command, ok := inspect(ctx, pid)
		if !ok {
			return false
		}
		name := filepath.Base(strings.TrimSpace(command))
		if name == "codex" || name == "codex-linux-sandbox" {
			return true
		}
		pid = parent
	}
	return false
}

func inspectPSProcess(ctx context.Context, pid int) (int, string, bool) {
	output, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "ppid=", "-o", "comm=").Output()
	if err != nil {
		return 0, "", false
	}
	value := strings.TrimSpace(string(output))
	cut := strings.IndexAny(value, " \t")
	if cut < 1 {
		return 0, "", false
	}
	parent, err := strconv.Atoi(value[:cut])
	command := strings.TrimSpace(value[cut:])
	return parent, command, err == nil && command != ""
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
