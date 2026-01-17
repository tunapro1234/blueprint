package bp

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadCurrentSnapshotID(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".blueprint")
	if _, err := readCurrentSnapshotID(stateDir); !errors.Is(err, ErrNoSnapshot) {
		t.Fatalf("expected ErrNoSnapshot, got %v", err)
	}

	writeFile(t, filepath.Join(stateDir, "current"), "not-a-snapshot")
	if _, err := readCurrentSnapshotID(stateDir); !errors.Is(err, errInvalidSnapshotID) {
		t.Fatalf("expected invalid snapshot id error, got %v", err)
	}

	valid := "20240102-0304-abcd-test"
	writeFile(t, filepath.Join(stateDir, "current"), valid)
	id, err := readCurrentSnapshotID(stateDir)
	if err != nil {
		t.Fatalf("readCurrentSnapshotID: %v", err)
	}
	if id != valid {
		t.Fatalf("unexpected id: %s", id)
	}
}

func TestDepLabel(t *testing.T) {
	root := filepath.Join("/tmp", "root")
	sub := filepath.Join(root, "sub")
	if label := depLabel(root, root); label != "." {
		t.Fatalf("expected '.', got %s", label)
	}
	if label := depLabel(root, sub); label != "./sub" {
		t.Fatalf("unexpected label: %s", label)
	}
}

func TestDependencyState(t *testing.T) {
	dir := t.TempDir()
	bpObj := loadBlueprintFromDir(t, dir, "_meta:\n  version: \"1\"\n")

	dep1Dir := filepath.Join(dir, "dep1")
	loadBlueprintFromDir(t, dep1Dir, "_meta:\n  version: \"1\"\napi:\n  foo: bar\n")
	pinnedID := "20240101-0000-abcd-old"
	latestID := "20240102-0304-abcd-new"
	writeFile(t, filepath.Join(dep1Dir, ".blueprint", "current"), latestID)
	writeMeta(t, filepath.Join(dep1Dir, ".blueprint"), pinnedID, "sha256:old")
	writeMeta(t, filepath.Join(dep1Dir, ".blueprint"), latestID, "sha256:new")

	dep2Dir := filepath.Join(dir, "dep2")
	loadBlueprintFromDir(t, dep2Dir, "_meta:\n  version: \"1\"\n")

	label1 := depLabel(dir, dep1Dir)
	state := &State{Deps: map[string]DepState{label1: {Pinned: pinnedID}}}
	if err := bpObj.SaveState(state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	deps, warnings, err := bpObj.DependencyState()
	if err != nil {
		t.Fatalf("DependencyState: %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("expected 1 dep, got %d", len(deps))
	}
	dep := deps[label1]
	if dep.Pinned != pinnedID || dep.Latest != latestID {
		t.Fatalf("unexpected dep ids: %+v", dep)
	}
	if dep.APIHash != "sha256:old" || dep.LatestAPIHash != "sha256:new" || !dep.APIChanged {
		t.Fatalf("unexpected api hashes: %+v", dep)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "dep2") {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
}

func writeMeta(t *testing.T, stateDir, id, apiHash string) {
	t.Helper()
	meta := "id: " + id + "\n" +
		"timestamp: 2024-01-02T03:04:05\n" +
		"api_hash: " + apiHash + "\n"
	writeFile(t, filepath.Join(stateDir, "history", id, "meta.yaml"), meta)
}
