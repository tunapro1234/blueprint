package models

type DebugConfig struct {
	BaseURL      string
	Token        string
	Provider     string
	Model        string
	Temperature  float64
	SystemPrompt string
	Debug        bool
}

type ExecuteRequest struct {
	Instruction  string   `json:"instruction"`
	SystemPrompt string   `json:"system_prompt,omitempty"`
	Provider     string   `json:"provider,omitempty"`
	Model        string   `json:"model,omitempty"`
	Temperature  *float64 `json:"temperature,omitempty"`
	Debug        *bool    `json:"debug,omitempty"`
}

type ExecuteResponse struct {
	Success bool           `json:"success"`
	Output  string         `json:"output"`
	TaskID  string         `json:"task_id,omitempty"`
	Trace   map[string]any `json:"trace,omitempty"`
}
