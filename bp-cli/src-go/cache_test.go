package bp

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSanitizeTagName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"bp/commands/v1-a3f2", "bp_commands_v1-a3f2"},
		{"simple", "simple"},
		{"bp/src/v2", "bp_src_v2"},
	}
	for _, tt := range tests {
		got := sanitizeTagName(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeTagName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractTagID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"bp/commands/v1-a3f2", "v1-a3f2"},
		{"bp/src/ss-abc123d4", "ss-abc123d4"},
		{"simple", "simple"},
	}
	for _, tt := range tests {
		got := ExtractTagID(tt.input)
		if got != tt.want {
			t.Errorf("ExtractTagID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestMaterializeTagSnapshot(t *testing.T) {
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

	// Create a file, commit, and tag
	if err := os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "add hello")
	run("tag", "bp/myapp/v1-abcd")

	// Create a blueprint object for testing
	stateDir := filepath.Join(dir, ".bp")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bpObj := &Blueprint{
		Dir:      dir,
		StateDir: stateDir,
	}

	// Materialize
	cachePath, err := bpObj.MaterializeTagSnapshot("bp/myapp/v1-abcd")
	if err != nil {
		t.Fatalf("MaterializeTagSnapshot: %v", err)
	}

	// Verify cache path
	expectedDir := filepath.Join(stateDir, "cache", "bp_myapp_v1-abcd")
	if cachePath != expectedDir {
		t.Fatalf("expected cache path %q, got %q", expectedDir, cachePath)
	}

	// Verify extracted file exists
	content, err := os.ReadFile(filepath.Join(cachePath, "hello.go"))
	if err != nil {
		t.Fatalf("read cached file: %v", err)
	}
	if string(content) != "package main" {
		t.Fatalf("unexpected content: %q", string(content))
	}

	// Verify idempotency — second call returns same path without error
	cachePath2, err := bpObj.MaterializeTagSnapshot("bp/myapp/v1-abcd")
	if err != nil {
		t.Fatalf("second MaterializeTagSnapshot: %v", err)
	}
	if cachePath2 != cachePath {
		t.Fatalf("expected same cache path on second call")
	}
}

func TestMaterializeTagSnapshot_InvalidTag(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	stateDir := filepath.Join(dir, ".bp")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bpObj := &Blueprint{
		Dir:      dir,
		StateDir: stateDir,
	}

	_, err := bpObj.MaterializeTagSnapshot("nonexistent-tag")
	if err == nil {
		t.Fatal("expected error for invalid tag")
	}

	// Verify cleanup — cache dir should not exist
	cacheDir := filepath.Join(stateDir, "cache", "nonexistent-tag")
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Fatal("expected cache dir to be cleaned up after failure")
	}
}
