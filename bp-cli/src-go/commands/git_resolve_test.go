package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initTestGitRepo creates a minimal git repo with an initial commit.
func initTestGitRepo(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "init")
	gitRun(t, dir, "config", "user.email", "test@test")
	gitRun(t, dir, "config", "user.name", "test")
	writeFile(t, filepath.Join(dir, ".gitkeep"), "")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-m", "init")
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// setupGitTagTest creates a git repo with a blueprint in a subdirectory
// and returns (repoDir, bpDir, stateDir).
func setupGitTagTest(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	initTestGitRepo(t, dir)

	bpDir := filepath.Join(dir, "pkg", "api")
	writeFile(t, filepath.Join(bpDir, "BLUEPRINT.yaml"), "_meta:\n  version: \"1\"\n")
	writeFile(t, filepath.Join(bpDir, "server.go"), "package api")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-m", "add api")

	stateDir := filepath.Join(bpDir, ".bp")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, bpDir, stateDir
}

func TestResolveGitTagSnapshot_ExactMatch(t *testing.T) {
	dir, _, stateDir := setupGitTagTest(t)

	gitRun(t, dir, "tag", "bp/pkg/api/ss-aabb1122")

	id, err := resolveGitTagSnapshot(stateDir, "ss-aabb1122")
	if err != nil {
		t.Fatalf("resolveGitTagSnapshot: %v", err)
	}
	if id != "ss-aabb1122" {
		t.Fatalf("expected ss-aabb1122, got %s", id)
	}
}

func TestResolveGitTagSnapshot_PrefixMatch(t *testing.T) {
	dir, _, stateDir := setupGitTagTest(t)

	gitRun(t, dir, "tag", "bp/pkg/api/my-feature-abcd")

	id, err := resolveGitTagSnapshot(stateDir, "my-feature")
	if err != nil {
		t.Fatalf("resolveGitTagSnapshot: %v", err)
	}
	if id != "my-feature-abcd" {
		t.Fatalf("expected my-feature-abcd, got %s", id)
	}
}

func TestResolveGitTagSnapshot_AmbiguousPrefix(t *testing.T) {
	dir, _, stateDir := setupGitTagTest(t)

	gitRun(t, dir, "tag", "bp/pkg/api/feat-alpha-1234")

	writeFile(t, filepath.Join(dir, "extra.txt"), "extra")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-m", "extra")
	gitRun(t, dir, "tag", "bp/pkg/api/feat-beta-5678")

	_, err := resolveGitTagSnapshot(stateDir, "feat")
	if err == nil {
		t.Fatal("expected ambiguous error")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected 'ambiguous' in error, got: %v", err)
	}
}

func TestResolveGitTagSnapshot_NotFound(t *testing.T) {
	_, _, stateDir := setupGitTagTest(t)

	_, err := resolveGitTagSnapshot(stateDir, "nonexistent")
	if err == nil {
		t.Fatal("expected not found error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' in error, got: %v", err)
	}
}

func TestResolveGitTagSnapshot_NotGitRepo(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, ".bp")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := resolveGitTagSnapshot(stateDir, "ss-aabb1122")
	if err == nil {
		t.Fatal("expected error for non-git repo")
	}
}

func TestResolveGitTagSnapshot_CaseInsensitive(t *testing.T) {
	dir, _, stateDir := setupGitTagTest(t)

	gitRun(t, dir, "tag", "bp/pkg/api/SS-AABB1122")

	id, err := resolveGitTagSnapshot(stateDir, "ss-aabb1122")
	if err != nil {
		t.Fatalf("resolveGitTagSnapshot: %v", err)
	}
	if id != "SS-AABB1122" {
		t.Fatalf("expected SS-AABB1122, got %s", id)
	}
}

func TestResolveGitTagSnapshot_IgnoresOtherPackageTags(t *testing.T) {
	dir, _, stateDir := setupGitTagTest(t)

	// Tag for a DIFFERENT package
	gitRun(t, dir, "tag", "bp/pkg/other/ss-aabb1122")

	_, err := resolveGitTagSnapshot(stateDir, "ss-aabb1122")
	if err == nil {
		t.Fatal("expected not found — tag belongs to different package")
	}
}

func TestResolveSnapshotID_GitFallback(t *testing.T) {
	dir, _, stateDir := setupGitTagTest(t)

	gitRun(t, dir, "tag", "bp/pkg/api/ss-aabb1122")

	// No local history — resolveSnapshotID should fall back to git tags
	id, err := resolveSnapshotID(stateDir, "ss-aabb1122")
	if err != nil {
		t.Fatalf("resolveSnapshotID: %v", err)
	}
	if id != "ss-aabb1122" {
		t.Fatalf("expected ss-aabb1122, got %s", id)
	}
}

func TestResolveSnapshotID_LocalOverGit(t *testing.T) {
	dir, _, stateDir := setupGitTagTest(t)

	gitRun(t, dir, "tag", "bp/pkg/api/ss-aabb1122")

	// Create local history — should be preferred over git tags
	localID := "ss-aabb1122"
	writeFile(t, filepath.Join(stateDir, "history", localID, "meta.yaml"), "id: "+localID)

	id, err := resolveSnapshotID(stateDir, localID)
	if err != nil {
		t.Fatalf("resolveSnapshotID: %v", err)
	}
	if id != localID {
		t.Fatalf("expected %s, got %s", localID, id)
	}
}

func TestResolveSnapshotID_GitFallbackAfterLocalMiss(t *testing.T) {
	dir, _, stateDir := setupGitTagTest(t)

	gitRun(t, dir, "tag", "bp/pkg/api/ss-ffff0000")

	// Create local history with a different ID
	otherID := "ss-11112222"
	writeFile(t, filepath.Join(stateDir, "history", otherID, "meta.yaml"), "id: "+otherID)

	// ss-ffff0000 is not in local history but exists as git tag
	id, err := resolveSnapshotID(stateDir, "ss-ffff0000")
	if err != nil {
		t.Fatalf("resolveSnapshotID: %v", err)
	}
	if id != "ss-ffff0000" {
		t.Fatalf("expected ss-ffff0000, got %s", id)
	}
}
