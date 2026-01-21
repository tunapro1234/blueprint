package agent

// AgentResult represents execution output.
type AgentResult struct {
	Success bool
	Output  string
	TaskID  string
	Trace   map[string]any
}
