package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"blueprint/internal/messagetext"
	bptmux "blueprint/internal/tmux"
)

type codexResumeSelection struct {
	command, targetIndex                     int
	target, cwd                              string
	last, all, remote, includeNonInteractive bool
	lastIndices                              map[int]bool
}

func parseCodexResume(args []string) codexResumeSelection {
	r := codexResumeSelection{command: -1, targetIndex: -1, lastIndices: map[int]bool{}}
	values := " -c --config -m --model -p --profile -s --sandbox -a --ask-for-approval -C --cd --add-dir --enable --disable --remote --remote-auth-token-env --local-provider -i --image "
	literal := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !literal {
			if arg == "--" {
				literal = true
				continue
			}
			if arg == "--remote" || strings.HasPrefix(arg, "--remote=") {
				r.remote = true
			}
			if arg == "--last" {
				r.last = true
				r.lastIndices[i] = true
				continue
			}
			if arg == "--all" {
				r.all = true
				continue
			}
			if arg == "--include-non-interactive" {
				r.includeNonInteractive = true
				continue
			}
			if strings.Contains(values, " "+arg+" ") {
				if (arg == "-C" || arg == "--cd") && i+1 < len(args) {
					r.cwd = args[i+1]
				}
				i++
				continue
			}
			if strings.HasPrefix(arg, "--cd=") {
				r.cwd = strings.TrimPrefix(arg, "--cd=")
			}
			if strings.HasPrefix(arg, "-") {
				continue
			}
		}
		if r.command < 0 {
			if arg != "resume" {
				return r
			}
			r.command = i
			continue
		}
		if r.targetIndex < 0 {
			r.target, r.targetIndex = arg, i
		}
	}
	return r
}

type codexResumeSession struct {
	ID, CWD, Title string
	Modified       time.Time
}

// Selection reads metadata only. The native CLI still performs the resume and
// restores its own settings; no transcript content or model turn is changed.
func codexResumeSessions(home, cwd string, all bool, includeNonInteractive ...bool) ([]codexResumeSession, error) {
	paths, e := filepath.Glob(filepath.Join(home, "sessions", "*", "*", "*", "*.jsonl"))
	if e != nil {
		return nil, e
	}
	titles := map[string]string{}
	if f, e := os.Open(filepath.Join(home, "session_index.jsonl")); e == nil {
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 4096), 1024*1024)
		for scanner.Scan() {
			var row struct {
				ID   string `json:"id"`
				Name string `json:"thread_name"`
			}
			if json.Unmarshal(scanner.Bytes(), &row) == nil {
				titles[row.ID] = row.Name
			}
		}
		_ = f.Close()
	}
	var sessions []codexResumeSession
	for _, path := range paths {
		f, e := os.Open(path)
		if e != nil {
			return nil, e
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 4096), 2*1024*1024)
		var row struct {
			Type    string `json:"type"`
			Payload struct {
				ID, CWD string
				Source  json.RawMessage `json:"source"`
			} `json:"payload"`
		}
		ok := scanner.Scan() && json.Unmarshal(scanner.Bytes(), &row) == nil
		_ = f.Close()
		var source string
		_ = json.Unmarshal(row.Payload.Source, &source)
		if !ok || row.Type != "session_meta" || !claudeResumeUUID.MatchString(row.Payload.ID) || (source != "cli" && source != "vscode" && source != "appServer" && source != "app-server" && !(len(includeNonInteractive) > 0 && includeNonInteractive[0] && source == "exec")) {
			continue
		}
		if !all && physicalPath(row.Payload.CWD) != physicalPath(cwd) {
			continue
		}
		info, e := os.Stat(path)
		if e != nil {
			return nil, e
		}
		title := titles[row.Payload.ID]
		if title == "" || messagetext.Label(title) != nil {
			title = row.Payload.ID
		}
		sessions = append(sessions, codexResumeSession{row.Payload.ID, row.Payload.CWD, title, info.ModTime()})
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].Modified.Equal(sessions[j].Modified) {
			return sessions[i].ID < sessions[j].ID
		}
		return sessions[i].Modified.After(sessions[j].Modified)
	})
	return sessions, nil
}

func chooseCodexResume(sessions []codexResumeSession, last bool, target string, in io.Reader, out io.Writer) (string, error) {
	if claudeResumeUUID.MatchString(target) {
		return strings.ToLower(target), nil
	}
	if target != "" {
		found := ""
		for _, s := range sessions {
			if s.Title == target {
				if found != "" && found != s.ID {
					return "", fmt.Errorf("multiple Codex sessions named %q; use its UUID", target)
				}
				found = s.ID
			}
		}
		if found == "" {
			return "", fmt.Errorf("Codex session %q not found; use its UUID", target)
		}
		return found, nil
	}
	if len(sessions) == 0 {
		return "", fmt.Errorf("no saved Codex sessions here; try codex resume --all")
	}
	if last {
		return sessions[0].ID, nil
	}
	fmt.Fprintln(out, "Resume Codex — open conversations attach to their existing tmux pane:")
	for i, s := range sessions {
		fmt.Fprintf(out, "%d. %s  (%s)\n", i+1, s.Title, s.Modified.Local().Format("Jan 02 15:04"))
	}
	fmt.Fprint(out, "Session number (Enter cancels): ")
	line, e := bufio.NewReader(in).ReadString('\n')
	if e != nil && !errors.Is(e, io.EOF) {
		return "", e
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", nil
	}
	n, e := strconv.Atoi(line)
	if e != nil || n < 1 || n > len(sessions) {
		return "", fmt.Errorf("invalid session number")
	}
	return sessions[n-1].ID, nil
}

