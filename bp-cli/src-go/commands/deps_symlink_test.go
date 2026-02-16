package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	bp "blueprint"
)

func setupDepSymlinkTest(t *testing.T) (*bp.Blueprint, []*bp.Blueprint, map[string]bp.DepState) {
	t.Helper()
	dir := t.TempDir()

	// Parent blueprint
	writeFile(t, filepath.Join(dir, "BLUEPRINT.yaml"),
		"_meta:\n  version: \"1\"\nintent: parent\ndependencies:\n  - path: ./child\n")

	// Child blueprint with a snapshot
	childDir := filepath.Join(dir, "child")
	writeFile(t, filepath.Join(childDir, "BLUEPRINT.yaml"),
		"_meta:\n  version: \"1\"\nintent: child\n")
	childStateDir := filepath.Join(childDir, ".bp")
	snapDir := filepath.Join(childStateDir, "history", "ss-aabb1122")
	writeFile(t, filepath.Join(snapDir, "meta.yaml"),
		"id: ss-aabb1122\ntimestamp: 2025-01-01T00:00:00\nmessage: init\ncontent_hash: sha256:x\napi_hash: sha256:api\nspec_hash: \"\"\nimpl_hash: \"\"\nrotten: false\nsource: local\n")
	writeFile(t, filepath.Join(snapDir, "BLUEPRINT.yaml"),
		"_meta:\n  version: \"1\"\nintent: child\n")
	writeFile(t, filepath.Join(snapDir, "main.go"), "package child")

	parent, err := bp.LoadBlueprint(dir)
	if err != nil {
		t.Fatalf("LoadBlueprint parent: %v", err)
	}
	child, err := bp.LoadBlueprint(childDir)
	if err != nil {
		t.Fatalf("LoadBlueprint child: %v", err)
	}

	deps := []*bp.Blueprint{child}
	depStates := map[string]bp.DepState{
		"./child": {Pinned: "ss-aabb1122", Latest: "ss-aabb1122"},
	}
	return parent, deps, depStates
}

func TestEnsureDepsSymlinks_Basic(t *testing.T) {
	parent, deps, depStates := setupDepSymlinkTest(t)

	destDir := filepath.Join(parent.StateDir, "history", "test-snap")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := ensureDepsSymlinks(parent, deps, depStates, destDir, false); err != nil {
		t.Fatalf("ensureDepsSymlinks: %v", err)
	}

	linkPath := filepath.Join(destDir, "child")
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("symlink not created: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("expected symlink, got regular file/dir")
	}
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(target, "ss-aabb1122") {
		t.Fatalf("symlink target should contain snapshot ID, got: %s", target)
	}
}

func TestEnsureDepsSymlinks_EmptyDestDir(t *testing.T) {
	parent, deps, depStates := setupDepSymlinkTest(t)
	_ = parent
	_ = deps
	_ = depStates

	// Empty destDir should be no-op
	if err := ensureDepsSymlinks(parent, deps, depStates, "", false); err != nil {
		t.Fatalf("expected no error for empty destDir, got: %v", err)
	}
}

func TestEnsureDepsSymlinks_NoPinnedVersion(t *testing.T) {
	parent, deps, _ := setupDepSymlinkTest(t)

	destDir := filepath.Join(parent.StateDir, "history", "test-snap2")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Empty pinned and latest = no symlink created
	emptyStates := map[string]bp.DepState{
		"./child": {Pinned: "", Latest: ""},
	}
	if err := ensureDepsSymlinks(parent, deps, emptyStates, destDir, false); err != nil {
		t.Fatalf("ensureDepsSymlinks: %v", err)
	}
	// Should not have created symlink
	if _, err := os.Lstat(filepath.Join(destDir, "child")); !os.IsNotExist(err) {
		t.Fatal("expected no symlink when pinned is empty")
	}
}

func TestEnsureDepsSymlinks_AllowExistingBlueprintDirs(t *testing.T) {
	parent, deps, depStates := setupDepSymlinkTest(t)

	destDir := filepath.Join(parent.StateDir, "history", "test-snap3")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a child directory WITH a BLUEPRINT.yaml at the link path
	childAtDest := filepath.Join(destDir, "child")
	writeFile(t, filepath.Join(childAtDest, "BLUEPRINT.yaml"),
		"_meta:\n  version: \"1\"\n")

	// With allowExistingBlueprintDirs=true, should skip (no error)
	if err := ensureDepsSymlinks(parent, deps, depStates, destDir, true); err != nil {
		t.Fatalf("expected no error with allowExistingBlueprintDirs=true, got: %v", err)
	}

	// The directory should still be a real dir, not a symlink
	info, err := os.Lstat(childAtDest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("expected real directory to be preserved, got symlink")
	}
}

func TestEnsureDepsSymlinks_CollisionWithNonBlueprint(t *testing.T) {
	parent, deps, depStates := setupDepSymlinkTest(t)

	destDir := filepath.Join(parent.StateDir, "history", "test-snap4")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a regular directory WITHOUT blueprint at the link path
	childAtDest := filepath.Join(destDir, "child")
	writeFile(t, filepath.Join(childAtDest, "somefile.txt"), "data")

	// allowExistingBlueprintDirs=false → collision error
	err := ensureDepsSymlinks(parent, deps, depStates, destDir, false)
	if err == nil {
		t.Fatal("expected Name collision error")
	}
	if !strings.Contains(err.Error(), "Name collision") {
		t.Fatalf("expected 'Name collision' error, got: %v", err)
	}
}

