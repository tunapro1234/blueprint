package task

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

// TaskStore manages tasks in memory with optional persistence.
type TaskStore struct {
	mu      sync.RWMutex
	tasks   map[string]Task
	persist bool
	path    string
}

// NewTaskStore creates a TaskStore.
func NewTaskStore(persist bool, path string) *TaskStore {
	if path == "" {
		path = "tasks.json"
	}
	s := &TaskStore{tasks: map[string]Task{}, persist: persist, path: path}
	if persist {
		_ = s.load()
	}
	return s
}

// Create adds a new task.
func (s *TaskStore) Create(instruction string) Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	task := Task{
		ID:          newTaskID(),
		Instruction: instruction,
		Status:      TaskPending,
		CreatedAt:   time.Now().UTC(),
	}
	s.tasks[task.ID] = task
	s.saveIfPersist()
	return task
}

// Update modifies a task.
func (s *TaskStore) Update(id string, status TaskStatus, output string, errMsg string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok {
		return Task{}, fmt.Errorf("task not found")
	}
	if status != "" {
		task.Status = status
	}
	if output != "" {
		task.Output = output
	}
	if errMsg != "" {
		task.Error = errMsg
	}
	s.tasks[id] = task
	s.saveIfPersist()
	return task, nil
}

// Get retrieves a task by ID.
func (s *TaskStore) Get(id string) (Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	task, ok := s.tasks[id]
	return task, ok
}

// List returns recent tasks.
func (s *TaskStore) List(limit int) []Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (s *TaskStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var tasks []Task
	if err := json.Unmarshal(data, &tasks); err != nil {
		return err
	}
	for _, t := range tasks {
		s.tasks[t.ID] = t
	}
	return nil
}

func (s *TaskStore) saveIfPersist() {
	if !s.persist {
		return
	}
	_ = s.save()
}

func (s *TaskStore) save() error {
	tasks := make([]Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		tasks = append(tasks, t)
	}
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}

func newTaskID() string {
	return fmt.Sprintf("task_%d", time.Now().UnixNano())
}
