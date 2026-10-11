// Package tasks is the durable task store behind the fleet console's task
// board (docs/fleet-console.md, "Tasks — WS2") and the `bp task` CLI. A task is
// a small, human-authored unit of work with an owner, a parent and a state; the
// console reads and edits it, and the CLI gives the same model without the UI.
//
// The store keeps one JSON file per task under <StateDir>/tasks, written
// atomically (temp file then rename) and shaped so a previous release still
// reads it (principle 12): every field is plain and optional-friendly. The
// store API itself never reads the clock or a random source — Create and Update
// take the id and the timestamp from the caller — so a test is fully
// deterministic; NewID is a separate convenience for production callers.
//
// derivedStatus (the owner agent's live runtime) is deliberately NOT stored
// here: the stored State is authoritative and is never overwritten by a derived
// value. The API layer computes derivedStatus for display on top of this store.
package tasks

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Task states. todo is the default for a new task; done and cancelled are
// terminal. The set is closed: an unknown state is refused, never coerced.
const (
	StateTodo       = "todo"
	StateInProgress = "in_progress"
	StateBlocked    = "blocked"
	StateDone       = "done"
	StateCancelled  = "cancelled"
)

// validStates is the closed set a stored state is checked against (fail closed).
var validStates = map[string]bool{
	StateTodo:       true,
	StateInProgress: true,
	StateBlocked:    true,
	StateDone:       true,
	StateCancelled:  true,
}

// ValidState reports whether state is one of the allowed task states.
func ValidState(state string) bool { return validStates[state] }

// Task priorities. An empty priority means "unset" and is always allowed; the
// named levels mirror the ladder Asana/Notion users expect (low..urgent).
const (
	PriorityLow    = "low"
	PriorityMedium = "medium"
	PriorityHigh   = "high"
	PriorityUrgent = "urgent"
)

// validPriorities is the closed set a non-empty priority is checked against.
var validPriorities = map[string]bool{
	PriorityLow:    true,
	PriorityMedium: true,
	PriorityHigh:   true,
	PriorityUrgent: true,
}

// ValidPriority reports whether priority is allowed: empty (unset) or one of the
// named levels. The set is closed, so an unknown priority is refused.
func ValidPriority(priority string) bool {
	return priority == "" || validPriorities[priority]
}

// dueLayout is the date-only format a task due date is stored and validated in.
const dueLayout = "2006-01-02"

// ValidDue reports whether due is allowed: empty (no due date) or a calendar
// date in YYYY-MM-DD form. A due date carries no time of day on purpose — a
// task board shows the day, not the minute.
func ValidDue(due string) bool {
	if due == "" {
		return true
	}
	_, err := time.Parse(dueLayout, due)
	return err == nil
}

// Field bounds. They keep a stored task small and a title/owner single-line;
// nothing a human writes legitimately comes close to the body bound.
const (
	maxTitle      = 500
	maxName       = 64
	maxBody       = 64 << 10
	maxAcceptance = 16 << 10
)

