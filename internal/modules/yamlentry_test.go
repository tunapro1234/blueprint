package modules

import (
	"os"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v3"
)

var hermesHookEntry = map[string]any{
	"command": "/home/user/.hermes/agent-hooks/bp-compaction.sh",
	"timeout": 5,
}

func decodeYAMLFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	must(t, err)
	var doc map[string]any
	must(t, yaml.Unmarshal(data, &doc))
	return doc
}

func TestYAMLEntryAddsToExistingSequence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := "model: anthropic/claude-sonnet-4.6\nhooks:\n  pre_tool_call:\n    - command: /other/hook.sh\n  pre_llm_call:\n    - command: /existing/hook.sh\n      timeout: 10\n"
	must(t, os.WriteFile(path, []byte(original), 0640))

	change, err := AddYAMLEntry(path, "hooks.pre_llm_call", hermesHookEntry)
	must(t, err)
	if change == nil {
		t.Fatal("expected a change, got nil")
	}
	if change.Created != "" {
		t.Fatalf("created = %q, want empty (the key already existed)", change.Created)
	}

	doc := decodeYAMLFile(t, path)
	hooks := doc["hooks"].(map[string]any)
	preLLM := hooks["pre_llm_call"].([]any)
	if len(preLLM) != 2 {
		t.Fatalf("pre_llm_call has %d entries, want 2:\n%+v", len(preLLM), preLLM)
	}
	// The user's other hook survives untouched.
	preTool := hooks["pre_tool_call"].([]any)
	if len(preTool) != 1 {
		t.Fatalf("pre_tool_call was touched: %+v", preTool)
	}

	// Adding again is a no-op and records nothing.
	again, err := AddYAMLEntry(path, "hooks.pre_llm_call", hermesHookEntry)
	must(t, err)
	if again != nil {
		t.Fatalf("second add returned a change: %+v", again)
	}
	doc = decodeYAMLFile(t, path)
	if got := len(doc["hooks"].(map[string]any)["pre_llm_call"].([]any)); got != 2 {
		t.Fatalf("second add changed pre_llm_call to %d entries", got)
	}

	if _, err := undoYAMLEntry(*change); err != nil {
		t.Fatal(err)
	}
	doc = decodeYAMLFile(t, path)
	hooks = doc["hooks"].(map[string]any)
	preLLM = hooks["pre_llm_call"].([]any)
	if len(preLLM) != 1 {
		t.Fatalf("undo left %d entries in pre_llm_call, want 1: %+v", len(preLLM), preLLM)
	}
	entry := preLLM[0].(map[string]any)
	if entry["command"] != "/existing/hook.sh" {
		t.Fatalf("undo removed the wrong entry: %+v", entry)
	}
	preTool = hooks["pre_tool_call"].([]any)
	if len(preTool) != 1 {
		t.Fatalf("undo touched pre_tool_call: %+v", preTool)
	}
}

func TestYAMLEntryCreatesMissingFileAndKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".hermes", "config.yaml")
	change, err := AddYAMLEntry(path, "hooks.pre_llm_call", hermesHookEntry)
	must(t, err)
	if change.Created != "/" {
		t.Fatalf("created = %q, want \"/\" (bp created the file)", change.Created)
	}
	doc := decodeYAMLFile(t, path)
	preLLM := doc["hooks"].(map[string]any)["pre_llm_call"].([]any)
	if len(preLLM) != 1 {
		t.Fatalf("pre_llm_call has %d entries, want 1", len(preLLM))
	}

	if _, err := undoYAMLEntry(*change); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("undo left %s in place", path)
	}
}

func TestYAMLEntryCreatesMissingKeyInExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := "model: anthropic/claude-sonnet-4.6\n"
	must(t, os.WriteFile(path, []byte(original), 0640))

	change, err := AddYAMLEntry(path, "hooks.pre_llm_call", hermesHookEntry)
	must(t, err)
	// "hooks" itself did not exist: it is the outermost container bp created,
	// the same convention AddJSONEntry uses for Created.
	if change.Created != "hooks" {
		t.Fatalf("created = %q, want %q", change.Created, "hooks")
	}
	doc := decodeYAMLFile(t, path)
	if doc["model"] != "anthropic/claude-sonnet-4.6" {
		t.Fatalf("existing key was lost: %+v", doc)
	}

	if _, err := undoYAMLEntry(*change); err != nil {
		t.Fatal(err)
	}
	// The created (now empty) pre_llm_call list is left in place rather than
	// re-walked away; only the entry itself is guaranteed gone.
	doc = decodeYAMLFile(t, path)
	hooks, ok := doc["hooks"].(map[string]any)
	if ok {
		if preLLM, ok := hooks["pre_llm_call"].([]any); ok && len(preLLM) != 0 {
			t.Fatalf("undo left entries behind: %+v", preLLM)
		}
	}
	if doc["model"] != "anthropic/claude-sonnet-4.6" {
		t.Fatalf("undo lost the user's own key: %+v", doc)
	}
}

func TestYAMLEntryUndoLeftInPlaceWhenModified(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := "hooks:\n  pre_llm_call: []\n"
	must(t, os.WriteFile(path, []byte(original), 0640))
	change, err := AddYAMLEntry(path, "hooks.pre_llm_call", hermesHookEntry)
	must(t, err)

	// The user deleted bp's entry and replaced it with their own before bp
	// could undo it: undo must refuse, not remove the user's entry.
	must(t, os.WriteFile(path, []byte("hooks:\n  pre_llm_call:\n    - command: /users/own-hook.sh\n"), 0640))
	if _, err := undoYAMLEntry(*change); err == nil {
		t.Fatal("expected undo to refuse a modified file")
	}
	doc := decodeYAMLFile(t, path)
	preLLM := doc["hooks"].(map[string]any)["pre_llm_call"].([]any)
	if len(preLLM) != 1 || preLLM[0].(map[string]any)["command"] != "/users/own-hook.sh" {
		t.Fatalf("undo touched the user's entry: %+v", preLLM)
	}
}
