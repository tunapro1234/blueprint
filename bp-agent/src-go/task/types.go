package task

import "time"

// TaskStatus represents the state of a task.
type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskRunning   TaskStatus = "running"
	TaskCompleted TaskStatus = "completed"
	TaskFailed    TaskStatus = "failed"
)

// Task stores execution metadata.
type Task struct {
	ID          string     `json:"id"`
	Instruction string     `json:"instruction"`
	Status      TaskStatus `json:"status"`
	Output      string     `json:"output,omitempty"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}
