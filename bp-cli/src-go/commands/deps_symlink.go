package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	bp "blueprint"
)

func ensureDepsSymlinks(bpObj *bp.Blueprint, deps []*bp.Blueprint, depStates map[string]bp.DepState, destDir string, allowExistingBlueprintDirs bool) error {
	if destDir == "" {
		return nil
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	aliases, err := bpObj.DependencyAliases()
	if err != nil {
		return err
	}
	used := map[string]string{}
	expected := map[string]string{}
	for _, dep := range deps {
		label := formatDepLabel(bpObj.Dir, dep.Dir)
		state, ok := depStates[label]
		if !ok {
			continue
		}
		pinned := state.Pinned
		if pinned == "" {
			pinned = state.Latest
		}
		if pinned == "" {
			continue
		}
		name := strings.TrimSpace(aliases[label])
		if name == "" {
			name = filepath.Base(dep.Dir)
		}
		if name == "" || strings.ContainsAny(name, `/\\`) {
			return fmt.Errorf("invalid dependency alias for %s", label)
		}
		if prev, ok := used[name]; ok && prev != label {
			return fmt.Errorf("dependency name collision: %s (%s, %s) - use 'as' alias", name, prev, label)
		}
		used[name] = label
		expected[name] = label
	}

	if err := cleanupDepSymlinks(destDir, expected); err != nil {
		return err
	}

	for _, dep := range deps {
		label := formatDepLabel(bpObj.Dir, dep.Dir)
		state, ok := depStates[label]
		if !ok {
			continue
		}
		pinned := state.Pinned
		if pinned == "" {
			pinned = state.Latest
		}
		if pinned == "" {
			continue
		}
		name := strings.TrimSpace(aliases[label])
		if name == "" {
			name = filepath.Base(dep.Dir)
		}
		linkPath := filepath.Join(destDir, name)
		if allowExistingBlueprintDirs {
			if info, err := os.Lstat(linkPath); err == nil {
				if info.Mode()&os.ModeSymlink == 0 && info.IsDir() {
					if _, err := bp.FindBlueprintFile(linkPath); err == nil {
						continue
					}
				}
			}
		}
		target, err := resolveSnapshotPath(dep, pinned)
		if err != nil {
			return err
		}
		if err := ensureSymlinkTargetAvailable(linkPath, name, allowExistingBlueprintDirs); err != nil {
			return err
		}
		if err := createRelSymlink(target, linkPath); err != nil {
			return err
		}
	}
	return nil
}

func cleanupDepSymlinks(destDir string, expected map[string]string) error {
	entries, err := os.ReadDir(destDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if expected != nil {
			if _, ok := expected[name]; ok {
				continue
			}
		}
		if entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		path := filepath.Join(destDir, name)
		target, err := os.Readlink(path)
		if err != nil {
			continue
		}
		if !looksLikeDependencyTarget(target) {
			continue
		}
		if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func looksLikeDependencyTarget(target string) bool {
	normalized := filepath.ToSlash(target)
	if strings.Contains(normalized, "/.bp/history/") || strings.HasPrefix(normalized, ".bp/history/") {
		return true
	}
	if strings.Contains(normalized, "/.bp/cache/") || strings.HasPrefix(normalized, ".bp/cache/") {
		return true
	}
	return false
}

func ensureSymlinkTargetAvailable(path, name string, allowExistingBlueprintDirs bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if allowExistingBlueprintDirs && info.IsDir() {
		if _, err := bp.FindBlueprintFile(path); err == nil {
			return nil
		}
	}
	return fmt.Errorf("Name collision - '%s' exists both as code and dependency", name)
}

// resolveSnapshotPath finds the on-disk directory for a pinned snapshot.
// It checks the local history first, then falls back to materializing
// a matching git tag into .bp/cache/.
func resolveSnapshotPath(dep *bp.Blueprint, pinned string) (string, error) {
	localPath := filepath.Join(dep.StateDir, "history", pinned)
	if _, err := os.Stat(localPath); err == nil {
		return localPath, nil
	}
	if !bp.GitAvailable(dep.Dir) {
		return "", fmt.Errorf("snapshot %s not found", pinned)
	}
	repoRoot, err := bp.GitRepoRoot(dep.Dir)
	if err != nil {
		return "", fmt.Errorf("snapshot %s not found", pinned)
	}
	rel, err := filepath.Rel(repoRoot, dep.Dir)
	if err != nil {
		return "", fmt.Errorf("snapshot %s not found", pinned)
	}
	pattern := "bp/" + filepath.ToSlash(rel) + "/*"
	tags, err := bp.GitListTags(dep.Dir, pattern)
	if err != nil {
		return "", fmt.Errorf("snapshot %s not found", pinned)
	}
	for _, tag := range tags {
		if bp.ExtractTagID(tag.Name) == pinned {
			cachePath, err := dep.MaterializeTagSnapshot(tag.Name)
			if err != nil {
				return "", err
			}
			return cachePath, nil
		}
	}
	return "", fmt.Errorf("snapshot %s not found (local or git tag)", pinned)
}

func createRelSymlink(target, linkPath string) error {
	if err := os.RemoveAll(linkPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return err
	}
	rel, err := filepath.Rel(filepath.Dir(linkPath), target)
	if err != nil {
		rel = target
	}
	return os.Symlink(rel, linkPath)
}
