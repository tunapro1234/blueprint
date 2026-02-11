package bp

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGetHistoryWithTags_MergesLocalAndTags(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	run := func(args ...string) {
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

	// Create blueprint in a subdirectory (so tag path is valid)
	subDir := filepath.Join(dir, "myapp")
	bpContent := "_meta:\n  version: 1\nintent: test\n"
	writeFile(t, filepath.Join(subDir, "BLUEPRINT.yaml"), bpContent)
	run("add", ".")
	run("commit", "-m", "add blueprint")

	// Create a tag snapshot with valid date
	earlyDate := "2025-01-01T00:00:00+00:00"
	gitRunWithEnv(t, dir, []string{
		"GIT_AUTHOR_DATE=" + earlyDate,
		"GIT_COMMITTER_DATE=" + earlyDate,
	}, "tag", "bp/myapp/v1-abcd")

	// Create a local snapshot
	stateDir := filepath.Join(subDir, ".bp")
	historyDir := filepath.Join(stateDir, "history", "ss-aa112233")
	if err := os.MkdirAll(historyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := "id: ss-aa112233\ntimestamp: 2025-06-01T12:00:00\nmessage: local snapshot\ncontent_hash: sha256:abc\napi_hash: \"\"\nspec_hash: \"\"\nimpl_hash: \"\"\nrotten: false\nsource: local\n"
	if err := os.WriteFile(filepath.Join(historyDir, "meta.yaml"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	bpObj, err := LoadBlueprint(subDir)
	if err != nil {
		t.Fatalf("LoadBlueprint: %v", err)
	}

	history, err := bpObj.GetHistoryWithTags()
	if err != nil {
		t.Fatalf("GetHistoryWithTags: %v", err)
	}

	if len(history) < 2 {
		t.Fatalf("expected at least 2 entries, got %d", len(history))
	}

	foundLocal := false
	foundTag := false
	for _, entry := range history {
		if entry.ID == "ss-aa112233" && entry.Source == "local" {
			foundLocal = true
		}
		if entry.ID == "v1-abcd" && entry.Source == "tag" {
			foundTag = true
		}
	}
	if !foundLocal {
		t.Fatal("expected to find local snapshot in history")
	}
	if !foundTag {
		t.Fatal("expected to find tag snapshot in history")
	}
}

func TestGetHistoryWithTags_LocalWinsOnDuplicate(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	run := func(args ...string) {
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

	subDir := filepath.Join(dir, "myapp")
	bpContent := "_meta:\n  version: 1\nintent: test\n"
	writeFile(t, filepath.Join(subDir, "BLUEPRINT.yaml"), bpContent)
	run("add", ".")
	run("commit", "-m", "add blueprint")

	// Create a tag with same ID as local snapshot
	run("tag", "bp/myapp/ss-aabb1122")

	// Create local snapshot with same ID
	stateDir := filepath.Join(subDir, ".bp")
	historyDir := filepath.Join(stateDir, "history", "ss-aabb1122")
	if err := os.MkdirAll(historyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := "id: ss-aabb1122\ntimestamp: 2025-06-01T12:00:00\nmessage: local\ncontent_hash: sha256:abc\napi_hash: \"\"\nspec_hash: \"\"\nimpl_hash: \"\"\nrotten: false\nsource: local\n"
	if err := os.WriteFile(filepath.Join(historyDir, "meta.yaml"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	bpObj, err := LoadBlueprint(subDir)
	if err != nil {
		t.Fatalf("LoadBlueprint: %v", err)
	}

	history, err := bpObj.GetHistoryWithTags()
	if err != nil {
		t.Fatalf("GetHistoryWithTags: %v", err)
	}

	count := 0
	for _, entry := range history {
		if entry.ID == "ss-aabb1122" {
			count++
			if entry.Source != "local" {
				t.Fatal("expected local entry to win over tag entry")
			}
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 entry with ID ss-aabb1122, got %d", count)
	}
}

func TestGetHistoryWithTags_NoGit(t *testing.T) {
	dir := t.TempDir()

	bpContent := "_meta:\n  version: 1\nintent: test\n"
	writeFile(t, filepath.Join(dir, "BLUEPRINT.yaml"), bpContent)

	stateDir := filepath.Join(dir, ".bp")
	historyDir := filepath.Join(stateDir, "history", "ss-aabbccdd")
	if err := os.MkdirAll(historyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := "id: ss-aabbccdd\ntimestamp: 2025-06-01T12:00:00\nmessage: test\ncontent_hash: sha256:abc\napi_hash: \"\"\nspec_hash: \"\"\nimpl_hash: \"\"\nrotten: false\n"
	if err := os.WriteFile(filepath.Join(historyDir, "meta.yaml"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	bpObj, err := LoadBlueprint(dir)
	if err != nil {
		t.Fatalf("LoadBlueprint: %v", err)
	}

	history, err := bpObj.GetHistoryWithTags()
	if err != nil {
		t.Fatalf("GetHistoryWithTags: %v", err)
	}

	if len(history) != 1 {
		t.Fatalf("expected 1 entry (local only, no git), got %d", len(history))
	}
	if history[0].ID != "ss-aabbccdd" {
		t.Fatalf("expected ss-aabbccdd, got %s", history[0].ID)
	}
}

func TestLoadSnapshotMeta_SourceField(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, ".bp")
	historyDir := filepath.Join(stateDir, "history", "ss-11223344")
	if err := os.MkdirAll(historyDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// With source field
	meta := "id: ss-11223344\ntimestamp: 2025-01-01T00:00:00\nmessage: test\ncontent_hash: sha256:x\napi_hash: \"\"\nspec_hash: \"\"\nimpl_hash: \"\"\nrotten: false\nsource: local\n"
	if err := os.WriteFile(filepath.Join(historyDir, "meta.yaml"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	entry, err := LoadSnapshotMeta(stateDir, "ss-11223344")
	if err != nil {
		t.Fatalf("LoadSnapshotMeta: %v", err)
	}
	if entry.Source != "local" {
		t.Fatalf("expected source 'local', got %q", entry.Source)
	}

	// Without source field — should default to "local"
	meta2 := "id: ss-11223344\ntimestamp: 2025-01-01T00:00:00\nmessage: test\ncontent_hash: sha256:x\napi_hash: \"\"\nspec_hash: \"\"\nimpl_hash: \"\"\nrotten: false\n"
	if err := os.WriteFile(filepath.Join(historyDir, "meta.yaml"), []byte(meta2), 0o644); err != nil {
		t.Fatal(err)
	}

	entry2, err := LoadSnapshotMeta(stateDir, "ss-11223344")
	if err != nil {
		t.Fatalf("LoadSnapshotMeta: %v", err)
	}
	if entry2.Source != "local" {
		t.Fatalf("expected default source 'local', got %q", entry2.Source)
	}
}
