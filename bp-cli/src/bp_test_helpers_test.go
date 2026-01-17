package bp

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func writeBlueprint(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "BLUEPRINT.yaml")
	writeFile(t, path, content)
	return path
}

func loadBlueprintFromDir(t *testing.T, dir, content string) *Blueprint {
	t.Helper()
	writeBlueprint(t, dir, content)
	bpObj, err := LoadBlueprint(dir)
	if err != nil {
		t.Fatalf("LoadBlueprint: %v", err)
	}
	return bpObj
}
