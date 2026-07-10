package worktree

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

// Derived contains the fixed path and branch convention for a managed worktree.
type Derived struct {
	Repo   string
	Path   string
	Branch string
}

// Info describes one entry from git worktree list.
type Info struct {
	Repo   string
	Path   string
	Branch string
	Main   bool
}

// Manager performs worktree operations through git.
type Manager struct {
	Git string
}

func New() *Manager {
	return &Manager{Git: "git"}
}

// Derive applies Blueprint's .worktrees/<topic> and <topic>/dev convention.
func Derive(repoDir, topic string) (Derived, error) {
	if err := validateTopic(topic); err != nil {
		return Derived{}, err
	}
	repo, err := filepath.Abs(repoDir)
	if err != nil {
		return Derived{}, fmt.Errorf("resolve repository path: %w", err)
	}
	repo = filepath.Clean(repo)
	return Derived{
		Repo:   repo,
		Path:   filepath.Join(repo, ".worktrees", topic),
		Branch: topic + "/dev",
	}, nil
}

func validateTopic(topic string) error {
	if topic == "" || topic == "." || topic == ".." || filepath.Base(topic) != topic || strings.ContainsAny(topic, `/\`) {
		return fmt.Errorf("invalid worktree topic %q: expected one path component", topic)
	}
	if strings.HasPrefix(topic, "-") || strings.HasPrefix(topic, ".") || strings.HasSuffix(topic, ".") ||
		strings.HasSuffix(topic, ".lock") || strings.Contains(topic, "..") || strings.Contains(topic, "@{") {
		return fmt.Errorf("invalid worktree topic %q", topic)
	}
	for _, char := range topic {
		if unicode.IsSpace(char) || unicode.IsControl(char) || strings.ContainsRune("~^:?*[", char) {
			return fmt.Errorf("invalid worktree topic %q", topic)
		}
	}
	return nil
}

func (m *Manager) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, m.Git, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), detail)
	}
	return out, nil
}

// ResolveRepo returns the main worktree for the repository containing dir.
func (m *Manager) ResolveRepo(ctx context.Context, dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve repository path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("repository directory does not exist: %s", dir)
	}
	bare, err := m.run(ctx, "-C", abs, "rev-parse", "--is-bare-repository")
	if err != nil {
		return "", fmt.Errorf("%s is not a git repository: %w", dir, err)
	}
	if strings.TrimSpace(string(bare)) == "true" {
		return "", fmt.Errorf("bare repositories are not supported: %s", dir)
	}
	entries, err := m.listFrom(ctx, abs)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("git returned no worktrees for %s", dir)
	}
	return canonical(entries[0].Path), nil
}

// Ensure returns the managed worktree, creating it when it is missing.
func (m *Manager) Ensure(ctx context.Context, repoDir, topic string) (Info, error) {
	repo, err := m.ResolveRepo(ctx, repoDir)
	if err != nil {
		return Info{}, err
	}
	derived, err := Derive(repo, topic)
	if err != nil {
		return Info{}, err
	}
	entries, err := m.listFrom(ctx, repo)
	if err != nil {
		return Info{}, err
	}
	for _, entry := range entries {
		if samePath(entry.Path, derived.Path) {
			if entry.Branch != derived.Branch {
				return Info{}, fmt.Errorf("worktree %s uses branch %s, expected %s", derived.Path, entry.Branch, derived.Branch)
			}
			entry.Repo = repo
			return entry, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(derived.Path), 0o755); err != nil {
		return Info{}, fmt.Errorf("create worktree directory: %w", err)
	}
	if _, err := m.run(ctx, "-C", repo, "worktree", "add", "-B", derived.Branch, derived.Path); err != nil {
		return Info{}, err
	}
	return Info{Repo: repo, Path: derived.Path, Branch: derived.Branch}, nil
}

// List returns worktrees managed below <repo>/.worktrees.
func (m *Manager) List(ctx context.Context, repoDir string) ([]Info, error) {
	repo, err := m.ResolveRepo(ctx, repoDir)
	if err != nil {
		return nil, err
	}
	entries, err := m.listFrom(ctx, repo)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(repo, ".worktrees")
	managed := make([]Info, 0, len(entries))
	for _, entry := range entries {
		if entry.Main || !ContainsPath(root, entry.Path) {
			continue
		}
		entry.Repo = repo
		managed = append(managed, entry)
	}
	return managed, nil
}

// Remove deletes a managed worktree. Dirty worktrees require force.
func (m *Manager) Remove(ctx context.Context, repoDir, topic string, force bool) error {
	repo, err := m.ResolveRepo(ctx, repoDir)
	if err != nil {
		return err
	}
	derived, err := Derive(repo, topic)
	if err != nil {
		return err
	}
	entries, err := m.listFrom(ctx, repo)
	if err != nil {
		return err
	}
	found := false
	for _, entry := range entries {
		if samePath(entry.Path, derived.Path) && !entry.Main {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("worktree does not exist: %s", derived.Path)
	}
	if !force {
		status, statusErr := m.run(ctx, "-C", derived.Path, "status", "--porcelain", "--untracked-files=all")
		if statusErr != nil {
			return statusErr
		}
		if len(bytes.TrimSpace(status)) > 0 {
			return fmt.Errorf("worktree is dirty: %s (use --force to remove it)", derived.Path)
		}
	}
	args := []string{"-C", repo, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, derived.Path)
	_, err = m.run(ctx, args...)
	return err
}

// Inspect finds the git worktree containing dir. Main is true for the primary checkout.
func (m *Manager) Inspect(ctx context.Context, dir string) (Info, error) {
	top, err := m.run(ctx, "-C", dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return Info{}, err
	}
	topPath := canonical(strings.TrimSpace(string(top)))
	entries, err := m.listFrom(ctx, topPath)
	if err != nil {
		return Info{}, err
	}
	if len(entries) == 0 {
		return Info{}, fmt.Errorf("git returned no worktrees for %s", dir)
	}
	repo := canonical(entries[0].Path)
	for _, entry := range entries {
		if samePath(entry.Path, topPath) {
			entry.Repo = repo
			return entry, nil
		}
	}
	return Info{}, fmt.Errorf("could not identify worktree for %s", dir)
}

func (m *Manager) listFrom(ctx context.Context, dir string) ([]Info, error) {
	out, err := m.run(ctx, "-C", dir, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	return parseList(out), nil
}

func parseList(data []byte) []Info {
	var entries []Info
	current := Info{}
	flush := func() {
		if current.Path == "" {
			return
		}
		current.Path = canonical(current.Path)
		current.Main = len(entries) == 0
		entries = append(entries, current)
		current = Info{}
	}
	for _, field := range bytes.Split(data, []byte{0}) {
		if len(field) == 0 {
			flush()
			continue
		}
		line := string(field)
		switch {
		case strings.HasPrefix(line, "worktree "):
			if current.Path != "" {
				flush()
			}
			current.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch refs/heads/"):
			current.Branch = strings.TrimPrefix(line, "branch refs/heads/")
		case line == "detached":
			current.Branch = "(detached)"
		}
	}
	flush()
	return entries
}

// ContainsPath reports whether path is root itself or lies below it.
func ContainsPath(root, path string) bool {
	root, path = canonical(root), canonical(path)
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func samePath(left, right string) bool {
	return canonical(left) == canonical(right)
}

func canonical(path string) string {
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}
