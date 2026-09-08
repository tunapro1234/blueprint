package book

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOnboardingPreservesBookAndExistingCoordinator(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	path := filepath.Join(t.TempDir(), "agentbook.json")
	os.WriteFile(path, []byte(`{"orchestrator":"local","custom":true,"agents":[{"name":"work","parent":"local","color":"red"}]}`), 0600)
	for i := 0; i < 2; i++ {
		name, err := EnsureLocalCoordinator([]string{path}, "/workspace/main")
		if err != nil || name != "main" {
			t.Fatal(name, err)
		}
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"custom": true`) {
		t.Fatal(string(data))
	}
	file, _ := Load(path)
	if len(file.Agents) != 2 || file.Agents[0].Parent != "main" || file.Agents[0].Color != "red" || file.Agents[1].ColorOverride != "160" {
		t.Fatal(file)
	}
	os.WriteFile(path, []byte(`{"orchestrator":"mine","agents":[{"name":"mine","folder":"/owned"}]}`), 0600)
	before, _ := os.ReadFile(path)
	name, err := EnsureLocalCoordinator([]string{path}, "/new")
	after, _ := os.ReadFile(path)
	if err != nil || name != "mine" || string(before) != string(after) {
		t.Fatal(name, err, string(after))
	}
}
