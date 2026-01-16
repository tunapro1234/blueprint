package bp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var errInvalidSnapshotID = errors.New("invalid snapshot id")

func (b *Blueprint) DependencyState() (map[string]DepState, []string, error) {
	tree := &BlueprintTree{Root: b.Dir}
	deps, warnings, err := tree.ResolveDeps(b)
	if err != nil {
		return nil, warnings, err
	}
	state := map[string]DepState{}
	for _, dep := range deps {
		label := depLabel(b.Dir, dep.Dir)
		snapshotID, err := readCurrentSnapshotID(dep.StateDir)
		if err != nil {
			if errors.Is(err, ErrNoSnapshot) {
				warnings = append(warnings, fmt.Sprintf("Dependency %s has no snapshot", label))
				continue
			}
			return nil, warnings, err
		}
		apiHash, err := dep.APIHash()
		if err != nil {
			return nil, warnings, err
		}
		state[label] = DepState{SnapshotID: snapshotID, APIHash: apiHash}
	}
	return state, warnings, nil
}

func readCurrentSnapshotID(stateDir string) (string, error) {
	currentPath := filepath.Join(stateDir, "current")
	data, err := os.ReadFile(currentPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNoSnapshot
		}
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "", ErrNoSnapshot
	}
	if !isSnapshotID(id) {
		return "", errInvalidSnapshotID
	}
	return id, nil
}

func depLabel(root, dir string) string {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return filepath.ToSlash(dir)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return "."
	}
	if !strings.HasPrefix(rel, ".") {
		rel = "./" + rel
	}
	return rel
}

func isSnapshotID(id string) bool {
	if len(id) != 12 {
		return false
	}
	for _, r := range id {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			continue
		}
		return false
	}
	return true
}
