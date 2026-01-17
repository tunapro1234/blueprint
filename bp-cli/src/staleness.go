package bp

import (
	"errors"
	"path/filepath"
	"sort"
)

type StalenessInfo struct {
	State        string
	ChangedFiles []string
	ChangedDeps  []string
	Reason       string
}

func (b *Blueprint) StalenessInfo() (StalenessInfo, error) {
	statePath := filepath.Join(b.StateDir, "state.yaml")
	state, err := LoadState(statePath)
	if err != nil {
		if errors.Is(err, ErrNoSnapshot) {
			return StalenessInfo{State: "no_snapshot"}, nil
		}
		return StalenessInfo{}, err
	}
	changedFiles, err := changedFilesFromState(b, state)
	if err != nil {
		return StalenessInfo{}, err
	}
	changedDeps, depsAPIChanged := compareDeps(b, state)
	reason := resolveStaleReason(changedFiles, changedDeps, depsAPIChanged)
	stateLabel := "fresh"
	if reason != "" {
		stateLabel = "stale"
	}
	return StalenessInfo{
		State:        stateLabel,
		ChangedFiles: changedFiles,
		ChangedDeps:  changedDeps,
		Reason:       reason,
	}, nil
}

func resolveStaleReason(changedFiles, changedDeps []string, depsAPIChanged bool) string {
	if len(changedFiles) > 0 {
		onlyBlueprint := true
		for _, f := range changedFiles {
			if f != "BLUEPRINT.yaml" {
				onlyBlueprint = false
				break
			}
		}
		if onlyBlueprint {
			return "blueprint_changed"
		}
		return "files_changed"
	}
	if depsAPIChanged && len(changedDeps) > 0 {
		return "deps_api_changed"
	}
	if len(changedDeps) > 0 {
		return "deps_changed"
	}
	return ""
}

func compareDeps(b *Blueprint, state *State) ([]string, bool) {
	if len(state.Deps) == 0 {
		return nil, false
	}
	changed := []string{}
	apiChanged := false
	keys := make([]string, 0, len(state.Deps))
	for k := range state.Deps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		depState := state.Deps[key]
		pinned := depState.Pinned
		if pinned == "" {
			pinned = depState.Latest
		}
		depDir := filepath.Join(b.Dir, filepath.FromSlash(key))
		depDir = filepath.Clean(depDir)
		bpPath, err := FindBlueprintFile(depDir)
		if err != nil {
			changed = append(changed, key)
			continue
		}
		depBp, err := LoadBlueprint(bpPath)
		if err != nil {
			changed = append(changed, key)
			continue
		}
		currentID, err := readCurrentSnapshotID(depBp.StateDir)
		if err != nil {
			if errors.Is(err, ErrNoSnapshot) {
				changed = append(changed, key)
				continue
			}
			changed = append(changed, key)
			continue
		}
		depChanged := currentID != pinned
		apiHash, err := apiHashForSnapshot(depBp, currentID)
		if err != nil {
			changed = append(changed, key)
			continue
		}
		if depState.APIHash != "" && apiHash != depState.APIHash {
			apiChanged = true
			depChanged = true
		}
		if depChanged {
			changed = append(changed, key)
		}
	}
	if len(changed) == 0 {
		return nil, false
	}
	changed = uniqueStrings(changed)
	sort.Strings(changed)
	return changed, apiChanged
}

func uniqueStrings(items []string) []string {
	if len(items) == 0 {
		return items
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}