// Task is one unit of work. State is the authoritative, human-set status; it is
// never overwritten by a derived runtime signal. CreatedAt and UpdatedAt are
// set by the caller through Create and Update so the store stays deterministic.
type Task struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Owner      string    `json:"owner,omitempty"`
	Parent     string    `json:"parent,omitempty"`
	State      string    `json:"state"`
	Priority   string    `json:"priority,omitempty"`
	Due        string    `json:"due,omitempty"`
	Body       string    `json:"body,omitempty"`
	Acceptance string    `json:"acceptance,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Filter narrows a List. An empty field matches every task.
type Filter struct {
	Owner  string
	State  string
	Parent string
}

func (f Filter) matches(t Task) bool {
	if f.Owner != "" && t.Owner != f.Owner {
		return false
	}
	if f.State != "" && t.State != f.State {
		return false
	}
	if f.Parent != "" && t.Parent != f.Parent {
		return false
	}
	return true
}

// Store errors, so a transport can map each to the right status.
var (
	// ErrNotFound reports an unknown task id.
	ErrNotFound = errors.New("task not found")
	// ErrExists reports a Create whose id is already taken.
	ErrExists = errors.New("task already exists")
	// ErrInvalid reports a task that fails validation.
	ErrInvalid = errors.New("invalid task")
)

// Store is the durable, per-user task store under <StateDir>/tasks. It is safe
// for concurrent use within a process (mu) and across processes (a shared flock
// on every mutation), matching how the rest of bp's state is written.
type Store struct {
	dir string
	mu  sync.Mutex
}

// Directory and file permissions match the rest of bp's private state: task
// bodies can carry sensitive planning text.
const (
	dirMode  = 0o700
	fileMode = 0o600
)

// NewStore returns the task store rooted at <stateDir>/tasks.
func NewStore(stateDir string) *Store {
	return &Store{dir: filepath.Join(stateDir, "tasks")}
}

// Dir is the directory the store keeps its task files in.
func (s *Store) Dir() string { return s.dir }

func (s *Store) path(id string) string { return filepath.Join(s.dir, id+".json") }

// NewID returns a short, URL-safe, collision-resistant task id. It is the
// convenience production callers use; the store API accepts any valid id, so a
// test passes its own deterministic ids instead.
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on the supported platforms; fall back to a
		// time-based suffix rather than panicking a CLI.
		return "t" + fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return "t" + hex.EncodeToString(b[:])
}

// validID reports whether id is a safe single path element: no separators, no
// "..", only the characters a task id is built from.
func validID(id string) bool {
	if id == "" || len(id) > 80 || strings.Contains(id, "..") || filepath.Base(id) != id {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// validate checks a task is well-formed before it is written. State is checked
// against the closed set so a bad state can never reach disk (fail closed).
func (t Task) validate() error {
	if !validID(t.ID) {
		return fmt.Errorf("%w: id %q is not a valid task id", ErrInvalid, t.ID)
	}
	if strings.TrimSpace(t.Title) == "" {
		return fmt.Errorf("%w: title is required", ErrInvalid)
	}
	if len(t.Title) > maxTitle {
		return fmt.Errorf("%w: title is %d bytes, over the %d limit", ErrInvalid, len(t.Title), maxTitle)
	}
	if len(t.Owner) > maxName {
		return fmt.Errorf("%w: owner is %d bytes, over the %d limit", ErrInvalid, len(t.Owner), maxName)
	}
	if len(t.Parent) > maxName {
		return fmt.Errorf("%w: parent is %d bytes, over the %d limit", ErrInvalid, len(t.Parent), maxName)
	}
	if len(t.Body) > maxBody {
		return fmt.Errorf("%w: body is %d bytes, over the %d limit", ErrInvalid, len(t.Body), maxBody)
	}
	if len(t.Acceptance) > maxAcceptance {
		return fmt.Errorf("%w: acceptance is %d bytes, over the %d limit", ErrInvalid, len(t.Acceptance), maxAcceptance)
	}
	if !ValidState(t.State) {
		return fmt.Errorf("%w: state %q is not one of todo|in_progress|blocked|done|cancelled", ErrInvalid, t.State)
	}
	if !ValidPriority(t.Priority) {
		return fmt.Errorf("%w: priority %q is not one of (empty)|low|medium|high|urgent", ErrInvalid, t.Priority)
	}
	if !ValidDue(t.Due) {
		return fmt.Errorf("%w: due %q is not a YYYY-MM-DD date", ErrInvalid, t.Due)
	}
	return nil
}

// withLock runs fn while holding the store's shared flock, so Create and Update
// are serialized across bp processes (the daemon, `bp serve --api`, `bp task`).
func (s *Store) withLock(fn func() error) error {
	if err := os.MkdirAll(s.dir, dirMode); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(s.dir, ".lock"), os.O_CREATE|os.O_RDWR, fileMode)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
}

// Create stores a new task. The caller supplies the id (NewID in production)
// and the CreatedAt/UpdatedAt timestamps, so the store API itself is
// deterministic. An id that is already taken is refused rather than
// overwritten.
func (s *Store) Create(t Task) (Task, error) {
	if t.State == "" {
		t.State = StateTodo
	}
	if err := t.validate(); err != nil {
		return Task{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.withLock(func() error {
		if _, statErr := os.Stat(s.path(t.ID)); statErr == nil {
			return fmt.Errorf("%w: %s", ErrExists, t.ID)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		return writeTaskAtomic(s.path(t.ID), t)
	})
	if err != nil {
		return Task{}, err
	}
	return t, nil
}

// Get returns one task by id.
func (s *Store) Get(id string) (Task, error) {
	if !validID(id) {
		return Task{}, fmt.Errorf("%w: id %q is not a valid task id", ErrInvalid, id)
	}
	task, err := readTask(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return Task{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Task{}, err
	}
	return task, nil
}

// List returns every task matching filter, oldest first (then by id), so the
// order is stable across reads.
func (s *Store) List(filter Filter) ([]Task, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Task{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		task, err := readTask(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			// A half-written or unreadable file must not hide the rest of the
			// board; skip it rather than failing the whole list. The atomic
			// writer never leaves a torn .json, so this only skips stray files.
			continue
		}
		if filter.matches(task) {
			out = append(out, task)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// Update applies mutate to the task under a lock and writes the result
// atomically, setting UpdatedAt to now. mutate changes only the fields it
// means to; the stored task is re-validated before it is written, so a mutate
// cannot persist an invalid state (fail closed). The id and CreatedAt are
// restored after mutate so they can never be changed by an update.
func (s *Store) Update(id string, now time.Time, mutate func(*Task) error) (Task, error) {
	if !validID(id) {
		return Task{}, fmt.Errorf("%w: id %q is not a valid task id", ErrInvalid, id)
	}
	if mutate == nil {
		return Task{}, errors.New("task update is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var result Task
	err := s.withLock(func() error {
		task, err := readTask(s.path(id))
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		if err != nil {
			return err
		}
		created, originalID := task.CreatedAt, task.ID
		if err := mutate(&task); err != nil {
			return err
		}
		// Identity and creation time are immutable; an update cannot move a task.
		task.ID, task.CreatedAt, task.UpdatedAt = originalID, created, now
		if err := task.validate(); err != nil {
			return err
		}
		if err := writeTaskAtomic(s.path(id), task); err != nil {
			return err
		}
		result = task
		return nil
	})
	if err != nil {
		return Task{}, err
	}
	return result, nil
}

// Delete removes a task by id. It is the board's own CRUD delete (a user
// discarding a todo they created), not a records-retention delete: it only ever
// removes a single task file this store owns under <StateDir>/tasks. An unknown
// id is reported as ErrNotFound rather than silently succeeding, so the caller
// can tell a no-op from a real delete.
func (s *Store) Delete(id string) error {
	if !validID(id) {
		return fmt.Errorf("%w: id %q is not a valid task id", ErrInvalid, id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withLock(func() error {
		err := os.Remove(s.path(id))
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return err
	})
}

func readTask(path string) (Task, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Task{}, err
	}
	var task Task
	if err := json.Unmarshal(data, &task); err != nil {
		return Task{}, fmt.Errorf("%s: %w", path, err)
	}
	return task, nil
}

// writeTaskAtomic replaces path with task using a temp file and rename, so a
// reader (a previous release included) never sees a partial file.
func writeTaskAtomic(path string, task Task) error {
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(fileMode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
