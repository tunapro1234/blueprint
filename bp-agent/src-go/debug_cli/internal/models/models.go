package models

// CLIConfig stores runtime settings.
type CLIConfig struct {
	BaseURL      string
	Provider     string
	Model        string
	SystemPrompt string
	Temperature  float64
	Debug        bool
	Token        string
}

// ChatMessage is a transcript entry.
type ChatMessage struct {
	Role    string
	Content string
}

// ExecuteRequest payload.
type ExecuteRequest struct {
	Instruction  string   `json:"instruction"`
	SystemPrompt string   `json:"system_prompt,omitempty"`
	Provider     string   `json:"provider,omitempty"`
	Model        string   `json:"model,omitempty"`
	Temperature  *float64 `json:"temperature,omitempty"`
	Debug        bool     `json:"debug,omitempty"`
}

// ExecuteResponse response payload.
type ExecuteResponse struct {
	Success bool           `json:"success"`
	Output  string         `json:"output"`
	TaskID  string         `json:"task_id,omitempty"`
	Trace   map[string]any `json:"trace,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// TaskInfo represents /tasks entries.
type TaskInfo struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Instruction string `json:"instruction"`
	Output      string `json:"output"`
	CreatedAt   string `json:"created_at"`
}
