package bp

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestBlueprintTreeWalkAndResolveDeps(t *testing.T) {
	root := t.TempDir()
	content := "_meta:\n  version: \"1\"\nimplementation:\n  structure:\n    - skip/:\n        has_blueprint: false\ndependencies:\n  - ./explicit\n  - ./missing\n"
	bpObj := loadBlueprintFromDir(t, root, content)
	loadBlueprintFromDir(t, filepath.Join(root, "skip"), "_meta:\n  version: \"1\"\n")
	auto := loadBlueprintFromDir(t, filepath.Join(root, "auto"), "_meta:\n  version: \"1\"\n")
	explicit := loadBlueprintFromDir(t, filepath.Join(root, "explicit"), "_meta:\n  version: \"1\"\n")

	tree := &BlueprintTree{Root: root}
	walked, err := tree.Walk()
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(walked) != 4 {
		t.Fatalf("expected 4 blueprints, got %d", len(walked))
	}

	deps, warnings, err := tree.ResolveDeps(bpObj)
	if err != nil {
		t.Fatalf("ResolveDeps: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %v", warnings)
	}
	if len(deps) != 2 {
		t.Fatalf("expected 2 deps, got %d", len(deps))
	}
	depDirs := []string{deps[0].Dir, deps[1].Dir}
	sort.Strings(depDirs)
	expected := []string{auto.Dir, explicit.Dir}
	sort.Strings(expected)
	if !reflect.DeepEqual(depDirs, expected) {
		t.Fatalf("unexpected deps: %v", depDirs)
	}
}

func TestBlueprintTreeResolveDepsRejectsAbsolute(t *testing.T) {
	bpObj := &Blueprint{Dir: t.TempDir(), Data: map[string]interface{}{"dependencies": []interface{}{string(filepath.Separator) + "abs"}}}
	tree := &BlueprintTree{Root: bpObj.Dir}
	if _, _, err := tree.ResolveDeps(bpObj); err == nil {
		t.Fatalf("expected error for absolute dependency path")
	}
}

func TestBlueprintTreeTopologicalSort(t *testing.T) {
	root := t.TempDir()
	util := filepath.Join(root, "util")
	lib := filepath.Join(root, "lib")
	app := filepath.Join(root, "app")

	loadBlueprintFromDir(t, util, "_meta:\n  version: \"1\"\n")
	loadBlueprintFromDir(t, lib, "_meta:\n  version: \"1\"\ndependencies:\n  - ../util\n")
	loadBlueprintFromDir(t, app, "_meta:\n  version: \"1\"\ndependencies:\n  - ../lib\n  - ../util\n")

	tree := &BlueprintTree{Root: root}
	sorted, err := tree.TopologicalSort()
	if err != nil {
		t.Fatalf("TopologicalSort: %v", err)
	}
	if len(sorted) != 3 {
		t.Fatalf("expected 3 blueprints, got %d", len(sorted))
	}
	order := []string{sorted[0].Dir, sorted[1].Dir, sorted[2].Dir}
	expected := []string{util, lib, app}
	if !reflect.DeepEqual(order, expected) {
		t.Fatalf("unexpected order: %v", order)
	}
}

func TestBlueprintTreeTopologicalSortDetectsCycle(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	loadBlueprintFromDir(t, a, "_meta:\n  version: \"1\"\ndependencies:\n  - ../b\n")
	loadBlueprintFromDir(t, b, "_meta:\n  version: \"1\"\ndependencies:\n  - ../a\n")

	tree := &BlueprintTree{Root: root}
	if _, err := tree.TopologicalSort(); err == nil {
		t.Fatalf("expected circular dependency error")
	}
}
