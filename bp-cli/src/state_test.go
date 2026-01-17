package bp

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestHashFunctions(t *testing.T) {
	impl := ComputeImplHash(map[string]string{"b": "hash2", "a": "hash1"})
	expectedImpl := HashString("a:hash1\nb:hash2\n")
	if impl != expectedImpl {
		t.Fatalf("unexpected impl hash: %s", impl)
	}
	deps := map[string]DepState{
		"./b": {Latest: "id2"},
		"./a": {Pinned: "id1"},
	}
	depsHash := ComputeDepsHash(deps)
	expectedDeps := HashString("./a:id1\n./b:id2\n")
	if depsHash != expectedDeps {
		t.Fatalf("unexpected deps hash: %s", depsHash)
	}
	content := ComputeContentHash("bp", "impl", "deps")
	expectedContent := HashString("bp:impl:deps")
	if content != expectedContent {
		t.Fatalf("unexpected content hash: %s", content)
	}
}

func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeFile(t, path, "hello")
	got, err := HashFile(path)
	if err != nil {
		t.Fatalf("HashFile: %v", err)
	}
	expected := HashString("hello")
	if got != expected {
		t.Fatalf("unexpected hash: %s", got)
	}
}

func TestBuildSnapshotIDAndSlugify(t *testing.T) {
	ts := time.Date(2024, 1, 2, 3, 4, 0, 0, time.UTC)
	id := BuildSnapshotID(ts, "sha256:abcd1234", "Hello, World!")
	if id != "20240102-0304-abcd-hello-world" {
		t.Fatalf("unexpected snapshot id: %s", id)
	}
	if slug := slugify("   "); slug != "snapshot" {
		t.Fatalf("unexpected slug: %s", slug)
	}
}

