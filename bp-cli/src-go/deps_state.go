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
	prev := map[string]DepState{}
	if current, err := LoadState(filepath.Join(b.StateDir, "state.yaml")); err == nil {
		if current.Deps != nil {
			prev = current.Deps
		}
	}
	for _, dep := range deps {
		label := depLabel(b.Dir, dep.Dir)
		latestID, err := readCurrentSnapshotID(dep.StateDir)
		if err != nil {
			if errors.Is(err, ErrNoSnapshot) {
				warnings = append(warnings, fmt.Sprintf("Dependency %s has no snapshot", label))
				continue
			}
			return nil, warnings, err
		}
		pinnedID := latestID
		if prevState, ok := prev[label]; ok && prevState.Pinned != "" {
			pinnedID = prevState.Pinned
		}
		pinnedAPI, err := apiHashForSnapshot(dep, pinnedID)
		if err != nil {
			return nil, warnings, err
		}
		latestAPI, err := apiHashForSnapshot(dep, latestID)
		if err != nil {
			return nil, warnings, err
		}
		latestRotten := false
		if meta, err := LoadSnapshotMeta(dep.StateDir, latestID); err == nil {
			latestRotten = meta.Rotten
		}
		apiChanged := pinnedAPI != "" && latestAPI != "" && pinnedAPI != latestAPI
		state[label] = DepState{
			Pinned:        pinnedID,
			Latest:        latestID,
			APIHash:       pinnedAPI,
			LatestAPIHash: latestAPI,
			APIChanged:    apiChanged,
			Rotten:        latestRotten,
		}
	}
	return state, warnings, nil
}

func apiHashForSnapshot(dep *Blueprint, id string) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", nil
	}
	meta, err := LoadSnapshotMeta(dep.StateDir, id)
	if err == nil && meta.APIHash != "" {
		return meta.APIHash, nil
	}
	return dep.APIHash()
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
	if !IsSnapshotID(id) {
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
