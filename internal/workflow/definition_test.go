package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validYAML = `name: sample
version: 1
units:
  file: units.json
  key: id
prompt:
  template: prompt.md
`

func writeDefinition(t *testing.T, root, yamlText, prompt, units string) {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "workflow.yaml"), []byte(yamlText), 0600); err != nil {
		t.Fatal(err)
	}
	if prompt != "" {
		if err := os.WriteFile(filepath.Join(root, "prompt.md"), []byte(prompt), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if units != "" {
		if err := os.WriteFile(filepath.Join(root, "units.json"), []byte(units), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDefinitionStrictAndSamples(t *testing.T) {
	t.Run("valid definition and sample", func(t *testing.T) {
		root := t.TempDir()
		writeDefinition(t, root, validYAML, "score {{.Unit.name}}", `[{"id":"one","name":"First"}]`)
		definition, err := ReadDefinition(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckSampleTemplates(root, root, definition); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("unknown field", func(t *testing.T) {
		root := t.TempDir()
		writeDefinition(t, root, validYAML+"mystery: true\n", "ok", `[{"id":"one"}]`)
		if _, err := ReadDefinition(root); err == nil || !strings.Contains(err.Error(), "field mystery not found") {
			t.Fatalf("expected strict unknown-key error, got %v", err)
		}
	})
	t.Run("bad name", func(t *testing.T) {
		root := t.TempDir()
		writeDefinition(t, root, strings.Replace(validYAML, "name: sample", "name: Bad_Name", 1), "ok", `[{"id":"one"}]`)
		if _, err := ReadDefinition(root); err == nil {
			t.Fatal("expected invalid name")
		}
	})
	t.Run("missing template", func(t *testing.T) {
		root := t.TempDir()
		writeDefinition(t, root, validYAML, "", `[{"id":"one"}]`)
		if _, err := ReadDefinition(root); err == nil || !strings.Contains(err.Error(), "prompt.md") {
			t.Fatalf("expected missing prompt error, got %v", err)
		}
	})
	t.Run("path escape", func(t *testing.T) {
		root := t.TempDir()
		yamlText := strings.Replace(validYAML, "prompt.md", "../outside.md", 1)
		writeDefinition(t, root, yamlText, "", `[{"id":"one"}]`)
		if _, err := ReadDefinition(root); err == nil || !strings.Contains(err.Error(), "escapes") {
			t.Fatalf("expected path escape error, got %v", err)
		}
	})
	t.Run("duplicate unit keys", func(t *testing.T) {
		root := t.TempDir()
		writeDefinition(t, root, validYAML, "ok", `[{"id":"one"},{"id":"one"}]`)
		definition, err := ReadDefinition(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := LoadUnits(definition, root); err == nil || !strings.Contains(err.Error(), "duplicate unit key") {
			t.Fatalf("expected duplicate key error, got %v", err)
		}
	})
	t.Run("sample catches missing key", func(t *testing.T) {
		root := t.TempDir()
		writeDefinition(t, root, validYAML, "score {{.Unit.missing}}", `[{"id":"one"}]`)
		definition, err := ReadDefinition(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckSampleTemplates(root, root, definition); err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("expected template missing-key error, got %v", err)
		}
	})
}

func TestStoreAddAndTornAppendLog(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, "source")
	writeDefinition(t, workflowDir, validYAML, "score {{.Unit.name}}", `[{"id":"one","name":"First"}]`)
	store := NewStore(filepath.Join(root, "state"))
	if _, _, err := store.Add(workflowDir, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Add(workflowDir, false); err != nil {
		t.Fatalf("identical add should be idempotent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "prompt.md"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Add(workflowDir, false); err == nil {
		t.Fatal("expected different workflow to refuse overwrite")
	}
	if _, _, err := store.Add(workflowDir, true); err != nil {
		t.Fatal(err)
	}

	run, err := store.CreateRun(store.WorkflowPath("sample"), mustDefinition(t, store.WorkflowPath("sample")), workflowDir, []string{"agent-a"}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.RunPath(run.ID), "units.jsonl"), []byte(`{"key":`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvent(run.ID, Event{Key: "one", Unit: "one", Agent: "agent-a", State: "sent"}); err != nil {
		t.Fatal(err)
	}
	events, torn, err := store.Events(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !torn || len(events) != 1 || events[0].State != "sent" {
		t.Fatalf("expected valid event plus torn-line report, events=%#v torn=%t", events, torn)
	}
	status, err := store.Status(run.ID)
	if err != nil || !status.TornLog {
		t.Fatalf("status should report torn log: %#v, %v", status, err)
	}
}

func mustDefinition(t *testing.T, dir string) Definition {
	t.Helper()
	definition, err := ReadDefinition(dir)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}
