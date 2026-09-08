package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// CodexWriterIDs reads the kernel's held write locks, not lock-file contents
// or mtimes. Descend launch wrappers only; never descend a Codex tool tree.
// Several threads in a shared daemon are left for the caller to reject/filter.
func CodexWriterIDs(ctx context.Context, panePID int, home string) ([]string, error) {
	pids := []int{panePID}
	seen := map[int]bool{}
	ids := map[string]bool{}
	for i := 0; i < len(pids) && i < 32; i++ {
		pid := pids[i]
		if pid <= 0 || seen[pid] {
			continue
		}
		seen[pid] = true
		base := filepath.Join("/proc", strconv.Itoa(pid))
		comm, err := os.ReadFile(filepath.Join(base, "comm"))
		name := strings.TrimSpace(string(comm))
		if err != nil {
			out, e := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
			if e != nil {
				continue
			}
			name = filepath.Base(strings.TrimSpace(string(out)))
		}
		if name == "codex" {
			entries, err := os.ReadDir(filepath.Join(base, "fd"))
			if err == nil {
				for _, entry := range entries {
					path, e := os.Readlink(filepath.Join(base, "fd", entry.Name()))
					if e != nil {
						continue
					}
					id := writerID(home, path)
					if id == "" {
						continue
					}
					info, e := os.ReadFile(filepath.Join(base, "fdinfo", entry.Name()))
					if e != nil {
						continue
					}
					for _, line := range strings.Split(string(info), "\n") {
						if heldWriterLock(line, pid) {
							ids[id] = true
						}
					}
				}
			} else {
				out, e := exec.CommandContext(ctx, "lsof", "-a", "-p", strconv.Itoa(pid), "-Ffln").Output()
				if e != nil && len(out) == 0 {
					return nil, fmt.Errorf("inspect Codex writer locks (lsof): %w", e)
				}
				for _, id := range parseWriterLSOF(home, string(out)) {
					ids[id] = true
				}
			}
			continue
		}
		switch name {
		case "node", "sh", "bash", "zsh", "bwrap":
		default:
			continue
		}
		children, err := os.ReadFile(filepath.Join(base, "task", strconv.Itoa(pid), "children"))
		if err == nil {
			for _, child := range strings.Fields(string(children)) {
				n, _ := strconv.Atoi(child)
				pids = append(pids, n)
			}
		} else {
			out, e := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=").Output()
			if e != nil {
				continue
			}
			for _, line := range strings.Split(string(out), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 2 && fields[1] == strconv.Itoa(pid) {
					n, _ := strconv.Atoi(fields[0])
					pids = append(pids, n)
				}
			}
		}
	}
	if len(pids) > 32 {
		return nil, fmt.Errorf("Codex launch process tree too large")
	}
	var result []string
	for id := range ids {
		result = append(result, id)
	}
	return result, nil
}

func heldWriterLock(line string, pid int) bool {
	f := strings.Fields(line)
	return len(f) >= 6 && f[0] == "lock:" && f[2] == "FLOCK" && f[4] == "WRITE" && f[5] == strconv.Itoa(pid)
}

func writerID(home, path string) string {
	if filepath.Clean(filepath.Dir(path)) != filepath.Join(home, "thread-writer-locks") {
		return ""
	}
	id := strings.TrimSuffix(filepath.Base(path), ".lock")
	if !strings.HasSuffix(path, ".lock") || !codexThreadID.MatchString(id) {
		return ""
	}
	return id
}

func parseWriterLSOF(home, data string) []string {
	var result []string
	locked := false
	for _, line := range strings.Split(data, "\n") {
		if strings.HasPrefix(line, "f") {
			locked = false
		}
		if strings.HasPrefix(line, "l") {
			locked = line == "lW"
		}
		if strings.HasPrefix(line, "n") && locked {
			if id := writerID(home, line[1:]); id != "" {
				result = append(result, id)
			}
		}
	}
	return result
}
