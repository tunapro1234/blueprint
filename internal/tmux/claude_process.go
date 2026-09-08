package tmux

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ClaudeProcessSession binds the pane's interactive Claude process to its
// current session, including /resume switches that do not change startup argv.
// No environment identity or newest-file selection; inaccessible old clients
// may still use an explicit pin or a unique title at the caller.
func ClaudeProcessSession(panePID int, folder string) (string, error) {
	return claudeProcessSession("/proc", filepath.Join(filepath.Dir(ClaudeProjectsRoot()), "sessions"), panePID, folder)
}

func claudeProcessSession(procRoot, sessions string, panePID int, folder string) (string, error) {
	pids := []int{panePID}
	found := ""
	seen := map[int]bool{}
	for i := 0; i < len(pids) && i < 32; i++ {
		pid := pids[i]
		if pid <= 0 || seen[pid] {
			continue
		}
		seen[pid] = true
		p := strconv.Itoa(pid)
		base := filepath.Join(procRoot, p)
		comm, err := os.ReadFile(filepath.Join(base, "comm"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(comm)) == "claude" {
			// Never descend into tools or CLI-internal subagents of this process.
			path := filepath.Join(sessions, p+".json")
			data, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				continue
			}
			var r struct {
				PID       int    `json:"pid"`
				SessionID string `json:"sessionId"`
				CWD       string `json:"cwd"`
				ProcStart string `json:"procStart"`
				Kind      string `json:"kind"`
				StartedAt int64  `json:"startedAt"`
			}
			stat, statErr := os.ReadFile(filepath.Join(base, "stat"))
			_, fields, _ := strings.Cut(string(stat), ") ")
			parts := strings.Fields(fields)
			cwd, cwdErr := os.Readlink(filepath.Join(base, "cwd"))
			if err != nil || json.Unmarshal(data, &r) != nil || statErr != nil || len(parts) <= 19 || r.PID != pid || r.ProcStart != parts[19] || r.Kind != "interactive" || !codexThreadID.MatchString(r.SessionID) || cwdErr != nil || filepath.Clean(cwd) != filepath.Clean(folder) || filepath.Clean(r.CWD) != filepath.Clean(folder) {
				return "", fmt.Errorf("invalid/stale Claude process session: %s (PID start/cwd/session mismatch)", path)
			}
			current, err := claudeContinuation(filepath.Join(filepath.Dir(sessions), "projects"), folder, r.SessionID, r.StartedAt)
			if err != nil {
				return "", err
			}
			r.SessionID = current
			if found != "" && found != r.SessionID {
				return "", fmt.Errorf("multiple interactive Claude sessions under pane PID %d: %s, %s", panePID, found, r.SessionID)
			}
			found = r.SessionID
			continue
		}
		// Only traverse launch wrappers, not arbitrary agent tool process trees.
		switch strings.TrimSpace(string(comm)) {
		case "sh", "bash", "zsh", "fish", "dash", "bwrap", "node":
			children, _ := os.ReadFile(filepath.Join(base, "task", p, "children"))
			for _, child := range strings.Fields(string(children)) {
				n, _ := strconv.Atoi(child)
				if n > 0 {
					pids = append(pids, n)
				}
			}
		}
	}
	if len(pids) > 32 {
		return "", fmt.Errorf("Claude pane process scan exceeded limit")
	}
	return found, nil
}