func TestSaveLoadState(t *testing.T) {
	dir := t.TempDir()
	bpObj := loadBlueprintFromDir(t, dir, "_meta:\n  version: \"1\"\n")
	state := &State{
		SnapshotID:    "20240102-0304-abcd-test",
		BlueprintHash: "bp",
		ImplHash:      "impl",
		Files:         map[string]string{"a.txt": "hash"},
		Deps: map[string]DepState{
			"./dep": {Pinned: "p", Latest: "l", APIHash: "a", LatestAPIHash: "b", APIChanged: true},
		},
		Dependents: map[string]DepRef{"./parent": {Using: "u"}},
	}
	if err := bpObj.SaveState(state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	loaded, err := LoadState(filepath.Join(bpObj.StateDir, "state.yaml"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if loaded.SnapshotID != state.SnapshotID || loaded.BlueprintHash != state.BlueprintHash || loaded.ImplHash != state.ImplHash {
		t.Fatalf("unexpected loaded state header: %+v", loaded)
	}
	if !reflect.DeepEqual(loaded.Files, state.Files) {
		t.Fatalf("unexpected loaded files: %v", loaded.Files)
	}
	if dep, ok := loaded.Deps["./dep"]; !ok || dep.Pinned != "p" || dep.Latest != "l" || !dep.APIChanged {
		t.Fatalf("unexpected loaded dep: %+v", dep)
	}
	if ref, ok := loaded.Dependents["./parent"]; !ok || ref.Using != "u" {
		t.Fatalf("unexpected loaded dependent: %+v", ref)
	}
}

func TestLoadStateMissingFile(t *testing.T) {
	_, err := LoadState(filepath.Join(t.TempDir(), "state.yaml"))
	if err != ErrNoSnapshot {
		t.Fatalf("expected ErrNoSnapshot, got %v", err)
	}
}

func TestLoadStateComputesAPIChanged(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.yaml")
	content := "snapshot_id: 20240102-0304-abcd-test\n" +
		"deps:\n" +
		"  \"./dep\":\n" +
		"    api_hash: sha256:old\n" +
		"    latest_api_hash: sha256:new\n"
	writeFile(t, statePath, content)
	loaded, err := LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	dep := loaded.Deps["./dep"]
	if !dep.APIChanged {
		t.Fatalf("expected APIChanged true, got false")
	}
}

func TestCollectTrackedFilesFallback(t *testing.T) {
	dir := t.TempDir()
	bpObj := loadBlueprintFromDir(t, dir, "_meta:\n  version: \"1\"\n")
	writeFile(t, filepath.Join(dir, "main.go"), "package main")
	writeFile(t, filepath.Join(dir, "main_test.go"), "package main")
	writeFile(t, filepath.Join(dir, "notes.txt"), "notes")
	writeFile(t, filepath.Join(dir, ".blueprint", "state.yaml"), "")
	writeFile(t, filepath.Join(dir, "node_modules", "skip.js"), "")
	writeFile(t, filepath.Join(dir, "sub", "BLUEPRINT.yaml"), "_meta:\n  version: \"1\"\n")
	writeFile(t, filepath.Join(dir, "sub", "file.go"), "package sub")

	files, err := bpObj.collectTrackedFiles()
	if err != nil {
		t.Fatalf("collectTrackedFiles: %v", err)
	}
	keys := sortedKeys(files)
	expected := []string{"main.go", "notes.txt", "sub/file.go"}
	if !reflect.DeepEqual(keys, expected) {
		t.Fatalf("unexpected tracked files: %v", keys)
	}
}

func TestCollectTrackedFilesWithStructure(t *testing.T) {
	t.Run("skip nested blueprint by default", func(t *testing.T) {
		dir := t.TempDir()
		content := "_meta:\n  version: \"1\"\nimplementation:\n  structure:\n    - pkg/\n    - lib/\n    - file.txt\n"
		bpObj := loadBlueprintFromDir(t, dir, content)
		writeFile(t, filepath.Join(dir, "file.txt"), "file")
		writeFile(t, filepath.Join(dir, "lib", "b.go"), "package lib")
		writeFile(t, filepath.Join(dir, "pkg", "a.go"), "package pkg")
		writeFile(t, filepath.Join(dir, "pkg", "BLUEPRINT.yaml"), "_meta:\n  version: \"1\"\n")

		files, err := bpObj.collectTrackedFiles()
		if err != nil {
			t.Fatalf("collectTrackedFiles: %v", err)
		}
		keys := sortedKeys(files)
		expected := []string{"file.txt", "lib/b.go"}
		if !reflect.DeepEqual(keys, expected) {
			t.Fatalf("unexpected tracked files: %v", keys)
		}
	})

	t.Run("include override when has_blueprint false", func(t *testing.T) {
		dir := t.TempDir()
		content := "_meta:\n  version: \"1\"\nimplementation:\n  structure:\n    - pkg/:\n        has_blueprint: false\n"
		bpObj := loadBlueprintFromDir(t, dir, content)
		writeFile(t, filepath.Join(dir, "pkg", "a.go"), "package pkg")
		writeFile(t, filepath.Join(dir, "pkg", "BLUEPRINT.yaml"), "_meta:\n  version: \"1\"\n")

		files, err := bpObj.collectTrackedFiles()
		if err != nil {
			t.Fatalf("collectTrackedFiles: %v", err)
		}
		keys := sortedKeys(files)
		expected := []string{"pkg/a.go"}
		if !reflect.DeepEqual(keys, expected) {
			t.Fatalf("unexpected tracked files: %v", keys)
		}
	})
}

func TestCollectTrackedFilesRejectsAbsolutePath(t *testing.T) {
	bpObj := &Blueprint{Dir: t.TempDir(), Data: map[string]interface{}{"implementation": map[string]interface{}{"structure": []interface{}{string(os.PathSeparator) + "abs"}}}}
	if _, err := bpObj.collectTrackedFiles(); err == nil {
		t.Fatalf("expected error for absolute path")
	}
}

func TestChangedFilesFromState(t *testing.T) {
	dir := t.TempDir()
	bpObj := loadBlueprintFromDir(t, dir, "_meta:\n  version: \"1\"\n")
	writeFile(t, filepath.Join(dir, "a.txt"), "one")
	writeFile(t, filepath.Join(dir, "old.txt"), "old")

	aHash, _ := HashFile(filepath.Join(dir, "a.txt"))
	oldHash, _ := HashFile(filepath.Join(dir, "old.txt"))
	bpHash, _ := HashFile(bpObj.Path)

	state := &State{
		BlueprintHash: bpHash,
		Files: map[string]string{
			"a.txt":   aHash,
			"old.txt": oldHash,
		},
	}
	if err := bpObj.SaveState(state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	writeFile(t, filepath.Join(dir, "a.txt"), "two")
	if err := os.Remove(filepath.Join(dir, "old.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	writeFile(t, filepath.Join(dir, "new.txt"), "new")
	writeFile(t, bpObj.Path, "_meta:\n  version: \"2\"\n")

	changed, err := bpObj.GetChangedFiles()
	if err != nil {
		t.Fatalf("GetChangedFiles: %v", err)
	}
	expected := []string{"BLUEPRINT.yaml", "a.txt", "deleted: old.txt", "new: new.txt"}
	if !reflect.DeepEqual(changed, expected) {
		t.Fatalf("unexpected changed files: %v", changed)
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
