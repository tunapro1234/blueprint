package projectschema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validSchemaYAML = `version: 1
history: none
agents:
  - name: lead
    folder: .
    runtime: claude
    role: lead
    color: blue
  - name: worker
    folder: sub
    parent: lead
    runtime: codex
    role: worker
    color: purple
    model: gpt-example
    effort: high
    launch: no-sandbox
`

func TestDecodeRequiresKnownFieldsAndSupportedValues(t *testing.T) {
	if _, err := Decode([]byte(validSchemaYAML)); err != nil {
		t.Fatalf("valid schema: %v", err)
	}
	for _, input := range []string{
		strings.Replace(validSchemaYAML, "history: none", "history: someday", 1),
		strings.Replace(validSchemaYAML, "folder: .", "folder: ../outside", 1),
		strings.Replace(validSchemaYAML, "    role: lead", "    role: lead\n    unknown: value", 1),
		strings.Replace(validSchemaYAML, "history: none", "history: none\nunknown: value", 1),
		strings.Replace(validSchemaYAML, "version: 1", "version: 2", 1),
	} {
		if _, err := Decode([]byte(input)); err == nil {
			t.Errorf("invalid schema accepted:\n%s", input)
		}
	}
}

func TestDecodeRejectsHistoryForUnsupportedRuntimes(t *testing.T) {
	input := strings.Replace(validSchemaYAML, "history: none", "history: file", 1)
	input = strings.Replace(input, "runtime: codex", "runtime: opencode", 1)
	if _, err := Decode([]byte(input)); err == nil || !strings.Contains(err.Error(), "not supported with history file") {
		t.Fatalf("unsupported history/runtime combination error = %v", err)
	}
}

func TestValidateRejectsCyclesAndControlCharacters(t *testing.T) {
	schema, err := Decode([]byte(validSchemaYAML))
	if err != nil {
		t.Fatal(err)
	}
	schema.Agents[0].Parent = "worker"
	if err := Validate(schema); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle error = %v", err)
	}
	schema, _ = Decode([]byte(validSchemaYAML))
	schema.Agents[0].Role = "lead\nforged plan"
	if err := Validate(schema); err == nil {
		t.Fatal("control character in role was accepted")
	}
}

func TestLoadRejectsFolderSymlinkOutsideProject(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "project")
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(filepath.Join(project, Directory), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(project, "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	data := strings.Replace(validSchemaYAML, "folder: sub", "folder: linked", 1)
	if err := os.WriteFile(Path(project), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(project); err == nil || !strings.Contains(err.Error(), "escapes the project root") {
		t.Fatalf("symlink escape error = %v", err)
	}
}

func TestOrderedAgentsPlacesParentsFirstDeterministically(t *testing.T) {
	schema, err := Decode([]byte(`version: 1
history: none
agents:
  - name: z-child
    folder: .
    parent: lead
    runtime: claude
    role: child
    color: blue
  - name: lead
    folder: .
    runtime: claude
    role: lead
    color: blue
  - name: alpha
    folder: .
    runtime: claude
    role: helper
    color: blue
`))
	if err != nil {
		t.Fatal(err)
	}
	ordered := OrderedAgents(schema)
	if len(ordered) != 3 || ordered[0].Name != "alpha" || ordered[1].Name != "lead" || ordered[2].Name != "z-child" {
		t.Fatalf("ordered agents = %#v", ordered)
	}
}
