package llm

import (
	"context"
	"fmt"
)

// GeminiConfig configures the Gemini adapter.
type GeminiConfig struct {
	APIKeys     []string
	Model       string
	Temperature float64
}

// GeminiAdapter is a placeholder implementation.
type GeminiAdapter struct {
	cfg     GeminiConfig
	rotator *Rotator
}

// NewGeminiAdapter creates a new adapter.
func NewGeminiAdapter(cfg GeminiConfig) *GeminiAdapter {
	return &GeminiAdapter{cfg: cfg, rotator: NewRotator(cfg.APIKeys)}
}

// Complete calls the Gemini API.
func (a *GeminiAdapter) Complete(ctx context.Context, req CompletionRequest) (LLMResponse, error) {
	_ = ctx
	return LLMResponse{}, ProviderError{Provider: "gemini", Message: "adapter not implemented"}
}

func (a *GeminiAdapter) nextKey() (string, error) {
	key := a.rotator.Next()
	if key == "" {
		return "", fmt.Errorf("no Gemini API keys configured")
	}
	return key, nil
}