func TestEnsureDepsSymlinks_ReplacesOldSymlink(t *testing.T) {
	parent, deps, depStates := setupDepSymlinkTest(t)

	destDir := filepath.Join(parent.StateDir, "history", "test-snap5")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a stale symlink
	linkPath := filepath.Join(destDir, "child")
	if err := os.Symlink("/nonexistent/old/path", linkPath); err != nil {
		t.Fatal(err)
	}

	// Should replace with correct symlink
	if err := ensureDepsSymlinks(parent, deps, depStates, destDir, false); err != nil {
		t.Fatalf("ensureDepsSymlinks: %v", err)
	}

	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(target, "ss-aabb1122") {
		t.Fatalf("expected updated symlink target, got: %s", target)
	}
}

func TestCleanupDepSymlinks(t *testing.T) {
	dir := t.TempDir()

	// Create a dependency symlink (should be cleaned)
	staleLink := filepath.Join(dir, "old-dep")
	target := filepath.Join(dir, ".bp", "history", "old-snap")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, staleLink); err != nil {
		t.Fatal(err)
	}

	// Create a non-dependency symlink (should NOT be cleaned)
	otherLink := filepath.Join(dir, "other")
	if err := os.Symlink("/tmp", otherLink); err != nil {
		t.Fatal(err)
	}

	// Create a regular file (should NOT be cleaned)
	writeFile(t, filepath.Join(dir, "regular.txt"), "data")

	expected := map[string]string{} // no expected deps
	if err := cleanupDepSymlinks(dir, expected); err != nil {
		t.Fatalf("cleanupDepSymlinks: %v", err)
	}

	// Stale dep symlink should be gone
	if _, err := os.Lstat(staleLink); !os.IsNotExist(err) {
		t.Fatal("expected stale dep symlink to be removed")
	}

	// Non-dep symlink should remain
	if _, err := os.Lstat(otherLink); err != nil {
		t.Fatal("non-dependency symlink should not be removed")
	}

	// Regular file should remain
	if _, err := os.Stat(filepath.Join(dir, "regular.txt")); err != nil {
		t.Fatal("regular file should not be removed")
	}
}

func TestLooksLikeDependencyTarget(t *testing.T) {
	tests := []struct {
		target string
		want   bool
	}{
		{"../../child/.bp/history/ss-aabb1122", true},
		{".bp/history/snap-1234", true},
		{"../../child/.bp/cache/bp_child_v1", true},
		{".bp/cache/tag-snap", true},
		{"/tmp/some/other/path", false},
		{"../sibling/code", false},
		{"", false},
	}
	for _, tt := range tests {
		got := looksLikeDependencyTarget(tt.target)
		if got != tt.want {
			t.Errorf("looksLikeDependencyTarget(%q) = %v, want %v", tt.target, got, tt.want)
		}
	}
}

func TestEnsureSymlinkTargetAvailable(t *testing.T) {
	t.Run("nonexistent path is ok", func(t *testing.T) {
		err := ensureSymlinkTargetAvailable("/nonexistent/path", "test", false)
		if err != nil {
			t.Fatalf("expected nil error for nonexistent path, got: %v", err)
		}
	})

	t.Run("existing symlink is removed", func(t *testing.T) {
		dir := t.TempDir()
		link := filepath.Join(dir, "mylink")
		if err := os.Symlink("/tmp", link); err != nil {
			t.Fatal(err)
		}
		if err := ensureSymlinkTargetAvailable(link, "mylink", false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := os.Lstat(link); !os.IsNotExist(err) {
			t.Fatal("symlink should have been removed")
		}
	})

	t.Run("existing blueprint dir allowed when flag set", func(t *testing.T) {
		dir := t.TempDir()
		bpDir := filepath.Join(dir, "child")
		writeFile(t, filepath.Join(bpDir, "BLUEPRINT.yaml"), "_meta:\n  version: \"1\"\n")

		err := ensureSymlinkTargetAvailable(bpDir, "child", true)
		if err != nil {
			t.Fatalf("expected no error for blueprint dir with flag=true, got: %v", err)
		}
	})

	t.Run("existing non-blueprint dir errors", func(t *testing.T) {
		dir := t.TempDir()
		codeDir := filepath.Join(dir, "code")
		writeFile(t, filepath.Join(codeDir, "main.go"), "package main")

		err := ensureSymlinkTargetAvailable(codeDir, "code", false)
		if err == nil || !strings.Contains(err.Error(), "Name collision") {
			t.Fatalf("expected Name collision error, got: %v", err)
		}
	})

	t.Run("existing non-blueprint dir errors even with flag", func(t *testing.T) {
		dir := t.TempDir()
		codeDir := filepath.Join(dir, "code")
		writeFile(t, filepath.Join(codeDir, "main.go"), "package main")

		err := ensureSymlinkTargetAvailable(codeDir, "code", true)
		if err == nil || !strings.Contains(err.Error(), "Name collision") {
			t.Fatalf("expected Name collision for non-blueprint dir even with flag, got: %v", err)
		}
	})
}
