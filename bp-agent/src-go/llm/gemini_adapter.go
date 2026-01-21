package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tunapro1234/base-agent/src-go/tools"
)

// GeminiConfig configures the Gemini adapter.
type GeminiConfig struct {
	APIKeys     []string
	BaseURL     string
	Model       string
	Temperature float64
}

// GeminiAdapter implements Gemini REST calls.
type GeminiAdapter struct {
	cfg     GeminiConfig
	rotator *Rotator
	client  *http.Client
}

// NewGeminiAdapter creates a new adapter.
func NewGeminiAdapter(cfg GeminiConfig) *GeminiAdapter {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://generativelanguage.googleapis.com"
	}
	if cfg.Model == "" {
		cfg.Model = "gemini-3-flash-preview"
	}
	if cfg.Temperature == 0 {
		cfg.Temperature = 0.3
	}
	return &GeminiAdapter{
		cfg:     cfg,
		rotator: NewRotator(cfg.APIKeys),
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

// Complete calls the Gemini API.
func (a *GeminiAdapter) Complete(ctx context.Context, req CompletionRequest) (LLMResponse, error) {
	model := req.Model
	if model == "" {
		model = a.cfg.Model
	}
	if !geminiModelAllowed(model) {
		return LLMResponse{}, ProviderError{Provider: "gemini", Message: fmt.Sprintf("model not allowed: %s", model)}
	}
	temp := a.cfg.Temperature
	if req.Temperature != nil {
		temp = *req.Temperature
	}

	payload := buildGeminiPayload(req.Messages, req.Tools, temp)

	tries := len(a.cfg.APIKeys)
	if tries == 0 {
		return LLMResponse{}, ProviderError{Provider: "gemini", Message: "no Gemini API keys configured"}
	}

	var lastErr error
	for i := 0; i < tries; i++ {
		key, err := a.nextKey()
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := a.sendRequest(ctx, payload, model, key)
		if err == nil {
			return resp, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return LLMResponse{}, lastErr
	}
	return LLMResponse{}, ProviderError{Provider: "gemini", Message: "request failed"}
}

func (a *GeminiAdapter) nextKey() (string, error) {
	key := a.rotator.Next()
	if key == "" {
		return "", fmt.Errorf("no Gemini API keys configured")
	}
	return key, nil
}

func geminiModelAllowed(model string) bool {
	switch model {
	case "gemini-3-flash-preview", "gemini-3-pro-preview":
		return true
	default:
		return false
	}
}

func buildGeminiPayload(messages []Message, tools []tools.ToolSchema, temperature float64) map[string]any {
	contents := make([]map[string]any, 0, len(messages))
	var systemInstruction string

	for _, msg := range messages {
		if msg.Role == "system" {
			systemInstruction = msg.Content
			continue
		}
		role := "user"
		if msg.Role != "user" {
			role = "model"
		}
		contents = append(contents, map[string]any{
			"role":  role,
			"parts": []map[string]any{{"text": msg.Content}},
		})
	}

	payload := map[string]any{
		"contents": contents,
		"generationConfig": map[string]any{
			"temperature": temperature,
		},
	}

	if systemInstruction != "" {
		payload["systemInstruction"] = map[string]any{
			"parts": []map[string]any{{"text": systemInstruction}},
		}
	}

	if len(tools) > 0 {
		functions := make([]map[string]any, 0, len(tools))
		for _, tool := range tools {
			functions = append(functions, map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  tool.Parameters,
			})
		}
		payload["tools"] = []map[string]any{
			{"functionDeclarations": functions},
		}
	}

	return payload
}

func (a *GeminiAdapter) sendRequest(ctx context.Context, payload map[string]any, model, apiKey string) (LLMResponse, error) {
	endpoint := strings.TrimRight(a.cfg.BaseURL, "/") + "/v1beta/models/" + model + ":generateContent"
	data, err := json.Marshal(payload)
	if err != nil {
		return LLMResponse{}, ProviderError{Provider: "gemini", Message: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return LLMResponse{}, ProviderError{Provider: "gemini", Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", apiKey)

	resp, err := a.client.Do(req)
	if err != nil {
		return LLMResponse{}, ProviderError{Provider: "gemini", Message: err.Error()}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(body))
		lowered := strings.ToLower(msg)
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return LLMResponse{}, ProviderError{Provider: "gemini", Message: "auth error: " + msg}
		case http.StatusTooManyRequests:
			return LLMResponse{}, ProviderError{Provider: "gemini", Message: "rate limit: " + msg}
		}
		if strings.Contains(lowered, "quota") || strings.Contains(lowered, "resource_exhausted") {
			return LLMResponse{}, ProviderError{Provider: "gemini", Message: "rate limit: " + msg}
		}
		if resp.StatusCode >= 500 {
			return LLMResponse{}, ProviderError{Provider: "gemini", Message: "server error: " + msg}
		}
		return LLMResponse{}, ProviderError{Provider: "gemini", Message: "api error: " + msg}
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return LLMResponse{}, ProviderError{Provider: "gemini", Message: "invalid json response"}
	}
	content, toolCalls := parseGeminiResponse(raw)
	return LLMResponse{Content: content, ToolCalls: toolCalls, Raw: raw}, nil
}

func parseGeminiResponse(raw map[string]any) (string, []ToolCall) {
	candidates, ok := raw["candidates"].([]any)
	if !ok || len(candidates) == 0 {
		return "", nil
	}
	first, ok := candidates[0].(map[string]any)
	if !ok {
		return "", nil
	}
	content, ok := first["content"].(map[string]any)
	if !ok {
		return "", nil
	}
	parts, ok := content["parts"].([]any)
	if !ok || len(parts) == 0 {
		return "", nil
	}

	var text strings.Builder
	var toolCalls []ToolCall
	for _, part := range parts {
		partMap, ok := part.(map[string]any)
		if !ok {
			continue
		}
		if chunk, ok := partMap["text"].(string); ok {
			text.WriteString(chunk)
		}
		if fc, ok := partMap["functionCall"].(map[string]any); ok {
			name, _ := fc["name"].(string)
			args, _ := fc["args"].(map[string]any)
			toolCalls = append(toolCalls, ToolCall{Name: name, Args: args})
		}
	}

	return text.String(), toolCalls
}
