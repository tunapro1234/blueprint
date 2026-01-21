package llm

import (
	"context"
	"fmt"
	"sync"
)

// LLMRouter dispatches requests to providers.
type LLMRouter struct {
	mu              sync.RWMutex
	defaultProvider string
	providers       map[string]ProviderAdapter
}

// NewRouter creates a router with a default provider.
func NewRouter(defaultProvider string) *LLMRouter {
	return &LLMRouter{
		defaultProvider: defaultProvider,
		providers:       map[string]ProviderAdapter{},
	}
}

// RegisterProvider adds a provider adapter.
func (r *LLMRouter) RegisterProvider(name string, adapter ProviderAdapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[name] = adapter
}

// Complete calls the selected provider.
func (r *LLMRouter) Complete(ctx context.Context, req CompletionRequest) (LLMResponse, error) {
	provider := req.Provider
	if provider == "" {
		provider = r.defaultProvider
	}
	r.mu.RLock()
	adapter, ok := r.providers[provider]
	r.mu.RUnlock()
	if !ok {
		return LLMResponse{}, fmt.Errorf("provider not registered: %s", provider)
	}
	return adapter.Complete(ctx, req)
}
