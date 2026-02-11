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

func warnRottenDependencies(bpObj *bp.Blueprint) {
	if bpObj == nil {
		return
	}
	state, err := bp.LoadState(filepath.Join(bpObj.StateDir, "state.yaml"))
	if err != nil {
		return
	}
	if len(state.Deps) == 0 {
		return
	}
	keys := make([]string, 0, len(state.Deps))
	for k := range state.Deps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if dep := state.Deps[key]; dep.Rotten {
			fmt.Fprintf(os.Stderr, "⚠ Rotten dependency: %s (tests failed on upgrade)\n", key)
		}
	}
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
	stateDirs := map[string]string{}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			parent := filepath.Dir(path)
			if stateDirRel, ok := stateDirs[parent]; ok {
				if isStateDirName(d.Name(), stateDirRel) {
					return filepath.SkipDir
				}
			} else if d.Name() == ".bp" {
				return filepath.SkipDir
			} else if d.Name() == "deps" {
				return filepath.SkipDir
			}
			bps, err := bp.FindBlueprintFiles(path)
			if err != nil {
				return err
			}
			if len(bps) > 0 {
				files = append(files, bps[0])
				stateDirs[path] = bp.StateDirRelFromFile(bps[0])
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

func isStateDirName(name, stateDirRel string) bool {
	if stateDirRel == "" {
		stateDirRel = ".bp"
	}
	if name == stateDirRel {
		return true
	}
	if stateDirRel != ".bp" && name == ".bp" {
		return true
	}
	return false
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
	if !bp.IsSnapshotID(id) {
		return "", ErrInvalidCurrentID
	}
	return id, nil
}

func resolveSnapshotID(stateDir, input string) (string, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return "", fmt.Errorf("Snapshot #%s not found", input)
	}
	if strings.EqualFold(raw, "current") {
		return "current", nil
	}
	id := strings.ToLower(raw)
	if bp.IsSnapshotID(id) {
		hasHistory, err := hasSnapshotHistory(stateDir)
		if err != nil {
			return "", err
		}
		if !hasHistory {
			// No local history — try git tags before giving up
			if tagID, err := resolveGitTagSnapshot(stateDir, id); err == nil {
				return tagID, nil
			}
			return "", bp.ErrNoSnapshot
		}
		if _, err := os.Stat(snapshotDir(stateDir, id)); err != nil {
			if os.IsNotExist(err) {
				matches, matchErr := matchSnapshotPrefix(stateDir, id)
				if matchErr != nil {
					return "", matchErr
				}
				if len(matches) == 1 {
					return matches[0], nil
				}
				if len(matches) > 1 {
					return "", fmt.Errorf("Snapshot #%s is ambiguous", raw)
				}
				// Not found locally — try git tags
				if tagID, err := resolveGitTagSnapshot(stateDir, id); err == nil {
					return tagID, nil
				}
				return "", fmt.Errorf("Snapshot #%s not found", raw)
			}
			return "", err
		}
		return id, nil
	}
	matches, err := matchSnapshotPrefix(stateDir, id)
	if err != nil && !errors.Is(err, bp.ErrNoSnapshot) {
		return "", err
	}
	if len(matches) == 0 {
		// Try git tags as fallback
		if tagID, err := resolveGitTagSnapshot(stateDir, id); err == nil {
			return tagID, nil
		}
		return "", fmt.Errorf("Snapshot #%s not found", raw)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("Snapshot #%s is ambiguous", raw)
	}
	return matches[0], nil
}

// resolveGitTagSnapshot searches git tags for a snapshot ID.
// stateDir is expected to be under a blueprint directory; the function
// derives the blueprint dir and repo-relative path from it.
func resolveGitTagSnapshot(stateDir, input string) (string, error) {
	bpDir := filepath.Dir(stateDir)
	if !bp.GitAvailable(bpDir) {
		return "", fmt.Errorf("git not available")
	}
	repoRoot, err := bp.GitRepoRoot(bpDir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(repoRoot, bpDir)
	if err != nil {
		return "", err
	}
	pattern := "bp/" + filepath.ToSlash(rel) + "/*"
	tags, err := bp.GitListTags(bpDir, pattern)
	if err != nil {
		return "", err
	}
	lowerInput := strings.ToLower(input)
	for _, tag := range tags {
		tagID := bp.ExtractTagID(tag.Name)
		if strings.ToLower(tagID) == lowerInput {
			return tagID, nil
		}
	}
	// prefix match
	var prefixMatches []string
	for _, tag := range tags {
		tagID := bp.ExtractTagID(tag.Name)
		if strings.HasPrefix(strings.ToLower(tagID), lowerInput) {
			prefixMatches = append(prefixMatches, tagID)
		}
	}
	if len(prefixMatches) == 1 {
		return prefixMatches[0], nil
	}
	if len(prefixMatches) > 1 {
		return "", fmt.Errorf("Snapshot #%s is ambiguous", input)
	}
	return "", fmt.Errorf("Snapshot #%s not found in git tags", input)
}

func matchSnapshotPrefix(stateDir, prefix string) ([]string, error) {
	historyDir := filepath.Join(stateDir, "history")
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, bp.ErrNoSnapshot
		}
		return nil, err
	}
	matches := []string{}
	found := false
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !bp.IsSnapshotID(name) {
			continue
		}
		found = true
		if strings.HasPrefix(strings.ToLower(name), prefix) {
			matches = append(matches, name)
		}
	}
	if !found {
		return nil, bp.ErrNoSnapshot
	}
	sort.Strings(matches)
	return matches, nil
}

func hasSnapshotHistory(stateDir string) (bool, error) {
	historyDir := filepath.Join(stateDir, "history")
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if bp.IsSnapshotID(entry.Name()) {
			return true, nil
		}
	}
	return false, nil
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
