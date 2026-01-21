package llm

import (
	"context"
	"fmt"

	"github.com/tunapro1234/base-agent/src-go/tools"
)

// Message represents a chat message.
type Message struct {
	Role    string
	Content string
}

// ToolCall represents a tool invocation request.
type ToolCall struct {
	Name string
	Args map[string]any
}

// LLMResponse is the model response.
type LLMResponse struct {
	Content   string
	ToolCalls []ToolCall
	Raw       map[string]any
}

// CompletionRequest is the standard request to a provider.
type CompletionRequest struct {
	Messages        []Message
	Tools           []tools.ToolSchema
	Temperature     *float64
	Model           string
	Provider        string
	ReasoningEffort string
}

// ProviderAdapter is implemented by model providers.
type ProviderAdapter interface {
	Complete(ctx context.Context, req CompletionRequest) (LLMResponse, error)
}

// ProviderError wraps provider failures.
type ProviderError struct {
	Provider string
	Message  string
}

func (e ProviderError) Error() string {
	return fmt.Sprintf("%s: %s", e.Provider, e.Message)
}
