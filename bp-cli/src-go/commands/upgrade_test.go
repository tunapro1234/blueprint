package commands

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	bp "blueprint"
)

func setupUpgradeTest(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()

	// Create consumer blueprint
	consumerDir := filepath.Join(dir, "consumer")
	writeFile(t, filepath.Join(consumerDir, "BLUEPRINT.yaml"),
		"_meta:\n  version: \"1\"\nintent: consumer\ndependencies:\n  - path: ../dep\ntests:\n  packages:\n    - ./...\n")

	// Create dependency blueprint
	depDir := filepath.Join(dir, "dep")
	writeFile(t, filepath.Join(depDir, "BLUEPRINT.yaml"),
		"_meta:\n  version: \"1\"\nintent: dep\n")
	writeFile(t, filepath.Join(depDir, "main.go"), "package dep")

	// Create two snapshots in dep (ss- format requires 8 hex chars)
	depStateDir := filepath.Join(depDir, ".bp")
	for _, id := range []string{"ss-aabb1122", "ss-ccdd3344"} {
		histDir := filepath.Join(depStateDir, "history", id)
		ts := "2025-01-01T00:00:00"
		if id == "ss-ccdd3344" {
			ts = "2025-06-01T00:00:00"
		}
		meta := "id: " + id + "\ntimestamp: " + ts + "\nmessage: " + id + "\ncontent_hash: sha256:abc\napi_hash: sha256:api1\nspec_hash: \"\"\nimpl_hash: \"\"\nrotten: false\nsource: local\n"
		writeFile(t, filepath.Join(histDir, "meta.yaml"), meta)
		writeFile(t, filepath.Join(histDir, "BLUEPRINT.yaml"),
			"_meta:\n  version: \"1\"\nintent: dep\n")
		writeFile(t, filepath.Join(histDir, "main.go"), "package dep")
	}
	// Set current to ss-ccdd3344
	writeFile(t, filepath.Join(depStateDir, "current"), "ss-ccdd3344")

	// Create dep state.yaml
	depState := "snapshot_id: ss-ccdd3344\nblueprint_hash: sha256:dep\nimpl_hash: sha256:dep\nfiles: {}\n"
	writeFile(t, filepath.Join(depStateDir, "state.yaml"), depState)

	// Create consumer state pinned to ss-aabb1122
	consumerState := "snapshot_id: ss-00001111\nblueprint_hash: sha256:x\nimpl_hash: sha256:y\nfiles: {}\ndeps:\n  \"../dep\":\n    pinned: ss-aabb1122\n    latest: ss-aabb1122\n    api_hash: sha256:api1\n    latest_api_hash: sha256:api1\n    api_changed: false\n    rotten: false\n"
	writeFile(t, filepath.Join(consumerDir, ".bp", "state.yaml"), consumerState)

	return consumerDir, depDir
}

func TestUpgradeCommand_ToFlag_PinsToSpecificVersion(t *testing.T) {
	consumerDir, _ := setupUpgradeTest(t)

	old := goTestRunner
	defer func() { goTestRunner = old }()
	goTestRunner = func(string, []string) error { return nil }

	result := UpgradeCommand(CommandContext{
		Path: consumerDir,
		Args: map[string]interface{}{
			"dep_path": "../dep",
			"to":       "ss-ccdd3344",
		},
	})

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", result.ExitCode, result.Output)
	}
	if !strings.Contains(result.Output, "Pinned") {
		t.Fatalf("expected 'Pinned' in output, got: %s", result.Output)
	}
	if !strings.Contains(result.Output, "ss-ccdd3344") {
		t.Fatalf("expected ss-ccdd3344 in output, got: %s", result.Output)
	}

	// Verify state was updated
	state, err := bp.LoadState(filepath.Join(consumerDir, ".bp", "state.yaml"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	depState := state.Deps["../dep"]
	if depState.Pinned != "ss-ccdd3344" {
		t.Fatalf("expected pinned to be ss-ccdd3344, got %s", depState.Pinned)
	}
}

func TestUpgradeCommand_ToFlag_RequiresDepPath(t *testing.T) {
	consumerDir, _ := setupUpgradeTest(t)

	result := UpgradeCommand(CommandContext{
		Path: consumerDir,
		Args: map[string]interface{}{
			"to": "ss-ccdd3344",
		},
	})

	if result.ExitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", result.ExitCode)
	}
	if !strings.Contains(result.Output, "--to requires") {
		t.Fatalf("expected '--to requires' in output, got: %s", result.Output)
	}
}

func TestUpgradeCommand_ToFlag_AlreadyAtVersion(t *testing.T) {
	consumerDir, _ := setupUpgradeTest(t)

	old := goTestRunner
	defer func() { goTestRunner = old }()
	goTestRunner = func(string, []string) error { return nil }

	result := UpgradeCommand(CommandContext{
		Path: consumerDir,
		Args: map[string]interface{}{
			"dep_path": "../dep",
			"to":       "ss-aabb1122",
		},
	})

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", result.ExitCode, result.Output)
	}
	if !strings.Contains(result.Output, "Already at") {
		t.Fatalf("expected 'Already at' in output, got: %s", result.Output)
	}
}

