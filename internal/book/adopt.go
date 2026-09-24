package book

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"syscall"
	"time"
)

// ThreadBinding identifies a registration that currently names a native thread.
type ThreadBinding struct {
	Name     string
	Status   string
	Path     string
	Archived bool
}

// ThreadBindings returns every record that refers to thread, including archived
// records for diagnostics and adoption. Results are stable across runs.
func ThreadBindings(paths []string, thread string) ([]ThreadBinding, error) {
	if thread == "" {
		return nil, nil
	}
	records, err := Records(paths)
	if err != nil {
		return nil, err
	}
	return bindingsFromRecords(records, thread), nil
}

// AdoptThread clears matching thread references from every other registration.
// All configured book locks are held while records are re-read and changed. A
// live session is never moved; callers provide the exact tmux-session probe.
func AdoptThread(paths []string, target, thread string, live func(string) bool) ([]ThreadBinding, error) {
	paths = Paths(paths)
	if len(paths) == 0 {
		return nil, fmt.Errorf("agentbook is not configured")
	}
	if thread == "" {
		return nil, fmt.Errorf("cannot adopt an empty thread id")
	}
	sort.Strings(paths)
	type lockedBook struct {
		path    string
		data    map[string]any
		mode    os.FileMode
		changed bool
	}
	var files []lockedBook
	var locks []*os.File
	defer func() {
		for i := len(locks) - 1; i >= 0; i-- {
			_ = syscall.Flock(int(locks[i].Fd()), syscall.LOCK_UN)
			_ = locks[i].Close()
		}
	}()
	seenPaths := map[string]bool{}
	var uniquePaths []string
	for _, path := range paths {
		if seenPaths[path] {
			continue
		}
		seenPaths[path] = true
		uniquePaths = append(uniquePaths, path)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, err
		}
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
			_ = lock.Close()
			return nil, err
		}
		locks = append(locks, lock)
	}

	var moved []ThreadBinding
	for _, path := range uniquePaths {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		var document map[string]any
		if err := json.Unmarshal(data, &document); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		file := lockedBook{path: path, data: document, mode: info.Mode().Perm()}
		rows, _ := document["agents"].([]any)
		for _, value := range rows {
			row, ok := value.(map[string]any)
			if !ok {
				continue
			}
			encoded, err := json.Marshal(row)
			if err != nil {
				return nil, err
			}
			var agent Agent
			if err := json.Unmarshal(encoded, &agent); err != nil {
				return nil, err
			}
			if agent.Name == target || !containsString(agentThreads(&agent), thread) {
				continue
			}
			binding := ThreadBinding{Name: agent.Name, Status: agent.Status, Path: path, Archived: agent.ArchivedAt != ""}
			if !binding.Archived && live != nil && live(agent.Name) {
				return nil, fmt.Errorf("thread %s is held by live tmux session %s; use bp attach %s; --adopt cannot move a live thread", thread, agent.Name, agent.Name)
			}
			if clearThreadReference(row, thread) {
				file.changed = true
				moved = append(moved, binding)
			}
		}
		if file.changed {
			document["updated"] = time.Now().Format("2006-01-02")
			files = append(files, file)
		}
	}
	for _, file := range files {
		if err := writeBook(file.path, file.data, file.mode); err != nil {
			return nil, err
		}
	}
	sort.Slice(moved, func(i, j int) bool {
		if moved[i].Name != moved[j].Name {
			return moved[i].Name < moved[j].Name
		}
		return moved[i].Path < moved[j].Path
	})
	return moved, nil
}

func clearThreadReference(row map[string]any, thread string) bool {
	changed := false
	if value, _ := row["identityThreadId"].(string); value == thread {
		delete(row, "identityThreadId")
		changed = true
	}
	if title, _ := row["nativeTitle"].(map[string]any); title != nil {
		if value, _ := title["threadId"].(string); value == thread {
			delete(title, "threadId")
			changed = true
		}
	}
	if launch, _ := row["launch"].(map[string]any); launch != nil {
		if value, _ := launch["resumeId"].(string); value == thread {
			delete(launch, "resumeId")
			delete(launch, "resume")
			changed = true
		}
	}
	return changed
}
