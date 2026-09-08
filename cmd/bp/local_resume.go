package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/cache"
	bptmux "blueprint/internal/tmux"
)

var errResumeCanceled = errors.New("resume canceled")

var claudeResumeUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$`)

func physicalPath(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

// Resolve -c once, then pass an explicit UUID to Claude. Otherwise another
// process can change what "latest" means between our check and native startup.
// This is launch routing only; it grants no identity or hierarchy authority.
func claudeResumeArgs(args []string, cwd, projects string, resolve ...func(string) (string, error)) (string, []string, error) {
	rest := make([]string, 0, len(args))
	id, continuing := "", false
	fork, picker := false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		switch {
		case arg == "--fork-session":
			fork = true
			rest = append(rest, arg)
		case arg == "-c" || arg == "--continue":
			continuing = true
		case arg == "-r" || arg == "--resume":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				picker = true
				continue
			}
			i++
			id = args[i]
		case strings.HasPrefix(arg, "--resume="):
			id = strings.TrimPrefix(arg, "--resume=")
			picker = id == ""
		default:
			rest = append(rest, arg)
			// Do not interpret a flag's value (e.g. a system prompt containing -c).
			if strings.Contains(" --model --effort --settings --setting-sources --permission-mode --append-system-prompt --system-prompt --agent --agents --name --session-id --output-format --input-format --debug-file --mcp-config ", " "+arg+" ") && i+1 < len(args) {
				i++
				rest = append(rest, args[i])
			}
		}
	}
	if fork {
		return "", args, nil
	}
	if id == "" && !continuing && !picker {
		return "", args, nil
	}
	if picker || (id != "" && !claudeResumeUUID.MatchString(id)) {
		if len(resolve) == 0 {
			return "", nil, fmt.Errorf("Claude resume selection needs an interactive resolver")
		}
		var err error
		id, err = resolve[0](id)
		if err != nil {
			return "", nil, err
		}
		if id == "" {
			return "", nil, errResumeCanceled
		}
	}

	if id == "" {
		var err error
		id, err = latestClaudeResume(projects, cwd)
		if err != nil {
			return "", nil, err
		}
		if id == "" {
			return "", args, nil
		} // no history: native -c starts a new conversation
	}
	return strings.ToLower(id), append([]string{"--resume", id}, rest...), nil
}

func latestClaudeResume(projects, cwd string) (string, error) {
	// Include the caller's logical path for histories created before canonical cwd
	// registration. New launches always use the physical directory.
	dirs := map[string]bool{cwd: true, physicalPath(cwd): true}
	var newest time.Time
	id := ""
	ambiguous := false
	for dir := range dirs {
		encoded := []byte(dir)
		for i, c := range encoded {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
				encoded[i] = '-'
			}
		}
		root := filepath.Join(projects, string(encoded))
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		for _, entry := range entries {
			candidate := strings.TrimSuffix(entry.Name(), ".jsonl")
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") || !claudeResumeUUID.MatchString(candidate) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return "", err
			}
			if !info.Mode().IsRegular() || info.Size() == 0 {
				continue
			}
			if id == "" || info.ModTime().After(newest) {
				id, newest, ambiguous = candidate, info.ModTime(), false
			} else if info.ModTime().Equal(newest) && candidate != id {
				ambiguous = true
			}
		}
	}
	if ambiguous {
		return "", fmt.Errorf("multiple equally recent Claude conversations; use claude --resume <UUID>")
	}
	return id, nil
}

type localResumeClaim struct {
	Name   string
	TmuxID string
}
type localResumeGuard struct {
	file                    *os.File
	claimPath, thread, name string
	cwd                     string
}

func (g *localResumeGuard) close() {
	if g != nil && g.file != nil {
		_ = syscall.Flock(int(g.file.Fd()), syscall.LOCK_UN)
		_ = g.file.Close()
		g.file = nil
	}
}

func (a *app) resumeTmuxID(name string) string {
	out, err := exec.Command(a.tmux.Bin, "display-message", "-p", "-t", "="+name+":", "#{pid}:#{session_id}:#{session_created}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// The lock spans owner lookup and detached tmux creation, never the attached
// client's lifetime. Claims also cover the gap before _session registers its PID.
func (a *app) guardClaudeResume(thread, requested, cwd string) (*localResumeGuard, string, error) {
	root := filepath.Join(a.config.StateDir, "local-resume")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, "", err
	}
	f, err := os.OpenFile(filepath.Join(root, "launch.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, "", err
	}
	g := &localResumeGuard{file: f, thread: thread}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			g.close()
			return nil, "", fmt.Errorf("another bp launch is in progress; retry: %w", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	fail := func(err error) (*localResumeGuard, string, error) { g.close(); return nil, "", err }
	key := sha256.Sum256([]byte(physicalPath(bptmux.ClaudeProjectsRoot()) + "\x00" + thread))
	g.claimPath = filepath.Join(root, fmt.Sprintf("%x.json", key))
	owners := map[string]bool{}
	var claim localResumeClaim
	if data, err := os.ReadFile(g.claimPath); err == nil {
		if json.Unmarshal(data, &claim) != nil {
			return fail(fmt.Errorf("invalid Claude ownership claim: %s", g.claimPath))
		}
		if claim.Name != "" && claim.TmuxID != "" && a.resumeTmuxID(claim.Name) == claim.TmuxID {
			owners[claim.Name] = true
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	fleet, err := book.LoadFleet(book.Paths(a.config.Agentbooks))
	if err != nil {
		return fail(err)
	}
	previous := ""
	var previousAt time.Time
	for _, agent := range fleet.Agents {
		if agent.Local == nil || agent.Local.Harness != "claude" {
			continue
		}
		observation, obsErr := cache.ReadLocalObservation(agent.Local, agent.Local.PID)
		live := a.tmux.HasSession(a.ctx, agent.Name)
		if obsErr != nil {
			if live && !owners[agent.Name] && physicalPath(agent.Local.CWD) == physicalPath(cwd) {
				return fail(fmt.Errorf("Claude session %s is still unbound; retry once its session is known", agent.Name))
			}
			continue
		}
		if observation.SessionID != thread {
			// Native /clear or /resume can move a live pane to another thread.
			// Its newer callback overrides a claim from an earlier launch.
			delete(owners, agent.Name)
			continue
		}
		if live {
			process, err := a.tmux.PaneProcess(a.ctx, agent.Name)
			if err != nil || process.PID != agent.Local.PID {
				return fail(fmt.Errorf("Claude ownership for %s is stale; inspect it before resuming %s", agent.Name, thread))
			}
			owners[agent.Name] = true
		} else if previous == "" || observation.ObservedAt.After(previousAt) {
			previous, previousAt = agent.Name, observation.ObservedAt
		}
	}
	names := make([]string, 0, len(owners))
	for name := range owners {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 1 {
		return fail(fmt.Errorf("Claude conversation %s has multiple live bp owners: %s; no pane was opened or closed", thread, strings.Join(names, ", ")))
	}
	if len(names) == 1 {
		g.name = names[0]
		return g, names[0], nil
	}
	g.name = requested
	if g.name == "" {
		g.name = previous
	}
	if g.name == "" {
		g.name = claim.Name
	}
	if g.name == "" {
		g.name = "claude-" + thread
	}
	if a.tmux.HasSession(a.ctx, g.name) {
		return fail(fmt.Errorf("tmux name %s is already occupied by another session", g.name))
	}
	return g, "", nil
}

func (a *app) recordClaudeResume(g *localResumeGuard) error {
	id := a.resumeTmuxID(g.name)
	if id == "" {
		return fmt.Errorf("new agent pane exited before ownership could be recorded")
	}
	data, _ := json.Marshal(localResumeClaim{Name: g.name, TmuxID: id})
	return os.WriteFile(g.claimPath, data, 0600)
}

func (a *app) attachLocal(name string) error {
	fmt.Fprintf(a.out, "bp: attached to %s; existing process, input and launch settings preserved\n", name)
	cmd := exec.Command(a.tmux.Bin, "attach-session", "-t", "="+name)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, a.out, a.err
	return cmd.Run()
}
