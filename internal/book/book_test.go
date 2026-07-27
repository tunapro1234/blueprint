package book

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSetStatusNoopAndPreservesFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentbook.json")
	original := []byte("{\n  \"updated\": \"hand-edited\",\n  \"agents\": [\n    {\"name\":\"ada\",\"folder\":\"/srv/ada\",\"status\":\"open\",\"parent\":\"manual-parent\",\"role\":\"manual role\",\"extra\":\"keep\"}\n  ]\n}\n")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	if err := SetStatus([]string{path}, "ada", "open", "/srv/ada", "ignored"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatalf("no-op changed file bytes:\n%s", after)
	}

	if err := SetStatus([]string{path}, "ada", "closed", "", "ignored"); err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Agents []map[string]any `json:"agents"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	agent := raw.Agents[0]
	if agent["status"] != "closed" || agent["parent"] != "manual-parent" || agent["role"] != "manual role" || agent["extra"] != "keep" {
		t.Fatalf("updated agent lost fields: %#v", agent)
	}
}

func TestSetStatusNewRecordGetsParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentbook.json")
	if err := os.WriteFile(path, []byte("{\"agents\":[]}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := SetStatus([]string{path}, "new-agent", "open", "/srv/new", "ada"); err != nil {
		t.Fatal(err)
	}
	file, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Agents) != 1 || file.Agents[0].Parent != "ada" {
		t.Fatalf("agents=%+v, want parent ada", file.Agents)
	}
}
