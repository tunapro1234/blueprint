package tasks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixedTime() time.Time { return time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC) }

func mustCreate(t *testing.T, s *Store, task Task) Task {
	t.Helper()
	if task.CreatedAt.IsZero() {
		task.CreatedAt = fixedTime()
	}
	if task.UpdatedAt.IsZero() {
		task.UpdatedAt = task.CreatedAt
	}
	created, err := s.Create(task)
	if err != nil {
		t.Fatalf("Create(%+v): %v", task, err)
	}
	return created
}

func TestStoreCreateGet(t *testing.T) {
	s := NewStore(t.TempDir())
	want := mustCreate(t, s, Task{ID: "t1", Title: "write tests", Owner: "worker", State: StateTodo})

	got, err := s.Get("t1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != want.Title || got.Owner != "worker" || got.State != StateTodo {
		t.Fatalf("Get = %+v, want %+v", got, want)
	}

	// An empty state defaults to todo rather than being stored invalid.
	defaulted := mustCreate(t, s, Task{ID: "t2", Title: "default state"})
	if defaulted.State != StateTodo {
		t.Fatalf("default state = %q, want %q", defaulted.State, StateTodo)
	}
}

func TestStoreCreateRejectsDuplicateAndInvalid(t *testing.T) {
	s := NewStore(t.TempDir())
	mustCreate(t, s, Task{ID: "dup", Title: "first"})

	if _, err := s.Create(Task{ID: "dup", Title: "second", CreatedAt: fixedTime(), UpdatedAt: fixedTime()}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate Create err = %v, want ErrExists", err)
	}
	// The original must be untouched by the rejected second create.
	if got, _ := s.Get("dup"); got.Title != "first" {
		t.Fatalf("duplicate overwrote the task: %+v", got)
	}

	bad := []Task{
		{ID: "", Title: "no id"},
		{ID: "bad/id", Title: "slash"},
		{ID: "ok", Title: ""},
		{ID: "ok", Title: "bad state", State: "frozen"},
	}
	for i, task := range bad {
		task.CreatedAt, task.UpdatedAt = fixedTime(), fixedTime()
		if _, err := s.Create(task); !errors.Is(err, ErrInvalid) {
			t.Fatalf("bad case %d err = %v, want ErrInvalid", i, err)
		}
	}
}

func TestStoreGetNotFound(t *testing.T) {
	s := NewStore(t.TempDir())
	if _, err := s.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing err = %v, want ErrNotFound", err)
	}
}

func TestStoreListFilter(t *testing.T) {
	s := NewStore(t.TempDir())
	base := fixedTime()
	mustCreate(t, s, Task{ID: "a", Title: "a", Owner: "alice", State: StateTodo, Parent: "epic", CreatedAt: base})
	mustCreate(t, s, Task{ID: "b", Title: "b", Owner: "bob", State: StateInProgress, CreatedAt: base.Add(time.Second)})
	mustCreate(t, s, Task{ID: "c", Title: "c", Owner: "alice", State: StateDone, Parent: "epic", CreatedAt: base.Add(2 * time.Second)})

	all, err := s.List(Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 || all[0].ID != "a" || all[2].ID != "c" {
		t.Fatalf("List order = %v, want a,b,c oldest first", ids(all))
	}

	byOwner, _ := s.List(Filter{Owner: "alice"})
	if len(byOwner) != 2 {
		t.Fatalf("owner=alice = %v, want a,c", ids(byOwner))
	}
	byState, _ := s.List(Filter{State: StateDone})
	if len(byState) != 1 || byState[0].ID != "c" {
		t.Fatalf("state=done = %v, want c", ids(byState))
	}
	byParent, _ := s.List(Filter{Parent: "epic", Owner: "alice", State: StateTodo})
	if len(byParent) != 1 || byParent[0].ID != "a" {
		t.Fatalf("combined filter = %v, want a", ids(byParent))
	}
}

func TestStoreListEmpty(t *testing.T) {
	s := NewStore(t.TempDir())
	got, err := s.List(Filter{})
	if err != nil {
		t.Fatalf("List on empty store: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty store list = %v, want none", ids(got))
	}
}

func TestStoreUpdate(t *testing.T) {
	s := NewStore(t.TempDir())
	created := mustCreate(t, s, Task{ID: "u1", Title: "orig", Owner: "worker", State: StateTodo})

	later := created.CreatedAt.Add(time.Hour)
	updated, err := s.Update("u1", later, func(task *Task) error {
		task.State = StateInProgress
		task.Title = "renamed"
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.State != StateInProgress || updated.Title != "renamed" {
		t.Fatalf("Update result = %+v", updated)
	}
	// Fields not touched by mutate survive; id and createdAt are immutable.
	if updated.Owner != "worker" {
		t.Fatalf("Update dropped owner: %+v", updated)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("createdAt changed: %v -> %v", created.CreatedAt, updated.CreatedAt)
	}
	if !updated.UpdatedAt.Equal(later) {
		t.Fatalf("updatedAt = %v, want %v", updated.UpdatedAt, later)
	}

	// An update that would leave an invalid state fails and leaves disk intact.
	if _, err := s.Update("u1", later, func(task *Task) error {
		task.State = "frozen"
		return nil
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid-state update err = %v, want ErrInvalid", err)
	}
	if got, _ := s.Get("u1"); got.State != StateInProgress {
		t.Fatalf("rejected update changed state: %+v", got)
	}

	// An attempt to change the id through mutate is ignored.
	moved, err := s.Update("u1", later, func(task *Task) error {
		task.ID = "elsewhere"
		return nil
	})
	if err != nil {
		t.Fatalf("Update (id change attempt): %v", err)
	}
	if moved.ID != "u1" {
		t.Fatalf("id changed to %q", moved.ID)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), "elsewhere.json")); !os.IsNotExist(err) {
		t.Fatalf("update created a second file: %v", err)
	}
}

func TestStoreUpdateNotFound(t *testing.T) {
	s := NewStore(t.TempDir())
	_, err := s.Update("missing", fixedTime(), func(*Task) error { return nil })
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update missing err = %v, want ErrNotFound", err)
	}
}

// TestStoreWriteIsAtomic checks a stored task is a whole, re-readable file with
// no temp file left behind: a reader (including a previous release) never sees a
// torn write.
func TestStoreWriteIsAtomic(t *testing.T) {
	s := NewStore(t.TempDir())
	mustCreate(t, s, Task{ID: "atom", Title: "atomic"})

	entries, err := os.ReadDir(s.Dir())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "atom.json" && e.Name() != ".lock" {
			t.Fatalf("stray file in store dir: %s", e.Name())
		}
	}
	// The file parses cleanly on its own, proving the rename published a whole file.
	got, err := readTask(filepath.Join(s.Dir(), "atom.json"))
	if err != nil || got.ID != "atom" {
		t.Fatalf("readTask = %+v, %v", got, err)
	}
}

func TestNewIDShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := NewID()
		if !validID(id) {
			t.Fatalf("NewID produced an invalid id: %q", id)
		}
		if seen[id] {
			t.Fatalf("NewID collided on %q", id)
		}
		seen[id] = true
	}
}

func ids(tasks []Task) []string {
	out := make([]string, len(tasks))
	for i, task := range tasks {
		out[i] = task.ID
	}
	return out
}
