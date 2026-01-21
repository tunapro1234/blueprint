package llm

import (
	"context"
)

// CodexConfig configures Codex adapter.
type CodexConfig struct {
	APIKeys         []string
	AuthFiles       []string
	Model           string
	ReasoningEffort string
}

// CodexAdapter is a placeholder implementation.
type CodexAdapter struct {
	cfg     CodexConfig
	rotator *Rotator
}

// NewCodexAdapter creates a new adapter.
func NewCodexAdapter(cfg CodexConfig) *CodexAdapter {
	return &CodexAdapter{cfg: cfg, rotator: NewRotator(cfg.APIKeys)}
}

// Complete calls the Codex API.
func (a *CodexAdapter) Complete(ctx context.Context, req CompletionRequest) (LLMResponse, error) {
	_ = ctx
	return LLMResponse{}, ProviderError{Provider: "codex", Message: "adapter not implemented"}
}
