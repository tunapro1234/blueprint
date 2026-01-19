package commands

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	bp "blueprint"
)

func TestReadTestConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "BLUEPRINT.yaml"), "_meta:\n  version: \"1\"\ntests:\n  packages:\n    - \"./...\"\n  dependency_snapshots: true\n  snapshot_readonly: false\n")
	bpObj, err := bp.LoadBlueprint(dir)
	if err != nil {
		t.Fatalf("LoadBlueprint: %v", err)
	}
	cfg, err := readTestConfig(bpObj)
	if err != nil {
		t.Fatalf("readTestConfig: %v", err)
	}
	if !reflect.DeepEqual(cfg.Packages, []string{"./..."}) {
		t.Fatalf("unexpected packages: %v", cfg.Packages)
	}
	if !cfg.DependencySnapshots {
		t.Fatalf("expected dependency_snapshots true")
	}
	if cfg.SnapshotReadOnly {
		t.Fatalf("expected snapshot_readonly false")
	}
}

func TestParseStringListErrors(t *testing.T) {
	if _, err := parseStringList("nope", "tests.packages"); err == nil {
		t.Fatalf("expected error for non-list")
	}
	if _, err := parseStringList([]interface{}{1}, "tests.packages"); err == nil {
		t.Fatalf("expected error for non-string list item")
	}
}

func TestRunPackageTestsUsesGoTestRunner(t *testing.T) {
	dir := t.TempDir()
	var gotDir string
	var gotPkgs []string
	old := goTestRunner
	defer func() { goTestRunner = old }()
	goTestRunner = func(workDir string, packages []string) error {
		gotDir = workDir
		gotPkgs = packages
		return nil
	}
	pkgs := []string{"./..."}
	if err := runPackageTests(dir, pkgs); err != nil {
		t.Fatalf("runPackageTests: %v", err)
	}
	if gotDir != dir {
		t.Fatalf("unexpected workDir: %s", gotDir)
	}
	if !reflect.DeepEqual(gotPkgs, pkgs) {
		t.Fatalf("unexpected packages: %v", gotPkgs)
	}
}

func TestRunDependencySnapshotTests(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "BLUEPRINT.yaml"), "_meta:\n  version: \"1\"\n")
	parent, err := bp.LoadBlueprint(root)
	if err != nil {
		t.Fatalf("LoadBlueprint: %v", err)
	}

	depDir := filepath.Join(root, "dep")
	writeFile(t, filepath.Join(depDir, "BLUEPRINT.yaml"), "_meta:\n  version: \"1\"\ntests:\n  packages:\n    - \".\"\n")
	depBp, err := bp.LoadBlueprint(depDir)
	if err != nil {
		t.Fatalf("LoadBlueprint dep: %v", err)
	}

	id := "20240102-0304-abcd-test"
	snapshotImpl := filepath.Join(depBp.StateDir, "history", id, "impl")
	writeFile(t, filepath.Join(snapshotImpl, "dummy.go"), "package dep\n")

	label, err := relativeLabel(parent.Dir, depBp.Dir)
	if err != nil {
		t.Fatalf("relativeLabel: %v", err)
	}
	state := &bp.State{Deps: map[string]bp.DepState{label: {Pinned: id}}}
	if err := parent.SaveState(state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	var gotDir string
	var gotPkgs []string
	old := goTestRunner
	defer func() { goTestRunner = old }()
	goTestRunner = func(workDir string, packages []string) error {
		gotDir = workDir
		gotPkgs = packages
		return nil
	}

	if err := runDependencySnapshotTests(parent); err != nil {
		t.Fatalf("runDependencySnapshotTests: %v", err)
	}
	if gotDir != snapshotImpl {
		t.Fatalf("unexpected workDir: %s", gotDir)
	}
	if !reflect.DeepEqual(gotPkgs, []string{"."}) {
		t.Fatalf("unexpected packages: %v", gotPkgs)
	}
}

func TestCopyImplementationSnapshotReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod semantics differ on windows")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "script.sh")
	writeFile(t, src, "#!/bin/sh\n")
	if err := os.Chmod(src, 0o755); err != nil {
		t.Fatalf("chmod src: %v", err)
	}
	stateDir := filepath.Join(dir, "state")
	if err := copyImplementationSnapshot(map[string]string{"script.sh": src}, stateDir, "snap", true); err != nil {
		t.Fatalf("copyImplementationSnapshot: %v", err)
	}
	dst := filepath.Join(stateDir, "history", "snap", "impl", "script.sh")
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat dst: %v", err)
	}
	mode := info.Mode().Perm()
	if mode&0o222 != 0 {
		t.Fatalf("expected write bits cleared, got mode %o", mode)
	}
	if mode&0o111 == 0 {
		t.Fatalf("expected exec bits preserved, got mode %o", mode)
	}
}
