package llm

import (
	"context"
)

// OpusConfig configures Opus adapter.
type OpusConfig struct {
	APIKeys     []string
	BaseURL     string
	Endpoint    string
	Model       string
	Temperature float64
}

// OpusAdapter is a placeholder implementation.
type OpusAdapter struct {
	cfg     OpusConfig
	rotator *Rotator
}

// NewOpusAdapter creates a new adapter.
func NewOpusAdapter(cfg OpusConfig) *OpusAdapter {
	return &OpusAdapter{cfg: cfg, rotator: NewRotator(cfg.APIKeys)}
}

// Complete calls the Opus-compatible endpoint.
func (a *OpusAdapter) Complete(ctx context.Context, req CompletionRequest) (LLMResponse, error) {
	_ = ctx
	return LLMResponse{}, ProviderError{Provider: "opus", Message: "adapter not implemented"}
}
