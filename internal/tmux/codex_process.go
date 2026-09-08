package tmux

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CodexProcess contains connection hints only. A remote TUI's argv is not
// evidence of the model, token usage, or execution policy of its server.
type CodexProcess struct {
	Home, ThreadID, Remote string
	Observed               bool // native Codex argv was read; an empty Remote means embedded
}

func CodexProcessInfo(pid int) CodexProcess {
	return codexProcessInfo(pid, "/proc")
}

func codexProcessInfo(pid int, procRoot string) CodexProcess {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		user, _ := os.UserHomeDir()
		home = filepath.Join(user, ".codex")
	}
	result := CodexProcess{Home: home}
	pids := []int{pid}
	for i := 0; i < len(pids) && i < 16; i++ {
		p := strconv.Itoa(pids[i])
		if pids[i] <= 0 {
			continue
		}
		env, _ := os.ReadFile(filepath.Join(procRoot, p, "environ"))
		for _, entry := range strings.Split(string(env), "\x00") {
			key, value, _ := strings.Cut(entry, "=")
			if key == "CODEX_HOME" && value != "" {
				result.Home = value
			}
		}
		data, commandErr := os.ReadFile(filepath.Join(procRoot, p, "cmdline"))
		executable, executableErr := os.Readlink(filepath.Join(procRoot, p, "exe"))
		native := executableErr == nil && filepath.Base(strings.TrimSuffix(executable, " (deleted)")) == "codex"
		if native && commandErr == nil && len(data) > 0 {
			// Wrapper/tool descendants cannot override the native CLI's mode.
			result.ThreadID, result.Remote, result.Observed = "", "", true
		}
		args := strings.Split(string(data), "\x00")
		for j, arg := range args {
			if arg == "--remote" && j+1 < len(args) {
				result.Remote = args[j+1]
			}
			if strings.HasPrefix(arg, "--remote=") {
				result.Remote = strings.TrimPrefix(arg, "--remote=")
			}
			if arg == "resume" && j+1 < len(args) && len(args[j+1]) == 36 {
				result.ThreadID = args[j+1]
			}
		}
		if native {
			return result
		}
		children, _ := os.ReadFile(filepath.Join(procRoot, p, "task", p, "children"))
		for _, child := range strings.Fields(string(children)) {
			n, _ := strconv.Atoi(child)
			if n > 0 {
				pids = append(pids, n)
			}
		}
	}
	return result
}

// CodexSocket resolves only local Unix endpoints; no server is started here.
func CodexSocket(home, endpoint string) string {
	if endpoint == "unix://" {
		return filepath.Join(home, "app-server-control", "app-server-control.sock")
	}
	if strings.HasPrefix(endpoint, "unix:///") {
		return strings.TrimPrefix(endpoint, "unix://")
	}
	return ""
}