func TestUpgradeCommand_ToFlag_SnapshotNotFound(t *testing.T) {
	consumerDir, _ := setupUpgradeTest(t)

	result := UpgradeCommand(CommandContext{
		Path: consumerDir,
		Args: map[string]interface{}{
			"dep_path": "../dep",
			"to":       "ss-00000000",
		},
	})

	if result.ExitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", result.ExitCode)
	}
	if !strings.Contains(result.Output, "not found") {
		t.Fatalf("expected 'not found' in output, got: %s", result.Output)
	}
}

func TestUpgradeCommand_NormalUpgradeToLatest(t *testing.T) {
	consumerDir, _ := setupUpgradeTest(t)

	old := goTestRunner
	defer func() { goTestRunner = old }()
	goTestRunner = func(string, []string) error { return nil }

	result := UpgradeCommand(CommandContext{
		Path: consumerDir,
		Args: map[string]interface{}{
			"dep_path": "../dep",
		},
	})

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", result.ExitCode, result.Output)
	}
	if !strings.Contains(result.Output, "Upgraded") {
		t.Fatalf("expected 'Upgraded' in output, got: %s", result.Output)
	}
	if !strings.Contains(result.Output, "ss-ccdd3344") {
		t.Fatalf("expected new snapshot ID in output, got: %s", result.Output)
	}

	state, err := bp.LoadState(filepath.Join(consumerDir, ".bp", "state.yaml"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.Deps["../dep"].Pinned != "ss-ccdd3344" {
		t.Fatalf("expected pinned=ss-ccdd3344, got %s", state.Deps["../dep"].Pinned)
	}
}

func TestUpgradeCommand_NoDeps(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "BLUEPRINT.yaml"),
		"_meta:\n  version: \"1\"\nintent: nodeps\n")
	writeFile(t, filepath.Join(dir, ".bp", "state.yaml"),
		"snapshot_id: ss-00001111\nfiles: {}\n")

	result := UpgradeCommand(CommandContext{
		Path: dir,
		Args: map[string]interface{}{},
	})

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", result.ExitCode, result.Output)
	}
	if !strings.Contains(result.Output, "No upgrades available") {
		t.Fatalf("expected 'No upgrades available', got: %s", result.Output)
	}
}

func TestUpgradeCommand_NoState(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "BLUEPRINT.yaml"),
		"_meta:\n  version: \"1\"\nintent: nostate\n")

	result := UpgradeCommand(CommandContext{
		Path: dir,
		Args: map[string]interface{}{},
	})

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", result.ExitCode, result.Output)
	}
	if !strings.Contains(result.Output, "No upgrades available") {
		t.Fatalf("expected 'No upgrades available', got: %s", result.Output)
	}
}

func TestUpgradeCommand_TestFailure_Rollback(t *testing.T) {
	consumerDir, _ := setupUpgradeTest(t)

	old := goTestRunner
	defer func() { goTestRunner = old }()
	testCalled := false
	goTestRunner = func(string, []string) error {
		testCalled = true
		return fmt.Errorf("tests failed")
	}

	result := UpgradeCommand(CommandContext{
		Path: consumerDir,
		Args: map[string]interface{}{
			"dep_path": "../dep",
		},
	})

	if !testCalled {
		t.Fatal("expected tests to be called")
	}
	if !strings.Contains(result.Output, "rolled back") {
		t.Fatalf("expected 'rolled back' in output, got: %s", result.Output)
	}

	// Pin should remain at old version
	state, err := bp.LoadState(filepath.Join(consumerDir, ".bp", "state.yaml"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.Deps["../dep"].Pinned != "ss-aabb1122" {
		t.Fatalf("expected pin rolled back to ss-aabb1122, got %s", state.Deps["../dep"].Pinned)
	}
}

func TestUpgradeCommand_SafeFlag_SkipsAPIChanged(t *testing.T) {
	consumerDir, depDir := setupUpgradeTest(t)

	// Make the latest snapshot have a different API hash
	meta := "id: ss-ccdd3344\ntimestamp: 2025-06-01T00:00:00\nmessage: ss-ccdd3344\ncontent_hash: sha256:abc\napi_hash: sha256:api2\nspec_hash: \"\"\nimpl_hash: \"\"\nrotten: false\nsource: local\n"
	writeFile(t, filepath.Join(depDir, ".bp", "history", "ss-ccdd3344", "meta.yaml"), meta)

	old := goTestRunner
	defer func() { goTestRunner = old }()
	goTestRunner = func(string, []string) error { return nil }

	result := UpgradeCommand(CommandContext{
		Path: consumerDir,
		Args: map[string]interface{}{
			"dep_path": "../dep",
			"safe":     true,
		},
	})

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", result.ExitCode, result.Output)
	}
	if !strings.Contains(result.Output, "Skipped") && !strings.Contains(result.Output, "API changed") {
		t.Fatalf("expected skip message for API change, got: %s", result.Output)
	}

	// Pin should remain unchanged
	state, err := bp.LoadState(filepath.Join(consumerDir, ".bp", "state.yaml"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.Deps["../dep"].Pinned != "ss-aabb1122" {
		t.Fatalf("expected pin unchanged, got %s", state.Deps["../dep"].Pinned)
	}
}
