package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeriveWorktreePathAndBranch(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "example-repo")
	got, err := Derive(repo, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(repo, ".worktrees", "shop"); got.Path != want {
		t.Fatalf("path=%q, want %q", got.Path, want)
	}
	if got.Branch != "shop/dev" {
		t.Fatalf("branch=%q, want shop/dev", got.Branch)
	}
	if got.Repo != repo {
		t.Fatalf("repo=%q, want %q", got.Repo, repo)
	}
}

func TestDeriveRejectsUnsafeTopics(t *testing.T) {
	for _, topic := range []string{"", ".", "..", "../shop", "shop/next", `shop\next`, "-shop", "shop dev", "shop..next"} {
		t.Run(topic, func(t *testing.T) {
			if _, err := Derive(t.TempDir(), topic); err == nil {
				t.Fatalf("Derive accepted unsafe topic %q", topic)
			}
		})
	}
}

func TestContainsPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo", ".worktrees", "shop")
	if !ContainsPath(root, filepath.Join(root, "cmd", "bp")) {
		t.Fatal("expected descendant path to match")
	}
	if ContainsPath(root, root+"-old") {
		t.Fatal("expected sibling path not to match")
	}
}

func TestManagerLifecycleAndDirtyRemoval(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Blueprint Test")
	if err := os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "seed.txt")
	runGit(t, repo, "commit", "-qm", "seed")

	manager := New()
	ctx := context.Background()
	entry, err := manager.Ensure(ctx, repo, "shop")
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(repo, ".worktrees", "shop")
	if entry.Path != wantPath || entry.Branch != "shop/dev" {
		t.Fatalf("entry=%+v, want path %q and branch shop/dev", entry, wantPath)
	}
	if again, err := manager.Ensure(ctx, repo, "shop"); err != nil || again.Path != wantPath {
		t.Fatalf("second Ensure=%+v, %v", again, err)
	}
	entries, err := manager.List(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Path != wantPath {
		t.Fatalf("List=%+v, want one managed worktree", entries)
	}

	nested := filepath.Join(wantPath, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	inspected, err := manager.Inspect(ctx, nested)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Main || inspected.Repo != repo || inspected.Branch != "shop/dev" {
		t.Fatalf("Inspect=%+v, want linked shop worktree", inspected)
	}
	if resolved, err := manager.ResolveRepo(ctx, nested); err != nil || resolved != repo {
		t.Fatalf("ResolveRepo=%q, %v; want %q", resolved, err, repo)
	}

	if err := os.WriteFile(filepath.Join(wantPath, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := manager.Remove(ctx, repo, "shop", false); err == nil || !strings.Contains(err.Error(), "worktree is dirty") {
		t.Fatalf("Remove dirty error=%v, want dirty refusal", err)
	}
	if err := manager.Remove(ctx, repo, "shop", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wantPath); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists after forced removal: %v", err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}
