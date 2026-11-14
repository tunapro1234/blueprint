package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
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

func TestDefinitionSupervisionAndBudgetValidation(t *testing.T) {
	full := validYAML + `supervise:
  breaker:
    failures: 3
    open_for: 15m
  meltdown:
    failures: 5
    window: 30m
budget:
  unit_tokens: 2000000
  run_tokens: 50000000
  cache_read_percent: 0
`
	root := t.TempDir()
	writeDefinition(t, root, full, "ok", `[{"id":"one"}]`)
	definition, err := ReadDefinition(root)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Supervise.Breaker.Failures != 3 || definition.Supervise.Meltdown.Window != "30m" || *definition.Budget.UnitTokens != 2000000 || *definition.Budget.RunTokens != 50000000 || definition.CacheReadWeight() != 0 {
		t.Fatalf("supervision keys not parsed: %#v %#v", definition.Supervise, definition.Budget)
	}
	defaulted := validYAML + "budget:\n  run_tokens: 10\n"
	writeDefinition(t, root, defaulted, "ok", `[{"id":"one"}]`)
	if definition, err := ReadDefinition(root); err != nil || definition.CacheReadWeight() != DefaultCacheReadPercent || definition.Supervise != nil {
		t.Fatalf("default cache-read weight: %v %#v", err, definition)
	}

	for _, test := range []struct {
		name  string
		extra string
		want  string
	}{
		{name: "empty supervise", extra: "supervise: {}\n", want: "supervise needs a breaker or meltdown block"},
		{name: "breaker without failures", extra: "supervise:\n  breaker:\n    open_for: 5m\n", want: "supervise.breaker.failures is required"},
		{name: "breaker zero failures", extra: "supervise:\n  breaker:\n    failures: 0\n    open_for: 5m\n", want: "supervise.breaker.failures is required and must be a positive integer, got 0"},
		{name: "breaker without open_for", extra: "supervise:\n  breaker:\n    failures: 2\n", want: "supervise.breaker.open_for is required"},
		{name: "breaker bad open_for", extra: "supervise:\n  breaker:\n    failures: 2\n    open_for: soon\n", want: `supervise.breaker.open_for must be a positive duration: "soon"`},
		{name: "breaker negative open_for", extra: "supervise:\n  breaker:\n    failures: 2\n    open_for: -5m\n", want: "supervise.breaker.open_for must be a positive duration"},
		{name: "meltdown negative failures", extra: "supervise:\n  meltdown:\n    failures: -1\n    window: 5m\n", want: "supervise.meltdown.failures is required and must be a positive integer, got -1"},
		{name: "meltdown without window", extra: "supervise:\n  meltdown:\n    failures: 2\n", want: "supervise.meltdown.window is required"},
		{name: "meltdown bad window", extra: "supervise:\n  meltdown:\n    failures: 2\n    window: 0s\n", want: "supervise.meltdown.window must be a positive duration"},
		{name: "unknown supervise key", extra: "supervise:\n  breaker:\n    failures: 2\n    open_for: 5m\n    cooldown: 1m\n", want: "field cooldown not found"},
		{name: "non-integer failures", extra: "supervise:\n  breaker:\n    failures: two\n    open_for: 5m\n", want: "cannot unmarshal"},
		{name: "empty budget", extra: "budget: {}\n", want: "budget needs unit_tokens or run_tokens"},
		{name: "percent only budget", extra: "budget:\n  cache_read_percent: 20\n", want: "budget needs unit_tokens or run_tokens"},
		{name: "zero unit tokens", extra: "budget:\n  unit_tokens: 0\n", want: "budget.unit_tokens must be a positive integer, got 0"},
		{name: "negative run tokens", extra: "budget:\n  run_tokens: -5\n", want: "budget.run_tokens must be a positive integer, got -5"},
		{name: "percent above 100", extra: "budget:\n  run_tokens: 5\n  cache_read_percent: 101\n", want: "budget.cache_read_percent must be between 0 and 100, got 101"},
		{name: "negative percent", extra: "budget:\n  run_tokens: 5\n  cache_read_percent: -1\n", want: "budget.cache_read_percent must be between 0 and 100, got -1"},
		{name: "unknown budget key", extra: "budget:\n  run_tokens: 5\n  usd: 10\n", want: "field usd not found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeDefinition(t, root, validYAML+test.extra, "ok", `[{"id":"one"}]`)
			if _, err := ReadDefinition(root); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestDefinitionWithoutSupervisionRoundTrips(t *testing.T) {
	root := t.TempDir()
	writeDefinition(t, root, validYAML, "ok", `[{"id":"one"}]`)
	definition, err := ReadDefinition(root)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Supervise != nil || definition.Budget != nil {
		t.Fatalf("absent keys produced values: %#v %#v", definition.Supervise, definition.Budget)
	}
	encodedJSON, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	encodedYAML, err := yaml.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	for _, encoded := range []string{string(encodedJSON), string(encodedYAML)} {
		if strings.Contains(encoded, "supervise") || strings.Contains(encoded, "budget") {
			t.Fatalf("definition without supervision encodes new keys: %s", encoded)
		}
	}
}

func TestExampleDefinitionIsValid(t *testing.T) {
	definition, err := ReadDefinition(filepath.Join("..", "..", "docs", "examples", "workflow"))
	if err != nil {
		t.Fatal(err)
	}
	if definition.Supervise == nil || definition.Supervise.Breaker == nil || definition.Supervise.Meltdown == nil || definition.Budget == nil {
		t.Fatalf("example does not show the supervision keys: %#v %#v", definition.Supervise, definition.Budget)
	}
}
