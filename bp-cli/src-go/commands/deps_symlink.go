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
		target := filepath.Join(dep.StateDir, "history", pinned)
		if _, err := os.Stat(target); err != nil {
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
	return strings.Contains(normalized, "/.blueprint/history/") || strings.HasPrefix(normalized, ".blueprint/history/")
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