// Resolve a selection before launching a second TUI. The writer-lock proof is
// used only for routing, never as agent identity/authority evidence.
func (a *app) routeCodexResume(args []string, cwd string) ([]string, *localResumeGuard, string, bool, error) {
	r := parseCodexResume(args)
	if r.command < 0 || r.remote {
		return args, nil, "", false, nil
	}
	home := bptmux.CodexProcessInfo(0).Home
	if r.cwd != "" {
		if filepath.IsAbs(r.cwd) {
			cwd = physicalPath(r.cwd)
		} else {
			cwd = physicalPath(filepath.Join(cwd, r.cwd))
		}
	}
	sessions, e := codexResumeSessions(home, cwd, r.all || r.target != "", r.includeNonInteractive)
	if e != nil {
		return nil, nil, "", true, e
	}
	thread, e := chooseCodexResume(sessions, r.last, r.target, os.Stdin, a.out)
	if e != nil || thread == "" {
		return nil, nil, "", true, e
	}
	root := filepath.Join(a.config.StateDir, "local-resume")
	if e = os.MkdirAll(root, 0700); e != nil {
		return nil, nil, "", true, e
	}
	f, e := os.OpenFile(filepath.Join(root, "launch.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, nil, "", true, e
	}
	guard := &localResumeGuard{file: f, thread: thread}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		guard.close()
		return nil, nil, "", true, fmt.Errorf("another BP launch is in progress; retry")
	}
	guard.claimPath = filepath.Join(root, fmt.Sprintf("codex-%x.json", sha256.Sum256([]byte(physicalPath(home)+"\x00"+thread))))
	for _, s := range sessions {
		if s.ID == thread {
			guard.cwd = physicalPath(s.CWD)
			break
		}
	}
	if r.cwd != "" {
		guard.cwd = cwd
	}
	owners, e := a.codexResumeOwners(home, thread)
	if e != nil {
		guard.close()
		return nil, nil, "", true, e
	}
	if len(owners) == 0 {
		var claim localResumeClaim
		if raw, err := os.ReadFile(guard.claimPath); err == nil {
			if json.Unmarshal(raw, &claim) != nil {
				guard.close()
				return nil, nil, "", true, fmt.Errorf("invalid Codex launch claim")
			}
			guard.name = claim.Name
			if claim.Name != "" && claim.TmuxID != "" && a.resumeTmuxID(claim.Name) == claim.TmuxID {
				if proc, err := a.tmux.PaneProcess(a.ctx, claim.Name); err == nil && !bptmux.CodexProcessInfo(proc.PID).Observed {
					owners = append(owners, claim.Name)
				}
			}
		} else if !os.IsNotExist(err) {
			guard.close()
			return nil, nil, "", true, err
		}
	}
	if len(owners) > 1 {
		guard.close()
		return nil, nil, "", true, fmt.Errorf("multiple Codex owners for %s: %s", thread, strings.Join(owners, ", "))
	}
	if len(owners) == 1 {
		guard.name = owners[0]
		return args, guard, owners[0], true, nil
	}
	if f, err := os.OpenFile(filepath.Join(home, "thread-writer-locks", thread+".lock"), os.O_RDWR, 0); err == nil {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		}
		_ = f.Close()
		if err != nil {
			guard.close()
			return nil, nil, "", true, fmt.Errorf("Codex thread %s has an active writer outside a matched tmux pane; attach its existing session or shared app-server; no process was opened or closed", thread)
		}
	} else if !os.IsNotExist(err) {
		guard.close()
		return nil, nil, "", true, err
	}
	// Preserve every native option; replace only the selector with an exact ID.
	rewritten := make([]string, 0, len(args)+1)
	for i, arg := range args {
		if i == r.targetIndex || r.lastIndices[i] {
			continue
		}
		rewritten = append(rewritten, arg)
		if i == r.command {
			rewritten = append(rewritten, thread)
		}
	}
	return rewritten, guard, "", false, nil
}

func (a *app) codexResumeOwners(home, thread string) ([]string, error) {
	sessions, e := a.tmux.Sessions(a.ctx)
	if e != nil {
		text := strings.ToLower(e.Error())
		if strings.Contains(text, "error connecting") && strings.Contains(text, "no such file or directory") {
			return nil, nil
		}
		return nil, e
	}
	var owners []string
	for _, name := range sessions {
		process, e := a.tmux.PaneProcess(a.ctx, name)
		if e != nil {
			continue
		}
		info := bptmux.CodexProcessInfo(process.PID)
		if physicalPath(info.Home) != physicalPath(home) {
			continue
		}
		// A remote TUI can switch threads without changing its argv. It owns
		// no writer lock, so argv alone must never route to that pane. The
		// shared writer check below rejects an embedded replacement instead.
		if info.Remote != "" {
			continue
		}
		ids, e := bptmux.CodexWriterIDs(a.ctx, process.PID, home)
		if e != nil {
			return nil, e
		}
		for _, id := range ids {
			if id == thread {
				owners = append(owners, name)
				break
			}
		}
	}
	sort.Strings(owners)
	return owners, nil
}
