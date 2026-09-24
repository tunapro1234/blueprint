package windowmap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"blueprint/internal/compositor"
	bptmux "blueprint/internal/tmux"
)

func TestProcListerWalksFakeProcTree(t *testing.T) {
	root := t.TempDir()
	writeProcStat(t, root, 100, 1, "terminal (test)")
	writeProcStat(t, root, 101, 100, "tmux: client")
	writeProcStat(t, root, 102, 101, "codex")
	writeProcStat(t, root, 103, 100, "browser")

	processes := ProcLister{Root: root}
	descendants, err := processes.Descendants(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, pid := range []int{100, 101, 102, 103} {
		if _, ok := descendants[pid]; !ok {
			t.Errorf("process %d is missing from descendants %v", pid, descendants)
		}
	}
	if _, err := processes.Descendants(context.Background(), 999); !os.IsNotExist(err) {
		t.Fatalf("missing process error=%v, want os.ErrNotExist", err)
	}
}

func TestMapMatchesRegisteredSessionsInsideWindowProcessTrees(t *testing.T) {
	processes := fakeProcesses{
		100: {100: {}, 101: {}, 102: {}},
		200: {200: {}, 201: {}},
	}
	windows := []compositor.Window{
		{ID: "window-b", PID: 200, Workspace: "2"},
		{ID: "window-a", PID: 100, Workspace: "1"},
	}
	clients := []bptmux.AttachedClient{
		{PID: 201, Session: "unregistered"},
		{PID: 102, Session: "agent-a"},
		{PID: 101, Session: "agent-a"},
		{PID: 200, Session: "another-unregistered"},
	}
	got, err := Map(context.Background(), windows, processes, clients, map[string]bool{"agent-a": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Agent != "agent-a" || got[0].Window.ID != "window-a" {
		t.Fatalf("mappings=%+v", got)
	}
}

func TestMapUsesOneBatchProcessSnapshotForMultipleWindows(t *testing.T) {
	processes := &batchFakeProcesses{
		sets: map[int]map[int]struct{}{
			100: {100: {}, 101: {}},
			200: {200: {}, 201: {}},
		},
	}
	windows := []compositor.Window{{ID: "a", PID: 100}, {ID: "b", PID: 200}}
	clients := []bptmux.AttachedClient{{PID: 101, Session: "agent-a"}, {PID: 201, Session: "agent-b"}}
	got, err := Map(context.Background(), windows, processes, clients, map[string]bool{"agent-a": true, "agent-b": true})
	if err != nil {
		t.Fatal(err)
	}
	if processes.calls != 1 || len(got) != 2 {
		t.Fatalf("batch calls=%d mappings=%+v", processes.calls, got)
	}
}

func TestMapRequiresProcessListerForWindows(t *testing.T) {
	_, err := Map(context.Background(), []compositor.Window{{ID: "window", PID: 100}}, nil, nil, nil)
	if err == nil || err.Error() != "process lister is unavailable" {
		t.Fatalf("Map error=%v, want missing process lister error", err)
	}
}

type fakeProcesses map[int]map[int]struct{}

func (f fakeProcesses) Descendants(_ context.Context, root int) (map[int]struct{}, error) {
	processes, ok := f[root]
	if !ok {
		return nil, os.ErrNotExist
	}
	return processes, nil
}

type batchFakeProcesses struct {
	sets  map[int]map[int]struct{}
	calls int
}

func (f *batchFakeProcesses) Descendants(_ context.Context, root int) (map[int]struct{}, error) {
	return f.sets[root], nil
}

func (f *batchFakeProcesses) DescendantsMany(_ context.Context, roots []int) (map[int]map[int]struct{}, error) {
	f.calls++
	result := make(map[int]map[int]struct{}, len(roots))
	for _, root := range roots {
		if set, ok := f.sets[root]; ok {
			result[root] = set
		}
	}
	return result, nil
}

func writeProcStat(t *testing.T, root string, pid, ppid int, command string) {
	t.Helper()
	dir := filepath.Join(root, fmt.Sprint(pid))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf("%d (%s) S %d 1 2 3 4 5 6\n", pid, command, ppid)
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}
