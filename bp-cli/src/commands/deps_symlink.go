package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	bp "blueprint"
)

func ensureDepsSymlinks(bpObj *bp.Blueprint, deps []*bp.Blueprint, depStates map[string]bp.DepState, destDir string) error {
	if destDir == "" {
		return nil
	}
	if err := os.RemoveAll(destDir); err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(deps) == 0 || len(depStates) == 0 {
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
		target := filepath.Join(dep.StateDir, "history", pinned, "impl")
		if _, err := os.Stat(target); err != nil {
			return err
		}
		linkPath := filepath.Join(destDir, name)
		if err := createRelSymlink(target, linkPath); err != nil {
			return err
		}
	}
	return nil
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
