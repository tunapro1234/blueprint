package commands

import (
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
		"_meta:\n  version: \"1\"\nintent: consumer\ndependencies:\n  - path: ../dep\n")

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
