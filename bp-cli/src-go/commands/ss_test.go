package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	bp "blueprint"
)

func setupSsTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "BLUEPRINT.yaml"),
		"_meta:\n  version: \"1\"\nintent: test\n")
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n")
	return dir
}

func TestSsCommand_Basic(t *testing.T) {
	dir := setupSsTest(t)

	old := goTestRunner
	defer func() { goTestRunner = old }()
	goTestRunner = func(string, []string) error { return nil }

	result := SsCommand(CommandContext{
		Path: dir,
		Args: map[string]interface{}{
			"message":    "initial",
			"skip_tests": true,
		},
	})

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", result.ExitCode, result.Output)
	}
	if !strings.Contains(result.Output, "Snapshot created") {
		t.Fatalf("expected 'Snapshot created' in output, got: %s", result.Output)
	}

	info, ok := result.Data.(SnapshotInfo)
	if !ok {
		t.Fatalf("expected SnapshotInfo data, got: %T", result.Data)
	}
	if info.ID == "" {
		t.Fatal("expected non-empty snapshot ID")
	}

	// Verify snapshot directory exists
	snapDir := filepath.Join(dir, ".bp", "history", info.ID)
	if _, err := os.Stat(snapDir); err != nil {
		t.Fatalf("snapshot dir not created: %v", err)
	}

	// Verify BLUEPRINT.yaml was copied into snapshot
	snapBP := filepath.Join(snapDir, "BLUEPRINT.yaml")
	if _, err := os.Stat(snapBP); err != nil {
		t.Fatalf("snapshot BLUEPRINT.yaml not created: %v", err)
	}

	// Verify meta.yaml was written
	metaPath := filepath.Join(snapDir, "meta.yaml")
	if _, err := os.Stat(metaPath); err != nil {
		t.Fatalf("meta.yaml not created: %v", err)
	}

	// Verify main.go was copied
	snapMain := filepath.Join(snapDir, "main.go")
	if _, err := os.Stat(snapMain); err != nil {
		t.Fatalf("main.go not copied: %v", err)
	}

	// Verify current file
	currentData, err := os.ReadFile(filepath.Join(dir, ".bp", "current"))
	if err != nil {
		t.Fatalf("current file not written: %v", err)
	}
	if strings.TrimSpace(string(currentData)) != info.ID {
		t.Fatalf("current file mismatch: %s vs %s", string(currentData), info.ID)
	}

	// Verify state was saved
	state, err := bp.LoadState(filepath.Join(dir, ".bp", "state.yaml"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.SnapshotID != info.ID {
		t.Fatalf("state snapshot ID mismatch: %s vs %s", state.SnapshotID, info.ID)
	}
	if state.Files["main.go"] == "" {
		t.Fatal("expected main.go in state files")
	}
}

func TestSsCommand_DuplicateSnapshot(t *testing.T) {
	dir := setupSsTest(t)

	old := goTestRunner
	defer func() { goTestRunner = old }()
	goTestRunner = func(string, []string) error { return nil }

	// First snapshot
	result1 := SsCommand(CommandContext{
		Path: dir,
		Args: map[string]interface{}{
			"message":    "initial",
			"skip_tests": true,
		},
	})
	if result1.ExitCode != 0 {
		t.Fatalf("first snapshot failed: %s", result1.Output)
	}

	// Second snapshot — same content, should say "already exists"
	result2 := SsCommand(CommandContext{
		Path: dir,
		Args: map[string]interface{}{
			"message":    "initial",
			"skip_tests": true,
		},
	})
	if result2.ExitCode != 0 {
		t.Fatalf("duplicate snapshot failed: %s", result2.Output)
	}
	if !strings.Contains(result2.Output, "already exists") {
		t.Fatalf("expected 'already exists' in output, got: %s", result2.Output)
	}
}

func TestSsCommand_NotBlueprint(t *testing.T) {
	dir := t.TempDir()

	result := SsCommand(CommandContext{
		Path: dir,
		Args: map[string]interface{}{},
	})

	if result.ExitCode == 0 {
		t.Fatal("expected non-zero exit code for non-blueprint dir")
	}
	if !strings.Contains(result.Output, "not a blueprint") {
		t.Fatalf("expected 'not a blueprint' error, got: %s", result.Output)
	}
}

func TestSsCommand_WithMessage(t *testing.T) {
	dir := setupSsTest(t)

	old := goTestRunner
	defer func() { goTestRunner = old }()
	goTestRunner = func(string, []string) error { return nil }

	result := SsCommand(CommandContext{
		Path: dir,
		Args: map[string]interface{}{
			"message":    "add login feature",
			"skip_tests": true,
		},
	})

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", result.ExitCode, result.Output)
	}

	info := result.Data.(SnapshotInfo)
	// With a message, snapshot ID should be slug-based (not ss- prefix)
	if strings.HasPrefix(info.ID, "ss-") {
		t.Fatalf("expected slug-based ID with message, got: %s", info.ID)
	}

	// Verify meta contains the message
	meta, err := bp.LoadSnapshotMeta(filepath.Join(dir, ".bp"), info.ID)
	if err != nil {
		t.Fatalf("LoadSnapshotMeta: %v", err)
	}
	if meta.Message != "add login feature" {
		t.Fatalf("expected message 'add login feature', got: %s", meta.Message)
	}
}

