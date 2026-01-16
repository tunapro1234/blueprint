package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	bp "blueprint"
)

var (
	ErrInvalidCurrentID = errors.New("invalid current snapshot id")
)

func formatPath(path string) string {
	if path == "" {
		return "."
	}
	cwd, err := os.Getwd()
	if err != nil {
		return filepath.ToSlash(path)
	}
	rel, err := filepath.Rel(cwd, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return "."
	}
	if !strings.HasPrefix(rel, ".") {
		return "./" + rel
	}
	return rel
}

func normalizeYAMLError(err error) string {
	msg := err.Error()
	if strings.HasPrefix(msg, "yaml: ") {
		return strings.TrimPrefix(msg, "yaml: ")
	}
	if strings.HasPrefix(strings.ToLower(msg), "yaml parse error at line") {
		idx := strings.Index(msg, "line")
		if idx >= 0 {
			return msg[idx:]
		}
	}
	return msg
}

func resolveBlueprintPath(path string) (string, error) {
	if path == "" {
		path = "."
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return bp.FindBlueprintFile(path)
	}
	return path, nil
}

func findBlueprintsRecursive(root string) ([]string, error) {
	if root == "" {
		root = "."
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		root = filepath.Dir(root)
	}
	var files []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".blueprint" {
				return filepath.SkipDir
			}
			bps, err := bp.FindBlueprintFiles(path)
			if err != nil {
				return err
			}
			if len(bps) > 0 {
				files = append(files, bps[0])
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func readCurrentSnapshotID(stateDir string) (string, error) {
	currentPath := filepath.Join(stateDir, "current")
	data, err := os.ReadFile(currentPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", bp.ErrNoSnapshot
		}
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "", bp.ErrNoSnapshot
	}
	if !isSnapshotID(id) {
		return "", ErrInvalidCurrentID
	}
	return id, nil
}

func isSnapshotID(id string) bool {
	if len(id) != 4 {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func formatTimestamp(ts string) string {
	parsed, err := time.Parse("2006-01-02T15:04:05", ts)
	if err != nil {
		if len(ts) >= 16 {
			return strings.ReplaceAll(ts[:16], "T", " ")
		}
		return ts
	}
	return parsed.Format("2006-01-02 15:04")
}

func snapshotDir(stateDir, id string) string {
	return filepath.Join(stateDir, "history", id)
}

func snapshotBlueprintPath(stateDir, id string) string {
	return filepath.Join(snapshotDir(stateDir, id), "BLUEPRINT.yaml")
}

func compareIDs(a, b string) int {
	if len(a) != len(b) {
		return strings.Compare(a, b)
	}
	return strings.Compare(a, b)
}

func resolveMessage(defaultText string, message any) string {
	if s, ok := message.(string); ok && strings.TrimSpace(s) != "" {
		return s
	}
	return defaultText
}

func formatErr(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%v", err)
}
