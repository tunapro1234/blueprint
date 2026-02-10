package bp

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestIsBlueprintFile(t *testing.T) {
	cases := map[string]bool{
		"BLUEPRINT.yaml":        true,
		".BLUEPRINT.yaml":       true,
		"BLUEPRINT.spec.yaml":   true,
		".BLUEPRINT.extra.yaml": true,
		"module.bp.yaml":        true,
		"module.BP.yaml":        true,
		"blueprint.yml":         false,
		"blueprint.yaml.bak":    false,
		"notes.txt":             false,
	}
	for name, expect := range cases {
		if got := IsBlueprintFile(name); got != expect {
			t.Fatalf("IsBlueprintFile(%q)=%v, want %v", name, got, expect)
		}
	}
}

func TestFindBlueprintFilesPriority(t *testing.T) {
	dir := t.TempDir()
	files := []string{
		".BLUEPRINT.yaml",
		"BLUEPRINT.spec.yaml",
		".BLUEPRINT.extra.yaml",
		"module.bp.yaml",
		"BLUEPRINT.yaml",
	}
	for _, name := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("_meta:\n  version: \"1\"\n"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
	}
	found, err := FindBlueprintFiles(dir)
	if err != nil {
		t.Fatalf("FindBlueprintFiles: %v", err)
	}
	for i := range found {
		found[i] = filepath.Base(found[i])
	}
	expect := []string{"BLUEPRINT.yaml", ".BLUEPRINT.yaml", "BLUEPRINT.spec.yaml", ".BLUEPRINT.extra.yaml", "module.bp.yaml"}
	if !reflect.DeepEqual(found, expect) {
		t.Fatalf("unexpected order: %v", found)
	}
}

func TestFindBlueprintFileMissing(t *testing.T) {
	_, err := FindBlueprintFile(t.TempDir())
	if err != ErrNotBlueprint {
		t.Fatalf("expected ErrNotBlueprint, got %v", err)
	}
}

func TestRelPathAndBlueprintState(t *testing.T) {
	base := filepath.Join("/tmp", "root")
	path := filepath.Join(base, "a", "b")
	rel, err := relPath(base, path)
	if err != nil {
		t.Fatalf("relPath: %v", err)
	}
	if rel != "a/b" {
		t.Fatalf("unexpected relPath: %q", rel)
	}
	if !isUnderBlueprintState(".bp", ".bp") || !isUnderBlueprintState(".bp/state.yaml", ".bp") {
		t.Fatalf("expected blueprint state detection")
	}
	if isUnderBlueprintState(".bpfake", ".bp") {
		t.Fatalf("did not expect blueprint state detection for .bpfake")
	}
	if !isUnderBlueprintState("blueprint", "blueprint") || !isUnderBlueprintState("blueprint/history/meta.yaml", "blueprint") {
		t.Fatalf("expected custom state dir detection")
	}
}

func TestStateDirRelFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "BLUEPRINT.yaml")
	writeFile(t, path, "_meta:\n  version: \"1\"\n  state_dir: \"blueprint\"\nintent: test\n")
	if got := StateDirRelFromFile(path); got != "blueprint" {
		t.Fatalf("unexpected state dir: %s", got)
	}
	writeFile(t, path, "_meta:\n  state_dir: blueprint\n: bad\n")
	if got := StateDirRelFromFile(path); got != "blueprint" {
		t.Fatalf("unexpected fallback state dir: %s", got)
	}
}
