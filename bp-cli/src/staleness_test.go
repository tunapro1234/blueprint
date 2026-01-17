package bp

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStalenessNoSnapshot(t *testing.T) {
	bpObj := loadBlueprintFromDir(t, t.TempDir(), "_meta:\n  version: \"1\"\n")
	info, err := bpObj.StalenessInfo()
	if err != nil {
		t.Fatalf("StalenessInfo: %v", err)
	}
	if info.State != "no_snapshot" {
		t.Fatalf("expected no_snapshot, got %s", info.State)
	}
	_, err = bpObj.IsStale()
	if !errors.Is(err, ErrNoSnapshot) {
		t.Fatalf("expected ErrNoSnapshot, got %v", err)
	}
}

func TestStalenessBlueprintChanged(t *testing.T) {
	dir := t.TempDir()
	bpObj := loadBlueprintFromDir(t, dir, "_meta:\n  version: \"1\"\n")
	state := &State{BlueprintHash: "sha256:old", Files: map[string]string{}}
	if err := bpObj.SaveState(state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	info, err := bpObj.StalenessInfo()
	if err != nil {
		t.Fatalf("StalenessInfo: %v", err)
	}
	if info.State != "stale" || info.Reason != "blueprint_changed" {
		t.Fatalf("unexpected staleness: %+v", info)
	}
	if !reflect.DeepEqual(info.ChangedFiles, []string{"BLUEPRINT.yaml"}) {
		t.Fatalf("unexpected changed files: %v", info.ChangedFiles)
	}
}

func TestStalenessFilesChanged(t *testing.T) {
	dir := t.TempDir()
	bpObj := loadBlueprintFromDir(t, dir, "_meta:\n  version: \"1\"\n")
	writeFile(t, filepath.Join(dir, "a.txt"), "one")
	aHash, _ := HashFile(filepath.Join(dir, "a.txt"))
	bpHash, _ := HashFile(bpObj.Path)
	state := &State{BlueprintHash: bpHash, Files: map[string]string{"a.txt": aHash}}
	if err := bpObj.SaveState(state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	writeFile(t, filepath.Join(dir, "a.txt"), "two")
	info, err := bpObj.StalenessInfo()
	if err != nil {
		t.Fatalf("StalenessInfo: %v", err)
	}
	if info.State != "stale" || info.Reason != "files_changed" {
		t.Fatalf("unexpected staleness: %+v", info)
	}
}

func TestStalenessDepsChanged(t *testing.T) {
	dir := t.TempDir()
	bpObj := loadBlueprintFromDir(t, dir, "_meta:\n  version: \"1\"\n")
	depDir := filepath.Join(dir, "dep")
	loadBlueprintFromDir(t, depDir, "_meta:\n  version: \"1\"\n")
	writeFile(t, filepath.Join(depDir, ".blueprint", "current"), "20240102-0304-abcd-dep")
	bpHash, _ := HashFile(bpObj.Path)
	state := &State{
		BlueprintHash: bpHash,
		Files:         map[string]string{},
		Deps: map[string]DepState{
			"./dep": {Pinned: "20240101-0000-abcd-old"},
		},
	}
	if err := bpObj.SaveState(state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	info, err := bpObj.StalenessInfo()
	if err != nil {
		t.Fatalf("StalenessInfo: %v", err)
	}
	if info.State != "stale" || info.Reason != "deps_changed" {
		t.Fatalf("unexpected staleness: %+v", info)
	}
	if !reflect.DeepEqual(info.ChangedDeps, []string{"./dep"}) {
		t.Fatalf("unexpected changed deps: %v", info.ChangedDeps)
	}
}

func TestStalenessDepsAPIChanged(t *testing.T) {
	dir := t.TempDir()
	bpObj := loadBlueprintFromDir(t, dir, "_meta:\n  version: \"1\"\n")
	depDir := filepath.Join(dir, "dep")
	loadBlueprintFromDir(t, depDir, "_meta:\n  version: \"1\"\napi:\n  foo: bar\n")
	writeFile(t, filepath.Join(depDir, ".blueprint", "current"), "20240102-0304-abcd-dep")
	bpHash, _ := HashFile(bpObj.Path)
	state := &State{
		BlueprintHash: bpHash,
		Files:         map[string]string{},
		Deps: map[string]DepState{
			"./dep": {Pinned: "20240101-0000-abcd-old", APIHash: "sha256:deadbeef"},
		},
	}
	if err := bpObj.SaveState(state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	info, err := bpObj.StalenessInfo()
	if err != nil {
		t.Fatalf("StalenessInfo: %v", err)
	}
	if info.State != "stale" || info.Reason != "deps_api_changed" {
		t.Fatalf("unexpected staleness: %+v", info)
	}
}
