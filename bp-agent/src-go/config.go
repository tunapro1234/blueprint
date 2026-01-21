package agent

// AgentConfig controls agent execution.
type AgentConfig struct {
	Provider        string
	Model           string
	ReasoningEffort string
	MaxIterations   int
	Temperature     float64
	EnableTaskStore bool
	CodexAuthFile   string
}

// DefaultAgentConfig returns baseline config values.
func DefaultAgentConfig() AgentConfig {
	return AgentConfig{
		Provider:        "gemini",
		Model:           "gemini-3-flash-preview",
		MaxIterations:   10,
		Temperature:     0.3,
		EnableTaskStore: true,
	}
}
