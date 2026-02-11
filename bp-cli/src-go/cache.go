package bp

import (
	"os"
	"path/filepath"
	"strings"
)

// MaterializeTagSnapshot extracts a git tag's content into .bp/cache/
// and returns the cache directory path. The operation is idempotent —
// if the cache directory already exists it is returned immediately.
func (b *Blueprint) MaterializeTagSnapshot(tagName string) (string, error) {
	cacheDir := filepath.Join(b.StateDir, "cache", sanitizeTagName(tagName))
	if info, err := os.Stat(cacheDir); err == nil && info.IsDir() {
		return cacheDir, nil
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	if err := GitArchive(b.Dir, tagName, []string{"."}, cacheDir); err != nil {
		_ = os.RemoveAll(cacheDir)
		return "", err
	}
	return cacheDir, nil
}

// sanitizeTagName converts a git tag name into a safe directory name
// by replacing path separators with underscores.
// e.g. "bp/commands/v1-a3f2" → "bp_commands_v1-a3f2"
func sanitizeTagName(tagName string) string {
	s := strings.ReplaceAll(tagName, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	return s
}

// ExtractTagID returns the last segment of a tag name, which is
// treated as the snapshot ID.
// e.g. "bp/commands/v1-a3f2" → "v1-a3f2"
func ExtractTagID(tagName string) string {
	parts := strings.Split(tagName, "/")
	return parts[len(parts)-1]
}
