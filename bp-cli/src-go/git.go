package bp

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// gitTimeout is the default timeout for git commands.
const gitTimeout = 30 * time.Second

// GitTag represents a git tag with its name and creation timestamp.
type GitTag struct {
	Name      string
	Timestamp string
}

// GitAvailable reports whether dir resides inside a git repository.
// It returns false when git is not installed or dir is not under a git repo.
func GitAvailable(dir string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--git-dir")
	cmd.Dir = dir
	return cmd.Run() == nil
}

// GitRepoRoot returns the top-level directory of the git repository
// that contains dir.
func GitRepoRoot(dir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --show-toplevel: %w", err)
	}
	root := strings.TrimSpace(string(out))
	return filepath.Clean(root), nil
}

// GitListTags returns tags matching pattern, sorted newest-first by
// creator date.  If the directory is not a git repo or git is not
// installed the function returns (nil, nil).
func GitListTags(dir string, pattern string) ([]GitTag, error) {
	if !GitAvailable(dir) {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	format := "%(refname:short)\t%(creatordate:iso8601-strict)"
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "tag", "-l", pattern,
		"--sort=-creatordate", "--format="+format)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git tag -l: %w", err)
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}
	lines := strings.Split(raw, "\n")
	tags := make([]GitTag, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		tag := GitTag{Name: parts[0]}
		if len(parts) == 2 {
			tag.Timestamp = parts[1]
		}
		tags = append(tags, tag)
	}
	return tags, nil
}

// GitArchive extracts the given paths from a tagged commit into destDir.
// It pipes `git archive` output through `tar -x`.
func GitArchive(dir string, tag string, paths []string, destDir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	// Build git archive command.
	gitArgs := []string{"-C", dir, "archive", tag, "--"}
	gitArgs = append(gitArgs, paths...)
	gitCmd := exec.CommandContext(ctx, "git", gitArgs...)
	gitCmd.Dir = dir

	// Build tar command.
	tarCmd := exec.CommandContext(ctx, "tar", "-x", "-C", destDir)
	tarCmd.Dir = destDir

	// Connect git archive stdout to tar stdin.
	var errBuf bytes.Buffer
	pipe, err := gitCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("git archive pipe: %w", err)
	}
	tarCmd.Stdin = pipe
	tarCmd.Stderr = &errBuf
	gitCmd.Stderr = &errBuf

	if err := gitCmd.Start(); err != nil {
		return fmt.Errorf("git archive start: %w", err)
	}
	if err := tarCmd.Start(); err != nil {
		_ = gitCmd.Process.Kill()
		return fmt.Errorf("tar start: %w", err)
	}

	gitErr := gitCmd.Wait()
	tarErr := tarCmd.Wait()

	if gitErr != nil {
		return fmt.Errorf("git archive: %w (%s)", gitErr, errBuf.String())
	}
	if tarErr != nil {
		return fmt.Errorf("tar extract: %w (%s)", tarErr, errBuf.String())
	}
	return nil
}
