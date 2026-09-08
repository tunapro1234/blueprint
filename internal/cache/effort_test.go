package cache

import (
	"path/filepath"
	"testing"
	"time"
)

func TestClaudeEffortFollowsLatestMainAssistant(t *testing.T) {
	row := func(model string, effort any, sidechain bool) any {
		return map[string]any{
			"type": "assistant", "effort": effort, "isSidechain": sidechain,
			"timestamp": time.Now().Format(time.RFC3339Nano),
			"message": map[string]any{"role": "assistant", "model": model,
				"usage": map[string]int{"input_tokens": 100, "cache_read_input_tokens": 200}},
		}
	}
	for _, tc := range []struct {
		name, model, effort string
		tail                []any
	}{
		{"observed-fable", "claude-fable-5-1", "medium", nil},
		{"effort-change", "claude-fable-5-1", "high", []any{row("claude-fable-5-1", "high", false)}},
		{"sidechain", "claude-fable-5-1", "medium", []any{row("claude-opus-5", "max", true)}},
		{"synthetic", "claude-fable-5-1", "medium", []any{row("<synthetic>", "high", false)}},
		{"missing-after-model-switch", "claude-opus-5", "", []any{row("claude-opus-5", nil, false)}},
		{"unknown-schema-retains-usage", "claude-opus-5", "", []any{row("claude-opus-5", map[string]int{"value": 3}, false)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			writeJSONL(t, path, append([]any{row("claude-fable-5-1", "medium", false)}, tc.tail...))
			s := ReadClaudePath(path)
			if s.Model != tc.model || s.Effort != tc.effort || !s.Known || s.CtxTokens != 300 {
				t.Fatalf("model=%q effort=%q known=%v tokens=%d", s.Model, s.Effort, s.Known, s.CtxTokens)
			}
		})
	}
}