func TestSsCommand_SnapshotIsReadOnly(t *testing.T) {
	dir := setupSsTest(t)

	old := goTestRunner
	defer func() { goTestRunner = old }()
	goTestRunner = func(string, []string) error { return nil }

	result := SsCommand(CommandContext{
		Path: dir,
		Args: map[string]interface{}{
			"skip_tests": true,
		},
	})

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", result.ExitCode, result.Output)
	}

	info := result.Data.(SnapshotInfo)
	snapMain := filepath.Join(dir, ".bp", "history", info.ID, "main.go")
	fileInfo, err := os.Stat(snapMain)
	if err != nil {
		t.Fatalf("stat snapshot main.go: %v", err)
	}
	// Write bits should be cleared (read-only)
	if fileInfo.Mode().Perm()&0o222 != 0 {
		t.Fatalf("expected read-only, got mode %o", fileInfo.Mode().Perm())
	}
}

func TestSsCommand_ValidationError(t *testing.T) {
	dir := t.TempDir()
	// Blueprint without required _meta.version
	writeFile(t, filepath.Join(dir, "BLUEPRINT.yaml"), "intent: broken\n")

	result := SsCommand(CommandContext{
		Path: dir,
		Args: map[string]interface{}{
			"skip_tests": true,
		},
	})

	if result.ExitCode != 2 {
		t.Fatalf("expected exit code 2 for validation error, got %d: %s", result.ExitCode, result.Output)
	}
}

func TestSsCommand_ChildBlueprintExcluded(t *testing.T) {
	dir := setupSsTest(t)
	// Create child blueprint directory
	writeFile(t, filepath.Join(dir, "child", "BLUEPRINT.yaml"),
		"_meta:\n  version: \"1\"\nintent: child\n")
	writeFile(t, filepath.Join(dir, "child", "code.go"), "package child\n")

	old := goTestRunner
	defer func() { goTestRunner = old }()
	goTestRunner = func(string, []string) error { return nil }

	result := SsCommand(CommandContext{
		Path: dir,
		Args: map[string]interface{}{
			"skip_tests": true,
		},
	})

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", result.ExitCode, result.Output)
	}

	info := result.Data.(SnapshotInfo)
	// child/code.go should NOT be in snapshot
	childInSnap := filepath.Join(dir, ".bp", "history", info.ID, "child", "code.go")
	if _, err := os.Stat(childInSnap); !os.IsNotExist(err) {
		t.Fatal("child blueprint files should not be included in parent snapshot")
	}
}
