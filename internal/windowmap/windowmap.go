// Package windowmap joins compositor processes to attached tmux clients.
package windowmap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"blueprint/internal/compositor"
	bptmux "blueprint/internal/tmux"
)

type ProcessLister interface {
	Descendants(context.Context, int) (map[int]struct{}, error)
}

// BatchProcessLister shares one process-tree observation across all windows
// in a poll. The default process listings are machine-wide, so scanning them
// once per window would multiply work as the desktop grows.
type BatchProcessLister interface {
	DescendantsMany(context.Context, []int) (map[int]map[int]struct{}, error)
}

type Mapping struct {
	Agent  string
	Window compositor.Window
}

// Map keeps only tmux sessions registered as agents. A window may contain more
// than one attached client, so the result is a list rather than a map.
func Map(ctx context.Context, windows []compositor.Window, processes ProcessLister, clients []bptmux.AttachedClient, agents map[string]bool) ([]Mapping, error) {
	byPID := make(map[int][]string, len(clients))
	for _, client := range clients {
		if client.PID > 0 && agents[client.Session] {
			byPID[client.PID] = append(byPID[client.PID], client.Session)
		}
	}
	roots := make([]int, 0, len(windows))
	rootSeen := map[int]bool{}
	for _, window := range windows {
		if !rootSeen[window.PID] {
			rootSeen[window.PID] = true
			roots = append(roots, window.PID)
		}
	}
	if len(roots) == 0 {
		return nil, nil
	}
	if processes == nil {
		return nil, fmt.Errorf("process lister is unavailable")
	}
	descendantSets := make(map[int]map[int]struct{}, len(roots))
	if batch, ok := processes.(BatchProcessLister); ok {
		var err error
		descendantSets, err = batch.DescendantsMany(ctx, roots)
		if err != nil {
			return nil, fmt.Errorf("list window process descendants: %w", err)
		}
	} else {
		attempted := map[int]bool{}
		for _, window := range windows {
			if attempted[window.PID] {
				continue
			}
			attempted[window.PID] = true
			descendants, err := processes.Descendants(ctx, window.PID)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, fmt.Errorf("list descendants of window %s: %w", window.ID, err)
			}
			descendantSets[window.PID] = descendants
		}
	}
	var mappings []Mapping
	for _, window := range windows {
		descendants, ok := descendantSets[window.PID]
		if !ok {
			continue
		}
		seen := map[string]bool{}
		for pid := range descendants {
			for _, session := range byPID[pid] {
				if !seen[session] {
					mappings = append(mappings, Mapping{Agent: session, Window: window})
					seen[session] = true
				}
			}
		}
	}
	sort.Slice(mappings, func(i, j int) bool {
		if mappings[i].Agent != mappings[j].Agent {
			return mappings[i].Agent < mappings[j].Agent
		}
		return mappings[i].Window.ID < mappings[j].Window.ID
	})
	return mappings, nil
}

func DefaultProcessLister() ProcessLister {
	if runtime.GOOS == "linux" {
		return ProcLister{Root: "/proc"}
	}
	return PSLister{}
}

// ProcLister reads only pid/ppid relationships and is root-path injectable for
// deterministic tests.
type ProcLister struct {
	Root string
}

func (p ProcLister) Descendants(ctx context.Context, rootPID int) (map[int]struct{}, error) {
	sets, err := p.DescendantsMany(ctx, []int{rootPID})
	if err != nil {
		return nil, err
	}
	set, ok := sets[rootPID]
	if !ok {
		return nil, os.ErrNotExist
	}
	return set, nil
}

func (p ProcLister) DescendantsMany(ctx context.Context, roots []int) (map[int]map[int]struct{}, error) {
	for _, pid := range roots {
		if pid <= 0 {
			return nil, fmt.Errorf("invalid process id %d", pid)
		}
	}
	root := p.Root
	if root == "" {
		root = "/proc"
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	children := map[int][]int{}
	found := map[int]bool{}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name(), "stat"))
		if err != nil {
			continue
		}
		close := strings.LastIndexByte(string(data), ')')
		if close < 0 {
			continue
		}
		fields := strings.Fields(string(data[close+1:]))
		if len(fields) < 2 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		children[ppid] = append(children[ppid], pid)
		found[pid] = true
	}
	return descendantsForRoots(ctx, roots, children, found)
}

// PSLister provides the same relationship walk on macOS, where procfs is not
// normally mounted.
type PSLister struct {
	Bin string
}

func (p PSLister) Descendants(ctx context.Context, rootPID int) (map[int]struct{}, error) {
	sets, err := p.DescendantsMany(ctx, []int{rootPID})
	if err != nil {
		return nil, err
	}
	set, ok := sets[rootPID]
	if !ok {
		return nil, os.ErrNotExist
	}
	return set, nil
}

func (p PSLister) DescendantsMany(ctx context.Context, roots []int) (map[int]map[int]struct{}, error) {
	for _, pid := range roots {
		if pid <= 0 {
			return nil, fmt.Errorf("invalid process id %d", pid)
		}
	}
	bin := p.Bin
	if bin == "" {
		bin = "ps"
	}
	out, err := exec.CommandContext(ctx, bin, "-axo", "pid=,ppid=").Output()
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	children := map[int][]int{}
	found := map[int]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		ppid, ppidErr := strconv.Atoi(fields[1])
		if pidErr != nil || ppidErr != nil {
			continue
		}
		children[ppid] = append(children[ppid], pid)
		found[pid] = true
	}
	return descendantsForRoots(ctx, roots, children, found)
}

func descendantsForRoots(ctx context.Context, roots []int, children map[int][]int, found map[int]bool) (map[int]map[int]struct{}, error) {
	result := make(map[int]map[int]struct{}, len(roots))
	for _, rootPID := range roots {
		if !found[rootPID] {
			continue
		}
		set := map[int]struct{}{rootPID: {}}
		queue := []int{rootPID}
		for len(queue) > 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			pid := queue[0]
			queue = queue[1:]
			for _, child := range children[pid] {
				if _, ok := set[child]; ok {
					continue
				}
				set[child] = struct{}{}
				queue = append(queue, child)
			}
		}
		result[rootPID] = set
	}
	return result, nil
}
