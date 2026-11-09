package cache

import (
	"path/filepath"
	"testing"
	"time"
)

// The bar showed the previous model until the first reply after a switch,
// because only assistant records carried a model. A request record and the
// /model command's own output both arrive earlier.
func TestClaudeModelFollowsRequestsAndCommandPicks(t *testing.T) {
	clock := time.Date(2026, 11, 9, 20, 0, 0, 0, time.UTC)
	stamp := func() string {
		clock = clock.Add(time.Second)
		return clock.Format(time.RFC3339Nano)
	}
	assistant := func(model, effort string) any {
		return map[string]any{"type": "assistant", "effort": effort, "timestamp": stamp(),
			"message": map[string]any{"role": "assistant", "model": model,
				"usage": map[string]int{"input_tokens": 100}}}
	}
	request := func(source, model, effort string) any {
		return map[string]any{"type": "api-request", "querySource": source, "timestamp": stamp(),
			"params": map[string]any{"model": model, "output_config": map[string]string{"effort": effort}}}
	}
	system := func(text string) any {
		return map[string]any{"type": "system", "subtype": "local_command", "timestamp": stamp(),
			"content": "<local-command-stdout>" + text + "</local-command-stdout>"}
	}
	user := func(content any) any {
		return map[string]any{"type": "user", "timestamp": stamp(),
			"message": map[string]any{"role": "user", "content": content}}
	}
	base := func() []any {
		return []any{request("repl_main_thread", "claude-opus-5-5", "medium"), assistant("claude-opus-5-5", "medium")}
	}
	for _, tc := range []struct {
		name                       string
		rows                       []any
		model, effort, pick, pickE string
	}{
		{"reply-only", []any{assistant("claude-opus-5-5", "medium")}, "claude-opus-5-5", "medium", "", ""},
		{"request-before-reply", append(base(), request("repl_main_thread", "claude-fable-5-2", "high")),
			"claude-fable-5-2", "high", "", ""},
		{"headless-request", []any{request("sdk", "claude-haiku-5-5", "low")}, "claude-haiku-5-5", "low", "", ""},
		{"helper-request-ignored", append(base(), request("generate_session_title", "claude-haiku-5-5", "low")),
			"claude-opus-5-5", "medium", "", ""},
		{"model-command", append(base(), system("Set model to \x1b[1mFable 5.2\x1b[22m and saved as your default for new sessions with \x1b[1mhigh\x1b[22m effort; .claude/settings.local.json pins Opus 5.5 — that applies on restart")),
			"claude-opus-5-5", "medium", "Fable 5.2", "high"},
		{"model-command-user-record", append(base(), user("<local-command-stdout>Set model to `Opus 5.5 (1M context)` and saved as your default for new sessions</local-command-stdout>")),
			"claude-opus-5-5", "medium", "Opus 5.5 (1M context)", ""},
		{"model-command-text-part", append(base(), user([]any{map[string]string{"type": "text", "text": "<local-command-stdout>Kept model as Fable 5.2</local-command-stdout>"}})),
			"claude-opus-5-5", "medium", "Fable 5.2", ""},
		{"effort-command", append(base(), system("Set effort level to high (saved as your default for new sessions): Comprehensive implementation")),
			"claude-opus-5-5", "medium", "", "high"},
		{"effort-after-model", append(base(), system("Set model to Fable 5.2"), system("Kept effort level as medium")),
			"claude-opus-5-5", "medium", "Fable 5.2", "medium"},
		{"ultracode-is-not-an-effort", append(base(), system("Set effort level to ultracode (this session only): high + workflows")),
			"claude-opus-5-5", "medium", "", ""},
		{"failed-command", append(base(), system("Model 'nope' not found")), "claude-opus-5-5", "medium", "", ""},
		{"quoted-in-a-prompt", append(base(), user("look at this: <local-command-stdout>Set model to Fable 5.2</local-command-stdout>")),
			"claude-opus-5-5", "medium", "", ""},
		{"request-spends-pick", append(base(), system("Set model to Fable 5.2 with high effort"), request("repl_main_thread", "claude-fable-5-2", "high")),
			"claude-fable-5-2", "high", "", ""},
		// A reply still streaming from the request before the switch must
		// not erase the pick.
		{"late-reply-keeps-pick", append(base(), system("Set model to Fable 5.2 with high effort"), assistant("claude-opus-5-5", "medium")),
			"claude-opus-5-5", "medium", "Fable 5.2", "high"},
		// Older Claude Code writes no request records: the next reply is the
		// first proof the pick was used.
		{"reply-spends-pick-without-requests", []any{assistant("claude-opus-5-5", "medium"), system("Set model to Fable 5.2"), assistant("claude-fable-5-2", "medium")},
			"claude-fable-5-2", "medium", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			writeJSONL(t, path, tc.rows)
			s := ReadClaudePath(path)
			if s.Model != tc.model || s.Effort != tc.effort || s.PickedModel != tc.pick || s.PickedEffort != tc.pickE {
				t.Fatalf("model=%q effort=%q picked=%q/%q", s.Model, s.Effort, s.PickedModel, s.PickedEffort)
			}
		})
	}
}

func TestClaudeModelAtIsTheNewestModelEvidence(t *testing.T) {
	at := func(second int) string {
		return time.Date(2026, 11, 9, 20, 0, second, 0, time.UTC).Format(time.RFC3339Nano)
	}
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeJSONL(t, path, []any{
		map[string]any{"type": "assistant", "effort": "medium", "timestamp": at(1),
			"message": map[string]any{"role": "assistant", "model": "claude-opus-5-5", "usage": map[string]int{"input_tokens": 1}}},
		map[string]any{"type": "system", "subtype": "local_command", "timestamp": at(5),
			"content": "<local-command-stdout>Set model to Fable 5.2</local-command-stdout>"},
		map[string]any{"type": "user", "timestamp": at(9), "message": map[string]any{"role": "user", "content": "hello"}},
	})
	if s := ReadClaudePath(path); s.ModelAt.Second() != 5 {
		t.Fatalf("ModelAt=%v, want the command at :05", s.ModelAt)
	}
}
