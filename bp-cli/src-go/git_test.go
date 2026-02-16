package bp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initGitRepo initialises a minimal git repo in dir and creates an
// initial commit so that tags can be created.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
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
	run("init")
	run("config", "user.email", "test@test")
	run("config", "user.name", "test")
	// create an initial commit
	placeholder := filepath.Join(dir, ".gitkeep")
	if err := os.WriteFile(placeholder, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
}

func TestGitAvailable_InGitRepo(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	if !GitAvailable(dir) {
		t.Fatal("expected GitAvailable to return true for a git repo")
	}
}

func TestGitAvailable_NotAGitRepo(t *testing.T) {
	dir := t.TempDir()
	if GitAvailable(dir) {
		t.Fatal("expected GitAvailable to return false for a non-git directory")
	}
}

func TestGitRepoRoot(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	// create a subdirectory and query from there
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := GitRepoRoot(sub)
	if err != nil {
		t.Fatalf("GitRepoRoot: %v", err)
	}
	// resolve symlinks for macOS /private/var vs /var
	wantRoot, _ := filepath.EvalSymlinks(dir)
	gotRoot, _ := filepath.EvalSymlinks(root)
	if gotRoot != wantRoot {
		t.Fatalf("expected root %q, got %q", wantRoot, gotRoot)
	}
}

func TestGitRepoRoot_NonGitDir(t *testing.T) {
	dir := t.TempDir()
	_, err := GitRepoRoot(dir)
	if err == nil {
		t.Fatal("expected error for non-git directory")
	}
}

// gitRunWithEnv runs a git command in dir with additional env vars.
func gitRunWithEnv(t *testing.T, dir string, extraEnv []string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestGitListTags(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	run := func(args ...string) {
		t.Helper()
		gitRunWithEnv(t, dir, nil, args...)
	}

	// First commit at a fixed earlier date, then tag v1.
	earlyDate := "2025-01-01T00:00:00+00:00"
	if err := os.WriteFile(filepath.Join(dir, "first.txt"), []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunWithEnv(t, dir, []string{
		"GIT_AUTHOR_DATE=" + earlyDate,
		"GIT_COMMITTER_DATE=" + earlyDate,
	}, "add", ".")
	gitRunWithEnv(t, dir, []string{
		"GIT_AUTHOR_DATE=" + earlyDate,
		"GIT_COMMITTER_DATE=" + earlyDate,
	}, "commit", "-m", "first tagged")
	run("tag", "bp/myapp/v1")
	run("tag", "unrelated/tag")

	// Second commit at a later date, then tag v2.
	laterDate := "2025-06-01T00:00:00+00:00"
	if err := os.WriteFile(filepath.Join(dir, "second.txt"), []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunWithEnv(t, dir, []string{
		"GIT_AUTHOR_DATE=" + laterDate,
		"GIT_COMMITTER_DATE=" + laterDate,
	}, "add", ".")
	gitRunWithEnv(t, dir, []string{
		"GIT_AUTHOR_DATE=" + laterDate,
		"GIT_COMMITTER_DATE=" + laterDate,
	}, "commit", "-m", "second tagged")
	run("tag", "bp/myapp/v2")

	tags, err := GitListTags(dir, "bp/myapp/*")
	if err != nil {
		t.Fatalf("GitListTags: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("expected 2 tags, got %d: %v", len(tags), tags)
	}
	// newest first (v2 was created after v1)
	if tags[0].Name != "bp/myapp/v2" {
		t.Fatalf("expected first tag bp/myapp/v2, got %s", tags[0].Name)
	}
	if tags[1].Name != "bp/myapp/v1" {
		t.Fatalf("expected second tag bp/myapp/v1, got %s", tags[1].Name)
	}
	// timestamps should be non-empty
	for _, tag := range tags {
		if tag.Timestamp == "" {
			t.Fatalf("expected non-empty timestamp for tag %s", tag.Name)
		}
	}
}

func TestGitListTags_NoMatch(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	tags, err := GitListTags(dir, "bp/nothing/*")
	if err != nil {
		t.Fatalf("GitListTags: %v", err)
	}
	if len(tags) != 0 {
		t.Fatalf("expected 0 tags, got %d", len(tags))
	}
}

func TestGitListTags_NonGitDir(t *testing.T) {
	dir := t.TempDir()
	tags, err := GitListTags(dir, "bp/*")
	if err != nil {
		t.Fatalf("expected no error for non-git dir, got: %v", err)
	}
	if tags != nil {
		t.Fatalf("expected nil tags for non-git dir, got %v", tags)
	}
}

func TestGitArchive(t *testing.T) {
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

	// create files, commit, then tag
	subDir := filepath.Join(dir, "pkg")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "hello.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "data.txt"), []byte("some data"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "add files")
	run("tag", "bp/pkg/snap-1")

	// extract into a dest dir
	dest := t.TempDir()
	err := GitArchive(dir, "bp/pkg/snap-1", []string{"pkg/hello.txt", "pkg/data.txt"}, dest)
	if err != nil {
		t.Fatalf("GitArchive: %v", err)
	}

	// verify extracted files
	content, err := os.ReadFile(filepath.Join(dest, "pkg", "hello.txt"))
	if err != nil {
		t.Fatalf("read extracted hello.txt: %v", err)
	}
	if string(content) != "hello world" {
		t.Fatalf("expected 'hello world', got %q", string(content))
	}
	content, err = os.ReadFile(filepath.Join(dest, "pkg", "data.txt"))
	if err != nil {
		t.Fatalf("read extracted data.txt: %v", err)
	}
	if string(content) != "some data" {
		t.Fatalf("expected 'some data', got %q", string(content))
	}
}

func TestGitArchive_WholeDirectory(t *testing.T) {
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
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "add src")
	run("tag", "bp/src/v1")

	dest := t.TempDir()
	err := GitArchive(dir, "bp/src/v1", []string{"src/"}, dest)
	if err != nil {
		t.Fatalf("GitArchive: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dest, "src", "main.go"))
	if err != nil {
		t.Fatalf("read extracted main.go: %v", err)
	}
	if !strings.Contains(string(content), "package main") {
		t.Fatalf("unexpected content: %q", string(content))
	}
}

func TestGitArchive_InvalidTag(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	dest := t.TempDir()
	err := GitArchive(dir, "nonexistent-tag", []string{"."}, dest)
	if err == nil {
		t.Fatal("expected error for invalid tag")
	}
}
